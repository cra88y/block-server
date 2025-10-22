package main

import (
	"context"
	"database/sql"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"time"
)

// Initialize user wallet currencies
func InitializeUser(ctx context.Context, logger Logger, db *sql.DB, nk NakamaModule, out *api.Session, in *api.AuthenticateDeviceRequest) error {
	if !out.Created{
		return;
	} // Only run if the authenticated account is new

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
	  return "", errors.New("Invalid context")
	}

	// initial wallet configuration
	changeset := map[string]interface{}{
	  "gold": 500,
	  "gems":  100,
	  "treats": 1,
	  "lootboxes": 0,
	  "dropsLeft":  3,
	}

	var metadata map[string]interface{}(
		"dailyRewardReset": ""
	)
	if err := nk.WalletUpdate(ctx, userID, changeset, metadata, true); err != nil {
	  // Handle error
	}
}
