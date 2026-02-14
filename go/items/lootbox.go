package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"

	"block-server/errors"
	"block-server/notify"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	storageCollectionLootboxes = "lootboxes"
)

// Lootbox represents an unopened or opened lootbox
type Lootbox struct {
	ID        string `json:"id"`
	Tier      string `json:"tier"`
	CreatedAt int64  `json:"created_at"`
	Opened    bool   `json:"opened"`
}

// LootboxContents represents the rewards from opening a lootbox (internal use)
type LootboxContents struct {
	Gold      int      `json:"gold"`
	Gems      int      `json:"gems"`
	Treats    int      `json:"treats"`
	Items     []uint32 `json:"items"`
	ItemTypes []string `json:"item_types"`
}

// RpcGetLootboxes returns all unopened lootboxes for a user
func RpcGetLootboxes(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	objects, err := listAllStorage(ctx, nk, logger, userID, storageCollectionLootboxes)
	if err != nil {
		logger.Error("Failed to list lootboxes: %v", err)
		return "", errors.ErrCouldNotReadStorage
	}

	lootboxes := make([]Lootbox, 0)
	for _, obj := range objects {
		var lb Lootbox
		if err := json.Unmarshal([]byte(obj.Value), &lb); err != nil {
			logger.Warn("Failed to unmarshal lootbox: %v", err)
			continue
		}
		if !lb.Opened {
			lootboxes = append(lootboxes, lb)
		}
	}

	respBytes, err := json.Marshal(lootboxes)
	if err != nil {
		return "", errors.ErrMarshal
	}

	return string(respBytes), nil
}

// RpcOpenLootbox opens a lootbox and grants rewards atomically
func RpcOpenLootbox(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", errors.ErrUnmarshal
	}

	// Read lootbox
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionLootboxes,
		Key:        req.ID,
		UserID:     userID,
	}})
	if err != nil || len(objects) == 0 {
		return "", errors.ErrCouldNotReadStorage
	}

	var lootbox Lootbox
	if err := json.Unmarshal([]byte(objects[0].Value), &lootbox); err != nil {
		return "", errors.ErrUnmarshal
	}

	if lootbox.Opened {
		return "", errors.ErrLootboxAlreadyOpened
	}

	// Generate contents based on tier, filtering owned items
	contents, err := generateLootboxContents(ctx, nk, userID, lootbox.Tier)
	if err != nil {
		return "", err
	}

	// Prepare all writes atomically
	pending := NewPendingWrites()

	// Currency rewards
	if contents.Gold > 0 || contents.Gems > 0 || contents.Treats > 0 {
		walletChanges := map[string]int64{
			"gold":   int64(contents.Gold),
			"gems":   int64(contents.Gems),
			"treats": int64(contents.Treats),
		}
		pending.AddWalletUpdate(userID, walletChanges)
	}

	// Item rewards - prepare inventory writes
	for i, itemID := range contents.Items {
		itemType := contents.ItemTypes[i]
		itemPending, err := PrepareItemGrant(ctx, nk, logger, userID, itemType, itemID)
		if err != nil {
			logger.Warn("Failed to prepare item %d grant: %v", itemID, err)
			continue
		}
		pending.Merge(itemPending)
	}

	// Mark lootbox as opened
	lootbox.Opened = true
	lootboxValue, _ := json.Marshal(lootbox)
	pending.AddStorageWrite(&runtime.StorageWrite{
		Collection:      storageCollectionLootboxes,
		Key:             lootbox.ID,
		UserID:          userID,
		Value:           string(lootboxValue),
		Version:         objects[0].Version,
		PermissionRead:  1,
		PermissionWrite: 0,
	})

	// Commit all writes atomically
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("Failed to commit lootbox open transaction: %v", err)
		return "", fmt.Errorf("lootbox open failed: %w", err)
	}

	// Build unified RewardPayload
	result := notify.NewRewardPayload("lootbox")
	result.ReasonKey = "reward.lootbox.opened"

	// Inventory from items
	if len(contents.Items) > 0 {
		items := make([]notify.ItemGrant, len(contents.Items))
		for i, id := range contents.Items {
			items[i] = notify.ItemGrant{
				ID:   id,
				Type: contents.ItemTypes[i],
			}
		}
		result.Inventory = &notify.InventoryDelta{Items: items}
	}

	// Wallet from currency
	if contents.Gold > 0 || contents.Gems > 0 || contents.Treats > 0 {
		result.Wallet = &notify.WalletDelta{
			Gold:   contents.Gold,
			Gems:   contents.Gems,
			Treats: contents.Treats,
		}
	}

	// Tier for display
	result.Lootboxes = []notify.LootboxGrant{{
		ID:   lootbox.ID,
		Tier: lootbox.Tier,
	}}

	respBytes, err := json.Marshal(result)
	if err != nil {
		return "", errors.ErrMarshal
	}

	logger.Info("Lootbox %s opened for user %s: gold=%d, gems=%d, treats=%d, items=%d",
		lootbox.ID, userID, contents.Gold, contents.Gems, contents.Treats, len(contents.Items))

	return string(respBytes), nil
}

