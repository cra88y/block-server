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
	EventType string  `json:"event_type"`
	Timestamp float64 `json:"timestamp"`
	Data      string  `json:"data"`
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
		Info("telemetry_event")
	return nil
}
