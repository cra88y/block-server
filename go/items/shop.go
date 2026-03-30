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
	ProductID             string `json:"product_id"`
	JwsRepresentation     string `json:"jws"`
	TransactionId         string `json:"transaction_id"`
	OriginalTransactionId string `json:"original_transaction_id"`
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

const (
	storageCollectionShopHistory  = "shop_history"
	StorageCollectionIAPPurchases = "iap_purchases"
)

// IAPPurchaseGrant is stored for dedup and revocation tracking.
type IAPPurchaseGrant struct {
	OriginalTransactionId string `json:"original_transaction_id"`
	ProductId             string `json:"product_id"`
	UserId                string `json:"user_id"`
	Jws                   string `json:"jws"`
	Status                string `json:"status"` // "validated" | "revoked"
	GrantedAt             int64  `json:"granted_at"`
	RevokedAt             *int64 `json:"revoked_at,omitempty"`
}

type PurchaseRequest struct {
	ShopItemID string `json:"shop_item_id"`
	RequestId  string `json:"request_id,omitempty"` // Client-generated UUID for idempotency
}

type PurchaseResponse struct {
	Success bool           `json:"success"`
	Error   string         `json:"error,omitempty"`
	Wallet  map[string]int `json:"wallet,omitempty"` // Post-purchase wallet state for client reconciliation
}

type PurchaseLootboxRequest struct {
	Tier      string `json:"tier"`
	RequestId string `json:"request_id,omitempty"` // Client-generated UUID for idempotency
}

