package main

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func AfterAuthroizeUserGC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session, in *api.AuthenticateGameCenterRequest) error {

	if err := InitializeUser(ctx, logger, db, nk, out); err != nil {
		return err
	}
	return nil
}

func AfterAuthroizeUserDevice(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session, in *api.AuthenticateDeviceRequest) error {

	if err := InitializeUser(ctx, logger, db, nk, out); err != nil {
		return err
	}
	return nil
}

// Initialize user wallet currencies
func InitializeUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session) error {
	if !out.Created {
		return nil
	} // Only run if the authenticated account is new

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return runtime.NewError("Invalid context", 13)
	}
	// initial wallet configuration
	changeset := map[string]int64{
		"gold":      500,
		"gems":      100,
		"treats":    1,
		"lootboxes": 0,
		"dropsLeft": 0,
	}

	var metadata map[string]interface{}
	if _, _, err := nk.WalletUpdate(ctx, userID, changeset, metadata, true); err != nil {
		return runtime.NewError("WalletUpdate error", 13)
	}
	return nil
}
