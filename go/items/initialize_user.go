package items

import (
	"context"
	"database/sql"

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

// InitializeUser sets up a new user's wallet, inventory, and equipment atomically.
func InitializeUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session) error {
	if !out.Created {
		return nil
	}

	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)

	// Collect all initialization writes
	pending := NewPendingWrites()

	// Add wallet initialization
	walletChangeset := map[string]int64{
		"gold":      500,
		"gems":      100,
		"treats":    1,
		"dropsLeft": 0,
	}
	pending.AddWalletUpdate(userID, walletChangeset)

	// Add all items to inventory
	if err := prepareAllItemGrants(ctx, nk, logger, userID, pending); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Failed to prepare item grants for initialization")
		return err
	}

	// Add default equipment writes
	equipWrites, err := PrepareEquipDefaults(ctx, nk, userID)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Failed to prepare equipment defaults")
		return err
	}
	for _, w := range equipWrites {
		pending.AddStorageWrite(w)
	}

	// Commit everything atomically
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("User initialization commit failed")
		return err
	}

	logger.WithFields(map[string]interface{}{
		"user": userID,
	}).Info("User initialized successfully")

	return nil
}

// prepareAllItemGrants collects all item grant writes into pending.
func prepareAllItemGrants(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, pending *PendingWrites) error {
	// Pets
	for id := range GameData.Pets {
		itemPending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKeyPet, id)
		if err != nil {
			logger.WithFields(map[string]interface{}{
				"user": userID,
				"pet":  id,
				"err":  err.Error(),
			}).Warn("Failed to prepare pet grant")
			continue
		}
		pending.Merge(itemPending)
	}

	// Classes
	for id := range GameData.Classes {
		itemPending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKeyClass, id)
		if err != nil {
			logger.WithFields(map[string]interface{}{
				"user":  userID,
				"class": id,
				"err":   err.Error(),
			}).Warn("Failed to prepare class grant")
			continue
		}
		pending.Merge(itemPending)
	}

	// Backgrounds
	for id := range GameData.Backgrounds {
		itemPending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKeyBackground, id)
		if err != nil {
			logger.WithFields(map[string]interface{}{
				"user":       userID,
				"background": id,
				"err":        err.Error(),
			}).Warn("Failed to prepare background grant")
			continue
		}
		pending.Merge(itemPending)
	}

	// PieceStyles
	for id := range GameData.PieceStyles {
		itemPending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKeyPieceStyle, id)
		if err != nil {
			logger.WithFields(map[string]interface{}{
				"user":       userID,
				"pieceStyle": id,
				"err":        err.Error(),
			}).Warn("Failed to prepare piece style grant")
			continue
		}
		pending.Merge(itemPending)
	}

	return nil
}

// GiveStarterItemsToUser grants only starter items atomically.
func GiveStarterItemsToUser(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) error {
	pending := NewPendingWrites()

	items := []struct {
		itemType string
		itemID   uint32
	}{
		{storageKeyPet, DefaultPetID},
		{storageKeyClass, DefaultClassID},
		{storageKeyBackground, DefaultBackgroundID},
		{storageKeyPieceStyle, DefaultPieceStyleID},
	}

	for _, item := range items {
		itemPending, err := PrepareItemGrant(ctx, nk, logger, userID, item.itemType, item.itemID)
		if err != nil {
			return err
		}
		pending.Merge(itemPending)
	}

	return CommitPendingWrites(ctx, nk, logger, pending)
}

// GiveAllItemsToUser grants all existing items in game data atomically.
func GiveAllItemsToUser(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) error {
	pending := NewPendingWrites()

	if err := prepareAllItemGrants(ctx, nk, logger, userID, pending); err != nil {
		return err
	}

	return CommitPendingWrites(ctx, nk, logger, pending)
}