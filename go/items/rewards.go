package items

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"block-server/errors"
	"block-server/notify"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Category constants — distinct from storage keys (storageKeyPet="pets", storageKeyClass="classes").
const (
	CategoryPet   = "pet"
	CategoryClass = "class"
)

// RewardIndices maps a level to its ability and sprite pool indices.
type RewardIndices struct {
	AbilityIndex int
	SpriteIndex  int
}

// Pre-computes which pool index each level's reward maps to.
// Abilities and sprites are tracked independently starting at index 1.
func BuildRewardIndexMap(treeName string) map[int]RewardIndices {
	tree, exists := GetLevelTree(treeName)
	if !exists {
		return nil
	}

	// Helper to check if level is in rewarded_levels array
	isRewarded := func(lvl int) bool {
		for _, r := range tree.RewardedLevels {
			if r == lvl {
				return true
			}
		}
		return false
	}

	// Collect and sort all rewarded levels from the Rewards map
	var levels []int
	for k := range tree.Rewards {
		level, err := strconv.Atoi(k)
		if err == nil && isRewarded(level) {
			levels = append(levels, level)
		}
	}
	sort.Ints(levels)

	abilityPos, spritePos := 0, 0
	result := make(map[int]RewardIndices)

	for _, level := range levels {
		reward := tree.Rewards[strconv.Itoa(level)]
		aIdx, sIdx := -1, -1

		if reward.Abilities != "" {
			aIdx = abilityPos + 1 // +1 because index 0 is pre-granted
			abilityPos++
		}
		if reward.Sprites != "" {
			sIdx = spritePos + 1 // +1 because index 0 is pre-granted
			spritePos++
		}

		result[level] = RewardIndices{AbilityIndex: aIdx, SpriteIndex: sIdx}
	}

	return result
}

// Prepares all rewards for a specific level without committing.
// Returns *PendingWrites to be merged and committed via MultiUpdate.
func PrepareLevelRewards(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, treeName string, level int, itemType string, itemID uint32) (*PendingWrites, RewardMutations, error) {
	mutations := RewardMutations{
		GrantedAbilities: make([]int32, 0),
		GrantedSprites:   make([]uint32, 0),
		WalletChanges:    make(map[string]int64),
		InventoryChanges: make([]uint32, 0),
	}

	tree, exists := GetLevelTree(treeName)
	if !exists {
		return nil, mutations, errors.ErrInvalidLevelTree
	}

	isRewarded := false
	for _, r := range tree.RewardedLevels {
		if r == level {
			isRewarded = true
			break
		}
	}
	
	if !isRewarded {
		// Not in rewarded_levels
		return nil, mutations, nil
	}

	levelStr := strconv.Itoa(level)
	rewardData, exists := tree.Rewards[levelStr]
	if !exists {
		// No rewards for this level
		return nil, mutations, nil
	}

	// Build reward index map to get position-based indices for this level
	indexMap := BuildRewardIndexMap(treeName)
	rewardIndices := indexMap[level]

	// Get pool sizes for bounds checking
	switch itemType {
	case "pet", storageKeyPet:
		if _, exists := GetPet(itemID); exists {
			// Item exists
		}
	case "class", storageKeyClass:
		if _, exists := GetClass(itemID); exists {
			// Item exists
		}
	}

	rewards := make(map[string]uint32)

	// currency rewards
	if rewardData.Gold != "" {
		val, err := ParseUint32Safely(rewardData.Gold, logger)
		if err != nil {
			return nil, mutations, errors.ErrParse
		}
		rewards["gold"] = val
	}
	if rewardData.Gems != "" {
		val, err := ParseUint32Safely(rewardData.Gems, logger)
		if err != nil {
			return nil, mutations, errors.ErrParse
		}
		rewards["gems"] = val
	}

	// progression rewards - use position-based indices from the index map
	if rewardData.Abilities != "" && rewardIndices.AbilityIndex >= 0 {
		rewards["abilities"] = uint32(rewardIndices.AbilityIndex)
	}
	if rewardData.Sprites != "" && rewardIndices.SpriteIndex >= 0 {
		rewards["sprites"] = uint32(rewardIndices.SpriteIndex)
	}

	// item rewards
	if rewardData.Backgrounds != "" {
		val, err := ParseUint32Safely(rewardData.Backgrounds, logger)
		if err != nil {
			return nil, mutations, errors.ErrParse
		}
		if val > 0 {
			rewards["backgrounds"] = val
		}
	}
	if rewardData.PieceStyles != "" {
		val, err := ParseUint32Safely(rewardData.PieceStyles, logger)
		if err != nil {
			return nil, mutations, errors.ErrParse
		}
		if val > 0 {
			rewards["piece_styles"] = val
		}
	}

	pending, err := PrepareRewardItems(ctx, nk, logger, userID, rewards, itemType, itemID, &mutations)
	if err != nil {
		LogError(ctx, logger, "Reward prepare failed", err)
		return nil, mutations, err
	}

	return pending, mutations, nil
}

// Prepares currency and item rewards without committing.
// Returns *PendingWrites to be merged and committed via MultiUpdate.
func PrepareRewardItems(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, rewards map[string]uint32, itemType string, itemID uint32, mutations *RewardMutations) (*PendingWrites, error) {
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
			case CategoryPet, storageKeyPet:
				if pet, exists := GetPet(itemID); exists {
					maxAbilitiesAvailable = len(pet.AbilityIDs)
					maxSpritesAvailable = pet.SpriteCount
					itemExists = true
				}
			case CategoryClass, storageKeyClass:
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

			rewardIndex := int(amount) // amount is the position-based index

			// Determine max available based on reward type
			maxAvailable := maxAbilitiesAvailable
			if rewardType == "sprites" {
				maxAvailable = maxSpritesAvailable
			}

			// Bounds check: index must be within pool size
			if rewardIndex >= maxAvailable {
				// Silently cap - reward is out of bounds
				continue
			}

			switch rewardType {
			case "abilities":
				mutations.GrantedAbilities = append(mutations.GrantedAbilities, int32(rewardIndex))
			case "sprites":
				mutations.GrantedSprites = append(mutations.GrantedSprites, uint32(rewardIndex))
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

// Calculates XP and level gains. Returns pending writes for progression and level rewards.
// Does not commit; caller must execute via MultiUpdate.
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
	var deltaMap map[string]notify.TierState

	// Prepare progression update
	prog, progWrite, err := PrepareProgressionUpdate(ctx, nk, logger, userID, progressionKey, itemID, func(prog *ItemProgression) error {
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
			deltaMap = make(map[string]notify.TierState)
			for lvl := prog.Level + 1; lvl <= calculatedLevel; lvl++ {
				lvlStr := strconv.Itoa(lvl)
				if _, hasReward := tree.Rewards[lvlStr]; hasReward {
					if prog.TierStates == nil {
						prog.TierStates = make(map[string]TierState)
					}
					now := time.Now().UnixMilli()
					prog.TierStates[lvlStr] = TierState{Status: "unclaimed", UnlockedAt: now}
					deltaMap[lvlStr] = notify.TierState{Status: "unclaimed", UnlockedAt: now}
				}
			}

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

		if len(deltaMap) > 0 {
			pending.Payload.Progression.UpdatedTierStates = deltaMap
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
	case CategoryPet, storageKeyPet:
		if pet, exists := GetPet(uint32(itemID)); exists {
			switch rewardType {
			case "backgrounds":
				ids = pet.BackgroundIDs
			case "piece_styles":
				ids = pet.StyleIDs
			}
		}
	case CategoryClass, storageKeyClass:
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
