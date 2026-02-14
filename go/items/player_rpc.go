package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"block-server/errors"
	"block-server/notify"

	"github.com/heroiclabs/nakama-common/runtime"
)

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

func RpcGetInventory(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for get inventory")
		return "", errors.ErrNoUserIdFound
	}
	inventory, err := GetUserInventory(ctx, nk, logger, userID)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":       userID,
			"collection": storageCollectionInventory,
			"error":      err.Error(),
		}).Error("Inventory storage read failure")
		return "", errors.ErrInventoryUnavailable
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

	objects, err := listAllStorage(ctx, nk, logger, userID, storageCollectionProgression)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":  userID,
			"error": err.Error(),
		}).Error("Progression storage list failure")
		return "", errors.ErrProgressionUnavailable
	}

	if len(objects) == 0 {
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

	for _, obj := range objects {
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

		if after, ok := strings.CutPrefix(obj.Key, ProgressionKeyPet); ok {
			id, err := ParseUint32Safely(after, logger)
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"user":  userID,
					"key":   obj.Key,
					"error": err.Error(),
				}).Warn("Invalid pet progression ID")
				continue
			}
			if _, exists := GetPet(id); !exists {
				logger.WithFields(map[string]interface{}{
					"user":  userID,
					"petID": id,
				}).Warn("No pet found for progression ID")
				continue
			}
			progression.Pets[id] = p
		} else if after, ok := strings.CutPrefix(obj.Key, ProgressionKeyClass); ok {
			id, err := ParseUint32Safely(after, logger)
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"user":  userID,
					"key":   obj.Key,
					"error": err.Error(),
				}).Warn("Invalid class progression ID")
				continue
			}
			if _, exists := GetClass(id); !exists {
				logger.WithFields(map[string]interface{}{
					"user":    userID,
					"classID": id,
				}).Warn("No class found for progression ID")
				continue
			}
			progression.Classes[id] = p
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

func RpcEquipPetAbility(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
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

func RpcEquipClassAbility(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
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
func RpcEquipPet(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
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

func RpcEquipClass(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
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

func RpcEquipBackground(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
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

func RpcEquipPieceStyle(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
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

	if !ValidateItemExists(storageKeyPet, req.PetID) {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"petID":  req.PetID,
			"action": "use_pet_treat",
		}).Error("Invalid pet ID")
		return "", errors.ErrInvalidPetID
	}

	owned, err := IsItemOwned(ctx, nk, userID, req.PetID, storageKeyPet)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"petID":  req.PetID,
			"error":  err.Error(),
			"action": "use_pet_treat",
		}).Error("Failed to check pet ownership")
		return "", errors.ErrFailedCheckOwnership
	}
	if !owned {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"petID":  req.PetID,
			"action": "use_pet_treat",
		}).Warn("Attempted to use treat on unowned pet")
		return "", errors.ErrPetNotOwned
	}

	// Prepare all writes atomically
	xpAmount := uint32(1000) // Fixed XP amount per treat
	newLevel, pending, err := PrepareExperience(ctx, nk, logger, userID, storageKeyPet, req.PetID, xpAmount)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"petID":  req.PetID,
			"xp":     xpAmount,
			"error":  err.Error(),
			"action": "use_pet_treat",
		}).Error("Failed to prepare pet XP")
		return "", errors.ErrPrepareFailed
	}

	// Add treat deduction to pending writes
	pending.AddWalletDeduction(userID, "treats", 1)

	// Commit all writes atomically via MultiUpdate
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"petID":  req.PetID,
			"error":  err.Error(),
			"action": "use_pet_treat",
		}).Error("Failed to commit pet treat transaction")
		return "", errors.ErrTransactionFailed
	}

	// Build response payload
	result := pending.Payload
	if result == nil {
		result = notify.NewRewardPayload("pet_treat")
	}
	result.Source = "pet_treat"
	result.ReasonKey = "reward.pet_treat.used"

	if newLevel > 0 && result.Progression != nil {
		result.Progression.NewPetLevel = notify.IntPtr(newLevel)
	}

	logger.WithFields(map[string]interface{}{
		"user":     userID,
		"petID":    req.PetID,
		"xp":       xpAmount,
		"newLevel": newLevel,
		"action":   "use_pet_treat",
	}).Info("Pet treat used successfully")

	respBytes, err := json.Marshal(result)
	if err != nil {
		return "", errors.ErrMarshal
	}

	return string(respBytes), nil
}

