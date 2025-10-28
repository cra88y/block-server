package items

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

func EquipDefaults(ctx context.Context, nk runtime.NakamaModule, userID string) error {
	reads := []*runtime.StorageRead{
		{Collection: storageCollectionEquipment, Key: storageKeyPet, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyClass, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyBackground, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyPieceStyle, UserID: userID},
	}

	objects, err := nk.StorageRead(ctx, reads)
	if err != nil {
		return fmt.Errorf("failed to read equipment defaults: %w", err)
	}
	writes := make([]*runtime.StorageWrite, 0, 4)

	for i, key := range []string{storageKeyPet, storageKeyClass, storageKeyBackground, storageKeyPieceStyle} {
		var version string
		if i < len(objects) && objects[i] != nil {
			version = objects[i].Version
		}

		var itemID uint32
		switch key {
		case storageKeyPet:
			itemID = DefaultPetID
		case storageKeyClass:
			itemID = DefaultClassID
		case storageKeyBackground:
			itemID = DefaultBackgroundID
		case storageKeyPieceStyle:
			itemID = DefaultPieceStyleID
		}

		data := EquipmentData{ID: itemID}
		value, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal equipment data for %s: %w", key, err)
		}
		writes = append(writes, &runtime.StorageWrite{
			Collection:      storageCollectionEquipment,
			Key:             key,
			UserID:          userID,
			Value:           string(value),
			PermissionRead:  2,
			PermissionWrite: 0,
			Version:         version,
		})
	}

	_, err = nk.StorageWrite(ctx, writes)
	if err != nil {
		return fmt.Errorf("failed to write equipment defaults: %w", err)
	}

	return nil
}

func EquipAbility(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, itemType string, payload string) error {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		return err
	}

	var req AbilityEquipRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return errors.ErrUnmarshal
	}

	if !ValidateItemExists(itemType, req.ItemID) {
		LogWarn(ctx, logger, "Invalid item ID for equip_ability")
		return errors.ErrInvalidItemID
	}

	owned, err := IsItemOwned(ctx, nk, userID, req.ItemID, itemType)
	if err != nil || !owned {
		return errors.ErrNotOwned
	}

	var abilities []uint32
	var itemExists bool

	switch itemType {
	case storageKeyPet:
		if pet, exists := GetPet(req.ItemID); exists {
			abilities = pet.AbilityIDs
			itemExists = true
		}
	case storageKeyClass:
		if class, exists := GetClass(req.ItemID); exists {
			abilities = class.AbilityIDs
			itemExists = true
		}
	}

	if !itemExists {
		return runtime.NewError("item not found", 3)
	}

	if len(abilities) == 0 {
		return runtime.NewError("no abilities available", 3)
	}

	abilityExists := false
	for _, id := range abilities {
		if id == req.AbilityID {
			abilityExists = true
			break
		}
	}
	if !abilityExists {
		return runtime.NewError("invalid ability for item", 3)
	}

	var prog *ItemProgression
	var progressionKey string

	if itemType == storageKeyPet {
		progressionKey = ProgressionKeyPet
	} else {
		progressionKey = ProgressionKeyClass
	}

	prog, err = GetItemProgression(ctx, nk, logger, userID, progressionKey, req.ItemID)
	if err != nil {
		return err
	}

	abilityIndex := -1
	for i, id := range abilities {
		if id == req.AbilityID {
			abilityIndex = i
			break
		}
	}
	if abilityIndex < 0 {
		return runtime.NewError("ability not unlocked", 3)
	}
	if abilityIndex >= prog.AbilitiesUnlocked {
		return runtime.NewError("ability not unlocked", 3)
	}

	prog.EquippedAbility = abilityIndex

	return SaveItemProgression(ctx, nk, logger, userID, progressionKey, req.ItemID, prog)
}

func IsAbilityAvailable(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, itemID uint32, abilityID uint32, itemType string) error {
	// Validate item exists first
	if !ValidateItemExists(itemType, itemID) {
		return errors.ErrInvalidItemID
	}

	var abilities []uint32
	var itemExists bool

	switch itemType {
	case storageKeyPet:
		if pet, exists := GetPet(itemID); exists {
			if _, exists := pet.AbilitySet[abilityID]; !exists {
				return runtime.NewError("invalid ability for pet", 3)
			}
			abilities = pet.AbilityIDs
			itemExists = true
		}
	case storageKeyClass:
		if class, exists := GetClass(itemID); exists {
			if _, exists := class.AbilitySet[abilityID]; !exists {
				return runtime.NewError("invalid ability for class", 3)
			}
			abilities = class.AbilityIDs
			itemExists = true
		}
	}

	if !itemExists {
		LogWarn(ctx, logger, "Attempted to check ability for non-existent item")
		return runtime.NewError("item not found", 3)
	}

	if len(abilities) == 0 {
		LogWarn(ctx, logger, "No abilities available for item")
		return runtime.NewError("no abilities available", 3)
	}

	var prog *ItemProgression
	var progressionKey string
	var err error

	if itemType == storageKeyPet {
		progressionKey = ProgressionKeyPet
	} else {
		progressionKey = ProgressionKeyClass
	}

	prog, err = GetItemProgression(ctx, nk, logger, userID, progressionKey, itemID)
	if err != nil {
		LogError(ctx, logger, "Failed to get item progression for ability check", err)
		return err
	}

	abilityIndex := -1
	for i, id := range abilities {
		if id == abilityID {
			abilityIndex = i
			break
		}
	}

	if abilityIndex < 0 {
		LogWarn(ctx, logger, "Ability not found in item ability list")
		return runtime.NewError("ability not found", 3)
	}

	if abilityIndex >= prog.AbilitiesUnlocked {
		LogWarn(ctx, logger, "Ability not unlocked")
		return runtime.NewError("ability not unlocked", 3)
	}

	// Ability availability check passed - no need to log this as it's a normal flow
	return nil
}

