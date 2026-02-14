package items

import (
	"context"
	"encoding/json"
	"fmt"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

// PrepareEquipDefaults prepares storage writes to equip default items.
// Returns the writes without committing.
func PrepareEquipDefaults(ctx context.Context, nk runtime.NakamaModule, userID string) ([]*runtime.StorageWrite, error) {
	reads := []*runtime.StorageRead{
		{Collection: storageCollectionEquipment, Key: storageKeyPet, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyClass, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyBackground, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyPieceStyle, UserID: userID},
	}

	objects, err := nk.StorageRead(ctx, reads)
	if err != nil {
		return nil, fmt.Errorf("failed to read equipment defaults: %w", err)
	}

	writes := make([]*runtime.StorageWrite, 0, 4)

	// NOTE (PL-7): Assumes StorageRead returns objects in request order.
	// Safe for current Nakama version. Verify on major version upgrades.
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
			return nil, fmt.Errorf("failed to marshal equipment data for %s: %w", key, err)
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

	return writes, nil
}

// EquipDefaults equips default items for a user.
func EquipDefaults(ctx context.Context, nk runtime.NakamaModule, userID string) error {
	writes, err := PrepareEquipDefaults(ctx, nk, userID)
	if err != nil {
		return err
	}

	if len(writes) == 0 {
		return nil
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
		return errors.ErrItemNotFound
	}

	if len(abilities) == 0 {
		return errors.ErrNoAbilitiesAvailable
	}

	abilityExists := false
	for _, id := range abilities {
		if id == req.AbilityID {
			abilityExists = true
			break
		}
	}
	if !abilityExists {
		return errors.ErrInvalidAbility
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
		return errors.ErrAbilityNotUnlocked
	}
	if abilityIndex >= prog.AbilitiesUnlocked {
		return errors.ErrAbilityNotUnlocked
	}

	prog.EquippedAbility = abilityIndex

	return SaveItemProgression(ctx, nk, logger, userID, progressionKey, req.ItemID, prog)
}

func IsAbilityAvailable(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, itemID uint32, abilityID uint32, itemType string) error {
	if !ValidateItemExists(itemType, itemID) {
		return errors.ErrInvalidItemID
	}

	var abilities []uint32
	var itemExists bool

	switch itemType {
	case storageKeyPet:
		if pet, exists := GetPet(itemID); exists {
			if _, exists := pet.AbilitySet[abilityID]; !exists {
				return errors.ErrInvalidAbilityPet
			}
			abilities = pet.AbilityIDs
			itemExists = true
		}
	case storageKeyClass:
		if class, exists := GetClass(itemID); exists {
			if _, exists := class.AbilitySet[abilityID]; !exists {
				return errors.ErrInvalidAbilityClass
			}
			abilities = class.AbilityIDs
			itemExists = true
		}
	}

	if !itemExists {
		LogWarn(ctx, logger, "Attempted to check ability for non-existent item")
		return errors.ErrItemNotFound
	}

	if len(abilities) == 0 {
		LogWarn(ctx, logger, "No abilities available for item")
		return errors.ErrNoAbilitiesAvailable
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
		return errors.ErrAbilityNotFound
	}

	if abilityIndex >= prog.AbilitiesUnlocked {
		LogWarn(ctx, logger, "Ability not unlocked")
		return errors.ErrAbilityNotUnlocked
	}

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

	data, err := UnmarshalJSON[InventoryData](objects[0].Value)
	if err != nil {
		return false, fmt.Errorf("inventory check: %w", err)
	}

	for _, id := range data.Items {
		if id == itemID {
			return true, nil
		}
	}
	return false, nil
}

// PrepareInventoryAdd prepares a storage write to add an item to inventory without committing.
// Returns the write and whether the item was already owned (no write needed).
func PrepareInventoryAdd(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, itemType string, itemID uint32) (*runtime.StorageWrite, bool, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: storageCollectionInventory, Key: itemType, UserID: userID},
	})
	if err != nil {
		LogError(ctx, logger, "Failed to read inventory for item addition", err)
		return nil, false, fmt.Errorf("inventory read failed: %w", err)
	}

	var current InventoryData
	var version string
	if len(objs) > 0 {
		inventoryData, err := UnmarshalJSON[InventoryData](objs[0].Value)
		if err != nil {
			LogError(ctx, logger, "Failed to unmarshal inventory data", err)
			return nil, false, fmt.Errorf("inventory data: %w", err)
		}
		version = objs[0].Version
		current = *inventoryData
	}

	// Check if already owned
	for _, id := range current.Items {
		if id == itemID {
			return nil, true, nil // Already owned, no write needed
		}
	}

	newItems := append(current.Items, itemID)
	data := InventoryData{Items: newItems}
	value, err := json.Marshal(data)
	if err != nil {
		LogError(ctx, logger, "Inventory marshal failed", err)
		return nil, false, fmt.Errorf("inventory marshal error: %w", err)
	}

	write := &runtime.StorageWrite{
		Collection:      storageCollectionInventory,
		Key:             itemType,
		UserID:          userID,
		Value:           string(value),
		PermissionRead:  2,
		PermissionWrite: 0,
		Version:         version,
	}

	return write, false, nil
}