// PurchaseLogEntry is stored in shop_history collection for audit trail and idempotency.
type PurchaseLogEntry struct {
	RequestId   string         `json:"request_id"`
	ItemId      string         `json:"item_id"`
	PriceGems   int            `json:"price_gems"`
	PriceGold   int            `json:"price_gold"`
	Timestamp   int64          `json:"timestamp"`
	Success     bool           `json:"success"`
	WalletAfter map[string]int `json:"wallet_after,omitempty"`
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

// RpcPurchaseShopItem handles purchasing a shop item atomically with idempotency.
// Uses PendingWrites for atomic commit, request_id for dedup, and purchase_log for audit.
func RpcPurchaseShopItem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	var req PurchaseRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", errors.ErrUnmarshal
	}

	// ── Idempotency check ────────────────────────────────────────────────
	if req.RequestId != "" {
		if cached, err := checkPurchaseLog(ctx, nk, logger, userID, req.RequestId); err == nil && cached != nil {
			logger.Info("Idempotent hit for request %s — returning cached result", req.RequestId)
			respBytes, _ := json.Marshal(cached)
			return string(respBytes), nil
		}
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
		return purchaseFail(req.RequestId, userID, nk, logger, "item not found")
	}

	if item.Type == "lootbox" {
		return purchaseFail(req.RequestId, userID, nk, logger, "use purchase_lootbox RPC for lootboxes")
	}

	// Check ownership
	ownedItems := getUserOwnedItems(ctx, nk, userID)
	if isItemOwned(ownedItems, item.Type, item.ItemID) {
		return purchaseFail(req.RequestId, userID, nk, logger, "item already owned")
	}

	// Check rotation (if applicable)
	if item.RotationSlot != nil {
		activeSlots := getActiveRotationSlots()
		if !isSlotActive(*item.RotationSlot, activeSlots) {
			return purchaseFail(req.RequestId, userID, nk, logger, "item not currently available")
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
			return purchaseFail(req.RequestId, userID, nk, logger, "insufficient gems")
		}
		pending.AddWalletDeduction(userID, "gems", int64(item.Price.Gems))
	} else if item.Price.Gold > 0 {
		if wallet["gold"] < int64(item.Price.Gold) {
			return purchaseFail(req.RequestId, userID, nk, logger, "insufficient gold")
		}
		pending.AddWalletDeduction(userID, "gold", int64(item.Price.Gold))
	}

	// type → storage key
	typeToKey := map[string]string{
		"background":  storageKeyBackground,
		"piece_style": storageKeyPieceStyle,
		"pet":         storageKeyPet,
		"class":       storageKeyClass,
	}
	storageKey, ok2 := typeToKey[item.Type]
	if !ok2 {
		return purchaseFail(req.RequestId, userID, nk, logger, "unknown item type")
	}
	itemPending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKey, item.ItemID)
	if err != nil {
		return purchaseFail(req.RequestId, userID, nk, logger, "failed to grant item")
	}
	pending.Merge(itemPending)

	// Commit atomically
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("Purchase commit failed for user %s item %s: %v", userID, item.ID, err)
		return "", errors.ErrInternalError
	}

	// Build post-purchase wallet for client reconciliation
	updatedWallet := map[string]int{
		"gold": int(wallet["gold"]),
		"gems": int(wallet["gems"]),
	}
	if item.Price.Gems > 0 {
		updatedWallet["gems"] -= item.Price.Gems
	} else if item.Price.Gold > 0 {
		updatedWallet["gold"] -= item.Price.Gold
	}

	// Audit log
	if req.RequestId != "" {
		writePurchaseLog(ctx, nk, userID, req.RequestId, item.ID, item.Price.Gems, item.Price.Gold, true, updatedWallet)
	}

	logger.Info("User %s purchased shop item %s", userID, item.ID)

	resp := PurchaseResponse{Success: true, Wallet: updatedWallet}
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
		logger.Error("[IAP] Failed to unmarshal payload: %v", err)
		return "", errors.ErrInvalidInput
	}
	if req.ProductID == "" || req.JwsRepresentation == "" {
		return "", errors.ErrInvalidInput
	}

	// Structured log prefix for correlation
	logPrefix := fmt.Sprintf("[IAP] tx=%s origTx=%s user=%s product=%s",
		req.TransactionId, req.OriginalTransactionId, userID, req.ProductID)

	// 2b. Dedup: if originalTransactionId is present, check if already granted
	if req.OriginalTransactionId != "" {
		objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
			Collection: StorageCollectionIAPPurchases,
			Key:        req.OriginalTransactionId,
			UserID:     userID,
		}})
		if err == nil && len(objects) > 0 {
			var existing IAPPurchaseGrant
			if jsonErr := json.Unmarshal([]byte(objects[0].Value), &existing); jsonErr == nil && existing.Status == "validated" {
				logger.Info("%s Dedup hit — returning success", logPrefix)
				return `{"success":true}`, nil
			}
		}
	}

	// 3. Validate receipt with Apple via Nakama
	// persist=true — Nakama stores validated purchases in its DB for idempotency
	resp, err := nk.PurchaseValidateApple(ctx, userID, req.JwsRepresentation, true)
	if err != nil {
		logger.Error("%s Apple validation failed: %v", logPrefix, err)
		return "", errors.ErrInternalError
	}

	if len(resp.ValidatedPurchases) == 0 {
		logger.Warn("%s No validated purchases in Apple response", logPrefix)
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
		logger.Warn("%s Product not found in Apple response", logPrefix)
		return "", errors.ErrInvalidInput
	}

	// 5. Idempotency — if Nakama already saw this transaction, skip grant
	if purchase.SeenBefore {
		logger.Info("%s Nakama seen_before — skipping grant", logPrefix)
		return `{"success":true}`, nil
	}

	// 6. Check for refunds
	if purchase.RefundTime != nil && !purchase.RefundTime.AsTime().IsZero() {
		logger.Warn("%s Refunded — skipping grant", logPrefix)
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
		logger.Error("%s Unknown product or zero gems", logPrefix)
		return "", errors.ErrInvalidInput
	}

	// 8. Grant gems via PendingWrites (atomic)
	pending := NewPendingWrites()
	pending.AddWalletUpdate(userID, map[string]int64{"gems": int64(gemAmount)})

	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("%s Failed to commit gem grant: %v", logPrefix, err)
		return "", errors.ErrInternalError
	}

	// 9. Persist grant record for dedup + revocation tracking
	if req.OriginalTransactionId != "" {
		grant := IAPPurchaseGrant{
			OriginalTransactionId: req.OriginalTransactionId,
			ProductId:             req.ProductID,
			UserId:                userID,
			Jws:                   req.JwsRepresentation,
			Status:                "validated",
			GrantedAt:             time.Now().UnixMilli(),
		}
		grantBytes, _ := json.Marshal(grant)
		_, writeErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      StorageCollectionIAPPurchases,
			Key:             req.OriginalTransactionId,
			UserID:          userID,
			Value:           string(grantBytes),
			PermissionRead:  0,
			PermissionWrite: 0,
		}})
		if writeErr != nil {
			logger.Warn("%s Failed to persist grant record: %v", logPrefix, writeErr)
			// Non-fatal — gems already granted
		}
	}

	// 10. Send CodeReward notification (server controls the payout)
	reward := notify.NewRewardPayload("iap")
	reward.Wallet = &notify.WalletDelta{Gems: gemAmount}
	if err := notify.SendReward(ctx, nk, userID, reward); err != nil {
		logger.Error("%s Failed to send reward notification: %v", logPrefix, err)
		// Non-fatal — gems already granted
	}

	logger.Info("%s Validated: gems=%d txn=%s", logPrefix, gemAmount, purchase.TransactionId)
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

