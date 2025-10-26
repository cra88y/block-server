package items

import (
	"context"
	"encoding/json"
	"strconv"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

func RpcEquipPetAbility(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	if err := EquipAbility(ctx, logger, nk, storageKeyPet, payload); err != nil {
		logger.Error("Error equipping ability: %v", err)
		return "", runtime.NewError("couldn't equip ability", 3)
	}
	return `{"success": true}`, nil
}

func RpcEquipClassAbility(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	if err := EquipAbility(ctx, logger, nk, storageKeyClass, payload); err != nil {
		logger.Error("Error equipping ability: %v", err)
		return "", runtime.NewError("couldn't equip ability", 3)
	}
	return `{"success": true}`, nil
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
	if abilityIndex >= prog.AbilitiesUnlocked {
		return runtime.NewError("ability not unlocked", 3)
	}

	prog.EquippedAbility = abilityIndex

	if itemType == storageKeyPet {
		return SaveItemProgression(ctx, nk, userID, ProgressionKeyPet, req.ItemID, prog)
	}
	return SaveItemProgression(ctx, nk, userID, ProgressionKeyClass, req.ItemID, prog)
}

func IsAbilityAvailable(ctx context.Context, nk runtime.NakamaModule, userID string, itemID uint32, abilityID uint32, itemType string) error {
	var abilities []uint32
	switch itemType {
	case storageKeyPet:
		if pet, exists := GetPet(itemID); exists {
			abilities = pet.AbilityIDs
		}
	case storageKeyClass:
		if class, exists := GetClass(itemID); exists {
			abilities = class.AbilityIDs
		}
	}

	if len(abilities) == 0 {
		return runtime.NewError("no abilities available", 3)
	}

	abilityExists := false
	for _, id := range abilities {
		if id == abilityID {
			abilityExists = true
			break
		}
	}
	if !abilityExists {
		return runtime.NewError("invalid ability for item", 3)
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

func RpcEquipPet(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	if err := EquipItem(ctx, logger, nk, storageKeyPet, payload); err != nil {
		logger.Error("Error equipping item: %v", err)
		return "", runtime.NewError("couldn't equip item", 3)
	}

	return `{"success": true}`, nil
}

func RpcEquipClass(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	if err := EquipItem(ctx, logger, nk, storageKeyClass, payload); err != nil {
		logger.Error("Error equipping class: %v", err)
		return "", runtime.NewError("couldn't equip class", 3)
	}

	return `{"success": true}`, nil
}

func RpcEquipBackground(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	if err := EquipItem(ctx, logger, nk, storageKeyBackground, payload); err != nil {
		logger.Error("Error equipping background: %v", err)
		return "", runtime.NewError("couldn't equip background", 3)
	}

	return `{"success": true}`, nil
}

func RpcEquipPieceStyle(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	if err := EquipItem(ctx, logger, nk, storageKeyPieceStyle, payload); err != nil {
		logger.Error("Error equipping style: %v", err)
		return "", runtime.NewError("couldn't equip style", 3)
	}

	return `{"success": true}`, nil
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

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      storageCollectionEquipment,
			Key:             itemStorageKey,
			UserID:          userID,
			Value:           string(itemIDBytes),
			PermissionRead:  2,
			PermissionWrite: 0,
		},
	})
	return err
}
func AddPetExp(ctx context.Context, nk runtime.NakamaModule, userID string, petID uint32, exp uint32) error {
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

	// Update EXP
	prog.Exp += int(exp)

	// Calculate new level with dynamic tree
	newLevel, err := CalculateLevel(treeName, prog.Exp)
	if err != nil {
		return err
	}

	// Check for level up
	if newLevel > prog.Level {
		oldLevel := prog.Level
		prog.Level = newLevel

		// Grant rewards for each level achieved
		for l := oldLevel + 1; l <= newLevel; l++ {
			if err := GrantLevelRewards(ctx, nk, userID, treeName, l, "pet", petID); err != nil {
				return err
			}
		}
	}

	// Save updated progression
	return SaveItemProgression(ctx, nk, userID, ProgressionKeyPet, petID, prog)
}

func AddClassExp(ctx context.Context, nk runtime.NakamaModule, userID string, classID uint32, exp uint32) error {
	treeName, err := GetLevelTreeName(storageKeyClass, classID)
	if err != nil {
		return runtime.NewError("invalid class configuration", 13)
	}

	prog, err := GetItemProgression(ctx, nk, userID, ProgressionKeyClass, classID)
	if err != nil {
		return err
	}

	prog.Exp += int(exp)
	newLevel, err := CalculateLevel(treeName, prog.Exp)
	if err != nil {
		return err
	}

	if newLevel > prog.Level {
		oldLevel := prog.Level
		prog.Level = newLevel

		for l := oldLevel + 1; l <= newLevel; l++ {
			if err := GrantLevelRewards(ctx, nk, userID, treeName, l, "class", classID); err != nil {
				return err
			}
		}
	}

	return SaveItemProgression(ctx, nk, userID, ProgressionKeyClass, classID, prog)
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

	var ownedItems []uint32
	if err := json.Unmarshal([]byte(objects[0].Value), &ownedItems); err != nil {
		return false, err
	}

	// Check both direct ownership and unlocked through progression
	for _, id := range ownedItems {
		if id == itemID {
			return true, nil
		}
	}
	return false, nil
}

func AtomicIncrement(ctx context.Context, nk runtime.NakamaModule, userID string, collection string, key string, delta int) error {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: collection, Key: key, UserID: userID},
	})
	if err != nil {
		return err
	}

	version := ""
	current := 0
	if len(objects) > 0 {
		var err error
		current, err = strconv.Atoi(objects[0].Value)
		if err != nil {
			return err
		}
		version = objects[0].Version
	}

	newValue := strconv.Itoa(current + delta)
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: collection,
			Key:        key,
			Value:      newValue,
			Version:    version,
		},
	})
	return err
}
