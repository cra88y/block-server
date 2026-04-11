package items

import (
	"context"
	"fmt"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

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

	// Get item level tree for cost configurations
	tree, treeExists := GetPetLevelTree(req.PetID)
	if !treeExists {
		return "", errors.ErrInvalidPetID
	}

	xpPerUpgrade := tree.XpPerUpgrade
	if xpPerUpgrade <= 0 {
		xpPerUpgrade = 1000
	}
	costPerUpgrade := tree.CostPerUpgrade
	if costPerUpgrade <= 0 {
		costPerUpgrade = 1
	}
	costCurrency := tree.UpgradeCostCurrency
	if costCurrency == "" {
		costCurrency = "treats"
	}

	// Default to min cost if client didn't send count or sent 0 (count represents currency amount here)
	costAmount := int64(req.Count)
	if costAmount < 1 {
		costAmount = int64(costPerUpgrade)
	}

	// Prepare all writes atomically — bulk XP in one PrepareExperience call
	xpPerCurrency := float64(xpPerUpgrade) / float64(costPerUpgrade)
	xpAmount := uint32(float64(costAmount) * xpPerCurrency)

	newLevel, pending, err := PrepareExperience(ctx, nk, logger, userID, storageKeyPet, req.PetID, xpAmount)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":     userID,
			"petID":    req.PetID,
			"xp":       xpAmount,
			"cost":     costAmount,
			"currency": costCurrency,
			"error":    err.Error(),
			"action":   "use_pet_treat",
		}).Error("Failed to prepare pet XP")
		return "", errors.ErrPrepareFailed
	}

	// Deduct from dynamic cost currency in one wallet write
	pending.AddWalletDeduction(userID, costCurrency, costAmount)

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

	// Get item level tree for cost configurations
	tree, treeExists := GetClassLevelTree(req.ClassID)
	if !treeExists {
		return "", errors.ErrInvalidItemID
	}

	xpPerUpgrade := tree.XpPerUpgrade
	if xpPerUpgrade <= 0 {
		xpPerUpgrade = 1000
	}
	costPerUpgrade := tree.CostPerUpgrade
	if costPerUpgrade <= 0 {
		costPerUpgrade = 100
	}
	costCurrency := tree.UpgradeCostCurrency
	if costCurrency == "" {
		costCurrency = "gold"
	}

	// Default cost, can be made configurable
	costAmount := int64(costPerUpgrade)
	if req.Amount > 0 {
		costAmount = int64(req.Amount)
	}

	// XP granted based on config
	xpPerCurrency := float64(xpPerUpgrade) / float64(costPerUpgrade)
	xpAmount := uint32(float64(costAmount) * xpPerCurrency)

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

	// Add currency deduction to pending writes
	pending.AddWalletDeduction(userID, costCurrency, costAmount)

	// Commit all writes atomically via MultiUpdate
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":     userID,
			"classID":  req.ClassID,
			"cost":     costAmount,
			"currency": costCurrency,
			"error":    err.Error(),
			"action":   "use_gold_for_class_xp",
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
		"cost":     costAmount,
		"currency": costCurrency,
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

type ClaimRewardRequest struct {
	ItemType string `json:"item_type"` // "pets" (storageKeyPet) or "classes" (storageKeyClass)
	ItemID   uint32 `json:"item_id"`
	Level    int    `json:"level"`
}

