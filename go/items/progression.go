package items

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Core progression operations

func GetItemProgression(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger,
	userID string, keyPrefix string, itemID uint32) (*ItemProgression, error) {
	key := fmt.Sprintf("%s%d", keyPrefix, itemID)
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: storageCollectionProgression,
			Key:        key,
			UserID:     userID,
		},
	})
	if err != nil {
		return nil, err
	}

	if len(objects) == 0 {
		return InitializeProgression(ctx, nk, logger, userID, keyPrefix, itemID)
	}

	prog, err := UnmarshalJSON[ItemProgression](objects[0].Value)
	if err != nil {
		return nil, fmt.Errorf("progression load: %w", err)
	}
	prog.Version = objects[0].Version
	return prog, nil
}

func SaveItemProgression(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, progressionKey string, itemID uint32, prog *ItemProgression) error {

	key := progressionKey + strconv.Itoa(int(itemID))

	value, err := json.Marshal(prog)
	if err != nil {
		logger.Error("error saving item progression %v", err)
		return errors.ErrMarshal
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      storageCollectionProgression,
			Key:             key,
			UserID:          userID,
			Value:           string(value),
			Version:         prog.Version,
			PermissionRead:  2,
			PermissionWrite: 0,
		},
	})
	return err
}

func UpdateProgressionAtomic(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger,
	userID string, progressionKey string, itemID uint32, updateFunc func(*ItemProgression) error) error {

	prog, err := GetItemProgression(ctx, nk, logger, userID, progressionKey, itemID)
	if err != nil {
		LogWithUser(ctx, logger, "error", "Failed to read progression for update", map[string]interface{}{
			"error":          err,
			"progressionKey": progressionKey,
			"itemID":         itemID,
		})
		return err
	}

	if err := updateFunc(prog); err != nil {
		LogWithUser(ctx, logger, "error", "Failed to apply progression update", map[string]interface{}{
			"error":          err,
			"progressionKey": progressionKey,
			"itemID":         itemID,
		})
		return err
	}

	err = SaveItemProgression(ctx, nk, logger, userID, progressionKey, itemID, prog)
	if err != nil {
		LogWithUser(ctx, logger, "error", "Failed to save progression", map[string]interface{}{
			"error":          err,
			"progressionKey": progressionKey,
			"itemID":         itemID,
		})
		return fmt.Errorf("failed to save progression: %w", err)
	}

	return nil
}

// Progression Initialization

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

// BatchProgressionInitialization initializes multiple progression records in a single operation
// This optimized version batches database writes for better performance during verification
func BatchInitializeProgression(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, progressionRecords []struct {
	ProgressionKey string
	ItemID         uint32
}) error {
	if len(progressionRecords) == 0 {
		return nil
	}

	// Validate all records before attempting batch operation to prevent partial failures
	for _, record := range progressionRecords {
		var itemType string
		switch record.ProgressionKey {
		case ProgressionKeyPet:
			itemType = storageKeyPet
		case ProgressionKeyClass:
			itemType = storageKeyClass
		default:
			return fmt.Errorf("unsupported progression key: %s", record.ProgressionKey)
		}
		
		if !ValidateItemExists(itemType, record.ItemID) {
			return fmt.Errorf("invalid item ID %d for item type %s", record.ItemID, itemType)
		}
	}

	writes := make([]*runtime.StorageWrite, 0, len(progressionRecords))
	
	for _, record := range progressionRecords {
		key := record.ProgressionKey + strconv.Itoa(int(record.ItemID))
		defaultProg := DefaultProgression()
		
		value, err := json.Marshal(defaultProg)
		if err != nil {
			logVerificationIssue(ctx, logger, "error", 
				fmt.Sprintf("Failed to marshal progression for batch initialization"),
				"", record.ItemID, userID, "batch_marshal", err)
			return fmt.Errorf("failed to marshal progression for item %d: %w", record.ItemID, err)
		}
		
		writes = append(writes, &runtime.StorageWrite{
			Collection:      storageCollectionProgression,
			Key:             key,
			UserID:          userID,
			Value:           string(value),
			PermissionRead:  2,
			PermissionWrite: 0,
		})
	}

	// Single atomic batch write ensures all progression records are created consistently
	_, err := nk.StorageWrite(ctx, writes)
	if err != nil {
		logVerificationIssue(ctx, logger, "error", 
			fmt.Sprintf("Failed to write batch progression records"),
			"", 0, userID, "batch_write", err)
		return fmt.Errorf("batch progression write failed: %w", err)
	}

	return nil
}

// Progression Verification System

type ProgressionVerificationReport struct {
	PetRepairs       map[uint32]string `json:"pet_repairs"`
	ClassRepairs     map[uint32]string `json:"class_repairs"`
	TotalFixed       int               `json:"total_fixed"`
	VerificationTime time.Time         `json:"verification_time"`
}

// Verification logging helpers

