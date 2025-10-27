package items

import (
	"block-server/errors"
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

// get equipped items
func RpcGetEquipment(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	// Fetch equipped items
	equipped := EquiptmentResponse{}

	equipmentReads := []*runtime.StorageRead{
		{Collection: storageCollectionEquipment, Key: storageKeyPet, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyClass, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyBackground, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyPieceStyle, UserID: userID},
	}

	equipmentObjs, err := nk.StorageRead(ctx, equipmentReads)
	if err == nil {
		for _, obj := range equipmentObjs {
			var data EquipmentData
			if err := json.Unmarshal([]byte(obj.Value), &data); err == nil {
				switch obj.Key {
				case storageKeyPet:
					equipped.Pet = data.ID
				case storageKeyClass:
					equipped.Class = data.ID
				case storageKeyBackground:
					equipped.Background = data.ID
				case storageKeyPieceStyle:
					equipped.PieceStyle = data.ID
				}
			}
		}
	}

	resp, err := json.Marshal(equipped)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(resp), nil
}

// get all items in inventory
func RpcGetInventory(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}
	inventory := InventoryResponse{
		Pets:        make([]uint32, 0),
		Classes:     make([]uint32, 0),
		Backgrounds: make([]uint32, 0),
		PieceStyles: make([]uint32, 0),
	}

	inventoryReads := []*runtime.StorageRead{
		{Collection: storageCollectionInventory, Key: storageKeyPet, UserID: userID},
		{Collection: storageCollectionInventory, Key: storageKeyClass, UserID: userID},
		{Collection: storageCollectionInventory, Key: storageKeyBackground, UserID: userID},
		{Collection: storageCollectionInventory, Key: storageKeyPieceStyle, UserID: userID},
	}

	objs, err := nk.StorageRead(ctx, inventoryReads)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":       userID,
			"collection": storageCollectionInventory,
			"error":      err.Error(),
		}).Error("Inventory storage read failure")
		return "", runtime.NewError("Inventory system unavailable", 13)
	}

	for _, obj := range objs {
		if obj == nil {
			continue
		}

		var data InventoryData
		if err := json.Unmarshal([]byte(obj.Value), &data); err == nil {
			switch obj.Key {
			case storageKeyPet:
				inventory.Pets = data.Items
			case storageKeyClass:
				inventory.Classes = data.Items
			case storageKeyBackground:
				inventory.Backgrounds = data.Items
			case storageKeyPieceStyle:
				inventory.PieceStyles = data.Items
			}
		}
	}
	resp, err := json.Marshal(inventory)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(resp), nil
}

// get all progression for all levelable items (pets/classes)
func RpcGetProgression(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	// Batch fetch progression keys
	reads := []*runtime.StorageRead{
		{Collection: storageCollectionProgression, UserID: userID},
	}

	objs, err := nk.StorageRead(ctx, reads)
	if err != nil {
		logger.Error("Progression read error: %v", err)
		return "", runtime.NewError("progression unavailable", 13)
	}

	progression := ProgressionResponse{
		Pets:    make(map[uint32]ItemProgression),
		Classes: make(map[uint32]ItemProgression),
	}

	for _, obj := range objs {
		var p ItemProgression
		if err := json.Unmarshal([]byte(obj.Value), &p); err != nil {
			continue
		}

		// Extract ID from storage key
		if after, ok := strings.CutPrefix(obj.Key, ProgressionKeyPet); ok {
			id, err := strconv.ParseUint(after, 10, 32)
			if err != nil {
				logger.Warn("Invalid pet progression ID: %s", after)
				continue
			}
			// Check if pet exists
			if _, exists := GetPet(uint32(id)); !exists {
				logger.Warn("No pet found for ID: %d", id)
				continue
			}
			progression.Pets[uint32(id)] = p
		} else if after, ok := strings.CutPrefix(obj.Key, ProgressionKeyClass); ok {
			id, err := strconv.ParseUint(after, 10, 32)
			if err != nil {
				logger.Warn("Invalid class progression ID: %s", after)
				continue
			}
			// Check if class exists
			if _, exists := GetClass(uint32(id)); !exists {
				logger.Warn("No class found for ID: %d", id)
				continue
			}
			progression.Classes[uint32(id)] = p
		}
	}

	resp, err := json.Marshal(progression)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(resp), nil
}

// equip abilities on items
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

// equip items
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
