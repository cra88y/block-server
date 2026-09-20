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
	"match_started":           true,
	"match_abandoned":         true,
	"performance":             true,
	"crash":                   true,
	"session_start":           true,
	"session_end":             true,
	"ability_used":            true,
	"latency":                 true,
	"state_hash":              true,
	"social_event":            true,
	"user_feedback":           true,
	"non_fatal_error":         true,
	"onboarding_progress":     true,
	"shop_interaction":        true,

	// Network Recovery & Resilience
	"network_ghost_socket_detected":   true,
	"network_snapshot_reconciliation": true,
	"network_partition_started":       true,
	"network_reconnect_attempt":       true,
	"network_match_salvaged":          true,
	"match_forfeit_grace_expired":     true,
	"match_forfeit_grace_cancelled":   true,
}

const retentionDays = 30

// Timestamp is client-provided; must be validated against server clock to prevent time-series corruption from mobile drift.
// Data is a raw JSON string for AOT compatibility; do not change to map[string]interface{}.
type TelemetryEvent struct {
	EventType   string  `json:"event_type"`
	Timestamp   float64 `json:"timestamp"`
	Data        string  `json:"data"`
	ClockSkewed bool    `json:"-"`
}

type TelemetryBatch struct {
	Events []TelemetryEvent `json:"events"`
}

// Bypasses the client whitelist for secure, server-authoritative events (e.g. iap_purchase, match_completed).
func EmitServerTelemetry(logger runtime.Logger, userID string, eventType string, payload interface{}) {
	payloadBytes, _ := json.Marshal(payload)
	logger.WithField("payload", string(payloadBytes)).
		WithField("event_type", eventType).
		WithField("timestamp", float64(time.Now().UnixMilli())/1000.0).
		WithField("user_id", userID).
		Info("telemetry_event")
}

// Atomic batch processing: Prevents a single malformed event from dropping the entire batch of valid metrics.
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

	for i := range batch.Events {
		event := &batch.Events[i]
		if err := validateTelemetryEvent(event); err != nil {
			logger.Warn("Invalid telemetry event %s: %v", event.EventType, err)
			continue
		}
		if err := processTelemetryEvent(ctx, logger, db, nk, userID, *event); err != nil {
			logger.Error("Failed to process telemetry event %s: %v", event.EventType, err)
		}
	}

	logger.Info("Processed %d telemetry events for user %s", len(batch.Events), userID)
	return `{"success": true}`, nil
}

func validateTelemetryEvent(event *TelemetryEvent) error {
	if !validEventTypes[event.EventType] {
		return fmt.Errorf("invalid event type: %s", event.EventType)
	}

	now := time.Now().Unix()
	eventTime := int64(event.Timestamp)

	if eventTime < now-(retentionDays*24*60*60) || eventTime > now+3600 {
		// Ingestion Stamp pattern: Don't drop the telemetry, just fix the timestamp and flag it.
		event.Timestamp = float64(now)
		event.ClockSkewed = true
	}

	// Validate payload size (prevent abuse).
	if len(event.Data) > 10240 { // 10KB max
		return fmt.Errorf("event data too large: %d bytes (max: 10240)", len(event.Data))
	}

	return nil
}

func processTelemetryEvent(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string, event TelemetryEvent) error {
	// We format it as a structured log line that Vector can easily parse
	logger.WithField("payload", event.Data).
		WithField("event_type", event.EventType).
		WithField("timestamp", event.Timestamp).
		WithField("user_id", userID).
		WithField("clock_skewed", event.ClockSkewed).
		Info("telemetry_event")
	return nil
}
