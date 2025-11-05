package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	storageCollectionDrops = "drops"
	storageKeyDaily        = "daily"
	walletKeyDropsLeft     = "dropsLeft"
	maxDrops               = 5
	dailyDropGrantCount    = 3
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

	var resp struct {
		DropsLeft int64 `json:"dropsLeft"`
	}
	resp.DropsLeft = int64(0)

	// get current dailyDrops state
	dailyDropsState, dropsStorageObj, err := getDailyDropsState(ctx, logger, nk)
	if err != nil {
		return err
	}

	// get account data
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		logger.Error("AccountGetId error: %v", err)
		return errors.ErrCouldNotGetAccount
	}

	// get drops total before
	currentDropsBefore := int64(0)

	var wallet map[string]int64
	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		logger.Error("Unmarshal error: %v", err)
		return errors.ErrUnmarshal
	}
	drops, ok := wallet[walletKeyDropsLeft]
	if !ok {
		logger.Error("wallet JSON is valid but missing required key: %s", walletKeyDropsLeft)
	}
	currentDropsBefore = drops

	// check if user has already claimed
	if !canUserClaimDailyDrops(dailyDropsState) {
		resp.DropsLeft = currentDropsBefore
		return errors.ErrDropsAlreadyClaimed
	}
	newTotalDrops, err := grantCappedDrops(ctx, logger, nk, userID, dailyDropGrantCount)
	if err != nil {
		logger.Error("wallet JSON is valid but missing required key (%s): %v", walletKeyDropsLeft, err)
	}

	// grant drops
	resp.DropsLeft = newTotalDrops
	// write current time to dailyDrops
	dailyDropsState.LastClaimUnix = time.Now().UTC().Unix()
	// marshall the response object
	dailyDropsBytes, err := json.Marshal(dailyDropsState)
	if err != nil {
		logger.Error("Marshal error: %v", err)
		return errors.ErrMarshal
	}
	// Version-based optimistic locking prevents race conditions during concurrent updates
	version := ""
	if dropsStorageObj != nil {
		version = dropsStorageObj.GetVersion()
	}
	
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionDrops,
		Key:             storageKeyDaily,
		PermissionRead:  1, // user access only
		PermissionWrite: 0, // no client writes
		Value:           string(dailyDropsBytes),
		Version:         version,
		UserID:          userID,
	}})
	if err != nil {
		logger.Error("StorageWrite error: %v", err)
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

// grant drops up to the cap
func grantCappedDrops(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, amountToAdd int64) (int64, error) {
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		logger.Error("AccountGetId error: %v", err)
		return 0, errors.ErrCouldNotGetAccount
	}
	// get the current number of drops from the wallet
	// get drops total before
	currentDropsBefore := int64(0)

	var wallet map[string]int64
	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		logger.Error("Unmarshal error: %v", err)
		return 0, errors.ErrUnmarshal
	}
	drops, ok := wallet[walletKeyDropsLeft]
	if !ok {
		logger.Error("wallet JSON is valid but missing required key: %s", walletKeyDropsLeft)
	}
	currentDropsBefore = drops
	// determine how many drops can granted
	spaceAvailable := maxDrops - currentDropsBefore
	if spaceAvailable <= 0 {
		logger.Info("User '%s' already at or over drop cap. No drops granted. Current total: %d", userID, currentDropsBefore)
		return currentDropsBefore, nil // No space, no drops granted, currentDrops returned
	}
	// clamp to space available
	dropsToGrant := amountToAdd
	if dropsToGrant > spaceAvailable {
		dropsToGrant = spaceAvailable
	}
	// update wallet
	changeset := map[string]int64{
		walletKeyDropsLeft: dropsToGrant,
	}
	if _, _, err := nk.WalletUpdate(ctx, userID, changeset, map[string]interface{}{}, false); err != nil {
		logger.Error("WalletUpdate error: %v", err)
		return 0, errors.ErrCouldNotUpdateWallet
	}
	//return new current drops total
	newTotalDrops := currentDropsBefore + dropsToGrant
	logger.Info("Granted %d drops to user '%s'. New total: %d.", dropsToGrant, userID, newTotalDrops)
	return newTotalDrops, nil
}

// check the last claimed time was before midnight
func canUserClaimDailyDrops(d dailyDrops) bool {
	nowUTC := time.Now().UTC()
	midnightUTC := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	lastClaimTime := time.Unix(d.LastClaimUnix, 0).UTC()
	return lastClaimTime.Before(midnightUTC)
}
