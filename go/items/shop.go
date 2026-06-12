package items

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
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
	ExchangeRates      ExchangeRates               `json:"exchange_rates"`
	LootboxTiers       map[string]LootboxTierDef   `json:"lootbox_tiers"`
	ShopItems          []ShopItem                  `json:"shop_items"`
	RotationConfig     RotationConfig              `json:"rotation_config"`
	IAPProducts        []IAPProduct                `json:"iap_products"`
	ItemPools          map[string][]PoolItem       `json:"item_pools"`
	DuplicateFallbacks map[string]DuplicateFallback `json:"duplicate_fallbacks"`
}

type DuplicateFallback struct {
	Currency string `json:"currency"`
	Amount   int    `json:"amount"`
}

type PoolItem struct {
	Type string `json:"type"` // "background", "piece_style", "pet", "class"
	ID   uint32 `json:"id"`
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
	Gold      DropRange `json:"gold"`
	Gems      DropRange `json:"gems"`
	Treats    DropRange `json:"treats"`
	ItemPools []PoolRef `json:"item_pools"`
}

// PoolRef defines a named item pool with an independent drop chance (0.0–1.0).
type PoolRef struct {
	Pool   string  `json:"pool"`
	Chance float64 `json:"chance"`
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
	Pool         string `json:"pool,omitempty"`
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

type IAPBundleReward struct {
	Type   string `json:"type"`             // "currency", "pet", "class", "lootbox"
	ID     string `json:"id,omitempty"`     // e.g., "gems", "premium" (for lootboxes)
	ItemID uint32 `json:"item_id,omitempty"`// e.g., 4 (for pets/classes)
	Amount int    `json:"amount,omitempty"` // For currencies/lootboxes
}

type IAPProduct struct {
	ProductID     string            `json:"product_id"`
	Gems          int               `json:"gems"`             // Base gems (legacy/simple support)
	USDCents      int               `json:"usd_cents"`
	RevokeGemDebt int               `json:"revoke_gem_debt"`  // Penalty applied if bundle is refunded
	Rewards       []IAPBundleReward `json:"rewards,omitempty"`// Dynamic starter pack contents
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

	// Auto-generate deterministic shop item IDs from type + item_id.
	// Eliminates stale manual slugs (e.g. "style_pixel" → "piece_style_3").
	// Lootbox items use their tier name as the identifier.
	for i := range shopConfig.ShopItems {
		item := &shopConfig.ShopItems[i]
		if item.Pool != "" {
			continue // ID generated dynamically at runtime
		}
		if item.Type == "lootbox" {
			if item.ID == "" {
				item.ID = fmt.Sprintf("lootbox_%s", item.Tier)
			}
		} else {
			item.ID = fmt.Sprintf("%s_%d", item.Type, item.ItemID)
		}
	}

	return nil
}

func GetShopConfig() *ShopConfig {
	return shopConfig
}

// Response types
type ShopCatalogResponse struct {
	RotatingItems  []ShopItemResponse             `json:"rotating_items"`
	PermanentItems []ShopItemResponse             `json:"permanent_items"`
	LootboxTiers   map[string]LootboxTierResponse `json:"lootbox_tiers"`
	NextRotationAt int64                          `json:"next_rotation_at"`
	IAPProducts    []IAPProduct                   `json:"iap_products"`
}

type LootboxTierResponse struct {
	PriceGems int       `json:"price_gems"`
	DropTable DropTable `json:"drop_table"`
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
		return "", errors.ErrShopNotConfigured
	}

	// Get user's inventory to check ownership
	ownedItems := getUserOwnedItems(ctx, nk, userID)

	// Compute active rotation
	activeSlots := getActiveRotationSlots()

	rotating := make([]ShopItemResponse, 0)
	permanent := make([]ShopItemResponse, 0)

	for i := range shopConfig.ShopItems {
		item := &shopConfig.ShopItems[i]
		if item.Type == "lootbox" {
			continue // Lootboxes handled separately
		}

		resolvedID, resolvedType, resolvedItemID := resolveShopItem(item)

		resp := ShopItemResponse{
			ID:        resolvedID,
			Type:      resolvedType,
			ItemID:    resolvedItemID,
			PriceGems: item.Price.Gems,
			PriceGold: item.Price.Gold,
			Owned:     isItemOwned(ownedItems, resolvedType, resolvedItemID),
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
	lootboxPrices := make(map[string]LootboxTierResponse)
	for tier, def := range shopConfig.LootboxTiers {
		lootboxPrices[tier] = LootboxTierResponse{
			PriceGems: def.PriceGems,
			DropTable: def.DropTable,
		}
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

// Handles purchasing a shop item atomically.
// Idempotent via request_id dedup and purchase_log.
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
	var resolvedID, resolvedType string
	var resolvedItemID uint32

	for i := range shopConfig.ShopItems {
		rID, rType, rItemID := resolveShopItem(&shopConfig.ShopItems[i])
		if rID == req.ShopItemID {
			item = &shopConfig.ShopItems[i]
			resolvedID = rID
			resolvedType = rType
			resolvedItemID = rItemID
			break
		}
	}

	if item == nil {
		return purchaseFail(req.RequestId, userID, nk, logger, errors.ErrItemNotFound)
	}

	if resolvedType == "lootbox" {
		return purchaseFail(req.RequestId, userID, nk, logger, errors.ErrWrongItemType)
	}

	// Check ownership
	ownedItems := getUserOwnedItems(ctx, nk, userID)
	if isItemOwned(ownedItems, resolvedType, resolvedItemID) {
		return purchaseFail(req.RequestId, userID, nk, logger, errors.ErrItemAlreadyOwned)
	}

	// Check rotation (if applicable)
	if item.RotationSlot != nil {
		activeSlots := getActiveRotationSlots()
		if !isSlotActive(*item.RotationSlot, activeSlots) {
			return purchaseFail(req.RequestId, userID, nk, logger, errors.ErrItemNotAvailable)
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
			return purchaseFail(req.RequestId, userID, nk, logger, errors.ErrInsufficientGems)
		}
		pending.AddWalletDeduction(userID, "gems", int64(item.Price.Gems))
	} else if item.Price.Gold > 0 {
		if wallet["gold"] < int64(item.Price.Gold) {
			return purchaseFail(req.RequestId, userID, nk, logger, errors.ErrInsufficientGold)
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
	storageKey, ok2 := typeToKey[resolvedType]
	if !ok2 {
		return purchaseFail(req.RequestId, userID, nk, logger, errors.ErrInvalidItemType)
	}
	itemPending, err := PrepareItemGrant(ctx, nk, logger, userID, storageKey, resolvedItemID)
	if err != nil {
		return purchaseFail(req.RequestId, userID, nk, logger, errors.ErrInternalError)
	}
	pending.Merge(itemPending)

	// Commit atomically
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("Purchase commit failed for user %s item %s: %v", userID, resolvedID, err)
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
		writePurchaseLog(ctx, nk, userID, req.RequestId, resolvedID, item.Price.Gems, item.Price.Gold, true, updatedWallet)
	}

	logger.Info("User %s purchased shop item %s", userID, resolvedID)

	telemetryData, _ := json.Marshal(map[string]interface{}{
		"action":     "purchase",
		"item_id":    resolvedID,
		"gems_spent": item.Price.Gems,
		"gold_spent": item.Price.Gold,
	})
	telemetryEvent := TelemetryEvent{
		EventType: "economy_transaction",
		Timestamp: float64(time.Now().Unix()),
		Data:      string(telemetryData),
	}
	processTelemetryEvent(context.Background(), logger, db, nk, userID, telemetryEvent)

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
		return "", errors.ErrInvalidLootboxTier
	}

	price := tierDef.PriceGems
	if price <= 0 {
		return "", errors.ErrTierNotPurchasable
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
		return "", errors.ErrInsufficientGems
	}

	// Prepare all writes atomically
	pending := NewPendingWrites()

	// Gem deduction
	pending.AddWalletDeduction(userID, "gems", int64(price))

	// Lootbox creation
	lootbox, lootboxWrite, err := PrepareCreateLootbox(userID, req.Tier)
	if err != nil {
		return "", errors.ErrPrepareFailed
	}
	pending.AddStorageWrite(lootboxWrite)

	// Commit atomically
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		return "", errors.ErrTransactionFailed
	}

	logger.Info("User %s purchased %s lootbox for %d gems", userID, req.Tier, price)

	telemetryData, _ := json.Marshal(map[string]interface{}{
		"action":     "purchase",
		"item_id":    req.Tier + "_lootbox",
		"gems_spent": price,
		"gold_spent": 0,
	})
	telemetryEvent := TelemetryEvent{
		EventType: "economy_transaction",
		Timestamp: float64(time.Now().Unix()),
		Data:      string(telemetryData),
	}
	processTelemetryEvent(context.Background(), logger, db, nk, userID, telemetryEvent)

	respBytes, _ := json.Marshal(lootbox)
	return string(respBytes), nil
}

// Validates Apple IAP (JWS) via Nakama.
// Idempotent via Nakama's seen_before flag. Server controls gem payout.
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

	// 4b. Identity Binding & Cryptographic Extraction
	// Nakama provides Apple's raw JSON decoded JWT payload in ProviderResponse
	var providerPayload struct {
		AppAccountToken       string `json:"appAccountToken"`
		OriginalTransactionId string `json:"originalTransactionId"`
	}
	if err := json.Unmarshal([]byte(purchase.ProviderResponse), &providerPayload); err != nil {
		logger.Error("%s Failed to decode Apple provider response: %v", logPrefix, err)
		return "", errors.ErrInvalidInput
	}
	// Apple returns UUIDs in uppercase or lowercase. Compare case-insensitively.
	if !strings.EqualFold(providerPayload.AppAccountToken, userID) {
		logger.Error("%s CRITICAL: Identity Binding failure! appAccountToken (%s) != userID (%s)", logPrefix, providerPayload.AppAccountToken, userID)
		return "", errors.ErrInvalidInput
	}

	verifiedOrigTxId := providerPayload.OriginalTransactionId
	if verifiedOrigTxId == "" {
		logger.Error("%s CRITICAL: No originalTransactionId in Apple payload", logPrefix)
		return "", errors.ErrInvalidInput
	}

	// 5. Absolute Idempotency Check: Query Nakama Storage
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: StorageCollectionIAPPurchases,
		Key:        verifiedOrigTxId,
		UserID:     userID,
	}})
	if err == nil && len(objects) > 0 {
		var existing IAPPurchaseGrant
		if jsonErr := json.Unmarshal([]byte(objects[0].Value), &existing); jsonErr == nil && existing.Status == "validated" {
			logger.Info("%s Dedup hit — returning success (Already granted)", logPrefix)
			return `{"success":true}`, nil
		}
	}

	// Note: If purchase.SeenBefore == true but the storage record DOES NOT exist, 
	// it means the server crashed exactly between Apple validation and gem granting.
	// We safely proceed to grant gems, fixing the 'Ghost Purchase' bug.

	// 6. Check for refunds
	if purchase.RefundTime != nil && !purchase.RefundTime.AsTime().IsZero() {
		logger.Warn("%s Refunded — skipping grant", logPrefix)
		return "", errors.ErrInvalidInput
	}

	// 7. Look up product in server config to determine payout
	var product *IAPProduct
	for _, p := range shopConfig.IAPProducts {
		if p.ProductID == req.ProductID {
			product = &p
			break
		}
	}
	if product == nil {
		logger.Error("%s Unknown product", logPrefix)
		return "", errors.ErrInvalidInput
	}

	// 8. Grant items and persist record ATOMICALLY
	pending := NewPendingWrites()
	
	if product.Gems > 0 {
		pending.AddWalletUpdate(userID, map[string]int64{"gems": int64(product.Gems)})
	}

	mutator := NewInventoryMutator()
	for _, reward := range product.Rewards {
		if reward.Type == "currency" {
			pending.AddWalletUpdate(userID, map[string]int64{reward.ID: int64(reward.Amount)})
		} else if reward.Type == "lootbox" {
			for i := 0; i < reward.Amount; i++ {
				_, boxWrite, err := PrepareCreateLootbox(userID, reward.ID)
				if err == nil && boxWrite != nil {
					pending.AddStorageWrite(boxWrite)
				}
			}
		} else if reward.Type == "pet" || reward.Type == "class" || reward.Type == "piece_style" || reward.Type == "background" {
			mutator.AddItem(reward.Type, reward.ItemID)
		}
	}

	invPending, err := mutator.CompileWrites(ctx, nk, logger, userID)
	if err == nil && invPending != nil {
		pending.Merge(invPending)
	}

	grant := IAPPurchaseGrant{
		OriginalTransactionId: verifiedOrigTxId,
		ProductId:             req.ProductID,
		UserId:                userID,
		Jws:                   req.JwsRepresentation,
		Status:                "validated",
		GrantedAt:             time.Now().UnixMilli(),
	}
	grantBytes, _ := json.Marshal(grant)
	pending.AddStorageWrite(&runtime.StorageWrite{
		Collection:      StorageCollectionIAPPurchases,
		Key:             verifiedOrigTxId,
		UserID:          userID,
		Value:           string(grantBytes),
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         "*", // OCC insert lock (prevents concurrent replay grants)
	})

	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("%s Failed to commit atomic IAP grant: %v", logPrefix, err)
		return "", errors.ErrInternalError
	}

	// Emit telemetry for IAP purchase
	EmitServerTelemetry(logger, userID, "iap_purchase", map[string]interface{}{
		"currency": "usd", 
		"amount":   float64(product.USDCents) / 100.0,
		"source":   "iap",
		"sink":     "wallet",
		"product_id": req.ProductID,
		"gems_granted": product.Gems,
	})

	// 10. Send CodeReward notification
	// We pass the full merged payload down to the client so UI responds instantly
	rewardPayload := notify.NewRewardPayload("iap")
	if pending.Payload != nil {
		rewardPayload = pending.Payload // Contains the newly granted pets/classes
	}
	if product.Gems > 0 {
		if rewardPayload.Wallet == nil {
			rewardPayload.Wallet = &notify.WalletDelta{}
		}
		rewardPayload.Wallet.Gems += product.Gems
	}

	if err := notify.SendReward(ctx, nk, userID, rewardPayload); err != nil {
		logger.Error("%s Failed to send reward notification: %v", logPrefix, err)
		// Non-fatal — items already granted
	}

	logger.Info("%s Validated bundle %s txn=%s", logPrefix, product.ProductID, purchase.TransactionId)
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

