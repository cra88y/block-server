package items

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

// GetItemProgression retrieves a user's item progression for a specific item.
// If no progression is found, it initializes a new one.
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

	prog := &ItemProgression{}
	if err := json.Unmarshal([]byte(objects[0].Value), prog); err != nil {
		return nil, err
	}
	prog.Version = objects[0].Version
	return prog, nil
}

// SaveItemProgression saves the current state of an item's progression.
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

// CalculateLevel determines the current level based on the provided experience points and a level tree.
func CalculateLevel(treeName string, exp int) (int, error) {
	tree, exists := GetLevelTree(treeName)
	if !exists {
		return 0, errors.ErrInvalidLevelTree
	}

	if exp < 0 {
		return 1, nil
	}

	if exp >= tree.LevelThresholds[tree.MaxLevel] {
		return tree.MaxLevel, nil
	}

	thresholds := tree.LevelThresholds
	low, high := 1, tree.MaxLevel
	if len(thresholds) < high {
		return 0, errors.ErrInvalidLevelThresholds
	}
	// Binary search to find the level
	for low <= high {
		mid := (low + high) / 2
		if exp >= thresholds[mid] { // If current experience is greater than or equal to the threshold for mid-level
			low = mid + 1 // Move to the upper half
		} else {
			high = mid - 1 // Move to the lower half
		}
	}

	return high, nil
}

// GetPet retrieves a pet by its ID from the game data.
func GetPet(id uint32) (*Pet, bool) {
	pet, exists := GameData.Pets[id]
	return pet, exists
}

// GetClass retrieves a class by its ID from the game data.
func GetClass(id uint32) (*Class, bool) {
	class, exists := GameData.Classes[id]
	return class, exists
}

// GetLevelTree retrieves a level tree by its name from the game data.
func GetLevelTree(name string) (LevelTree, bool) {
	tree, exists := GameData.LevelTrees[name]
	return tree, exists
}

// GetPetLevelTree retrieves the level tree associated with a specific pet.
func GetPetLevelTree(petID uint32) (LevelTree, bool) {
	if pet, exists := GetPet(petID); exists {
		return GetLevelTree(pet.LevelTreeName)
	}
	return LevelTree{}, false
}

// GetClassLevelTree retrieves the level tree associated with a specific class.
func GetClassLevelTree(classID uint32) (LevelTree, bool) {
	if class, exists := GetClass(classID); exists {
		return GetLevelTree(class.LevelTreeName)
	}
	return LevelTree{}, false
}

// GetLevelTreeName retrieves the name of the level tree for a given item category and ID.
func GetLevelTreeName(category string, id uint32) (string, error) {
	switch category {
	case storageKeyPet:
		if pet, exists := GameData.Pets[id]; exists {
			return pet.LevelTreeName, nil
		}
	case storageKeyClass:
		if class, exists := GameData.Classes[id]; exists {
			return class.LevelTreeName, nil
		}
	default:
		return "", errors.ErrNoCategory
	}
	return "", errors.ErrInvalidItem
}

// ValidateItemExists checks if an item exists within a given category.
func ValidateItemExists(category string, id uint32) bool {
	switch category {
	case storageKeyPet:
		_, exists := GameData.Pets[id]
		return exists
	case storageKeyClass:
		_, exists := GameData.Classes[id]
		return exists
	case storageKeyBackground:
		_, exists := GameData.Backgrounds[id]
		return exists
	case storageKeyPieceStyle:
		_, exists := GameData.PieceStyles[id]
		return exists
	default:
		return false
	}

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

	// Apply the update function to the retrieved progression
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