// RpcClaimProgressionReward allows a player to manually claim a reward from the progression tree
func RpcClaimProgressionReward(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for reward claim")
		return "", errors.ErrNoUserIdFound
	}

	var req ClaimRewardRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "claim_progression_reward",
		}).Error("Failed to unmarshal claim reward request")
		return "", errors.ErrUnmarshal
	}

	if !ValidateItemExists(req.ItemType, req.ItemID) {
		logger.WithFields(map[string]interface{}{
			"user":     userID,
			"itemType": req.ItemType,
			"itemID":   req.ItemID,
			"action":   "claim_progression_reward",
		}).Error("Invalid item ID or type")
		return "", errors.ErrInvalidItemID
	}

	owned, err := IsItemOwned(ctx, nk, userID, req.ItemID, req.ItemType)
	if err != nil {
		return "", errors.ErrFailedCheckOwnership
	}
	if !owned {
		return "", errors.ErrItemNotOwnedForbidden
	}

	treeName, err := GetLevelTreeName(req.ItemType, req.ItemID)
	if err != nil {
		return "", errors.ErrInvalidConfig
	}

	var progressionKey string
	switch req.ItemType {
	case storageKeyPet:
		progressionKey = ProgressionKeyPet
	case storageKeyClass:
		progressionKey = ProgressionKeyClass
	default:
		return "", errors.ErrInvalidItemType
	}

	pending := NewPendingWrites()
	rewardFound := false

	// Use PrepareProgressionUpdate to safely modify UnclaimedRewards
	_, progWrite, err := PrepareProgressionUpdate(ctx, nk, logger, userID, progressionKey, req.ItemID, func(prog *ItemProgression) error {
		// Verify the level is actually unclaimed
		indexToRemove := -1
		for i, lvl := range prog.UnclaimedRewards {
			if lvl == req.Level {
				indexToRemove = i
				rewardFound = true
				break
			}
		}

		if !rewardFound {
			return errors.ErrRewardAlreadyClaimed
		}

		// Remove the claimed level from the array
		prog.UnclaimedRewards = append(prog.UnclaimedRewards[:indexToRemove], prog.UnclaimedRewards[indexToRemove+1:]...)
		return nil
	})

	if err != nil {
		return "", err
	}

	if !rewardFound {
		return "", errors.ErrRewardAlreadyClaimed
	}

	if progWrite != nil {
		pending.AddStorageWrite(progWrite)
	}

	// Actually prepare the rewards to be granted
	levelRewards, err := PrepareLevelRewards(ctx, nk, logger, userID, treeName, req.Level, req.ItemType, req.ItemID)
	if err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"level":  req.Level,
			"error":  err.Error(),
			"action": "claim_progression_reward",
		}).Error("Failed to prepare level rewards")
		return "", errors.ErrPrepareFailed
	}

	pending.Merge(levelRewards)

	// Commit everything atomically
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.WithFields(map[string]interface{}{
			"user":   userID,
			"error":  err.Error(),
			"action": "claim_progression_reward",
		}).Error("Failed to commit claim transaction")
		return "", errors.ErrTransactionFailed
	}

	// Return the payload for the UI toasts
	result := pending.Payload
	if result == nil {
		result = notify.NewRewardPayload("claim_reward")
	}
	result.Source = "claim_reward"
	result.ReasonKey = "reward.progression.claimed"

	respBytes, err := json.Marshal(result)
	if err != nil {
		return "", errors.ErrMarshal
	}

	logger.WithFields(map[string]interface{}{
		"user":     userID,
		"itemType": req.ItemType,
		"itemID":   req.ItemID,
		"level":    req.Level,
		"action":   "claim_progression_reward",
	}).Info("Progression reward claimed successfully")

	// Emit directly into the persistent analytics pipeline without network overhead
	telemetryData, _ := json.Marshal(map[string]interface{}{
		"itemType": req.ItemType,
		"itemId":   req.ItemID,
		"level":    req.Level,
		"reward":   result,
	})
	telemetryEvent := TelemetryEvent{
		EventType: "progression_claimed",
		Timestamp: float64(time.Now().Unix()),
		Data:      string(telemetryData),
	}

	// Fire and forget telemetry event via background context to unblock UI thread instantly
	go func() {
		if telErr := processTelemetryEvent(context.Background(), logger, db, nk, userID, telemetryEvent); telErr != nil {
			logger.Warn("Failed to record progression telemetry: %v", telErr)
		}
	}()

	return string(respBytes), nil
}

