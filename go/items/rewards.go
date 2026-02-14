package items

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"block-server/errors"
	"block-server/notify"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Category constants — distinct from storage keys (storageKeyPet="pets", storageKeyClass="classes").
const (
	CategoryPet   = "pet"
	CategoryClass = "class"
)

// PrepareLevelRewards prepares all rewards for a specific level without committing.
// Returns *PendingWrites with all writes needed for level rewards.
// Caller should merge into their pending writes and commit via MultiUpdate.
func PrepareLevelRewards(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, treeName string, level int, itemType string, itemID uint32) (*PendingWrites, error) {
	tree, exists := GetLevelTree(treeName)
	if !exists {
		return nil, errors.ErrInvalidLevelTree
	}

	levelStr := strconv.Itoa(level)
	rewardData, exists := tree.Rewards[levelStr]
	if !exists {
		// No rewards for this level
		return nil, nil
	}

	rewards := make(map[string]uint32)

	// currency rewards
	if rewardData.Gold != "" {
		val, err := ParseUint32Safely(rewardData.Gold, logger)
		if err != nil {
			return nil, errors.ErrParse
		}
		rewards["gold"] = val
	}
	if rewardData.Gems != "" {
		val, err := ParseUint32Safely(rewardData.Gems, logger)
		if err != nil {
			return nil, errors.ErrParse
		}
		rewards["gems"] = val
	}

	// progression rewards
	if rewardData.Abilities != "" {
		val, err := ParseUint32Safely(rewardData.Abilities, logger)
		if err != nil {
			return nil, errors.ErrParse
		}
		if val > 0 {
			rewards["abilities"] = val
		}
	}
	if rewardData.Sprites != "" {
		val, err := ParseUint32Safely(rewardData.Sprites, logger)
		if err != nil {
			return nil, errors.ErrParse
		}
		if val > 0 {
			rewards["sprites"] = val
		}
	}

	// item rewards
	if rewardData.Backgrounds != "" {
		val, err := ParseUint32Safely(rewardData.Backgrounds, logger)
		if err != nil {
			return nil, errors.ErrParse
		}
		if val > 0 {
			rewards["backgrounds"] = val
		}
	}
	if rewardData.PieceStyles != "" {
		val, err := ParseUint32Safely(rewardData.PieceStyles, logger)
		if err != nil {
			return nil, errors.ErrParse
		}
		if val > 0 {
			rewards["piece_styles"] = val
		}
	}

	pending, err := PrepareRewardItems(ctx, nk, logger, userID, rewards, itemType, itemID)
	if err != nil {
		LogError(ctx, logger, "Reward prepare failed", err)
		return nil, err
	}

	// Add progression unlocks to payload
	if pending != nil && pending.Payload != nil {
		var unlocks []notify.ProgressionUnlock

		if abilities, ok := rewards["abilities"]; ok && abilities > 0 {
			unlocks = append(unlocks, notify.ProgressionUnlock{
				System: itemType,
				ItemID: itemID,
				Type:   "ability",
				Count:  int(abilities),
			})
		}
		if sprites, ok := rewards["sprites"]; ok && sprites > 0 {
			unlocks = append(unlocks, notify.ProgressionUnlock{
				System: itemType,
				ItemID: itemID,
				Type:   "sprite",
				Count:  int(sprites),
			})
		}

		if len(unlocks) > 0 {
			if pending.Payload.Progression == nil {
				pending.Payload.Progression = &notify.ProgressionDelta{}
			}
			pending.Payload.Progression.Unlocks = unlocks
		}
	}

	return pending, nil
}

