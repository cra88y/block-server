package items

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"block-server/errors"
	"block-server/notify"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

//go:embed gamedata/shop.json
var shopdata []byte

// Shop configuration loaded from gamedata/shop.json
type ShopConfig struct {
	ExchangeRates  ExchangeRates             `json:"exchange_rates"`
	LootboxTiers   map[string]LootboxTierDef `json:"lootbox_tiers"`
	ShopItems      []ShopItem                `json:"shop_items"`
	RotationConfig RotationConfig            `json:"rotation_config"`
	IAPProducts    []IAPProduct              `json:"iap_products"`
}

type ExchangeRates struct {
	GoldPerGem   int `json:"gold_per_gem"`
	TreatsPerGem int `json:"treats_per_gem"`
}

type LootboxTierDef struct {
	PriceGems int       `json:"price_gems"`
	DropTable DropTable `json:"drop_table"`
}

type DropTable struct {
	Gold       DropRange `json:"gold"`
	Gems       DropRange `json:"gems"`
	Treats     DropRange `json:"treats"`
	ItemChance float64   `json:"item_chance"`
	ItemPools  []string  `json:"item_pools"`
}

type DropRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type ShopItem struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	ItemID       uint32 `json:"item_id,omitempty"`
	Tier         string `json:"tier,omitempty"`
	Price        Price  `json:"price"`
	RotationSlot *int   `json:"rotation_slot"`
}

type Price struct {
	Gold int `json:"gold,omitempty"`
	Gems int `json:"gems,omitempty"`
}

type RotationConfig struct {
	Slots                int    `json:"slots"`
	RefreshIntervalHours int    `json:"refresh_interval_hours"`
	EpochStart           string `json:"epoch_start"`
}

type IAPProduct struct {
	ProductID string `json:"product_id"`
	Gems      int    `json:"gems"`
	USDCents  int    `json:"usd_cents"`
}

// ValidateIAPPayload is the client request for IAP receipt validation.
type ValidateIAPPayload struct {
	ProductID         string `json:"product_id"`
	JwsRepresentation string `json:"jws"`
}

var shopConfig *ShopConfig

func LoadShopData() error {
	shopConfig = &ShopConfig{}
	if err := json.Unmarshal(shopdata, shopConfig); err != nil {
		return fmt.Errorf("failed to parse shop.json: %w", err)
	}
	return nil
}

func GetShopConfig() *ShopConfig {
	return shopConfig
}

// Response types
type ShopCatalogResponse struct {
	RotatingItems  []ShopItemResponse `json:"rotating_items"`
	PermanentItems []ShopItemResponse `json:"permanent_items"`
	LootboxTiers   map[string]int     `json:"lootbox_tiers"`
	NextRotationAt int64              `json:"next_rotation_at"`
	IAPProducts    []IAPProduct       `json:"iap_products"`
}

type ShopItemResponse struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	ItemID    uint32 `json:"item_id,omitempty"`
	Tier      string `json:"tier,omitempty"`
	PriceGems int    `json:"price_gems,omitempty"`
	PriceGold int    `json:"price_gold,omitempty"`
	Owned     bool   `json:"owned"`
}

type PurchaseRequest struct {
	ShopItemID string `json:"shop_item_id"`
}

type PurchaseResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type PurchaseLootboxRequest struct {
	Tier string `json:"tier"`
}

