package items

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

var (
	ErrInvalidItem = errors.New("invalid item ID")
)

// Gives pet to user with initialized progression
func GivePet(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, petID uint32) error {
	if !ValidateItemExists(storageKeyPet, petID) {
		return ErrInvalidItem
	}

	if err := addToInventory(ctx, nk, logger, userID, storageKeyPet, petID); err != nil {
		return err
	}

	// Initialize progression if none
	prog, err := GetItemProgression(ctx, nk, userID, ProgressionKeyPet, petID)
	if err != nil || prog == nil {
		return InitializeProgression(ctx, nk, logger, userID, ProgressionKeyPet, petID)
	}
	return nil
}

// Gives class to user with initialized progression
func GiveClass(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, classID uint32) error {
	if !ValidateItemExists(storageKeyClass, classID) {
		return ErrInvalidItem
	}

	if err := addToInventory(ctx, nk, logger, userID, storageKeyClass, classID); err != nil {
		return err
	}

	prog, err := GetItemProgression(ctx, nk, userID, ProgressionKeyClass, classID)
	if err != nil || prog == nil {
		return InitializeProgression(ctx, nk, logger, userID, ProgressionKeyClass, classID)
	}
	return nil
}

func GiveBackground(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, backgroundID uint32) error {
	if !ValidateItemExists(storageKeyBackground, backgroundID) {
		return ErrInvalidItem
	}

	return addToInventory(ctx, nk, logger, userID, storageKeyBackground, backgroundID)
}

func GivePieceStyle(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, styleID uint32) error {
	if !ValidateItemExists(storageKeyPieceStyle, styleID) {
		return ErrInvalidItem
	}
	return addToInventory(ctx, nk, logger, userID, storageKeyPieceStyle, styleID)
}

func addToInventory(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, itemType string, itemID uint32) error {
	// fetch current inventory
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: storageCollectionInventory, Key: itemType, UserID: userID},
	})
	if err != nil {
		return err
	}

	var current []uint32
	var version string
	if len(objs) > 0 {
		// Unmarshal existing items
		if err := json.Unmarshal([]byte(objs[0].Value), &current); err != nil {
			return err
		}
		version = objs[0].Version // set version if object exists
	}

	// Check if already owned
	for _, id := range current {
		if id == itemID {
			return nil // already owned
		}
	}

	// write item to inventory
	newItems := append(current, itemID)
	data := InventoryData{Items: newItems}
	value, err := json.Marshal(data)
	if err != nil {
		logger.WithField("items", newItems).Error("Inventory marshal failed")
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
	return err
}

func InitializeProgression(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, progressionKey string, itemID uint32) error {
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
	return SaveItemProgression(ctx, nk, logger, userID, progressionKey, itemID, prog)
}
