package items

import (
	"context"
	"database/sql"
	"encoding/json"
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

// InitializeUser sets up a new user's wallet, inventory, and equipment atomically.
func InitializeUser(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session) error {
	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)

	if !out.Created {
		// The Watermark Check: Migrates old accounts to the latest starter pack
		configVersion := GetStarterPack().Version
		meta, err := GetAccountMetadata(ctx, nk, logger, userID)
		if err == nil && meta.StarterPackVersion < configVersion {
			if err := MigrateStarterPack(ctx, nk, logger, userID, meta.StarterPackVersion, configVersion); err != nil {
				logger.Error("Failed to migrate starter pack: %v", err)
			}
		}
		return nil
	}

	username, _ := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string)

	// Anchor authoritative event to prevent spoofed client metrics.
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

	// Batch atomic writes to ensure safe ACID commit
	pending := NewPendingWrites()

	// Initial grant for new economy loop
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

	// Pre-equip defaults to bypass null checks in UI
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

	// Anchor account watermark to skip future migration diffs
	meta := &AccountMetadata{StarterPackVersion: GetStarterPack().Version}
	metaValue, _ := json.Marshal(meta)
	pending.AddStorageWrite(&runtime.StorageWrite{
		Collection:      storageCollectionInventory,
		Key:             storageKeyMetadata,
		UserID:          userID,
		Value:           string(metaValue),
		PermissionRead:  2,
		PermissionWrite: 0,
	})

	// Commit atomic payload
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

// prepareAllItemGrants compiles the entire catalog into a single pending write mutation.
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

// MigrateStarterPack retroactively grants missing starter items for users who haven't received them due to a schema update.
func MigrateStarterPack(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, currentVersion, newVersion int) error {
	pack := GetStarterPack()
	inv, err := GetUserInventory(ctx, nk, logger, userID)
	if err != nil {
		return fmt.Errorf("failed to get inventory: %w", err)
	}

	mutator := NewInventoryMutator()
	
	hasItem := func(slice []uint32, item uint32) bool {
		for _, v := range slice {
			if v == item {
				return true
			}
		}
		return false
	}

	addedAny := false
	for _, id := range pack.Pets {
		if !hasItem(inv.Pets, id) {
			mutator.AddItem(storageKeyPet, id)
			addedAny = true
		}
	}
	for _, id := range pack.Classes {
		if !hasItem(inv.Classes, id) {
			mutator.AddItem(storageKeyClass, id)
			addedAny = true
		}
	}
	for _, id := range pack.Backgrounds {
		if !hasItem(inv.Backgrounds, id) {
			mutator.AddItem(storageKeyBackground, id)
			addedAny = true
		}
	}
	for _, id := range pack.PieceStyles {
		if !hasItem(inv.PieceStyles, id) {
			mutator.AddItem(storageKeyPieceStyle, id)
			addedAny = true
		}
	}

	if addedAny {
		invPending, err := mutator.CompileWrites(ctx, nk, logger, userID)
		if err != nil {
			return err
		}
		if err := CommitPendingWrites(ctx, nk, logger, invPending); err != nil {
			return err
		}
		logger.WithFields(map[string]interface{}{
			"user": userID,
			"old_version": currentVersion,
			"new_version": newVersion,
		}).Info("Migrated starter pack items successfully")
	}

	meta := &AccountMetadata{StarterPackVersion: newVersion}
	if err := SaveAccountMetadata(ctx, nk, logger, userID, meta); err != nil {
		return fmt.Errorf("failed to save account metadata after migration: %w", err)
	}

	return nil
}

