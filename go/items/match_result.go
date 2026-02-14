package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"block-server/errors"
	"block-server/notify"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	storageCollectionMatchHistory = "match_history"
	storageKeyLastMatch           = "last_match"
	storageCollectionActiveMatch  = "active_match"
	storageKeyCurrentMatch        = "current"
)

type MatchHistory struct {
	LastMatchTime int64 `json:"last_match_time"`
}

type ActiveMatch struct {
	MatchID       string `json:"match_id"`
	StartTime     int64  `json:"start_time"`
	OpponentID    string `json:"opponent_id,omitempty"`
}

// MatchResultRecord stores a player's claimed result for consensus
type MatchResultRecord struct {
	UserID      string `json:"user_id"`
	ClaimedWin  bool   `json:"claimed_win"`
	Score       int    `json:"score"`
	SubmittedAt int64  `json:"submitted_at"`
}

type NotifyMatchStartRequest struct {
	MatchID    string `json:"match_id"`
	OpponentID string `json:"opponent_id,omitempty"`
}

// RpcNotifyMatchStart records the start of a match for validation
func RpcNotifyMatchStart(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	var req NotifyMatchStartRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Error("Failed to unmarshal notify match start: %v", err)
		return "", errors.ErrUnmarshal
	}

	if req.MatchID == "" {
		return "", errors.ErrInvalidInput
	}

	// R2: Token Immutability - reject if active match already exists (with staleness expiry)
	existing, _ := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionActiveMatch,
		Key:        storageKeyCurrentMatch,
		UserID:     userID,
	}})
	if len(existing) > 0 {
		var staleCheck ActiveMatch
		if err := json.Unmarshal([]byte(existing[0].Value), &staleCheck); err == nil {
			if time.Now().UnixMilli()-staleCheck.StartTime > maxMatchDurationMs {
				// Stale lock — auto-clear so player isn't permanently blocked.
				clearActiveMatch(ctx, nk, logger, userID)
			} else {
				logger.Warn("User %s already has active match, rejecting notify", userID)
				return "", fmt.Errorf("active match already exists")
			}
		}
	}

	activeMatch := ActiveMatch{
		MatchID:    req.MatchID,
		StartTime:  time.Now().UnixMilli(),
		OpponentID: req.OpponentID,
	}

	value, err := json.Marshal(activeMatch)
	if err != nil {
		return "", err
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionActiveMatch,
		Key:             storageKeyCurrentMatch,
		UserID:          userID,
		Value:           string(value),
		PermissionRead:  0, // Hidden
		PermissionWrite: 0,
	}})

	if err != nil {
		logger.Error("Failed to write active match: %v", err)
		return "", errors.ErrCouldNotWriteStorage
	}

	logger.Info("Match start notified for user %s: match_id=%s", userID, req.MatchID)
	return "{}", nil
}

// RpcSubmitMatchResult handles match result submission and reward generation
func RpcSubmitMatchResult(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

    // Rate Limit Check
	if err := checkMatchRateLimit(ctx, nk, userID); err != nil {
		logger.Warn("Rate limit exceeded for user %s: %v", userID, err)
		return "", err
	}

	var req MatchResultRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Error("Failed to unmarshal match result: %v", err)
		return "", errors.ErrUnmarshal
	}

	// Validate against Active Match (Security)
	activeMatch, err := validateActiveMatch(ctx, nk, logger, userID, req.MatchID)
	if err != nil {
		logger.Warn("Match validation failed for user %s: %v", userID, err)
		return "", err
	}

	// R1: Consensus Check - verify both players agree on outcome
	consensusResult, err := checkMatchConsensus(ctx, nk, logger, userID, activeMatch.OpponentID, req.MatchID, req.Won, req.FinalScore)
	if err != nil {
		logger.Warn("Consensus check failed for user %s: %v", userID, err)
		return "", err
	}

	// If consensus invalidated our win claim (opponent also claimed win), downgrade to loss
	actualWon := req.Won
	if consensusResult == "conflict" {
		logger.Warn("Match %s: Both players claimed victory. Voiding win for user %s", req.MatchID, userID)
		actualWon = false // Neither gets winner rewards
	}

	// Validate equipped items exist
	if !ValidateItemExists(storageKeyPet, req.EquippedPetID) {
		logger.Warn("Invalid pet ID in match result: %d", req.EquippedPetID)
		return "", errors.ErrInvalidItemID
	}
	if !ValidateItemExists(storageKeyClass, req.EquippedClassID) {
		logger.Warn("Invalid class ID in match result: %d", req.EquippedClassID)
		return "", errors.ErrInvalidItemID
	}

	// Override request with consensus-validated result
	req.Won = actualWon

	// Process rewards atomically, then clean up active match
	result, err := processMatchRewards(ctx, nk, logger, userID, &req)
	if err != nil {
		logger.Error("Failed to process match rewards: %v", err)
		return "", err
	}

	respBytes, err := json.Marshal(result)
	if err != nil {
		logger.Error("Failed to marshal match reward response: %v", err)
		return "", errors.ErrMarshal
	}

	xpAmount := 0
	if result.Progression != nil && result.Progression.XpGranted != nil {
		xpAmount = *result.Progression.XpGranted
	}
	logger.Info("Match result processed for user %s: won=%v, xp=%d",
		userID, req.Won, xpAmount)

	return string(respBytes), nil
}

