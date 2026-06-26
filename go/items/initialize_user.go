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

	username, _ := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string)

	// Emit authoritative account_created telemetry
	EmitServerTelemetry(logger, userID, "account_created", map[string]interface{}{
		"provider": username, // or other identifying metadata
	})

	metadata := map[string]interface{}{
		"has_completed_onboarding": false,
	}
	if err := nk.AccountUpdateId(ctx, userID, "", metadata, "", "", "", "", ""); err != nil {
		logger.Error("Failed to update account metadata during initialization: %v", err)
		return err
	}

	// Collect all initialization writes
	pending := NewPendingWrites()

	// Add wallet initialization
	walletChangeset := map[string]int64{
		"gold":      500,
		"gems":      100,
		"treats":    1,
	}
	pending.AddWalletUpdate(userID, walletChangeset)

	// Grant only starter items to new accounts. Full catalog grants are prohibited here.
	if err := GiveStarterItemsToUser(ctx, nk, logger, userID); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Failed to grant starter items during initialization")
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
	mutator := NewInventoryMutator()

	// Pets
	for id := range GameData.Pets {
		mutator.AddItem(storageKeyPet, id)
	}

	// Classes
	for id := range GameData.Classes {
		mutator.AddItem(storageKeyClass, id)
	}

	// Backgrounds
	for id := range GameData.Backgrounds {
		mutator.AddItem(storageKeyBackground, id)
	}

	// PieceStyles
	for id := range GameData.PieceStyles {
		mutator.AddItem(storageKeyPieceStyle, id)
	}

	invPending, err := mutator.CompileWrites(ctx, nk, logger, userID)
	if err == nil && invPending != nil {
		pending.Merge(invPending)
	} else if err != nil {
		logger.Error("Failed to compile batch item grants during user init: %v", err)
		return err
	}

	return nil
}

// GiveStarterItemsToUser grants only starter items atomically.
// Item IDs are driven by starter_pack config in items.json.
func GiveStarterItemsToUser(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) error {
	pending := NewPendingWrites()
	pack := GetStarterPack()

	mutator := NewInventoryMutator()

	for _, id := range pack.Pets {
		mutator.AddItem(storageKeyPet, id)
	}
	for _, id := range pack.Classes {
		mutator.AddItem(storageKeyClass, id)
	}
	for _, id := range pack.Backgrounds {
		mutator.AddItem(storageKeyBackground, id)
	}
	for _, id := range pack.PieceStyles {
		mutator.AddItem(storageKeyPieceStyle, id)
	}

	invPending, err := mutator.CompileWrites(ctx, nk, logger, userID)
	if err == nil && invPending != nil {
		pending.Merge(invPending)
	} else if err != nil {
		return err
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