// getOwnedItemsForLootbox loads all owned items across all lootbox-eligible types
func getOwnedItemsForLootbox(ctx context.Context, nk runtime.NakamaModule, userID string) map[string][]uint32 {
	owned := make(map[string][]uint32)

	// All types that can drop from lootboxes
	types := []string{storageKeyBackground, storageKeyPieceStyle, storageKeyPet, storageKeyClass}
	for _, t := range types {
		objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
			Collection: storageCollectionInventory,
			Key:        t,
			UserID:     userID,
		}})
		if err != nil || len(objects) == 0 {
			continue
		}

		var inv InventoryData
		if err := json.Unmarshal([]byte(objects[0].Value), &inv); err != nil {
			continue
		}
		owned[t] = inv.Items
	}

	return owned
}

func generateLootboxContents(ctx context.Context, nk runtime.NakamaModule, userID string, tier string) (*LootboxContents, error) {
	shopCfg := GetShopConfig()
	if shopCfg == nil {
		return nil, fmt.Errorf("shop config not loaded")
	}

	tierDef, exists := shopCfg.LootboxTiers[tier]
	if !exists {
		tierDef = shopCfg.LootboxTiers["standard"]
		if tierDef.DropTable.Gold.Max == 0 {
			return nil, fmt.Errorf("no valid lootbox tier found")
		}
	}

	// Load owned items to filter duplicates
	ownedItems := getOwnedItemsForLootbox(ctx, nk, userID)

	dt := tierDef.DropTable
	contents := &LootboxContents{
		Gold:      randomRange(dt.Gold.Min, dt.Gold.Max),
		Gems:      randomRange(dt.Gems.Min, dt.Gems.Max),
		Treats:    randomRange(dt.Treats.Min, dt.Treats.Max),
		Items:     make([]uint32, 0),
		ItemTypes: make([]string, 0),
	}

	if rand.Float64() < dt.ItemChance && len(dt.ItemPools) > 0 {
		itemType, itemID := pickRandomItemFromPools(dt.ItemPools, ownedItems)
		if itemID > 0 {
			contents.Items = append(contents.Items, itemID)
			contents.ItemTypes = append(contents.ItemTypes, itemType)
		}
	}

	return contents, nil
}

func pickRandomItemFromPools(pools []string, ownedItems map[string][]uint32) (string, uint32) {
	if len(pools) == 0 {
		return "", 0
	}

	// Helper to check if item is owned
	isOwned := func(storageKey string, itemID uint32) bool {
		for _, id := range ownedItems[storageKey] {
			if id == itemID {
				return true
			}
		}
		return false
	}

	// Helper to filter owned items from a pool
	filterOwned := func(storageKey string, allIDs []uint32) []uint32 {
		available := make([]uint32, 0, len(allIDs))
		for _, id := range allIDs {
			if !isOwned(storageKey, id) {
				available = append(available, id)
			}
		}
		return available
	}

	// Shuffle pools to try different ones if first is exhausted
	shuffledPools := make([]string, len(pools))
	copy(shuffledPools, pools)
	rand.Shuffle(len(shuffledPools), func(i, j int) {
		shuffledPools[i], shuffledPools[j] = shuffledPools[j], shuffledPools[i]
	})

	for _, pool := range shuffledPools {
		var storageKey string
		var allIDs []uint32

		switch pool {
		case "backgrounds":
			storageKey = storageKeyBackground
			allIDs = make([]uint32, 0, len(GameData.Backgrounds))
			for id := range GameData.Backgrounds {
				allIDs = append(allIDs, id)
			}
		case "piece_styles":
			storageKey = storageKeyPieceStyle
			allIDs = make([]uint32, 0, len(GameData.PieceStyles))
			for id := range GameData.PieceStyles {
				allIDs = append(allIDs, id)
			}
		case "pets":
			storageKey = storageKeyPet
			allIDs = make([]uint32, 0, len(GameData.Pets))
			for id := range GameData.Pets {
				allIDs = append(allIDs, id)
			}
		case "classes":
			storageKey = storageKeyClass
			allIDs = make([]uint32, 0, len(GameData.Classes))
			for id := range GameData.Classes {
				allIDs = append(allIDs, id)
			}
		default:
			continue
		}

		// Filter out owned items
		available := filterOwned(storageKey, allIDs)
		if len(available) > 0 {
			return pool, available[rand.Intn(len(available))]
		}
	}

	// All pools exhausted (player owns everything)
	return "", 0
}

func randomRange(min, max int) int {
	if min >= max {
		return min
	}
	return min + rand.Intn(max-min+1)
}