const (
	minMatchDurationMs         = 10000   // 10 seconds
	minRateLimitMs             = 30000   // 30 seconds
	maxMatchDurationMs         = 3600000 // 1 hour — no match lasts this long
	storageCollectionResults   = "match_results"
)

func validateActiveMatch(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, matchID string) (*ActiveMatch, error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionActiveMatch,
		Key:        storageKeyCurrentMatch,
		UserID:     userID,
	}})
	if err != nil {
		return nil, errors.ErrCouldNotReadStorage
	}

	if len(objects) == 0 {
		return nil, fmt.Errorf("no active match found")
	}

	var activeMatch ActiveMatch
	if err := json.Unmarshal([]byte(objects[0].Value), &activeMatch); err != nil {
		return nil, errors.ErrUnmarshal
	}

	if activeMatch.MatchID != matchID {
		return nil, fmt.Errorf("match ID mismatch")
	}

	if time.Now().UnixMilli()-activeMatch.StartTime < minMatchDurationMs {
		return nil, fmt.Errorf("match duration too short")
	}

	if time.Now().UnixMilli()-activeMatch.StartTime > maxMatchDurationMs {
		// Stale match — auto-clear and reject. Player can start fresh.
		clearActiveMatch(ctx, nk, logger, userID)
		return nil, fmt.Errorf("stale active match expired")
	}

	return &activeMatch, nil
}

// checkMatchConsensus reads opponent first, then writes ours.
// Ordering matters: read→write prevents first-submitter-wins exploit.
func checkMatchConsensus(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, opponentID string, matchID string, claimedWin bool, score int) (string, error) {
	// Check opponent's claim before writing ours
	var opponentClaimedWin bool
	var opponentFound bool

	if opponentID != "" {
		opponentResults, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
			Collection: storageCollectionResults,
			Key:        matchID + "_" + opponentID,
			UserID:     opponentID,
		}})
		if err == nil && len(opponentResults) > 0 {
			var opponentRecord MatchResultRecord
			if err := json.Unmarshal([]byte(opponentResults[0].Value), &opponentRecord); err == nil {
				opponentFound = true
				opponentClaimedWin = opponentRecord.ClaimedWin
			}
		}
	}

	// Always write our result for audit
	myRecord := MatchResultRecord{
		UserID:      userID,
		ClaimedWin:  claimedWin,
		Score:       score,
		SubmittedAt: time.Now().UnixMilli(),
	}
	myRecordBytes, _ := json.Marshal(myRecord)

	_, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionResults,
		Key:             matchID + "_" + userID,
		UserID:          userID,
		Value:           string(myRecordBytes),
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	if err != nil {
		return "", err
	}

	// Resolve
	if opponentID == "" {
		return "ok", nil // No opponent to check (solo or unknown)
	}
	if !opponentFound {
		return "pending", nil // Opponent hasn't submitted yet
	}
	if claimedWin && opponentClaimedWin {
		logger.Warn("CONFLICT: Match %s - both %s and %s claimed victory", matchID, userID, opponentID)
		return "conflict", nil
	}

	return "ok", nil
}

func clearActiveMatch(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) {
	// Background context so client disconnect can't cancel the cleanup.
	err := nk.StorageDelete(context.Background(), []*runtime.StorageDelete{{
		Collection: storageCollectionActiveMatch,
		Key:        storageKeyCurrentMatch,
		UserID:     userID,
	}})
	if err != nil {
		logger.Error("Failed to clear active match for user %s: %v", userID, err)
	}
}

func checkMatchRateLimit(ctx context.Context, nk runtime.NakamaModule, userID string) error {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionMatchHistory,
		Key:        storageKeyLastMatch,
		UserID:     userID,
	}})
	if err != nil {
		return errors.ErrCouldNotReadStorage
	}

	if len(objects) > 0 {
		var history MatchHistory
		if err := json.Unmarshal([]byte(objects[0].Value), &history); err == nil {
			if time.Now().UnixMilli()-history.LastMatchTime < minRateLimitMs {
				return fmt.Errorf("rate limit exceeded")
			}
		}
	}
	return nil
}

