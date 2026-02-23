// Package items — game economy, progression, and item RPCs.
package items

// logging helpers auto-tag lines with the calling user's ID.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)


func GetUserIDFromContext(ctx context.Context, logger runtime.Logger) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		logger.Error("No user ID found in context")
		return "", errors.ErrNoUserIdFound
	}

	if userID == "" {
		logger.Error("Empty user ID found in context")
		return "", errors.ErrNoUserIdFound
	}
	return userID, nil
}


func ParseUint32Safely(value string, logger runtime.Logger) (uint32, error) {
	result, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		logger.Error("Failed to parse uint32 value: %v", err)
		return 0, fmt.Errorf("invalid value: %w", err)
	}
	return uint32(result), nil
}


// LogWithUser logs with user_id from ctx injected — keeps every request line queryable by user.
func LogWithUser(ctx context.Context, logger runtime.Logger, level, message string, fields map[string]interface{}) {
	userID := ""
	if uid, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); ok {
		userID = uid
	}

	// Always include user ID if available
	if userID != "" {
		if fields == nil {
			fields = make(map[string]interface{})
		}
		fields["user"] = userID
	}

	// Log with fields if we have any, otherwise log without
	if len(fields) > 0 {
		switch level {
		case "debug":
			logger.WithFields(fields).Debug(message)
		case "info":
			logger.WithFields(fields).Info(message)
		case "warn":
			logger.WithFields(fields).Warn(message)
		case "error":
			logger.WithFields(fields).Error(message)
		default:
			logger.WithFields(fields).Info(message)
		}
	} else {
		switch level {
		case "debug":
			logger.Debug(message)
		case "info":
			logger.Info(message)
		case "warn":
			logger.Warn(message)
		case "error":
			logger.Error(message)
		default:
			logger.Info(message)
		}
	}
}

func LogError(ctx context.Context, logger runtime.Logger, message string, err error) {
	fields := map[string]interface{}{}
	if err != nil {
		fields["error"] = err.Error()
	}
	LogWithUser(ctx, logger, "error", message, fields)
}

func LogInfo(ctx context.Context, logger runtime.Logger, message string) {
	LogWithUser(ctx, logger, "info", message, nil)
}

func LogWarn(ctx context.Context, logger runtime.Logger, message string) {
	LogWithUser(ctx, logger, "warn", message, nil)
}

func LogDebug(ctx context.Context, logger runtime.Logger, message string) {
	LogWithUser(ctx, logger, "debug", message, nil)
}

func LogSuccess(ctx context.Context, logger runtime.Logger, operation string) {
	LogWithUser(ctx, logger, "info", operation+" completed", nil)
}



// Typed JSON decoding wrapper so we get clean errors.
func UnmarshalJSON[T any](value string) (*T, error) {
	var data T
	if err := json.Unmarshal([]byte(value), &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %T: %w", data, err)
	}
	return &data, nil
}
