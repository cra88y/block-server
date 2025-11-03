package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

// get equipped items
func RpcGetEquipment(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for get equipment")
		return "", errors.ErrNoUserIdFound
	}

	equipped := EquipmentResponse{
		Pet:        DefaultPetID,
		Class:      DefaultClassID,
		Background: DefaultBackgroundID,
		PieceStyle: DefaultPieceStyleID,
	}

	reads := []*runtime.StorageRead{
		{Collection: storageCollectionEquipment, Key: storageKeyPet, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyClass, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyBackground, UserID: userID},
		{Collection: storageCollectionEquipment, Key: storageKeyPieceStyle, UserID: userID},
	}

	objs, err := nk.StorageRead(ctx, reads)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Equipment storage read failure")
		return "", errors.ErrEquipmentUnavailable
	}

	for _, obj := range objs {
		if obj == nil {
			continue
		}

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
		} else {
			logger.WithFields(map[string]interface{}{
				"user":  userID,
				"key":   obj.Key,
				"error": err.Error(),
			}).Warn("Failed to unmarshal equipment data")
		}
	}

	resp, err := json.Marshal(equipped)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Failed to marshal equipment response")
		return "", errors.ErrMarshal
	}

	return string(resp), nil
}

// get all items in inventory
func RpcGetInventory(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for get inventory")
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
		return "", errors.ErrInventoryUnavailable
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
		} else {
			logger.WithFields(map[string]interface{}{
				"user":  userID,
				"key":   obj.Key,
				"error": err.Error(),
			}).Warn("Failed to unmarshal inventory data")
		}
	}

	resp, err := json.Marshal(inventory)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Failed to marshal inventory response")
		return "", errors.ErrMarshal
	}

	return string(resp), nil
}

func RpcGetProgression(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for get progression")
		return "", errors.ErrNoUserIdFound
	}

	progression := ProgressionResponse{
		Pets:    make(map[uint32]ItemProgression),
		Classes: make(map[uint32]ItemProgression),
	}

	// List all progression storage objects first
	objects, _, err := nk.StorageList(ctx, "", userID, storageCollectionProgression, 100, "")
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Progression storage list failure")
		return "", errors.ErrProgressionUnavailable
	}

	// Build StorageRead operations for each found object
	if len(objects) == 0 {
		// No progression data found, return empty response
		resp, err := json.Marshal(progression)
		if err != nil {
			logger.WithFields(map[string]interface{}{
				"user":  userID,
				"error": err.Error(),
			}).Error("Failed to marshal empty progression response")
			return "", errors.ErrMarshal
		}
		return string(resp), nil
	}

	reads := make([]*runtime.StorageRead, 0, len(objects))
	for _, obj := range objects {
		reads = append(reads, &runtime.StorageRead{
			Collection: storageCollectionProgression,
			Key:        obj.Key,
			UserID:     userID,
		})
	}

	objs, err := nk.StorageRead(ctx, reads)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Progression storage read failure")
		return "", errors.ErrProgressionUnavailable
	}

	for _, obj := range objs {
		if obj == nil {
			continue
		}

		var p ItemProgression
		if err := json.Unmarshal([]byte(obj.Value), &p); err != nil {
			logger.WithFields(map[string]interface{}{
				"user":  userID,
				"key":   obj.Key,
				"error": err.Error(),
			}).Warn("Failed to unmarshal progression data")
			continue
		}

		// Extract ID from storage key
		if after, ok := strings.CutPrefix(obj.Key, ProgressionKeyPet); ok {
			id, err := strconv.ParseUint(after, 10, 32)
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"user":  userID,
					"key":   obj.Key,
					"error": err.Error(),
				}).Warn("Invalid pet progression ID")
				continue
			}
			// Check if pet exists
			if _, exists := GetPet(uint32(id)); !exists {
				logger.WithFields(map[string]interface{}{
					"user":  userID,
					"petID": id,
				}).Warn("No pet found for progression ID")
				continue
			}
			progression.Pets[uint32(id)] = p
		} else if after, ok := strings.CutPrefix(obj.Key, ProgressionKeyClass); ok {
			id, err := strconv.ParseUint(after, 10, 32)
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"user":  userID,
					"key":   obj.Key,
					"error": err.Error(),
				}).Warn("Invalid class progression ID")
				continue
			}
			// Check if class exists
			if _, exists := GetClass(uint32(id)); !exists {
				logger.WithFields(map[string]interface{}{
					"user":    userID,
					"classID": id,
				}).Warn("No class found for progression ID")
				continue
			}
			progression.Classes[uint32(id)] = p
		}
	}

	resp, err := json.Marshal(progression)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Failed to marshal progression response")
		return "", errors.ErrMarshal
	}

	return string(resp), nil
}