func updateMatchHistory(ctx context.Context, nk runtime.NakamaModule, userID string) {
	history := MatchHistory{
		LastMatchTime: time.Now().UnixMilli(),
	}
	value, _ := json.Marshal(history)
	nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionMatchHistory,
		Key:             storageKeyLastMatch,
		UserID:          userID,
		Value:           string(value),
		PermissionRead:  0, // Hidden from client
		PermissionWrite: 0,
	}})
}

// processMatchRewards handles reward generation with two-phase commit.
// Phase 1 (failable): wallet deduction for drop ticket — may fail on insufficient balance.
// Phase 2 (idempotent): XP, match history, and lootbox (only if Phase 1 succeeded).
func processMatchRewards(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, req *MatchResultRequest) (*notify.RewardPayload, error) {
	pending := NewPendingWrites()

	result := notify.NewRewardPayload("match")
	result.ReasonKey = "reward.match.complete"
	result.Progression = &notify.ProgressionDelta{}

	// Determine XP based on win/loss
	xpAmount := GetMatchConfig().LossXP
	if req.Won {
		xpAmount = GetMatchConfig().WinXP
	}
	result.Progression.XpGranted = notify.IntPtr(xpAmount)

	// Player XP
	playerLevelUp, xpPending, err := preparePlayerXP(ctx, nk, logger, userID, xpAmount)
	if err != nil {
		logger.Warn("Failed to prepare player XP: %v", err)
	} else {
		pending.Merge(xpPending)
		if playerLevelUp > 0 {
			result.Progression.NewPlayerLevel = notify.IntPtr(playerLevelUp)
		}
	}

	// Phase 1: Wallet deduction (may fail on insufficient balance from concurrent request)
	hasDropTicket, err := checkDropTicketAvailable(ctx, nk, logger, userID)
	if err != nil {
		logger.Warn("Failed to check drop ticket: %v", err)
	}

	if hasDropTicket {
		walletPending := NewPendingWrites()
		walletPending.AddWalletUpdate(userID, map[string]int64{walletKeyDropsLeft: -1})
		if err := CommitPendingWrites(ctx, nk, logger, walletPending); err != nil {
			// Race or insufficient balance — degrade gracefully, no lootbox
			logger.Warn("Drop ticket unavailable (race or insufficient balance): %v", err)
			hasDropTicket = false
		}
	}

	// Lootbox (only if Phase 1 wallet deduction succeeded)
	if hasDropTicket {
		tier := GetLootboxConfig().MatchLossTier
		if req.Won {
			tier = GetLootboxConfig().MatchWinTier
		}

		lootbox, lootboxWrite, err := PrepareCreateLootbox(userID, tier)
		if err != nil {
			logger.Error("Failed to prepare lootbox: %v", err)
		} else {
			pending.AddStorageWrite(lootboxWrite)
			result.Lootboxes = []notify.LootboxGrant{{
				ID:     lootbox.ID,
				Tier:   lootbox.Tier,
				Source: "match_drop",
			}}
			result.Action = "open_lootbox"
			result.ActionPayload = lootbox.ID
		}
	}

	// Match history for rate limiting
	historyValue, _ := json.Marshal(MatchHistory{LastMatchTime: time.Now().UnixMilli()})
	pending.AddStorageWrite(&runtime.StorageWrite{
		Collection:      storageCollectionMatchHistory,
		Key:             storageKeyLastMatch,
		UserID:          userID,
		Value:           string(historyValue),
		PermissionRead:  0,
		PermissionWrite: 0,
	})

	// Phase 2: Idempotent writes (XP, history, lootbox if ticket succeeded)
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("Match result commit failed: %v", err)
		return nil, fmt.Errorf("match reward commit failed: %w", err)
	}

	// StorageDelete can't go in MultiUpdate, so this runs after commit
	clearActiveMatch(ctx, nk, logger, userID)

	return result, nil
}

