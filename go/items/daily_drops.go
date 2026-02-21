package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	storageCollectionDrops = "drops"
	storageKeyDaily        = "daily"
	walletKeyDropsLeft     = "dropsLeft"
	// walletKeyRoundTokens stores half-token units: 2 units = 1.0 token, 6 units = exchange threshold (3.0 tokens).
	walletKeyRoundTokens = "roundTokens"
	maxDrops             = 5
	dailyDropGrantCount  = 3
)

type dailyDrops struct {
	LastClaimUnix int64 `json:"last_claim_unix"` // The last time the user claimed the daily drops in UNIX time.
}

func RpcCanClaimDailyDrops(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var resp struct {
		CanClaimDailyDrops bool `json:"canClaimDailyDrops"`
	}

	dailyDropsState, _, err := getDailyDropsState(ctx, logger, nk)
	if err != nil {
		logger.Error("Error getting daily drops: %v", err)
		return "", errors.ErrInternalError
	}

	resp.CanClaimDailyDrops = canUserClaimDailyDrops(dailyDropsState)

	respBytes, err := json.Marshal(resp)
	if err != nil {
		logger.Error("Marshal error: %v", err)
		return "", errors.ErrMarshal
	}

	return string(respBytes), nil
}

func TryClaimDailyDrops(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) error {

	// get UserID from context
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)

	// reject if not from valid client
	if !ok {
		return errors.ErrNoUserIdFound
	}

	// get current dailyDrops state
	dailyDropsState, dropsStorageObj, err := getDailyDropsState(ctx, logger, nk)
	if err != nil {
		return err
	}

	// check if user has already claimed
	if !canUserClaimDailyDrops(dailyDropsState) {
		return errors.ErrDropsAlreadyClaimed
	}

	// Prepare wallet changeset without committing — prevents free-drops exploit
	// if the timestamp write were to fail after an immediate WalletUpdate.
	changeset, _, err := prepareCappedDrops(ctx, nk, logger, userID, dailyDropGrantCount)
	if err != nil {
		logger.Error("failed to prepare daily drops: %v", err)
		return err
	}

	// Nothing to grant (already at cap)
	if changeset == nil {
		return nil
	}

	// Build timestamp write
	dailyDropsState.LastClaimUnix = time.Now().UTC().Unix()
	dailyDropsBytes, err := json.Marshal(dailyDropsState)
	if err != nil {
		logger.Error("Marshal error: %v", err)
		return errors.ErrMarshal
	}

	version := ""
	if dropsStorageObj != nil {
		version = dropsStorageObj.GetVersion()
	}

	// Commit wallet + timestamp atomically via MultiUpdate
	pending := NewPendingWrites()
	pending.AddWalletUpdate(userID, changeset)
	pending.AddStorageWrite(&runtime.StorageWrite{
		Collection:      storageCollectionDrops,
		Key:             storageKeyDaily,
		// PermissionRead: 1 — drops state is private (last claim time is PII-adjacent).
		// Other game data uses 2 (public) for leaderboard/social features.
		// daily_matches uses 0 (server-only) since it's a rate-limit counter.
		PermissionRead:  1,
		PermissionWrite: 0, // no client writes
		Value:           string(dailyDropsBytes),
		Version:         version,
		UserID:          userID,
	})

	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("daily drops commit failed: %v", err)
		return errors.ErrCouldNotWriteStorage
	}

	return nil
}

// get last daily drop object
func getDailyDropsState(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) (dailyDrops, *api.StorageObject, error) {
	var data dailyDrops
	data.LastClaimUnix = 0 // Default for new users

	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		return data, nil, errors.ErrNoUserIdFound
	}
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionDrops,
		Key:        storageKeyDaily,
		UserID:     userID,
	}})
	if err != nil {
		logger.Error("StorageRead error: %v", err)
		return data, nil, errors.ErrCouldNotReadStorage
	}

	if len(objects) == 0 {
		return data, nil, nil // New user case
	}

	storageObj := objects[0]
	if err := json.Unmarshal([]byte(storageObj.GetValue()), &data); err != nil {
		logger.Error("Unmarshal error: %v", err)
		return data, nil, errors.ErrUnmarshal
	}

	return data, storageObj, nil
}

