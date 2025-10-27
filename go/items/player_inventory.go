package items

import (
	"context"
	"encoding/json"
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

	objects, _ := nk.StorageRead(ctx, reads)
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

		value, _ := json.Marshal(itemID)
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

	_, err := nk.StorageWrite(ctx, writes)
	return err
}

func EquipAbility(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, itemType string, payload string) error {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return errors.ErrNoUserIdFound
	}

	var req AbilityEquipRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return errors.ErrUnmarshal
	}

	if !ValidateItemExists(itemType, req.ItemID) {
		return runtime.NewError("invalid item ID", 3)
	}

	owned, err := IsItemOwned(ctx, nk, userID, req.ItemID, itemType)
	if err != nil || !owned {
		return runtime.NewError("item not owned", 3)
	}

	var abilities []uint32
	switch itemType {
	case storageKeyPet:
		if pet, exists := GetPet(req.ItemID); exists {
			abilities = pet.AbilityIDs
		}
	case storageKeyClass:
		if class, exists := GetClass(req.ItemID); exists {
			abilities = class.AbilityIDs
		}
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
	if itemType == storageKeyPet {
		prog, err = GetItemProgression(ctx, nk, userID, ProgressionKeyPet, req.ItemID)
	} else {
		prog, err = GetItemProgression(ctx, nk, userID, ProgressionKeyClass, req.ItemID)
	}
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

	if itemType == storageKeyPet {
		return SaveItemProgression(ctx, nk, logger, userID, ProgressionKeyPet, req.ItemID, prog)
	}
	return SaveItemProgression(ctx, nk, logger, userID, ProgressionKeyClass, req.ItemID, prog)
}

func IsAbilityAvailable(ctx context.Context, nk runtime.NakamaModule, userID string, itemID uint32, abilityID uint32, itemType string) error {
	var abilities []uint32
	switch itemType {
	case storageKeyPet:
		if pet, exists := GetPet(itemID); exists {
			if _, exists := pet.AbilitySet[abilityID]; !exists {
				return runtime.NewError("invalid ability for pet", 3)
			}
			abilities = pet.AbilityIDs
		}
	case storageKeyClass:
		if class, exists := GetClass(itemID); exists {
			if _, exists := class.AbilitySet[abilityID]; !exists {
				return runtime.NewError("invalid ability for class", 3)
			}
			abilities = class.AbilityIDs
		}
	}

	var prog *ItemProgression
	var err error
	if itemType == storageKeyPet {
		prog, err = GetItemProgression(ctx, nk, userID, ProgressionKeyPet, itemID)
	} else {
		prog, err = GetItemProgression(ctx, nk, userID, ProgressionKeyClass, itemID)
	}
	if err != nil {
		return err
	}

	abilityIndex := -1
	for i, id := range abilities {
		if id == abilityID {
			abilityIndex = i
			break
		}
	}
	if abilityIndex >= prog.AbilitiesUnlocked {
		return runtime.NewError("ability not unlocked", 3)
	}

	return nil
}

func EquipItem(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, itemStorageKey string, payload string) error {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return errors.ErrNoUserIdFound
	}

	var req EquipRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return errors.ErrUnmarshal
	}

	if !ValidateItemExists(itemStorageKey, req.ID) {
		return runtime.NewError("invalid item ID", 3)
	}

	owned, err := IsItemOwned(ctx, nk, userID, req.ID, itemStorageKey)
	if err != nil || !owned {
		return runtime.NewError("item not owned", 403)
	}

	itemIDBytes, err := json.Marshal(req.ID)
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
			Value:           string(itemIDBytes),
			PermissionRead:  2,
			PermissionWrite: 0,
			Version:         version,
		},
	})
	return err
}
func AddPetExp(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, petID uint32, exp uint32) error {
	// Get level tree name for this pet
	treeName, err := GetLevelTreeName(storageKeyPet, petID)
	if err != nil {
		return runtime.NewError("invalid pet configuration", 13)
	}

	// Get current progression
	prog, err := GetItemProgression(ctx, nk, userID, ProgressionKeyPet, petID)
	if err != nil {
		return err
	}

	// Add XP
	newExp := prog.Exp + int(exp)
	if newExp < prog.Exp {
		newExp = math.MaxInt32
	}
	prog.Exp = newExp

	newLevel, err := CalculateLevel(treeName, prog.Exp)
	if err != nil {
		return err
	}
	tree, exists := GetLevelTree(treeName)
	if !exists {
		return errors.ErrInvalidLevelTree
	}
	if newLevel > tree.MaxLevel {
		newLevel = tree.MaxLevel
		prog.Exp = tree.LevelThresholds[tree.MaxLevel]
	}
	// Check for level up
	if newLevel > prog.Level {
		oldLevel := prog.Level
		prog.Level = newLevel

		// Grant rewards for each level achieved
		for l := oldLevel + 1; l <= newLevel; l++ {
			if err := GrantLevelRewards(ctx, nk, logger, userID, treeName, l, "pet", petID); err != nil {
				return err
			}
		}
	}

	// Save updated progression
	return SaveItemProgression(ctx, nk, logger, userID, ProgressionKeyPet, petID, prog)
}

func AddClassExp(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, classID uint32, exp uint32) error {
	treeName, err := GetLevelTreeName(storageKeyClass, classID)
	if err != nil {
		return runtime.NewError("invalid class configuration", 13)
	}

	prog, err := GetItemProgression(ctx, nk, userID, ProgressionKeyClass, classID)
	if err != nil {
		return err
	}

	newExp := prog.Exp + int(exp)
	if newExp < prog.Exp {
		newExp = math.MaxInt32
	}
	prog.Exp = newExp

	newLevel, err := CalculateLevel(treeName, prog.Exp)
	if err != nil {
		return err
	}

	if newLevel > prog.Level {
		oldLevel := prog.Level
		prog.Level = newLevel

		for l := oldLevel + 1; l <= newLevel; l++ {
			if err := GrantLevelRewards(ctx, nk, logger, userID, treeName, l, "class", classID); err != nil {
				return err
			}
		}
	}

	return SaveItemProgression(ctx, nk, logger, userID, ProgressionKeyClass, classID, prog)
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