// PrepareItemGrant prepares writes to grant an item (inventory + progression if needed).
// For pets/classes, also prepares progression initialization.
func PrepareItemGrant(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, itemType string, itemID uint32) (*PendingWrites, error) {
	pending := NewPendingWrites()

	if !ValidateItemExists(itemType, itemID) {
		return nil, errors.ErrInvalidItem
	}

	// Prepare inventory addition
	invWrite, alreadyOwned, err := PrepareInventoryAdd(ctx, nk, logger, userID, itemType, itemID)
	if err != nil {
		return nil, err
	}
	if alreadyOwned {
		return pending, nil // Nothing to do
	}
	if invWrite != nil {
		pending.AddStorageWrite(invWrite)
	}

	// For pets and classes, also prepare progression initialization
	if itemType == storageKeyPet || itemType == storageKeyClass {
		var progressionKey string
		switch itemType {
		case storageKeyPet:
			progressionKey = ProgressionKeyPet
		case storageKeyClass:
			progressionKey = ProgressionKeyClass
		}

		progWrite, err := PrepareProgressionInit(userID, progressionKey, itemID)
		if err != nil {
			return nil, err
		}
		if progWrite != nil {
			pending.AddStorageWrite(progWrite)
		}
	}

	return pending, nil
}

// PrepareProgressionInit prepares a storage write to initialize progression for an item.
func PrepareProgressionInit(userID string, progressionKey string, itemID uint32) (*runtime.StorageWrite, error) {
	prog := DefaultProgression()
	value, err := json.Marshal(prog)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal progression: %w", err)
	}

	key := progressionKey + fmt.Sprintf("%d", itemID)
	return &runtime.StorageWrite{
		Collection:      storageCollectionProgression,
		Key:             key,
		UserID:          userID,
		Value:           string(value),
		PermissionRead:  2,
		PermissionWrite: 0,
	}, nil
}

// GivePet grants a pet to a user atomically.
func GivePet(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, petID uint32) error {
	pending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKeyPet, petID)
	if err != nil {
		return err
	}
	return CommitPendingWrites(ctx, nk, logger, pending)
}

// GiveClass grants a class to a user atomically.
func GiveClass(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, classID uint32) error {
	pending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKeyClass, classID)
	if err != nil {
		return err
	}
	return CommitPendingWrites(ctx, nk, logger, pending)
}

// GiveBackground grants a background to a user atomically.
func GiveBackground(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, backgroundID uint32) error {
	pending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKeyBackground, backgroundID)
	if err != nil {
		return err
	}
	return CommitPendingWrites(ctx, nk, logger, pending)
}

// GivePieceStyle grants a piece style to a user atomically.
func GivePieceStyle(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, styleID uint32) error {
	pending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKeyPieceStyle, styleID)
	if err != nil {
		return err
	}
	return CommitPendingWrites(ctx, nk, logger, pending)
}




func RemoveItemFromInventory(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, itemType string, itemID uint32) error {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: storageCollectionInventory, Key: itemType, UserID: userID},
	})
	if err != nil {
		LogError(ctx, logger, "Failed to read inventory for item removal", err)
		return fmt.Errorf("inventory read failed: %w", err)
	}

	if len(objs) == 0 {
		// Nothing to remove, treat as success
		return nil
	}

	inventoryData, err := UnmarshalJSON[InventoryData](objs[0].Value)
	if err != nil {
		LogError(ctx, logger, "Failed to unmarshal inventory data for removal", err)
		return fmt.Errorf("inventory data unmarshal: %w", err)
	}
	version := objs[0].Version
	current := *inventoryData

	// Find and remove the itemID
	found := false
	newItems := make([]uint32, 0, len(current.Items))
	for _, id := range current.Items {
		if id == itemID {
			found = true
			continue // Skip the item to remove it
		}
		newItems = append(newItems, id)
	}

	if !found {
		// Item not in inventory, nothing to do
		return nil
	}

	data := InventoryData{Items: newItems}
	value, err := json.Marshal(data)
	if err != nil {
		LogError(ctx, logger, "Inventory marshal failed for removal", err)
		return fmt.Errorf("inventory marshal error: %w", err)
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      storageCollectionInventory,
			Key:             itemType,
			UserID:          userID,
			Value:           string(value),
			PermissionRead:  2,
			PermissionWrite: 0,
			Version:         version,
		},
	})
	if err != nil {
		LogError(ctx, logger, "Failed to write inventory update for removal", err)
		return fmt.Errorf("inventory write failed: %w", err)
	}

	return nil
}
