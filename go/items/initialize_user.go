package items

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	DefaultPetID        = 0
	DefaultClassID      = 0
	DefaultBackgroundID = 0
	DefaultPieceStyleID = 0

	WhiteoutPieceStyleID = 8
)

func AfterAuthorizeUserGC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session, in *api.AuthenticateGameCenterRequest) error {

	if err := InitializeUser(ctx, logger, db, nk, out); err != nil {
		logger.Error("User initialization failed: %v", err)
		return err
	}
	return nil
}

func AfterAuthorizeUserDevice(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session, in *api.AuthenticateDeviceRequest) error {

	if err := InitializeUser(ctx, logger, db, nk, out); err != nil {
		logger.Error("User initialization failed: %v", err)
		return err
	}
	return nil
}

// Initialize user wallet / items
func InitializeUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session) error {
	if !out.Created {
		return nil
	}

	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)

	// wallet
	changeset := map[string]int64{
		"gold":      500,
		"gems":      100,
		"treats":    1,
		"dropsLeft": 0,
	}
	if _, _, err := nk.WalletUpdate(ctx, userID, changeset, map[string]interface{}{}, true); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"wallet": changeset,
			"error":  err.Error(),
		}).Error("Wallet initialization failed")
		return fmt.Errorf("wallet setup error: %w", err)
	}

	
	if err := GivePet(ctx, nk, logger, userID, DefaultPetID); err != nil {
		return err
	}
	if err := GiveClass(ctx, nk, logger, userID, DefaultClassID); err != nil {
		return err
	}
	if err := GiveBackground(ctx, nk, logger, userID, DefaultBackgroundID); err != nil {
		return err
	}
	if err := GivePieceStyle(ctx, nk, logger, userID, DefaultPieceStyleID); err != nil {
		return err
	}
	if err := GivePieceStyle(ctx, nk, logger, userID, WhiteoutPieceStyleID); err != nil {
		return err
	}

	
	return EquipDefaults(ctx, nk, userID)
}