// PrepareRewardItems prepares currency and item rewards without committing.
// Returns *PendingWrites with all writes needed. Caller commits via MultiUpdate.
func PrepareRewardItems(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, rewards map[string]uint32, itemType string, itemID uint32) (*PendingWrites, error) {
	pending := NewPendingWrites()
	walletChanges := make(map[string]int64)
	grantedItems := make([]notify.ItemGrant, 0)

	if !ValidateItemExists(itemType, itemID) {
		LogWarn(ctx, logger, "Invalid item ID for prepare_reward_items")
		return nil, errors.ErrInvalidItemID
	}

	for rewardType, amount := range rewards {
		switch rewardType {
		case "gold", "gems":
			walletChanges[rewardType] = int64(amount)

		case "abilities", "sprites":
			var maxAbilitiesAvailable int
			var maxSpritesAvailable int
			var itemExists bool

			switch itemType {
			case CategoryPet:
				if pet, exists := GetPet(itemID); exists {
					maxAbilitiesAvailable = len(pet.AbilityIDs)
					maxSpritesAvailable = pet.SpriteCount
					itemExists = true
				}
			case CategoryClass:
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
			case CategoryPet:
				progressionKey = ProgressionKeyPet
			case CategoryClass:
				progressionKey = ProgressionKeyClass
			default:
				return nil, errors.ErrInvalidItemType
			}

			// Prepare progression update without committing
			_, progWrite, err := PrepareProgressionUpdate(ctx, nk, logger, userID,
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
				LogError(ctx, logger, "Failed to prepare item progression for rewards", err)
				return nil, err
			}
			if progWrite != nil {
				pending.AddStorageWrite(progWrite)
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
				return nil, fmt.Errorf("inventory read failed: %w", err)
			}

			var ownedItems InventoryData
			var version string

			if len(objects) > 0 {
				if err := json.Unmarshal([]byte(objects[0].Value), &ownedItems); err != nil {
					LogError(ctx, logger, "Failed to unmarshal inventory data", err)
					return nil, fmt.Errorf("inventory unmarshal failed: %w", err)
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
					grantedItems = append(grantedItems, notify.ItemGrant{
						ID:   id,
						Type: rewardType,
					})
				}
			}

			if len(newItems) > 0 {
				updatedItems := append(ownedItems.Items, newItems...)
				write, err := BuildInventoryWrite(userID, storageKey, updatedItems, version)
				if err != nil {
					LogError(ctx, logger, "Failed to build inventory write", err)
					return nil, err
				}
				pending.AddStorageWrite(write)
			}
		}
	}

	// Add wallet changes to pending
	if len(walletChanges) > 0 {
		pending.AddWalletUpdate(userID, walletChanges)
	}

	// Build payload
	payload := notify.NewRewardPayload("level_up")
	hasContent := false

	if len(walletChanges) > 0 {
		payload.Wallet = &notify.WalletDelta{
			Gold: int(walletChanges["gold"]),
			Gems: int(walletChanges["gems"]),
		}
		hasContent = true
	}

	if len(grantedItems) > 0 {
		payload.Inventory = &notify.InventoryDelta{Items: grantedItems}
		hasContent = true
	}

	if hasContent {
		pending.Payload = payload
	}

	return pending, nil
}

