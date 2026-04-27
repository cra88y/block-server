package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Wire-protocol whitelist. Must match client-side TelemetryEventTypes.cs.
var validEventTypes = map[string]bool{
	"match_completed":         true,
	"match_abandoned":         true,
	"network_quality":         true,
	"performance":             true,
	"crash":                   true,
	"session_start":           true,
	"session_end":             true,
	"ability_used":            true,
	"piece_placed":            true,
	"round_won":               true,
	"round_lost":              true,
	"progression_claimed":     true,
	"progression_claimed_all": true,
	"latency":                 true, // LatencyMetric
	"state_hash":              true, // StateHashMetric
	"social_event":            true, // SocialEventMetric
}

const retentionDays = 30

// Timestamp is client-provided.
// Data is a raw JSON string for AOT compatibility; do not change to map[string]interface{}.
type TelemetryEvent struct {
	EventType string `json:"event_type"`
	Timestamp float64 `json:"timestamp"`
	Data      string `json:"data"`
}

type TelemetryBatch struct {
	Events []TelemetryEvent `json:"events"`
}

// Batch processing is atomic per-request; individual event failures don't abort the batch.
func RpcSubmitTelemetry(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var batch TelemetryBatch
	if err := json.Unmarshal([]byte(payload), &batch); err != nil {
		logger.Error("Failed to unmarshal telemetry batch: %v", err)
		return "", errors.ErrUnmarshal
	}

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

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
	return `{"success": true}`, nil
}

func validateTelemetryEvent(event TelemetryEvent) error {
	if !validEventTypes[event.EventType] {
		return fmt.Errorf("invalid event type: %s", event.EventType)
	}

	now := time.Now().Unix()
	eventTime := int64(event.Timestamp)

	if eventTime < now-(retentionDays*24*60*60) {
		return fmt.Errorf("event timestamp too old: %d (retention: %d days)", eventTime, retentionDays)
	}
	if eventTime > now+3600 { // 1 hour in future
		return fmt.Errorf("event timestamp in future: %d", eventTime)
	}

	// Validate payload size (prevent abuse).
	// Data is a raw JSON string — measure it directly, not via json.Marshal
	// (which would add escaping and double-count the size).
	if len(event.Data) > 10240 { // 10KB max
		return fmt.Errorf("event data too large: %d bytes (max: 10240)", len(event.Data))
	}

	return nil
}

// Persist event before aggregating.
func processTelemetryEvent(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string, event TelemetryEvent) error {
	// Data is already a JSON string from the client — embed it directly.
	// Do not re-marshal: mustMarshal(event.Data) would double-escape it.
	storageWrite := &runtime.StorageWrite{
		Collection:      "telemetry",
		Key:             fmt.Sprintf("%s_%d", event.EventType, int64(event.Timestamp)),
		UserID:          userID,
		Value:           fmt.Sprintf(`{"event_type":"%s","timestamp":%f,"data":%s}`, event.EventType, event.Timestamp, event.Data),
		PermissionRead:  0,
		PermissionWrite: 0,
	}

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{storageWrite}); err != nil {
		return fmt.Errorf("failed to write telemetry event to storage: %w", err)
	}

	// Stats update failure is non-fatal; event is already persisted.
	if err := updateAggregatedStats(ctx, logger, db, nk, userID, event); err != nil {
		logger.Error("Failed to update aggregated stats: %v", err)
	}

	return nil
}

// Warning: Read-modify-write here lacks OCC; concurrent requests may lose aggregated updates.
func updateAggregatedStats(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string, event TelemetryEvent) error {
	// Daily key format enables time-range queries without secondary indexing.
	today := time.Now().Format("2006-01-02")

	statsKey := fmt.Sprintf("daily_%s", today)

	storageRead := &runtime.StorageRead{
		Collection: "telemetry_stats",
		Key:        statsKey,
		UserID:     userID,
	}

	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{storageRead})
	if err != nil {
		return fmt.Errorf("failed to read existing stats: %w", err)
	}

	var stats map[string]interface{}
	if len(objects) > 0 && objects[0].Value != "" {
		if err := json.Unmarshal([]byte(objects[0].Value), &stats); err != nil {
			logger.Error("Failed to unmarshal existing stats: %v", err)
			stats = make(map[string]interface{})
		}
	} else {
		stats = make(map[string]interface{})
	}

	// Nakama's JSON unmarshaler yields float64 for numbers.
	eventCountKey := fmt.Sprintf("%s_count", event.EventType)
	if count, ok := stats[eventCountKey].(float64); ok {
		stats[eventCountKey] = count + 1
	} else {
		stats[eventCountKey] = 1
	}

	if total, ok := stats["total_events"].(float64); ok {
		stats["total_events"] = total + 1
	} else {
		stats["total_events"] = 1
	}

	stats["last_updated"] = time.Now().Unix()

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

// Deletes telemetry events older than retention period. Best run during off-peak hours.
func CleanupOldTelemetry(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) error {
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays).Unix()

	// TODO: Pagination required for large datasets.
	objects, _, err := nk.StorageList(ctx, "", "", "telemetry", 1000, "")
	if err != nil {
		return fmt.Errorf("failed to list telemetry objects: %w", err)
	}

	var deletes []*runtime.StorageDelete
	for _, obj := range objects {
		var eventType string
		var timestamp int64
		if _, err := fmt.Sscanf(obj.Key, "%s_%d", &eventType, &timestamp); err != nil {
			logger.Warn("Failed to parse telemetry key %s: %v", obj.Key, err)
			continue
		}

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
