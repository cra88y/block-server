package items

import (
	"context"
	"encoding/json"
	"strconv"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

func GetItemProgression(ctx context.Context, nk runtime.NakamaModule, userID string, ProgressionKey string, itemID uint32) (*ItemProgression, error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: storageCollectionProgression, Key: ProgressionKey + strconv.Itoa(int(itemID)), UserID: userID},
	})

	if err != nil || len(objects) == 0 {
		return &ItemProgression{
			Level:             1,
			AbilitiesUnlocked: 1,
			SpritesUnlocked:   1,
		}, nil
	}

	var prog ItemProgression
	if err := json.Unmarshal([]byte(objects[0].Value), &prog); err != nil {
		return nil, errors.ErrUnmarshal
	}
	return &prog, nil
}
func SaveItemProgression(ctx context.Context, nk runtime.NakamaModule, userID string, ProgressionKey string, itemID uint32, prog *ItemProgression) error {

	key := ProgressionKeyPet + strconv.Itoa(int(itemID))

	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: storageCollectionProgression, Key: key, UserID: userID},
	})
	if err != nil {
		return err
	}

	version := ""
	if len(objects) > 0 {
		version = objects[0].Version
	}

	value, err := json.Marshal(prog)
	if err != nil {
		return errors.ErrMarshal
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: storageCollectionProgression,
			Key:        key,
			UserID:     userID,
			Value:      string(value),
			Version:    version,
		},
	})
	return err
}

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
	// binary search for threshold
	for low <= high {
		mid := (low + high) / 2
		if exp >= thresholds[mid] {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	return high, nil
}

func GetPet(id uint32) (Pet, bool) {
	pet, exists := GameData.Pets[id]
	return pet, exists
}

func GetClass(id uint32) (Class, bool) {
	class, exists := GameData.Classes[id]
	return class, exists
}

func GetLevelTree(name string) (LevelTree, bool) {
	tree, exists := GameData.LevelTrees[name]
	return tree, exists
}

func GetPetLevelTree(petID uint32) (LevelTree, bool) {
	if pet, exists := GetPet(petID); exists {
		return GetLevelTree(pet.LevelTreeName)
	}
	return LevelTree{}, false
}

func GetClassLevelTree(classID uint32) (LevelTree, bool) {
	if class, exists := GetClass(classID); exists {
		return GetLevelTree(class.LevelTreeName)
	}
	return LevelTree{}, false
}

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
