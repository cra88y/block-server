package items

import (
	"context"

	"github.com/heroiclabs/nakama-common/runtime"
)

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

// LogError provides simple error logging
func LogError(ctx context.Context, logger runtime.Logger, message string, err error) {
	fields := map[string]interface{}{}
	if err != nil {
		fields["error"] = err.Error()
	}
	LogWithUser(ctx, logger, "error", message, fields)
}

// LogInfo provides simple info logging
func LogInfo(ctx context.Context, logger runtime.Logger, message string) {
	LogWithUser(ctx, logger, "info", message, nil)
}

// LogWarn provides simple warning logging
func LogWarn(ctx context.Context, logger runtime.Logger, message string) {
	LogWithUser(ctx, logger, "warn", message, nil)
}

// LogDebug provides simple debug logging
func LogDebug(ctx context.Context, logger runtime.Logger, message string) {
	LogWithUser(ctx, logger, "debug", message, nil)
}

// LogSuccess provides simple success logging
func LogSuccess(ctx context.Context, logger runtime.Logger, operation string) {
	LogWithUser(ctx, logger, "info", operation+" completed", nil)
}