// ── Purchase audit & idempotency helpers ─────────────────────────────────────

// checkPurchaseLog reads a previously processed purchase from storage.
// Returns the cached PurchaseResponse if found, nil if not.
func checkPurchaseLog(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID, requestId string) (*PurchaseResponse, error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionShopHistory,
		Key:        requestId,
		UserID:     userID,
	}})
	if err != nil || len(objects) == 0 {
		return nil, err
	}

	var entry PurchaseLogEntry
	if err := json.Unmarshal([]byte(objects[0].Value), &entry); err != nil {
		return nil, err
	}

	if !entry.Success {
		return nil, fmt.Errorf("cached purchase was a failure")
	}

	return &PurchaseResponse{
		Success: true,
		Wallet:  entry.WalletAfter,
	}, nil
}

// writePurchaseLog stores a purchase record for audit trail and idempotency.
func writePurchaseLog(ctx context.Context, nk runtime.NakamaModule, userID, requestId, itemId string, priceGems, priceGold int, success bool, wallet map[string]int) {
	entry := PurchaseLogEntry{
		RequestId:   requestId,
		ItemId:      itemId,
		PriceGems:   priceGems,
		PriceGold:   priceGold,
		Timestamp:   time.Now().UnixMilli(),
		Success:     success,
		WalletAfter: wallet,
	}

	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return
	}

	// Non-persistent (permission write = owner only, no read for others)
	_, _ = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionShopHistory,
		Key:             requestId,
		UserID:          userID,
		Value:           string(entryBytes),
		PermissionRead:  0, // Owner only
		PermissionWrite: 0, // Owner only
	}})
}

// purchaseFail returns an error response and writes a failed audit log if requestId is present.
func purchaseFail(requestId, userID string, nk runtime.NakamaModule, logger runtime.Logger, reason string) (string, error) {
	if requestId != "" {
		writePurchaseLog(context.Background(), nk, userID, requestId, "", 0, 0, false, nil)
	}
	return "", fmt.Errorf("%s", reason)
}

// ── Revocation RPC ───────────────────────────────────────────────────────────

// RpcRevokeIAPPurchase handles server-side revocation of a previously validated IAP.
// Called by Apple App Store Server Notifications or manual admin action.
func RpcRevokeIAPPurchase(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", errors.ErrNoUserIdFound
	}

	var req struct {
		OriginalTransactionId string `json:"original_transaction_id"`
		RevocationReason      string `json:"revocation_reason"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", fmt.Errorf("invalid payload: %w", err)
	}

	logPrefix := fmt.Sprintf("[IAP-REVOKE] origTx=%s user=%s", req.OriginalTransactionId, userID)

	// Read grant record
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: StorageCollectionIAPPurchases,
		Key:        req.OriginalTransactionId,
		UserID:     userID,
	}})
	if err != nil || len(objects) == 0 {
		logger.Warn("%s Unknown grant — no-op", logPrefix)
		return `{"success":true}`, nil // Not an error
	}

	var grant IAPPurchaseGrant
	if err := json.Unmarshal([]byte(objects[0].Value), &grant); err != nil {
		return "", fmt.Errorf("corrupt grant record: %w", err)
	}

	if grant.Status == "revoked" {
		logger.Info("%s Already revoked", logPrefix)
		return `{"success":true}`, nil // Idempotent
	}

	// Update grant record
	now := time.Now().UnixMilli()
	grant.Status = "revoked"
	grant.RevokedAt = &now

	grantBytes, _ := json.Marshal(grant)
	_, writeErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      StorageCollectionIAPPurchases,
		Key:             req.OriginalTransactionId,
		UserID:          userID,
		Value:           string(grantBytes),
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	if writeErr != nil {
		logger.Error("%s Failed to write revocation: %v", logPrefix, writeErr)
	}

	// TODO: Compute balance adjustment from grant.ProductId → gem mapping
	// For now, just log the revocation event
	logger.Info("%s Revoked: reason=%s", logPrefix, req.RevocationReason)
	return `{"success":true}`, nil
}