// PrepareExperience calculates XP and level gains, returns pending writes for progression and level rewards.
// Does not commit anything - caller should use MultiUpdate.
func PrepareExperience(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, itemType string, itemID uint32, exp uint32) (newLevel int, pending *PendingWrites, err error) {
	pending = NewPendingWrites()

	// Input validation
	if exp == 0 {
		LogInfo(ctx, logger, "Zero experience provided, skipping update")
		return 0, pending, nil
	}
	if exp > 1000000 {
		LogWarn(ctx, logger, "Unusually large experience value provided")
		return 0, nil, errors.ErrInvalidExperience
	}

	if !ValidateItemExists(itemType, itemID) {
		return 0, nil, errors.ErrInvalidItemID
	}

	treeName, err := GetLevelTreeName(itemType, itemID)
	if err != nil {
		LogError(ctx, logger, "Invalid item configuration", err)
		return 0, nil, errors.ErrInvalidConfig
	}

	var progressionKey string
	switch itemType {
	case storageKeyPet:
		progressionKey = ProgressionKeyPet
	case storageKeyClass:
		progressionKey = ProgressionKeyClass
	default:
		return 0, nil, errors.ErrInvalidItemType
	}

	var resultLevel int
	var oldLevel int

	// Prepare progression update
	prog, progWrite, err := PrepareProgressionUpdate(ctx, nk, logger, userID, progressionKey, itemID, func(prog *ItemProgression) error {
		oldLevel = prog.Level
		newExp := prog.Exp + int(exp)

		// Integer overflow protection
		if newExp < prog.Exp {
			newExp = 1<<31 - 1 // math.MaxInt32
		}

		tree, exists := GetLevelTree(treeName)
		if !exists {
			return errors.ErrInvalidLevelTree
		}

		// Cap experience at max level threshold
		maxExp := tree.LevelThresholds[tree.MaxLevel]
		if newExp > maxExp {
			newExp = maxExp
		}

		prog.Exp = newExp

		calculatedLevel, err := CalculateLevel(treeName, prog.Exp)
		if err != nil {
			return err
		}

		// Ensure level doesn't exceed maximum
		if calculatedLevel > tree.MaxLevel {
			calculatedLevel = tree.MaxLevel
			prog.Exp = maxExp
		}

		if calculatedLevel > prog.Level {
			prog.Level = calculatedLevel
			resultLevel = calculatedLevel
		}

		return nil
	})

	if err != nil {
		return 0, nil, err
	}

	if progWrite != nil {
		pending.AddStorageWrite(progWrite)
	}

	// Prepare level-up rewards for each level gained
	if resultLevel > oldLevel {
		for lvl := oldLevel + 1; lvl <= resultLevel; lvl++ {
			levelRewards, err := PrepareLevelRewards(ctx, nk, logger, userID, treeName, lvl, itemType, itemID)
			if err != nil {
				LogWarn(ctx, logger, fmt.Sprintf("Failed to prepare level %d rewards: %v", lvl, err))
				continue
			}
			pending.Merge(levelRewards)
		}
	}

	// Add final level to payload
	if resultLevel > 0 {
		if pending.Payload == nil {
			pending.Payload = notify.NewRewardPayload("level_up")
		}
		if pending.Payload.Progression == nil {
			pending.Payload.Progression = &notify.ProgressionDelta{}
		}
		pending.Payload.Progression.XpGranted = notify.IntPtr(int(exp))

		switch itemType {
		case storageKeyPet:
			pending.Payload.Progression.NewPetLevel = notify.IntPtr(resultLevel)
		case storageKeyClass:
			pending.Payload.Progression.NewClassLevel = notify.IntPtr(resultLevel)
		}
	} else if prog != nil {
		// Even if no level-up, still report XP granted
		if pending.Payload == nil {
			pending.Payload = notify.NewRewardPayload("xp_grant")
		}
		if pending.Payload.Progression == nil {
			pending.Payload.Progression = &notify.ProgressionDelta{}
		}
		pending.Payload.Progression.XpGranted = notify.IntPtr(int(exp))
	}

	return resultLevel, pending, nil
}

// CommitPendingWrites executes all pending writes atomically via MultiUpdate.
func CommitPendingWrites(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, pending *PendingWrites) error {
	if pending == nil || pending.IsEmpty() {
		return nil
	}

	_, _, err := nk.MultiUpdate(ctx, nil, pending.StorageWrites, nil, pending.WalletUpdates, true)
	if err != nil {
		LogError(ctx, logger, "MultiUpdate commit failed", err)
		return fmt.Errorf("atomic commit failed: %w", err)
	}

	return nil
}



func GetRewardItemIDs(itemType string, itemID uint32, rewardType string, amount uint32) []uint32 {
	var ids []uint32

	switch itemType {
	case CategoryPet:
		if pet, exists := GetPet(uint32(itemID)); exists {
			switch rewardType {
			case "backgrounds":
				ids = pet.BackgroundIDs
			case "piece_styles":
				ids = pet.StyleIDs
			}
		}
	case CategoryClass:
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