func logVerificationIssue(ctx context.Context, logger runtime.Logger, level, message, itemType string, 
	itemID uint32, userID string, repairAction string, err error) {
	fields := map[string]interface{}{
		"user_id":   userID,
		"item_type": itemType,
		"item_id":   itemID,
		"action":    repairAction,
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	LogWithUser(ctx, logger, level, message, fields)
}

func VerifyAndFixUserProgression(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) (*ProgressionVerificationReport, error) {
	report := &ProgressionVerificationReport{
		PetRepairs:       make(map[uint32]string),
		ClassRepairs:     make(map[uint32]string),
		VerificationTime: time.Now(),
	}

	inventory, err := GetUserInventory(ctx, nk, logger, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user inventory: %w", err)
	}

	existingProgression, err := GetUserProgression(ctx, nk, logger, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user progression: %w", err)
	}

	petFixes, err := verifyAndFixItemProgression(ctx, nk, logger, userID, storageKeyPet, inventory.Pets, existingProgression.Pets, ProgressionKeyPet)
	if err != nil {
		logVerificationIssue(ctx, logger, "error", "Failed to verify pet progression", 
			"pet", 0, userID, "verify_pet_progression", err)
	} else {
		report.PetRepairs = petFixes
	}

	classFixes, err := verifyAndFixItemProgression(ctx, nk, logger, userID, storageKeyClass, inventory.Classes, existingProgression.Classes, ProgressionKeyClass)
	if err != nil {
		logVerificationIssue(ctx, logger, "error", "Failed to verify class progression", 
			"class", 0, userID, "verify_class_progression", err)
	} else {
		report.ClassRepairs = classFixes
	}

	report.TotalFixed = len(report.PetRepairs) + len(report.ClassRepairs)

	if report.TotalFixed > 0 {
		LogWithUser(ctx, logger, "info", "Progression verification completed with repairs", map[string]interface{}{
			"user_id":       userID,
			"total_fixed":   report.TotalFixed,
			"pet_repairs":   len(report.PetRepairs),
			"class_repairs": len(report.ClassRepairs),
		})
	} else {
		LogWithUser(ctx, logger, "debug", "Progression verification completed - no issues found", map[string]interface{}{
			"user_id": userID,
		})
	}

	return report, nil
}

func verifyAndFixItemProgression(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string,
	itemType string, inventoryItems []uint32, existingProgression map[uint32]ItemProgression, progressionKeyPrefix string) (map[uint32]string, error) {

	repairs := make(map[uint32]string)
	var progressionRecords []struct {
		ProgressionKey string
		ItemID         uint32
	}

	for _, itemID := range inventoryItems {
		if _, exists := existingProgression[itemID]; !exists {
			if !ValidateItemExists(itemType, itemID) {
				// Remove invalid item from inventory
				if err := RemoveItemFromInventory(ctx, nk, logger, userID, itemType, itemID); err != nil {
					logVerificationIssue(ctx, logger, "error",
						fmt.Sprintf("Failed to remove invalid %s ID %d", itemType, itemID),
						itemType, itemID, userID, "remove_invalid_item", err)
					repairs[itemID] = "failed_to_remove"
				} else {
					logVerificationIssue(ctx, logger, "warn",
						fmt.Sprintf("Removed invalid %s ID %d from inventory", itemType, itemID),
						itemType, itemID, userID, "removed_invalid_item", nil)
					repairs[itemID] = "removed_invalid_item"
				}
				continue
			}

			var progressionKey string
			switch itemType {
			case storageKeyPet:
				progressionKey = ProgressionKeyPet
			case storageKeyClass:
				progressionKey = ProgressionKeyClass
			default:
				repairs[itemID] = "unsupported_item_type"
				continue
			}

			progressionRecords = append(progressionRecords, struct {
				ProgressionKey string
				ItemID         uint32
			}{
				ProgressionKey: progressionKey,
				ItemID:         itemID,
			})
		}
	}

	// Use optimized batch operation to create all missing progression records efficiently
	if len(progressionRecords) > 0 {
		if err := BatchInitializeProgression(ctx, nk, logger, userID, progressionRecords); err != nil {
			logVerificationIssue(ctx, logger, "error", 
				fmt.Sprintf("Failed to initialize missing progression records for %s", itemType),
				itemType, 0, userID, "batch_initialize_progression", err)
			
			// Mark all records as failed for verification tracking and error analysis
			for _, record := range progressionRecords {
				repairs[record.ItemID] = fmt.Sprintf("failed_to_initialize: %v", err)
			}
		} else {
			// Mark all records as successfully initialized for verification tracking
			for _, record := range progressionRecords {
				repairs[record.ItemID] = "initialized_missing_progression"
				logVerificationIssue(ctx, logger, "info", 
					fmt.Sprintf("Initialized missing progression record for %s ID %d", itemType, record.ItemID),
					itemType, record.ItemID, userID, "progression_initialized", nil)
			}
		}
	}

	return repairs, nil
}