// RpcGetShopCatalog returns the current shop state
func RpcGetShopCatalog(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	if shopConfig == nil {
		return "", fmt.Errorf("shop not configured")
	}

	// Get user's inventory to check ownership
	ownedItems := getUserOwnedItems(ctx, nk, userID)

	// Compute active rotation
	activeSlots := getActiveRotationSlots()

	rotating := make([]ShopItemResponse, 0)
	permanent := make([]ShopItemResponse, 0)

	for _, item := range shopConfig.ShopItems {
		if item.Type == "lootbox" {
			continue // Lootboxes handled separately
		}

		resp := ShopItemResponse{
			ID:        item.ID,
			Type:      item.Type,
			ItemID:    item.ItemID,
			PriceGems: item.Price.Gems,
			PriceGold: item.Price.Gold,
			Owned:     isItemOwned(ownedItems, item.Type, item.ItemID),
		}

		if item.RotationSlot != nil {
			// Check if this slot is active
			if isSlotActive(*item.RotationSlot, activeSlots) {
				rotating = append(rotating, resp)
			}
		} else {
			permanent = append(permanent, resp)
		}
	}

	// Build lootbox tier prices for client
	lootboxPrices := make(map[string]int)
	for tier, def := range shopConfig.LootboxTiers {
		lootboxPrices[tier] = def.PriceGems
	}

	response := ShopCatalogResponse{
		RotatingItems:  rotating,
		PermanentItems: permanent,
		LootboxTiers:   lootboxPrices,
		NextRotationAt: getNextRotationTime(),
		IAPProducts:    shopConfig.IAPProducts,
	}

	respBytes, err := json.Marshal(response)
	if err != nil {
		return "", errors.ErrMarshal
	}

	return string(respBytes), nil
}

// RpcPurchaseShopItem handles purchasing a shop item atomically
func RpcPurchaseShopItem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	var req PurchaseRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", errors.ErrUnmarshal
	}

	// Find the shop item
	var item *ShopItem
	for i := range shopConfig.ShopItems {
		if shopConfig.ShopItems[i].ID == req.ShopItemID {
			item = &shopConfig.ShopItems[i]
			break
		}
	}

	if item == nil {
		return "", fmt.Errorf("item not found: %s", req.ShopItemID)
	}

	if item.Type == "lootbox" {
		return "", fmt.Errorf("use purchase_lootbox RPC for lootboxes")
	}

	// Check ownership
	ownedItems := getUserOwnedItems(ctx, nk, userID)
	if isItemOwned(ownedItems, item.Type, item.ItemID) {
		return "", fmt.Errorf("item already owned")
	}

	// Check rotation (if applicable)
	if item.RotationSlot != nil {
		activeSlots := getActiveRotationSlots()
		if !isSlotActive(*item.RotationSlot, activeSlots) {
			return "", fmt.Errorf("item not currently available")
		}
	}

	// Verify sufficient balance before preparing writes
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", errors.ErrCouldNotGetAccount
	}
	var wallet map[string]int64
	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		return "", errors.ErrUnmarshal
	}

	// Prepare all writes atomically
	pending := NewPendingWrites()

	// Currency deduction
	if item.Price.Gems > 0 {
		if wallet["gems"] < int64(item.Price.Gems) {
			return "", fmt.Errorf("insufficient gems: have %d, need %d", wallet["gems"], item.Price.Gems)
		}
		pending.AddWalletDeduction(userID, "gems", int64(item.Price.Gems))
	} else if item.Price.Gold > 0 {
		if wallet["gold"] < int64(item.Price.Gold) {
			return "", fmt.Errorf("insufficient gold: have %d, need %d", wallet["gold"], item.Price.Gold)
		}
		pending.AddWalletDeduction(userID, "gold", int64(item.Price.Gold))
	}

	// type → storage key. Some types don't pluralize with just "+s" (e.g. class → classs).
	typeToKey := map[string]string{
		"background":  storageKeyBackground,
		"piece_style": storageKeyPieceStyle,
		"pet":         storageKeyPet,
		"class":       storageKeyClass,
	}
	storageKey, ok2 := typeToKey[item.Type]
	if !ok2 {
		return "", fmt.Errorf("unknown shop item type: %s", item.Type)
	}
	itemPending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKey, item.ItemID)
	if err != nil {
		return "", fmt.Errorf("failed to prepare item grant: %w", err)
	}
	pending.Merge(itemPending)

	// Commit atomically
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		return "", fmt.Errorf("purchase failed: %w", err)
	}

	logger.Info("User %s purchased shop item %s for %d gems / %d gold", userID, item.ID, item.Price.Gems, item.Price.Gold)

	resp := PurchaseResponse{Success: true}
	respBytes, _ := json.Marshal(resp)
	return string(respBytes), nil
}

