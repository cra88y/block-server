package items

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

// GetUserIDFromContext extracts user ID from context
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

// ParseUint32Safely parses a string to uint32
func ParseUint32Safely(value string, logger runtime.Logger) (uint32, error) {
	result, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		logger.Error("Failed to parse uint32 value: %v", err)
		return 0, fmt.Errorf("invalid value: %w", err)
	}
	return uint32(result), nil
}

// Logging helpers

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

// JSON Unmarshal Helpers

// UnmarshalJSON provides type-safe JSON decoding with standardized error handling
func UnmarshalJSON[T any](value string) (*T, error) {
	var data T
	if err := json.Unmarshal([]byte(value), &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %T: %w", data, err)
	}
	return &data, nil
}
