package items

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

func GrantLevelRewards(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, treeName string, level int, itemType string, itemID uint32) error {
	tree, exists := GetLevelTree(treeName)
	if !exists {
		return errors.ErrInvalidLevelTree
	}

	levelStr := strconv.Itoa(level)
	rewardData, exists := tree.Rewards[levelStr]
	if !exists {
		LogWarn(ctx, logger, "No reward configuration found for level")
		return nil
	}

	rewards := make(map[string]uint32)

	// currency rewards
	if rewardData.Gold != "" {
		val, err := ParseUint32Safely(rewardData.Gold, logger)
		if err != nil {
			return errors.ErrParse
		}
		rewards["gold"] = val
	}
	if rewardData.Gems != "" {
		val, err := ParseUint32Safely(rewardData.Gems, logger)
		if err != nil {
			return errors.ErrParse
		}
		rewards["gems"] = val
	}

	// progression rewards
	if rewardData.Abilities != "" {
		val, err := ParseUint32Safely(rewardData.Abilities, logger)
		if err != nil {
			return errors.ErrParse
		}
		if val > 0 {
			rewards["abilities"] = val
		}
	}
	if rewardData.Sprites != "" {
		val, err := ParseUint32Safely(rewardData.Sprites, logger)
		if err != nil {
			return errors.ErrParse
		}
		if val > 0 {
			rewards["sprites"] = val
		}
	}

	// item rewards
	if rewardData.Backgrounds != "" {
		val, err := ParseUint32Safely(rewardData.Backgrounds, logger)
		if err != nil {
			return errors.ErrParse
		}
		if val > 0 {
			rewards["backgrounds"] = val
		}
	}
	if rewardData.PieceStyles != "" {
		val, err := ParseUint32Safely(rewardData.PieceStyles, logger)
		if err != nil {
			return errors.ErrParse
		}
		if val > 0 {
			rewards["piece_styles"] = val
		}
	}
	if err := GrantRewardItems(ctx, nk, logger, userID, rewards, itemType, itemID); err != nil {
		LogError(ctx, logger, "Reward grant failed", err)
		return err
	}
	return nil
}

func GrantRewardItems(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, rewards map[string]uint32, itemType string, itemID uint32) error {
	writes := make([]*runtime.StorageWrite, 0)
	walletUpdates := make(map[string]int64)

	if !ValidateItemExists(itemType, itemID) {
		LogWarn(ctx, logger, "Invalid item ID for grant_reward_items")
		return errors.ErrInvalidItemID
	}

	for rewardType, amount := range rewards {
		switch rewardType {
		case "gold", "gems":
			walletUpdates[rewardType] = int64(amount)

		case "abilities", "sprites":
			var maxAbilitiesAvailable int
			var maxSpritesAvailable int
			var itemExists bool

			switch itemType {
			case "pet":
				if pet, exists := GetPet(itemID); exists {
					maxAbilitiesAvailable = len(pet.AbilityIDs)
					maxSpritesAvailable = pet.SpriteCount
					itemExists = true
				}
			case "class":
				if class, exists := GetClass(itemID); exists {
					maxAbilitiesAvailable = len(class.AbilityIDs)
					maxSpritesAvailable = class.SpriteCount
					itemExists = true
				}
			}

			if !itemExists {
				LogWarn(ctx, logger, "Attempted to grant rewards for non-existent item")
				continue
			}

			var progressionKey string
			switch itemType {
			case "pet":
				progressionKey = ProgressionKeyPet
			case "class":
				progressionKey = ProgressionKeyClass
			default:
				return errors.ErrInvalidItemType
			}

			err := UpdateProgressionAtomic(ctx, nk, logger, userID,
				progressionKey, itemID, func(prog *ItemProgression) error {
					currentUnlocked := prog.AbilitiesUnlocked
					maxAvailable := maxAbilitiesAvailable
					if rewardType == "sprites" {
						currentUnlocked = prog.SpritesUnlocked
						maxAvailable = maxSpritesAvailable
					}

					if maxAvailable <= 0 {
						return nil
					}

					newUnlocked := currentUnlocked + int(amount)
					if newUnlocked > maxAvailable {
						amount = uint32(maxAvailable - currentUnlocked)
					}
					if amount == 0 {
						return nil
					}

					switch rewardType {
					case "abilities":
						prog.AbilitiesUnlocked += int(amount)
					case "sprites":
						prog.SpritesUnlocked += int(amount)
					}

					return nil
				})

			if err != nil {
				LogError(ctx, logger, "Failed to update item progression for rewards", err)
				return err
			}

		case "backgrounds", "piece_styles":
			storageKey := storageKeyBackground
			if rewardType == "piece_styles" {
				storageKey = storageKeyPieceStyle
			}

			objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
				{Collection: storageCollectionInventory, Key: storageKey, UserID: userID},
			})
			if err != nil {
				LogError(ctx, logger, "Failed to read inventory for rewards", err)
				return fmt.Errorf("inventory read failed: %w", err)
			}

			var ownedItems InventoryData
			var version string

			if len(objects) > 0 {
				if err := json.Unmarshal([]byte(objects[0].Value), &ownedItems); err != nil {
					LogError(ctx, logger, "Failed to unmarshal inventory data", err)
					return fmt.Errorf("inventory unmarshal failed: %w", err)
				}
				version = objects[0].Version
			}

			rewardIDs := GetRewardItemIDs(itemType, itemID, rewardType, amount)
			newItems := make([]uint32, 0)

			for _, id := range rewardIDs {
				exists := false
				for _, owned := range ownedItems.Items {
					if owned == id {
						exists = true
						break
					}
				}
				if !exists {
					newItems = append(newItems, id)
				}
			}

			if len(newItems) > 0 {
				updatedItems := append(ownedItems.Items, newItems...)
				data := InventoryData{Items: updatedItems}
				value, err := json.Marshal(data)
				if err != nil {
					LogError(ctx, logger, "Failed to marshal inventory data", err)
					return err
				}

				writes = append(writes, &runtime.StorageWrite{
					Collection:      storageCollectionInventory,
					Key:             storageKey,
					UserID:          userID,
					Value:           string(value),
					PermissionRead:  2,
					PermissionWrite: 0,
					Version:         version,
				})

			}
		}
	}

	if len(walletUpdates) > 0 {
		if _, _, err := nk.WalletUpdate(ctx, userID, walletUpdates, map[string]interface{}{}, true); err != nil {
			LogError(ctx, logger, "Failed to update wallet with rewards", err)
			return err
		}
	}

	if len(writes) > 0 {
		if _, err := nk.StorageWrite(ctx, writes); err != nil {
			LogError(ctx, logger, "Failed to write inventory updates", err)
			return err
		}
	}

	return nil
}

func GetRewardItemIDs(itemType string, itemID uint32, rewardType string, amount uint32) []uint32 {
	var ids []uint32

	switch itemType {
	case "pet":
		if pet, exists := GetPet(uint32(itemID)); exists {
			switch rewardType {
			case "backgrounds":
				ids = pet.BackgroundIDs
			case "piece_styles":
				ids = pet.StyleIDs
			}
		}
	case "class":
		if class, exists := GetClass(uint32(itemID)); exists {
			switch rewardType {
			case "backgrounds":
				ids = class.BackgroundIDs
			case "piece_styles":
				ids = class.StyleIDs
			}
		}
	}
	if ids == nil {
		return []uint32{}
	}
	if len(ids) > int(amount) {
		return ids[:amount]
	}

	return ids
}
