package items

import (
	"context"
	"fmt"
	"strconv"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

// GetUserIDFromContext extracts user ID from context with error handling
func GetUserIDFromContext(ctx context.Context, logger runtime.Logger) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		logger.Error("No user ID found in context")
		return "", errors.ErrNoUserIdFound
	}
	return userID, nil
}

// ParseUint32Safely safely parses a string to uint32 with error handling
func ParseUint32Safely(value string, logger runtime.Logger) (uint32, error) {
	result, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		logger.Error("Failed to parse uint32 value: %v", err)
		return 0, fmt.Errorf("invalid value: %w", err)
	}
	return uint32(result), nil
}