// preparePlayerXP applies diminishing returns and returns deferred progression writes.
// Can't use PrepareExperience here because that only handles pets/classes.
func preparePlayerXP(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, xpAmount int) (int, *PendingWrites, error) {
	const treeName = "player_level"
	const playerItemID = uint32(0)

	pending := NewPendingWrites()

	// Daily match count writes independently (OCC). It's a soft signal for XP scaling,
	// not critical state. Worst case the multiplier is off by one match.
	matchesToday, err := incrementDailyMatchCount(ctx, nk, userID)
	if err != nil {
		logger.Warn("Failed to get daily match count, using conservative default: %v", err)
		matchesToday = 5 // worst case: >4 matches today → minimum multiplier (0.25)
	}

	// Diminishing XP curve: 100%, 80%, 60%, 40%, 25%...
	xpMultiplier := 1.0
	switch {
	case matchesToday <= 1:
		xpMultiplier = 1.0
	case matchesToday == 2:
		xpMultiplier = 0.8
	case matchesToday == 3:
		xpMultiplier = 0.6
	case matchesToday == 4:
		xpMultiplier = 0.4
	default:
		xpMultiplier = 0.25
	}

	adjustedXP := int(float64(xpAmount) * xpMultiplier)
	if adjustedXP < 1 {
		adjustedXP = 1
	}

	var resultLevel int
	var oldLevel int

	// Deferred progression write
	_, progWrite, err := PrepareProgressionUpdate(ctx, nk, logger, userID, ProgressionKeyPlayer, playerItemID, func(prog *ItemProgression) error {
		oldLevel = prog.Level
		prog.Exp += adjustedXP

		// Level tree might not exist yet. Just bank the XP if so.
		tree, exists := GetLevelTree(treeName)
		if !exists {
			return nil
		}

		calculatedLevel, err := CalculateLevel(treeName, prog.Exp)
		if err != nil {
			// Save XP even if level calc fails
			logger.Warn("Player level calculation failed, XP saved without leveling: %v", err)
			return nil
		}

		// Cap at max level
		if calculatedLevel > tree.MaxLevel {
			calculatedLevel = tree.MaxLevel
			prog.Exp = tree.LevelThresholds[tree.MaxLevel]
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

	// Level-up rewards
	if resultLevel > oldLevel {
		for lvl := oldLevel + 1; lvl <= resultLevel; lvl++ {
			levelRewards, err := PrepareLevelRewards(ctx, nk, logger, userID, treeName, lvl, "player", playerItemID)
			if err != nil {
				logger.Warn("Failed to prepare player level %d rewards: %v", lvl, err)
				continue
			}
			pending.Merge(levelRewards)
		}
	}

	return resultLevel, pending, nil
}

// checkDropTicketAvailable checks wallet for available drops without modifying any state
func checkDropTicketAvailable(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string) (bool, error) {
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

	logger.Info("User %s has %d drop tickets available", userID, dropsLeft)
	return true, nil
}

// MatchConfig holds match reward configuration
type MatchConfig struct {
	WinXP             int `json:"win_xp"`
	LossXP            int `json:"loss_xp"`
	ParticipationGold int `json:"participation_gold"`
	WinBonusGold      int `json:"win_bonus_gold"`
}

var matchConfig *MatchConfig

func GetMatchConfig() *MatchConfig {
	if matchConfig == nil {
		// Default values if not loaded from game data
		matchConfig = &MatchConfig{
			WinXP:             100,
			LossXP:            25,
			ParticipationGold: 10,
			WinBonusGold:      15,
		}
	}
	return matchConfig
}

// LootboxConfig holds lootbox tier configuration
type LootboxConfig struct {
	MatchWinTier  string `json:"match_win_tier"`
	MatchLossTier string `json:"match_loss_tier"`
}

var lootboxConfig *LootboxConfig

func GetLootboxConfig() *LootboxConfig {
	if lootboxConfig == nil {
		lootboxConfig = &LootboxConfig{
			MatchWinTier:  "standard",
			MatchLossTier: "standard",
		}
	}
	return lootboxConfig
}
// PrepareCreateLootbox prepares a lootbox creation without committing.
// Returns the lootbox and the storage write to be committed later.
func PrepareCreateLootbox(userID string, tier string) (*Lootbox, *runtime.StorageWrite, error) {
	timestamp := time.Now().UnixMilli()
	lootbox := &Lootbox{
		ID:        fmt.Sprintf("lb_%s_%d_%04x", userID[:8], timestamp, rand.Intn(0xFFFF)),
		Tier:      tier,
		CreatedAt: timestamp,
		Opened:    false,
	}

	value, err := json.Marshal(lootbox)
	if err != nil {
		return nil, nil, errors.ErrMarshal
	}

	write := &runtime.StorageWrite{
		Collection:      storageCollectionLootboxes,
		Key:             lootbox.ID,
		UserID:          userID,
		Value:           string(value),
		PermissionRead:  1,
		PermissionWrite: 0,
	}

	return lootbox, write, nil
}

// createLootbox creates a new unopened lootbox for the user
func createLootbox(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, tier string) (*Lootbox, error) {
	lootbox, write, err := PrepareCreateLootbox(userID, tier)
	if err != nil {
		return nil, err
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{write})
	if err != nil {
		return nil, fmt.Errorf("failed to write lootbox: %w", err)
	}

	return lootbox, nil
}