// RpcClaimAllProgressionRewards claims all unclaimed rewards for a given item.
func RpcClaimAllProgressionRewards(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		logger.Error("No user ID found in context for claim all rewards")
		return "", errors.ErrNoUserIdFound
	}

	var req ClaimRewardRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Error("Failed to unmarshal claim all rewards request: %v", err)
		return "", errors.ErrUnmarshal
	}

	if !ValidateItemExists(req.ItemType, req.ItemID) {
		return "", errors.ErrInvalidItemID
	}

	owned, err := IsItemOwned(ctx, nk, userID, req.ItemID, req.ItemType)
	if err != nil {
		return "", errors.ErrFailedCheckOwnership
	}
	if !owned {
		return "", errors.ErrItemNotOwnedForbidden
	}

	treeName, err := GetLevelTreeName(req.ItemType, req.ItemID)
	if err != nil {
		return "", errors.ErrInvalidConfig
	}

	var progressionKey string
	switch req.ItemType {
	case storageKeyPet:
		progressionKey = ProgressionKeyPet
	case storageKeyClass:
		progressionKey = ProgressionKeyClass
	default:
		return "", errors.ErrInvalidItemType
	}

	pending := NewPendingWrites()
	var levelsToClaim []int

	// Prepare progression update to claim all unclaimed rewards
	_, progWrite, err := PrepareProgressionUpdate(ctx, nk, logger, userID, progressionKey, req.ItemID, func(prog *ItemProgression) error {
		if len(prog.UnclaimedRewards) == 0 {
			return errors.ErrRewardAlreadyClaimed
		}
		// Safely copy levels out of closure
		levelsToClaim = append([]int(nil), prog.UnclaimedRewards...)
		prog.UnclaimedRewards = []int{}
		return nil
	})

	if err != nil {
		return "", err
	}

	if progWrite != nil {
		pending.AddStorageWrite(progWrite)
	}

	if len(levelsToClaim) == 0 {
		return "", errors.ErrRewardAlreadyClaimed
	}

	// Prepare rewards for all levels safely OUTSIDE the synchronous DB lock closure
	for _, level := range levelsToClaim {
		levelRewards, err := PrepareLevelRewards(ctx, nk, logger, userID, treeName, level, req.ItemType, req.ItemID)
		if err != nil {
			logger.Error("Failed to prepare level rewards: %v", err)
			return "", err
		}
		pending.Merge(levelRewards)
	}

	// Commit atomically
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("Failed to commit claim all rewards: %v", err)
		return "", errors.ErrTransactionFailed
	}

	result := pending.Payload
	if result == nil {
		result = notify.NewRewardPayload("claim_all_rewards")
	}
	result.Source = "claim_all_rewards"
	result.ReasonKey = "reward.progression.all_claimed"

	respBytes, err := json.Marshal(result)
	if err != nil {
		return "", errors.ErrMarshal
	}

	logger.Info("Claimed %d progression rewards for user=%s itemType=%s itemID=%d", len(levelsToClaim), userID, req.ItemType, req.ItemID)

	// Emit directly into the persistent analytics pipeline without network overhead
	telemetryData, _ := json.Marshal(map[string]interface{}{
		"itemType":    req.ItemType,
		"itemId":      req.ItemID,
		"levelsCount": len(levelsToClaim),
		"reward":      result,
	})
	telemetryEvent := TelemetryEvent{
		EventType: "progression_claimed_all",
		Timestamp: float64(time.Now().Unix()),
		Data:      string(telemetryData),
	}

	// Fire and forget telemetry event via background context to unblock UI thread instantly
	go func() {
		if telErr := processTelemetryEvent(context.Background(), logger, db, nk, userID, telemetryEvent); telErr != nil {
			logger.Warn("Failed to record batch progression telemetry: %v", telErr)
		}
	}()

	return string(respBytes), nil
}


func RpcGetUsersLoadouts(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req GetUsersLoadoutsPayload
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Error("Failed to parse get_users_loadouts payload: %v", err)
		return "", errors.ErrUnmarshal
	}

	if len(req.UserIDs) == 0 {
		return "{}", nil
	}

	loadouts := make(map[string]PlayerLoadout)

	for _, userID := range req.UserIDs {
		// Prepare reads for equipment
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
			}).Error("Equipment storage read failure in get_users_loadouts")
			continue
		}

		loadout := PlayerLoadout{
			PetID:        DefaultPetID,
			PetLevel:     1,
			ClassID:      DefaultClassID,
			ClassLevel:   1,
			BackgroundID: DefaultBackgroundID,
			ThemeID:      DefaultPieceStyleID,
		}

		for _, obj := range objs {
			if obj == nil {
				continue
			}

			var data EquipmentData
			if err := json.Unmarshal([]byte(obj.Value), &data); err == nil {
				switch obj.Key {
				case storageKeyPet:
					loadout.PetID = data.ID
				case storageKeyClass:
					loadout.ClassID = data.ID
				case storageKeyBackground:
					loadout.BackgroundID = data.ID
				case storageKeyPieceStyle:
					loadout.ThemeID = data.ID
				}
			}
		}

		petKey := fmt.Sprintf("%s%d", ProgressionKeyPet, loadout.PetID)
		classKey := fmt.Sprintf("%s%d", ProgressionKeyClass, loadout.ClassID)

		progReads := []*runtime.StorageRead{
			{Collection: storageCollectionProgression, Key: petKey, UserID: userID},
			{Collection: storageCollectionProgression, Key: classKey, UserID: userID},
		}

		progObjs, err := nk.StorageRead(ctx, progReads)
		if err == nil {
			for _, pObj := range progObjs {
				if pObj == nil {
					continue
				}
				var prog ItemProgression
				if err := json.Unmarshal([]byte(pObj.Value), &prog); err == nil {
					if pObj.Key == petKey {
						loadout.PetLevel = prog.Level
						if loadout.PetLevel < 1 { loadout.PetLevel = 1 }
						loadout.PetAbilityID = uint32(prog.EquippedAbility)
					} else if pObj.Key == classKey {
						loadout.ClassLevel = prog.Level
						if loadout.ClassLevel < 1 { loadout.ClassLevel = 1 }
						loadout.ClassAbilityID = uint32(prog.EquippedAbility)
					}
				}
			}
		}

		loadouts[userID] = loadout
	}

	resp, err := json.Marshal(loadouts)
	if err != nil {
		logger.Error("Failed to marshal get_users_loadouts response: %v", err)
		return "", errors.ErrMarshal
	}

	return string(resp), nil
}


