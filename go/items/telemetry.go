package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// INVARIANT: Whitelist prevents malformed events from corrupting stats or filling storage
var validEventTypes = map[string]bool{
	"match_completed":   true,
	"match_abandoned":   true,
	"network_quality":   true,
	"performance":       true,
	"crash":             true,
	"session_start":     true,
	"session_end":       true,
	"ability_used":      true,
	"piece_placed":      true,
	"round_won":         true,
	"round_lost":        true,
	"progression_claimed":     true,
	"progression_claimed_all": true,
}

// INVARIANT: 30-day retention balances analytics value vs storage cost
const retentionDays = 30

// INVARIANT: Timestamp is client-provided for ordering, not server-generated
type TelemetryEvent struct {
	EventType string                 `json:"event_type"`
	Timestamp float64                `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
}

// INVARIANT: Batch size is bounded by client-side queue (MAX_QUEUE_SIZE)
type TelemetryBatch struct {
	Events []TelemetryEvent `json:"events"`
}

// INVARIANT: Batch processing is atomic per-request - partial failures don't rollback
func RpcSubmitTelemetry(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var batch TelemetryBatch
	if err := json.Unmarshal([]byte(payload), &batch); err != nil {
		logger.Error("Failed to unmarshal telemetry batch: %v", err)
		return "", fmt.Errorf("invalid telemetry payload: %w", err)
	}

	// HAZARD: User ID is required for storage ownership - missing = silent data loss
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", fmt.Errorf("user ID not found in context")
	}

	// INVARIANT: Individual event failures don't abort batch - best-effort delivery
	for _, event := range batch.Events {
		if err := validateTelemetryEvent(event); err != nil {
			logger.Warn("Invalid telemetry event %s: %v", event.EventType, err)
			continue
		}
		if err := processTelemetryEvent(ctx, logger, db, nk, userID, event); err != nil {
			logger.Error("Failed to process telemetry event %s: %v", event.EventType, err)
		}
	}

	logger.Info("Processed %d telemetry events for user %s", len(batch.Events), userID)
	return "ok", nil
}

// INVARIANT: Validation prevents malformed events from corrupting stats or filling storage
func validateTelemetryEvent(event TelemetryEvent) error {
	// Validate event type
	if !validEventTypes[event.EventType] {
		return fmt.Errorf("invalid event type: %s", event.EventType)
	}

	// Validate timestamp (not too old, not in future)
	now := time.Now().Unix()
	eventTime := int64(event.Timestamp)
	
	// HAZARD: Timestamp bounds prevent stale or future events from corrupting time-range queries
	if eventTime < now-(retentionDays*24*60*60) {
		return fmt.Errorf("event timestamp too old: %d (retention: %d days)", eventTime, retentionDays)
	}
	if eventTime > now+3600 { // 1 hour in future
		return fmt.Errorf("event timestamp in future: %d", eventTime)
	}

	// Validate payload size (prevent abuse)
	dataJSON, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}
	if len(dataJSON) > 10240 { // 10KB max
		return fmt.Errorf("event data too large: %d bytes (max: 10240)", len(dataJSON))
	}

	return nil
}

// INVARIANT: Write-first ordering collapses the TOCTOU window for stats updates
func processTelemetryEvent(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string, event TelemetryEvent) error {
	storageWrite := &runtime.StorageWrite{
		Collection:      "telemetry",
		Key:             fmt.Sprintf("%s_%d", event.EventType, int64(event.Timestamp)),
		UserID:          userID,
		Value:           fmt.Sprintf(`{"event_type":"%s","timestamp":%f,"data":%s}`, event.EventType, event.Timestamp, mustMarshal(event.Data)),
		PermissionRead:  0, // INVARIANT: Server-only access prevents client tampering
		PermissionWrite: 0, // INVARIANT: Server-only access prevents client tampering
	}

	// HAZARD: Storage write failure loses event permanently - no retry mechanism
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{storageWrite}); err != nil {
		return fmt.Errorf("failed to write telemetry event to storage: %w", err)
	}

	// INVARIANT: Stats update failure is non-fatal - event already persisted
	if err := updateAggregatedStats(ctx, logger, db, nk, userID, event); err != nil {
		logger.Error("Failed to update aggregated stats: %v", err)
	}

	return nil
}

// HAZARD: Read-modify-write race condition - concurrent requests can lose updates
func updateAggregatedStats(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string, event TelemetryEvent) error {
	// INVARIANT: Daily key format enables time-range queries without indexing
	today := time.Now().Format("2006-01-02")
	
	statsKey := fmt.Sprintf("daily_%s", today)
	
	// HAZARD: No locking - concurrent reads can return stale data
	storageRead := &runtime.StorageRead{
		Collection: "telemetry_stats",
		Key:        statsKey,
		UserID:      userID,
	}
	
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{storageRead})
	if err != nil {
		return fmt.Errorf("failed to read existing stats: %w", err)
	}
	
	// INVARIANT: Empty stats = first event of the day, not an error
	var stats map[string]interface{}
	if len(objects) > 0 && objects[0].Value != "" {
		if err := json.Unmarshal([]byte(objects[0].Value), &stats); err != nil {
			logger.Error("Failed to unmarshal existing stats: %v", err)
			stats = make(map[string]interface{})
		}
	} else {
		stats = make(map[string]interface{})
	}
	
	// INVARIANT: Float64 type matches Nakama's JSON unmarshaling behavior
	eventCountKey := fmt.Sprintf("%s_count", event.EventType)
	if count, ok := stats[eventCountKey].(float64); ok {
		stats[eventCountKey] = count + 1
	} else {
		stats[eventCountKey] = 1
	}
	
	// INVARIANT: Total count enables quick health checks without scanning all events
	if total, ok := stats["total_events"].(float64); ok {
		stats["total_events"] = total + 1
	} else {
		stats["total_events"] = 1
	}
	
	// INVARIANT: Unix timestamp enables TTL-based cleanup without parsing dates
	stats["last_updated"] = time.Now().Unix()
	
	// HAZARD: Write failure loses stats update - event already persisted successfully
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("failed to marshal stats: %w", err)
	}
	
	storageWrite := &runtime.StorageWrite{
		Collection:      "telemetry_stats",
		Key:             statsKey,
		UserID:          userID,
		Value:           string(statsJSON),
		PermissionRead:  0,
		PermissionWrite: 0,
	}
	
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{storageWrite}); err != nil {
		return fmt.Errorf("failed to write updated stats: %w", err)
	}
	
	return nil
}

// INVARIANT: Panic on marshal failure = corrupted data is worse than missing data
func mustMarshal(data interface{}) string {
	bytes, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal data: %v", err))
	}
	return string(bytes)
}

// CleanupOldTelemetry deletes telemetry events older than retention period
// HAZARD: Run during off-peak hours to avoid impacting active users
func CleanupOldTelemetry(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) error {
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays).Unix()
	
	// List all telemetry objects for all users
	// HAZARD: This is a full scan - consider pagination for large datasets
	objects, _, err := nk.StorageList(ctx, "", "", "telemetry", 1000, "")
	if err != nil {
		return fmt.Errorf("failed to list telemetry objects: %w", err)
	}

	var deletes []*runtime.StorageDelete
	for _, obj := range objects {
		// Parse timestamp from key (format: eventtype_timestamp)
		var eventType string
		var timestamp int64
		if _, err := fmt.Sscanf(obj.Key, "%s_%d", &eventType, &timestamp); err != nil {
			logger.Warn("Failed to parse telemetry key %s: %v", obj.Key, err)
			continue
		}

		// HAZARD: Delete only if timestamp is parseable and older than cutoff
		if timestamp < cutoffTime {
			deletes = append(deletes, &runtime.StorageDelete{
				Collection: "telemetry",
				Key:        obj.Key,
				UserID:     obj.UserId,
			})
		}
	}

	if len(deletes) > 0 {
		if err := nk.StorageDelete(ctx, deletes); err != nil {
			return fmt.Errorf("failed to delete old telemetry: %w", err)
		}
		logger.Info("Cleaned up %d telemetry events older than %d days", len(deletes), retentionDays)
	}

	return nil
}