// RpcPurchaseLootbox handles purchasing a lootbox with gems atomically
func RpcPurchaseLootbox(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	var req PurchaseLootboxRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", errors.ErrUnmarshal
	}

	tierDef, exists := shopConfig.LootboxTiers[req.Tier]
	if !exists {
		return "", fmt.Errorf("invalid lootbox tier: %s", req.Tier)
	}

	price := tierDef.PriceGems
	if price <= 0 {
		return "", fmt.Errorf("tier %s cannot be purchased", req.Tier)
	}

	// Verify sufficient balance
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", errors.ErrCouldNotGetAccount
	}
	var wallet map[string]int64
	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		return "", errors.ErrUnmarshal
	}
	if wallet["gems"] < int64(price) {
		return "", fmt.Errorf("insufficient gems: have %d, need %d", wallet["gems"], price)
	}

	// Prepare all writes atomically
	pending := NewPendingWrites()

	// Gem deduction
	pending.AddWalletDeduction(userID, "gems", int64(price))

	// Lootbox creation
	lootbox, lootboxWrite, err := PrepareCreateLootbox(userID, req.Tier)
	if err != nil {
		return "", fmt.Errorf("failed to prepare lootbox: %w", err)
	}
	pending.AddStorageWrite(lootboxWrite)

	// Commit atomically
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		return "", fmt.Errorf("lootbox purchase failed: %w", err)
	}

	logger.Info("User %s purchased %s lootbox for %d gems", userID, req.Tier, price)

	respBytes, _ := json.Marshal(lootbox)
	return string(respBytes), nil
}

// RpcValidateIAPReceipt validates an Apple IAP purchase (JWS) via Nakama's built-in
// Apple receipt validation. Server is the authority on gem payout — client does not
// control reward amounts. Uses Nakama's PurchaseValidateApple which calls Apple's
// App Store Server API under the hood. Idempotent via Nakama's seen_before flag.
func RpcValidateIAPReceipt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// 1. Extract user ID
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", errors.ErrNoUserIdFound
	}

	// 2. Parse payload
	var req ValidateIAPPayload
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Error("Failed to unmarshal IAP validation payload: %v", err)
		return "", errors.ErrInvalidInput
	}
	if req.ProductID == "" || req.JwsRepresentation == "" {
		return "", errors.ErrInvalidInput
	}

	// 3. Validate receipt with Apple via Nakama
	// persist=true — Nakama stores validated purchases in its DB for idempotency
	resp, err := nk.PurchaseValidateApple(ctx, userID, req.JwsRepresentation, true)
	if err != nil {
		logger.Error("Apple receipt validation failed: %v", err)
		return "", errors.ErrInternalError
	}

	if len(resp.ValidatedPurchases) == 0 {
		logger.Warn("No validated purchases in Apple response for product: %s", req.ProductID)
		return "", errors.ErrInvalidInput
	}

	// 4. Find the matching purchase
	var purchase *api.ValidatedPurchase
	for _, p := range resp.ValidatedPurchases {
		if p.ProductId == req.ProductID {
			purchase = p
			break
		}
	}
	if purchase == nil {
		logger.Warn("Product %s not found in validated purchases for user %s", req.ProductID, userID)
		return "", errors.ErrInvalidInput
	}

	// 5. Idempotency — if Nakama already saw this transaction, skip grant
	if purchase.SeenBefore {
		logger.Info("Duplicate IAP transaction %s for product %s — skipping grant", purchase.TransactionId, req.ProductID)
		return `{"success":true}`, nil
	}

	// 6. Check for refunds
	if purchase.RefundTime != nil && !purchase.RefundTime.AsTime().IsZero() {
		logger.Warn("Refunded IAP transaction %s for product %s — skipping grant", purchase.TransactionId, req.ProductID)
		return "", errors.ErrInvalidInput
	}

	// 7. Look up product in server config to determine payout
	var gemAmount int
	productFound := false
	for _, p := range shopConfig.IAPProducts {
		if p.ProductID == req.ProductID {
			gemAmount = p.Gems
			productFound = true
			break
		}
	}
	if !productFound || gemAmount <= 0 {
		logger.Error("Unknown IAP product or zero gems: %s", req.ProductID)
		return "", errors.ErrInvalidInput
	}

	// 8. Grant gems via PendingWrites (atomic)
	pending := NewPendingWrites()
	pending.AddWalletUpdate(userID, map[string]int64{"gems": int64(gemAmount)})

	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("Failed to commit IAP gem grant for user %s: %v", userID, err)
		return "", errors.ErrInternalError
	}

	// 9. Send CodeReward notification (server controls the payout)
	reward := notify.NewRewardPayload("iap")
	reward.Wallet = &notify.WalletDelta{Gems: gemAmount}
	if err := notify.SendReward(ctx, nk, userID, reward); err != nil {
		logger.Error("Failed to send IAP reward notification: %v", err)
		// Non-fatal — gems already granted, just log
	}

	logger.Info("IAP validated: user=%s product=%s gems=%d txn=%s", userID, req.ProductID, gemAmount, purchase.TransactionId)
	return `{"success":true}`, nil
}