// ClassXPRequest is the request payload for using gold to grant class XP
type ClassXPRequest struct {
	ClassID uint32 `json:"class_id"`
	Amount  int    `json:"amount"` // Amount of gold to spend (optional, defaults to 100)
}

// RpcUseGoldForClassXP spends gold to grant XP to a class
func RpcUseGoldForClassXP(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for class XP purchase")
		return "", errors.ErrNoUserIdFound
	}

	var req ClassXPRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "use_gold_for_class_xp",
		}).Error("Failed to unmarshal class XP request")
		return "", errors.ErrUnmarshal
	}

	if !ValidateItemExists(storageKeyClass, req.ClassID) {
		logger.WithFields(map[string]interface{}{
			"user":    userID,
			"classID": req.ClassID,
			"action":  "use_gold_for_class_xp",
		}).Error("Invalid class ID")
		return "", errors.ErrInvalidItemID
	}

	owned, err := IsItemOwned(ctx, nk, userID, req.ClassID, storageKeyClass)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":    userID,
			"classID": req.ClassID,
			"error":   err.Error(),
			"action":  "use_gold_for_class_xp",
		}).Error("Failed to check class ownership")
		return "", errors.ErrFailedCheckOwnership
	}
	if !owned {
		logger.WithFields(map[string]interface{}{
			"user":    userID,
			"classID": req.ClassID,
			"action":  "use_gold_for_class_xp",
		}).Warn("Attempted to grant XP to unowned class")
		return "", errors.ErrClassNotOwned
	}

	// Default gold cost, can be made configurable
	goldCost := int64(100)
	if req.Amount > 0 {
		goldCost = int64(req.Amount)
	}

	// XP granted per gold spent (10 XP per 1 gold)
	xpAmount := uint32(goldCost * 10)

	// Prepare all writes atomically
	newLevel, pending, err := PrepareExperience(ctx, nk, logger, userID, storageKeyClass, req.ClassID, xpAmount)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":    userID,
			"classID": req.ClassID,
			"xp":      xpAmount,
			"error":   err.Error(),
			"action":  "use_gold_for_class_xp",
		}).Error("Failed to prepare class XP")
		return "", errors.ErrPrepareFailed
	}

	// Add gold deduction to pending writes
	pending.AddWalletDeduction(userID, "gold", goldCost)

	// Commit all writes atomically via MultiUpdate
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":    userID,
			"classID": req.ClassID,
			"gold":    goldCost,
			"error":   err.Error(),
			"action":  "use_gold_for_class_xp",
		}).Error("Failed to commit class XP transaction")
		return "", errors.ErrTransactionFailed
	}

	// Build response payload
	result := pending.Payload
	if result == nil {
		result = notify.NewRewardPayload("class_training")
	}
	result.Source = "class_training"
	result.ReasonKey = "reward.class_training.complete"

	if newLevel > 0 && result.Progression != nil {
		result.Progression.NewClassLevel = notify.IntPtr(newLevel)
	}

	logger.WithFields(map[string]interface{}{
		"user":     userID,
		"classID":  req.ClassID,
		"gold":     goldCost,
		"xp":       xpAmount,
		"newLevel": newLevel,
		"action":   "use_gold_for_class_xp",
	}).Info("Gold used for class XP successfully")

	respBytes, err := json.Marshal(result)
	if err != nil {
		return "", errors.ErrMarshal
	}

	return string(respBytes), nil
}