// equip abilities on items
func RpcEquipPetAbility(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for pet ability equip")
		return "", errors.ErrNoUserIdFound
	}

	if err := EquipAbility(ctx, logger, nk, storageKeyPet, payload); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "equip_pet_ability",
		}).Error("Failed to equip pet ability")
		return "", errors.ErrCouldNotEquipAbility
	}
	return `{"success": true}`, nil
}

func RpcEquipClassAbility(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for class ability equip")
		return "", errors.ErrNoUserIdFound
	}

	if err := EquipAbility(ctx, logger, nk, storageKeyClass, payload); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "equip_class_ability",
		}).Error("Failed to equip class ability")
		return "", errors.ErrCouldNotEquipAbility
	}
	return `{"success": true}`, nil
}

// equip items
func RpcEquipPet(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for pet equip")
		return "", errors.ErrNoUserIdFound
	}

	if err := EquipItem(ctx, logger, nk, storageKeyPet, payload); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "equip_pet",
		}).Error("Failed to equip pet")
		return "", errors.ErrCouldNotEquipItem
	}

	return `{"success": true}`, nil
}

func RpcEquipClass(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for class equip")
		return "", errors.ErrNoUserIdFound
	}

	if err := EquipItem(ctx, logger, nk, storageKeyClass, payload); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "equip_class",
		}).Error("Failed to equip class")
		return "", errors.ErrCouldNotEquipClass
	}

	return `{"success": true}`, nil
}

func RpcEquipBackground(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for background equip")
		return "", errors.ErrNoUserIdFound
	}

	if err := EquipItem(ctx, logger, nk, storageKeyBackground, payload); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "equip_background",
		}).Error("Failed to equip background")
		return "", errors.ErrCouldNotEquipBackground
	}

	return `{"success": true}`, nil
}

func RpcEquipPieceStyle(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for piece style equip")
		return "", errors.ErrNoUserIdFound
	}

	if err := EquipItem(ctx, logger, nk, storageKeyPieceStyle, payload); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "equip_piece_style",
		}).Error("Failed to equip piece style")
		return "", errors.ErrCouldNotEquipStyle
	}

	return `{"success": true}`, nil
}

// use pet treat to grant xp to a pet
func RpcUsePetTreat(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for pet treat usage")
		return "", errors.ErrNoUserIdFound
	}

	var req PetTreatRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "use_pet_treat",
		}).Error("Failed to unmarshal pet treat request")
		return "", errors.ErrUnmarshal
	}

	// Validate pet exists
	if !ValidateItemExists(storageKeyPet, req.PetID) {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"petID":  req.PetID,
			"action": "use_pet_treat",
		}).Error("Invalid pet ID")
		return "", runtime.NewError("invalid pet ID", 3)
	}

	// Check if pet is owned
	owned, err := IsItemOwned(ctx, nk, userID, req.PetID, storageKeyPet)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"petID":  req.PetID,
			"error":  err.Error(),
			"action": "use_pet_treat",
		}).Error("Failed to check pet ownership")
		return "", runtime.NewError("failed to check pet ownership", 13)
	}
	if !owned {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"petID":  req.PetID,
			"action": "use_pet_treat",
		}).Warn("Attempted to use treat on unowned pet")
		return "", runtime.NewError("pet not owned", 403)
	}

	// Attempt to deduct one pet treat
	walletUpdates := map[string]int64{
		"pet_treat": -1,
	}

	// todo: update to use MultiUpdate / rollback on fail
	if _, _, err := nk.WalletUpdate(ctx, userID, walletUpdates, map[string]interface{}{"action": "use_pet_treat"}, true); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "use_pet_treat",
		}).Warn("Failed to deduct pet treat - likely insufficient balance")
		return "", runtime.NewError("insufficient pet treats", 3)
	}

	// Grant XP
	xpAmount := uint32(1000) // Fixed XP amount per treat
	if err := AddPetExp(ctx, nk, logger, userID, req.PetID, xpAmount); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"petID":  req.PetID,
			"xp":     xpAmount,
			"error":  err.Error(),
			"action": "use_pet_treat",
		}).Error("Failed to grant pet XP")
		return "", runtime.NewError("failed to grant pet XP", 13)
	}

	logger.WithFields(map[string]interface{}{
		"user":   userID,
		"petID":  req.PetID,
		"xp":     xpAmount,
		"action": "use_pet_treat",
	}).Info("Pet treat used successfully")

	return `{"success": true, "xp_granted": 1000}`, nil
}