// Helper functions

func getUserOwnedItems(ctx context.Context, nk runtime.NakamaModule, userID string) map[string][]uint32 {
	owned := make(map[string][]uint32)

	// Read inventory for each type
	types := []string{storageKeyBackground, storageKeyPieceStyle}
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

func isItemOwned(owned map[string][]uint32, itemType string, itemID uint32) bool {
	var key string
	switch itemType {
	case "background":
		key = storageKeyBackground
	case "piece_style":
		key = storageKeyPieceStyle
	default:
		return false
	}

	items, exists := owned[key]
	if !exists {
		return false
	}

	for _, id := range items {
		if id == itemID {
			return true
		}
	}
	return false
}

func getActiveRotationSlots() []int {
	if shopConfig == nil || shopConfig.RotationConfig.Slots == 0 {
		return []int{1, 2, 3, 4}
	}

	epoch, err := time.Parse(time.RFC3339, shopConfig.RotationConfig.EpochStart)
	if err != nil {
		return []int{1, 2, 3, 4}
	}

	hoursSinceEpoch := int(time.Since(epoch).Hours())
	rotationPeriod := shopConfig.RotationConfig.RefreshIntervalHours
	if rotationPeriod <= 0 {
		rotationPeriod = 24
	}

	currentRotation := (hoursSinceEpoch / rotationPeriod) % shopConfig.RotationConfig.Slots

	// Return slots based on current rotation
	slots := make([]int, shopConfig.RotationConfig.Slots)
	for i := 0; i < shopConfig.RotationConfig.Slots; i++ {
		slots[i] = ((currentRotation + i) % shopConfig.RotationConfig.Slots) + 1
	}

	return slots
}

func isSlotActive(slot int, activeSlots []int) bool {
	for _, s := range activeSlots {
		if s == slot {
			return true
		}
	}
	return false
}

func getNextRotationTime() int64 {
	if shopConfig == nil {
		return 0
	}

	epoch, err := time.Parse(time.RFC3339, shopConfig.RotationConfig.EpochStart)
	if err != nil {
		return 0
	}

	hoursSinceEpoch := time.Since(epoch).Hours()
	rotationPeriod := float64(shopConfig.RotationConfig.RefreshIntervalHours)
	if rotationPeriod <= 0 {
		rotationPeriod = 24
	}

	nextRotationHours := (int(hoursSinceEpoch/rotationPeriod) + 1) * int(rotationPeriod)
	return epoch.Add(time.Duration(nextRotationHours) * time.Hour).UnixMilli()
}