func EquipItem(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, itemStorageKey string, payload string) error {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		return err
	}

	var req EquipmentData
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return errors.ErrUnmarshal
	}

	if !ValidateItemExists(itemStorageKey, req.ID) {
		return errors.ErrInvalidItemID
	}

	owned, err := IsItemOwned(ctx, nk, userID, req.ID, itemStorageKey)
	if err != nil || !owned {
		return errors.ErrItemNotOwnedForbidden
	}

	value, err := json.Marshal(req)
	if err != nil {
		return errors.ErrMarshal
	}
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: storageCollectionEquipment, Key: itemStorageKey, UserID: userID},
	})
	if err != nil {
		return err
	}
	var version string
	if len(objects) > 0 {
		version = objects[0].Version
	}
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      storageCollectionEquipment,
			Key:             itemStorageKey,
			UserID:          userID,
			Value:           string(value),
			PermissionRead:  2,
			PermissionWrite: 0,
			Version:         version,
		},
	})
	return err
}
func AddPetExp(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, petID uint32, exp uint32) error {
	return addExperience(ctx, nk, logger, userID, storageKeyPet, petID, exp)
}

func AddClassExp(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, classID uint32, exp uint32) error {
	return addExperience(ctx, nk, logger, userID, storageKeyClass, classID, exp)
}

func addExperience(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, itemType string, itemID uint32, exp uint32) error {
	// Validate input
	if exp == 0 {
		LogInfo(ctx, logger, "Zero experience provided, skipping update")
		return nil
	}
	if exp > 1000000 {
		LogWarn(ctx, logger, "Unusually large experience value provided")
		return errors.ErrInvalidExperience
	}

	// Get level tree name for this item
	treeName, err := GetLevelTreeName(itemType, itemID)
	if err != nil {
		LogError(ctx, logger, "Invalid item configuration", err)
		return errors.ErrInvalidConfig
	}

	var progressionKey string
	switch itemType {
	case storageKeyPet:
		progressionKey = ProgressionKeyPet
	case storageKeyClass:
		progressionKey = ProgressionKeyClass
	default:
		return errors.ErrInvalidItemType
	}

	return UpdateProgressionAtomic(ctx, nk, logger, userID, progressionKey, itemID, func(prog *ItemProgression) error {
		// Add XP with overflow protection
		newExp := prog.Exp + int(exp)
		if newExp < prog.Exp { // Overflow detected
			newExp = math.MaxInt32
		}

		// Get level tree to check max level and cap experience
		tree, exists := GetLevelTree(treeName)
		if !exists {
			return errors.ErrInvalidLevelTree
		}

		// Cap experience at max level threshold if needed
		maxExp := tree.LevelThresholds[tree.MaxLevel]
		if newExp > maxExp {
			newExp = maxExp
		}

		prog.Exp = newExp

		newLevel, err := CalculateLevel(treeName, prog.Exp)
		if err != nil {
			return err
		}

		// Cap level at max level
		if newLevel > tree.MaxLevel {
			newLevel = tree.MaxLevel
			prog.Exp = maxExp
		}

		// Check for level up
		if newLevel > prog.Level {
			oldLevel := prog.Level
			prog.Level = newLevel

			// Grant rewards for each level achieved
			for l := oldLevel + 1; l <= newLevel; l++ {
				if err := GrantLevelRewards(ctx, nk, logger, userID, treeName, l, itemType, itemID); err != nil {
					return err
				}
			}
		}

		return nil
	})
}

func IsItemOwned(ctx context.Context, nk runtime.NakamaModule, userID string, itemID uint32, itemStorageKey string) (bool, error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: storageCollectionInventory, Key: itemStorageKey, UserID: userID},
	})
	if err != nil {
		return false, err
	}
	if len(objects) == 0 {
		return false, nil
	}

	var data InventoryData
	if err := json.Unmarshal([]byte(objects[0].Value), &data); err != nil {
		return false, err
	}

	for _, id := range data.Items {
		if id == itemID {
			return true, nil
		}
	}
	return false, nil
}
