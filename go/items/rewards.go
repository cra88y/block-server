package items

import (
	"context"
	"encoding/json"
	"strconv"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

func GrantLevelRewards(ctx context.Context, nk runtime.NakamaModule, userID string, treeName string, level int, itemType string, itemID uint32) error {
	tree, exists := GetLevelTree(treeName)
	if !exists {
		return errors.ErrInvalidLevelTree
	}

	levelStr := strconv.Itoa(level)
	rewardData, exists := tree.Rewards[levelStr]
	if !exists {
		return nil
	}

	rewards := make(map[string]uint32)

	// Parse currency rewards
	if rewardData.Gold != "" {
		if val, err := strconv.ParseUint(rewardData.Gold, 10, 32); err == nil {
			rewards["gold"] = uint32(val)
		}
	}
	if rewardData.Gems != "" {
		if val, err := strconv.ParseUint(rewardData.Gems, 10, 32); err == nil {
			rewards["gems"] = uint32(val)
		}
	}

	// Parse progression rewards
	if rewardData.Abilities != "" {
		if val, err := strconv.ParseUint(rewardData.Abilities, 10, 32); err == nil && val > 0 {
			rewards["abilities"] = uint32(val)
		}
	}
	if rewardData.Sprites != "" {
		if val, err := strconv.ParseUint(rewardData.Sprites, 10, 32); err == nil && val > 0 {
			rewards["sprites"] = uint32(val)
		}
	}

	// Parse item rewards
	if rewardData.Backgrounds != "" {
		if val, err := strconv.ParseUint(rewardData.Backgrounds, 10, 32); err == nil && val > 0 {
			rewards["backgrounds"] = uint32(val)
		}
	}
	if rewardData.PieceStyles != "" {
		if val, err := strconv.ParseUint(rewardData.PieceStyles, 10, 32); err == nil && val > 0 {
			rewards["piece_styles"] = uint32(val)
		}
	}

	return GrantRewardItems(ctx, nk, userID, rewards, itemType, itemID)
}

func GrantRewardItems(ctx context.Context, nk runtime.NakamaModule, userID string, rewards map[string]uint32, itemType string, itemID uint32) error {
	writes := make([]*runtime.StorageWrite, 0)
	walletUpdates := make(map[string]int64)

	for rewardType, amount := range rewards {
		switch rewardType {
		case "gold", "gems":
			walletUpdates[rewardType] = int64(amount)

		case "abilities", "sprites":
			var maxAbilitiesAvailable int
			var maxSpritesAvailable int
			// var abilityIDs []uint32

			switch itemType {
			case "pet":
				if pet, exists := GetPet(itemID); exists {
					maxAbilitiesAvailable = len(pet.AbilityIDs)
					maxSpritesAvailable = pet.SpriteCount
				}
			case "class":
				if class, exists := GetClass(itemID); exists {
					maxAbilitiesAvailable = len(class.AbilityIDs)
					maxSpritesAvailable = class.SpriteCount
				}
			}

			var prog *ItemProgression
			var err error
			if itemType == "pet" {
				prog, err = GetItemProgression(ctx, nk, userID, ProgressionKeyPet, itemID)
			} else {
				prog, err = GetItemProgression(ctx, nk, userID, ProgressionKeyClass, itemID)
			}
			if err != nil {
				return err
			}

			currentUnlocked := prog.AbilitiesUnlocked
			maxAvailable := maxAbilitiesAvailable
			if rewardType == "sprites" {
				currentUnlocked = prog.SpritesUnlocked
				maxAvailable = maxSpritesAvailable
			}

			if maxAvailable <= 0 {
				continue
			}

			// Calculate actual unlock amount
			newUnlocked := currentUnlocked + int(amount)
			if newUnlocked > maxAvailable {
				amount = uint32(maxAvailable - currentUnlocked)
			}
			if amount == 0 {
				continue
			}

			// Update progression
			switch rewardType {
			case "abilities":
				prog.AbilitiesUnlocked += int(amount)

			case "sprites":
				prog.SpritesUnlocked += int(amount)

			}

			if itemType == "pet" {
				err = SaveItemProgression(ctx, nk, userID, ProgressionKeyPet, itemID, prog)
			} else {
				err = SaveItemProgression(ctx, nk, userID, ProgressionKeyClass, itemID, prog)
			}
			if err != nil {
				return err
			}

		case "backgrounds", "piece_styles":
			storageKey := storageKeyBackground
			if rewardType == "piece_styles" {
				storageKey = storageKeyPieceStyle
			}

			objects, _ := nk.StorageRead(ctx, []*runtime.StorageRead{
				{Collection: storageCollectionInventory, Key: storageKey, UserID: userID},
			})

			var ownedItems []uint32
			if len(objects) > 0 {
				if err := json.Unmarshal([]byte(objects[0].Value), &ownedItems); err != nil {
					return err
				}
			}

			// Get actual reward items
			rewardIDs := GetRewardItemIDs(itemType, itemID, rewardType, amount)
			newItems := make([]uint32, 0)

			for _, id := range rewardIDs {
				exists := false
				for _, owned := range ownedItems {
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
				// Add new items to inventory
				updatedItems := append(ownedItems, newItems...)
				itemsBytes, _ := json.Marshal(updatedItems)

				writes = append(writes, &runtime.StorageWrite{
					Collection:      storageCollectionInventory,
					Key:             storageKey,
					UserID:          userID,
					Value:           string(itemsBytes),
					PermissionRead:  1,
					PermissionWrite: 0,
				})
			}
		}
	}

	if len(walletUpdates) > 0 {
		if _, _, err := nk.WalletUpdate(ctx, userID, walletUpdates, nil, true); err != nil {
			return err
		}
	}

	if len(writes) > 0 {
		if _, err := nk.StorageWrite(ctx, writes); err != nil {
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

	if len(ids) > int(amount) {
		return ids[:amount]
	}
	return ids
}