// prepareCappedDrops calculates the wallet changeset for granting drops up to the cap.
// Returns (changeset, newTotal, error). Caller commits via PendingWrites.
func prepareCappedDrops(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, amountToAdd int64) (map[string]int64, int64, error) {
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		logger.Error("AccountGetId error: %v", err)
		return nil, 0, errors.ErrCouldNotGetAccount
	}

	var wallet map[string]int64
	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		logger.Error("Unmarshal error: %v", err)
		return nil, 0, errors.ErrUnmarshal
	}

	currentDrops, ok := wallet[walletKeyDropsLeft]
	if !ok {
		logger.Warn("wallet key not yet initialized (new user): %s", walletKeyDropsLeft)
	}

	spaceAvailable := maxDrops - currentDrops
	if spaceAvailable <= 0 {
		logger.Info("User '%s' already at or over drop cap. No drops granted. Current total: %d", userID, currentDrops)
		return nil, currentDrops, nil
	}

	dropsToGrant := amountToAdd
	if dropsToGrant > spaceAvailable {
		dropsToGrant = spaceAvailable
	}

	changeset := map[string]int64{walletKeyDropsLeft: dropsToGrant}
	newTotal := currentDrops + dropsToGrant
	logger.Info("Prepared %d drops for user '%s'. New total: %d.", dropsToGrant, userID, newTotal)
	return changeset, newTotal, nil
}

// check the last claimed time was before midnight
func canUserClaimDailyDrops(d dailyDrops) bool {
	nowUTC := time.Now().UTC()
	midnightUTC := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	lastClaimTime := time.Unix(d.LastClaimUnix, 0).UTC()
	return lastClaimTime.Before(midnightUTC)
}

func consumeDropTicket(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) (bool, error) {
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return false, err
	}

	var wallet map[string]int64
	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		return false, err
	}

	dropsLeft := wallet[walletKeyDropsLeft]
	if dropsLeft <= 0 {
		return false, nil
	}

	changeset := map[string]int64{walletKeyDropsLeft: -1}
	if _, _, err := nk.WalletUpdate(ctx, userID, changeset, nil, false); err != nil {
		return false, err
	}

	logger.Info("User %s consumed drop ticket. Remaining: %d", userID, dropsLeft-1)
	return true, nil
}

const storageKeyDailyMatches = "daily_matches"

type dailyMatches struct {
	Count     int   `json:"count"`
	ResetUnix int64 `json:"reset_unix"`
}

func incrementDailyMatchCount(ctx context.Context, nk runtime.NakamaModule, userID string) (int, error) {
	nowUTC := time.Now().UTC()
	midnightUTC := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)

	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionDrops,
		Key:        storageKeyDailyMatches,
		UserID:     userID,
	}})

	var data dailyMatches
	var version string
	if err == nil && len(objects) > 0 {
		if err := json.Unmarshal([]byte(objects[0].Value), &data); err != nil {
			return 0, fmt.Errorf("unmarshal daily matches: %w", err)
		}
		version = objects[0].Version
	}

	// Reset if last reset was before today's midnight
	if time.Unix(data.ResetUnix, 0).UTC().Before(midnightUTC) {
		data.Count = 0
		data.ResetUnix = midnightUTC.Unix()
	}

	data.Count++

	value, _ := json.Marshal(data)
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionDrops,
		Key:             storageKeyDailyMatches,
		UserID:          userID,
		Value:           string(value),
		Version:         version,
		PermissionRead:  0,
		PermissionWrite: 0,
	}})

	return data.Count, err
}