func getRotationIndex() int {
	if shopConfig == nil {
		return 0
	}

	epoch, err := time.Parse(time.RFC3339, shopConfig.RotationConfig.EpochStart)
	if err != nil {
		return 0
	}

	hoursSinceEpoch := int(time.Since(epoch).Hours())
	rotationPeriod := shopConfig.RotationConfig.RefreshIntervalHours
	if rotationPeriod <= 0 {
		rotationPeriod = 24
	}

	return hoursSinceEpoch / rotationPeriod
}

func getActiveRotationSlots() []int {
	if shopConfig == nil || shopConfig.RotationConfig.Slots == 0 {
		return []int{1, 2, 3, 4}
	}

	currentRotation := getRotationIndex() % shopConfig.RotationConfig.Slots

	// Return slots based on current rotation
	slots := make([]int, shopConfig.RotationConfig.Slots)
	for i := 0; i < shopConfig.RotationConfig.Slots; i++ {
		slots[i] = ((currentRotation + i) % shopConfig.RotationConfig.Slots) + 1
	}

	return slots
}

func resolveShopItem(item *ShopItem) (string, string, uint32) {
	if item.Pool != "" && item.RotationSlot != nil {
		pool, exists := shopConfig.ItemPools[item.Pool]
		if exists && len(pool) > 0 {
			rotIdx := getRotationIndex()
			// Deterministic pick based on rotation day and slot number to avoid overlap
			idx := (rotIdx + *item.RotationSlot) % len(pool)
			poolItem := pool[idx]
			return fmt.Sprintf("%s_%d", poolItem.Type, poolItem.ID), poolItem.Type, poolItem.ID
		}
	}
	return item.ID, item.Type, item.ItemID
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
func purchaseFail(requestId, userID string, nk runtime.NakamaModule, logger runtime.Logger, err error) (string, error) {
	if requestId != "" {
		writePurchaseLog(context.Background(), nk, userID, requestId, "", 0, 0, false, nil)
	}
	return "", err
}

// ── Revocation RPC ───────────────────────────────────────────────────────────

// Handles server-side IAP revocation from Apple App Store Server Notifications or admin action.
func RpcRevokeIAPPurchase(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req struct {
		OriginalTransactionId string `json:"original_transaction_id"`
		RevocationReason      string `json:"revocation_reason"`
		UserId                string `json:"user_id,omitempty"` // Used for Server-to-Server webhooks
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", errors.ErrUnmarshal
	}

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		// S2S Execution: Webhook proxy must provide user_id (extracted from appAccountToken)
		if req.UserId == "" {
			return "", fmt.Errorf("missing user_id for server-to-server invocation")
		}
		userID = req.UserId
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
	
	var product *IAPProduct
	for _, p := range shopConfig.IAPProducts {
		if p.ProductID == grant.ProductId {
			product = &p
			break
		}
	}

	if product == nil {
		logger.Warn("%s Unknown product %s in revocation", logPrefix, grant.ProductId)
		return `{"success":true}`, nil
	}

	pending := NewPendingWrites()

	// Add the grant status update to the atomic batch using OCC Version lock
	pending.AddStorageWrite(&runtime.StorageWrite{
		Collection:      StorageCollectionIAPPurchases,
		Key:             req.OriginalTransactionId,
		UserID:          userID,
		Value:           string(grantBytes),
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         objects[0].Version, // OCC lock
	})

	gemDeduction := product.Gems
	if product.RevokeGemDebt > 0 {
		gemDeduction = product.RevokeGemDebt
	}

	if gemDeduction > 0 {
		pending.AddWalletDeduction(userID, "gems", int64(gemDeduction))
	}

	mutator := NewInventoryMutator()
	for _, reward := range product.Rewards {
		if reward.Type == "pet" || reward.Type == "class" || reward.Type == "piece_style" || reward.Type == "background" {
			mutator.RemoveItem(reward.Type, reward.ItemID)
		}
	}

	invPending, err := mutator.CompileWrites(ctx, nk, logger, userID)
	if err == nil && invPending != nil {
		pending.Merge(invPending)
	}

	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("%s Failed to process revocation: %v", logPrefix, err)
		return "", errors.ErrInternalError // CRITICAL: Tell Apple to retry later
	}

	// Emit telemetry for IAP revocation
	EmitServerTelemetry(logger, userID, "iap_revocation", map[string]interface{}{
		"product_id":   grant.ProductId,
		"gems_revoked": gemDeduction,
		"reason":       req.RevocationReason,
	})

	logger.Info("%s Revoked: reason=%s (Deducted %d gems)", logPrefix, req.RevocationReason, gemDeduction)
	return `{"success":true}`, nil
}
