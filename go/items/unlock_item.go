package items

import (
	"context"
	"encoding/json"
	"fmt"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Gives pet to user with initialized progression
func GivePet(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, petID uint32) error {
	if !ValidateItemExists(storageKeyPet, petID) {
		return errors.ErrInvalidItem
	}

	if err := addToInventory(ctx, nk, logger, userID, storageKeyPet, petID); err != nil {
		return err
	}

	if _, err := InitializeProgression(ctx, nk, logger, userID, ProgressionKeyPet, petID); err != nil {
		return err
	}
	return nil
}

// Gives class to user with initialized progression
func GiveClass(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, classID uint32) error {
	if !ValidateItemExists(storageKeyClass, classID) {
		return errors.ErrInvalidItem
	}

	if err := addToInventory(ctx, nk, logger, userID, storageKeyClass, classID); err != nil {
		return err
	}

	if _, err := InitializeProgression(ctx, nk, logger, userID, ProgressionKeyClass, classID); err != nil {
		return err
	}

	return nil
}

func GiveBackground(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, backgroundID uint32) error {
	if !ValidateItemExists(storageKeyBackground, backgroundID) {
		return errors.ErrInvalidItem
	}

	return addToInventory(ctx, nk, logger, userID, storageKeyBackground, backgroundID)
}

func GivePieceStyle(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, styleID uint32) error {
	if !ValidateItemExists(storageKeyPieceStyle, styleID) {
		return errors.ErrInvalidItem
	}
	return addToInventory(ctx, nk, logger, userID, storageKeyPieceStyle, styleID)
}

func addToInventory(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, itemType string, itemID uint32) error {
	// fetch current inventory
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: storageCollectionInventory, Key: itemType, UserID: userID},
	})
	if err != nil {
		LogError(ctx, logger, "Failed to read inventory for item addition", err)
		return fmt.Errorf("inventory read failed: %w", err)
	}

	var current InventoryData
	var version string
	if len(objs) > 0 {
		// Unmarshal existing items
		if err := json.Unmarshal([]byte(objs[0].Value), &current); err != nil {
			LogError(ctx, logger, "Failed to unmarshal inventory data", err)
			return fmt.Errorf("inventory unmarshal failed: %w", err)
		}
		version = objs[0].Version // set version if object exists
	}

	// Check if already owned
	for _, id := range current.Items {
		if id == itemID {
			return nil // already owned
		}
	}

	// write item to inventory
	newItems := append(current.Items, itemID)
	data := InventoryData{Items: newItems}
	value, err := json.Marshal(data)
	if err != nil {
		LogError(ctx, logger, "Inventory marshal failed", err)
		return fmt.Errorf("inventory marshal error: %w", err)
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      storageCollectionInventory,
			Key:             itemType,
			UserID:          userID,
			Value:           string(value),
			PermissionRead:  2, // Inventory is public
			PermissionWrite: 0,
			Version:         version,
		},
	})
	if err != nil {
		LogError(ctx, logger, "Failed to write inventory update", err)
		return fmt.Errorf("inventory write failed: %w", err)
	}

	return nil
}

func InitializeProgression(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, progressionKey string, itemID uint32) (*ItemProgression, error) {
	prog := &ItemProgression{
		Level:               1,
		Exp:                 0,
		EquippedAbility:     0,
		EquippedSprite:      0,
		AbilitiesUnlocked:   1, // First ability unlocked
		SpritesUnlocked:     1, // First sprite unlocked
		BackgroundsUnlocked: 0,
		PieceStylesUnlocked: 0,
	}
	if err := SaveItemProgression(ctx, nk, logger, userID, progressionKey, itemID, prog); err != nil {
		return nil, err
	}
	return prog, nil
}
