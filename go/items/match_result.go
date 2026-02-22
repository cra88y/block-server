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
	Resolved    bool   `json:"resolved"` // True when this player was the second submitter and resolved consensus
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

	// Validate round history (logging + self-healing only; not a hard rejection gate).
	validateRounds(&req, logger)

	// Validate against Active Match (Security)
	activeMatch, err := validateActiveMatch(ctx, nk, logger, userID, req.MatchID)
	if err != nil {
		logger.Warn("Match validation failed for user %s: %v", userID, err)
		return "", err
	}

	// Consensus check (unified path: solo short-circuits in resolveMatchConsensus)
	consensusResult, err := resolveMatchConsensus(ctx, nk, logger, userID, activeMatch.OpponentID, req.MatchID, req.Won, req.FinalScore)
	if err != nil {
		logger.Warn("Consensus check failed for user %s: %v", userID, err)
		return "", err
	}

	// Resolve per-role rewards:
	//   pending  — first submitter, opponent not yet in. Participation-only (no win bonus).
	//   ok       — second submitter, resolved. Full rewards + deferred bonus to first submitter.
	//   resolved — late arrival, already resolved by opponent. Participation-only (bonus already pushed).
	//   conflict — both claimed win. Both downgraded, opponent retroactively penalised.
	isSolo := activeMatch.OpponentID == ""
	actualWon := req.Won
	var opponentIDForDeferred string
	var opponentWonForDeferred bool

	switch consensusResult {
	case "pending":
		// First submitter: withhold win bonus until opponent confirms
		actualWon = false
		logger.Info("Match %s: user %s is first submitter, granting participation rewards", req.MatchID, userID)

	case "resolved":
		// Opponent already resolved and pushed our deferred win bonus via notification
		actualWon = false
		logger.Info("Match %s: user %s arrived late, rewards already resolved by opponent", req.MatchID, userID)

	case "conflict":
		logger.Warn("Match %s: Both players claimed victory. Voiding win for user %s", req.MatchID, userID)
		actualWon = false
		// No retroactive penalty under the token economy: tokens are computed per-player at
		// submission time from their own round history. There is no shared ledger to unwind.
		// NOTE: req.Won is set to false below via req.Won = actualWon, but req.RoundsWon is NOT
		// zeroed — the player still earns tokens from their round history in a conflict.

	case "ok":
		// Second submitter: grant ourselves full rewards, then push deferred gold to first submitter
		if activeMatch.OpponentID != "" {
			opponentIDForDeferred = activeMatch.OpponentID
			// Opponent won if they claimed win AND we didn't (only one can win)
			opponentWonForDeferred = !req.Won
		}
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
	result, err := processMatchRewards(ctx, nk, logger, userID, &req, isSolo)
	if err != nil {
		logger.Error("Failed to process match rewards: %v", err)
		return "", err
	}

	// Second submitter: push deferred gold win bonus to first submitter
	if opponentIDForDeferred != "" {
		deferredReward, err := processDeferredWinBonus(ctx, nk, logger, opponentIDForDeferred, opponentWonForDeferred)
		if err != nil {
			logger.Error("Failed to grant deferred rewards to opponent %s in match %s: %v", opponentIDForDeferred, req.MatchID, err)
			// Non-fatal: our own rewards succeeded. Opponent will have lost their win bonus — acceptable.
		} else if deferredReward != nil {
			if err := notify.SendReward(ctx, nk, opponentIDForDeferred, deferredReward); err != nil {
				logger.Error("Failed to notify deferred reward to opponent %s: %v", opponentIDForDeferred, err)
			}
		}
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
	minMatchDurationMs         = 10000  // 10 seconds
	minRateLimitMs             = 15000  // 15 seconds (reduced from 30s for legitimate rapid rematches)
	maxMatchDurationMs         = 600000 // 10 minutes (reduced from 1 hour - no match lasts this long)
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
		// Clear stale match state.
		clearActiveMatch(ctx, nk, logger, userID)
		return nil, fmt.Errorf("stale active match expired")
	}

	return &activeMatch, nil
}

// resolveMatchConsensus implements write-first single-resolution consensus.
//
// Write-first ordering: we commit our claim before reading opponent's.
// After our StorageWrite returns, opponent's subsequent StorageRead will see our record.
// This collapses the TOCTOU window vs. the prior read→write ordering.
//
// Resolution roles:
//
//	pending  — first submitter (opponent not yet written). Caller grants participation-only.
//	ok       — second submitter. Caller grants full rewards + deferred bonus to first submitter.
//	resolved — late arrival (opponent already set Resolved=true on their record). Participation-only.
//	conflict — both claimed win. Both downgraded.
func resolveMatchConsensus(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, opponentID string, matchID string, claimedWin bool, score int) (string, error) {
	if opponentID == "" {
		return "ok", nil // Solo — no consensus needed, caller handles isSolo reward reduction
	}

	// Step 1: Write our claim FIRST (unconditional)
	myRecord := MatchResultRecord{
		UserID:      userID,
		ClaimedWin:  claimedWin,
		Score:       score,
		SubmittedAt: time.Now().UnixMilli(),
		Resolved:    false,
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

	// Step 2: Read opponent's claim AFTER writing ours
	opponentResults, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionResults,
		Key:        matchID + "_" + opponentID,
		UserID:     opponentID,
	}})
	if err != nil || len(opponentResults) == 0 {
		// First submitter: opponent hasn't written yet
		return "pending", nil
	}

	var opponentRecord MatchResultRecord
	if err := json.Unmarshal([]byte(opponentResults[0].Value), &opponentRecord); err != nil {
		return "pending", nil
	}

	// If opponent's record has Resolved=true, they were the second submitter and already resolved.
	// Our deferred win bonus (if applicable) was already pushed via notify.SendReward.
	if opponentRecord.Resolved {
		return "resolved", nil
	}

	// Conflict: both claimed win simultaneously
	if claimedWin && opponentRecord.ClaimedWin {
		logger.Warn("CONFLICT: Match %s - both %s and %s claimed victory", matchID, userID, opponentID)
		return "conflict", nil
	}

	// Mark local user record as resolved.
	// Opponents will read this record to confirm consensus.
	// Maintains authority boundaries by not mutating opponent records directly.
	myRecord.Resolved = true
	myRecordBytes, _ = json.Marshal(myRecord)
	nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionResults,
		Key:             matchID + "_" + userID,
		UserID:          userID,
		Value:           string(myRecordBytes),
		PermissionRead:  0,
		PermissionWrite: 0,
	}})

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

// processMatchRewards handles reward generation atomically.
//
// Lootbox economy: dropsLeft is a daily pool of lootbox slots (3/day, max 5).
// Round tokens are the key — 3 full tokens (6 half-units) exchange one slot for a lootbox.
// No lootbox is granted per-match directly; tokens must accumulate across matches.
//
// Pre-read pattern: one AccountGetId at the top covers token read and drop availability.
// isSolo: halves XP to prevent solo-match farming.
func processMatchRewards(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, req *MatchResultRequest, isSolo bool) (*notify.RewardPayload, error) {
	cfg := GetMatchConfig()
	pending := NewPendingWrites()

	result := notify.NewRewardPayload("match")
	result.ReasonKey = "reward.match.complete"
	result.Progression = &notify.ProgressionDelta{}

	// --- Pre-read wallet: one AccountGetId covers drop check, token read, and metadata ---
	var preTokens, preDrops int64
	if account, err := nk.AccountGetId(ctx, userID); err == nil {
		var wallet map[string]int64
		if json.Unmarshal([]byte(account.Wallet), &wallet) == nil {
			preTokens = wallet[walletKeyRoundTokens]
			preDrops = wallet[walletKeyDropsLeft]
		}
	}

	// --- XP ---
	xpAmount := cfg.LossXP
	if req.Won {
		xpAmount = cfg.WinXP
	}
	if isSolo {
		xpAmount = xpAmount / 2
		if xpAmount < 1 {
			xpAmount = 1
		}
	}
	result.Progression.XpGranted = notify.IntPtr(xpAmount)

	playerLevelUp, xpPending, matchesToday, err := preparePlayerXP(ctx, nk, logger, userID, xpAmount)
	if err != nil {
		logger.Warn("Failed to prepare player XP: %v", err)
	} else {
		pending.Merge(xpPending)
		if playerLevelUp > 0 {
			result.Progression.NewPlayerLevel = notify.IntPtr(playerLevelUp)
		}
	}

	// dropsLeft = daily pool of lootbox slots. Round tokens are the key.
	// Exchange fires when tokens cross the threshold AND a slot is available.
	tokensEarned := computeTokensEarned(req, isSolo, cfg)
	postTokens := preTokens + int64(tokensEarned)

	willExchange := postTokens >= int64(cfg.TokenExchangeThresh) && preDrops >= 1

	// If no slots remain, clamp at threshold (no infinite banking).
	if !willExchange && preDrops <= 0 && postTokens > int64(cfg.TokenExchangeThresh) {
		postTokens = int64(cfg.TokenExchangeThresh)
	}

	if willExchange {
		// Consume one drop slot and perform exchange. Carry over excess tokens.
		pending.AddWalletUpdate(userID, map[string]int64{
			walletKeyRoundTokens: int64(tokensEarned) - int64(cfg.TokenExchangeThresh),
			walletKeyDropsLeft:   -1,
		})
		tier := GetLootboxConfig().MatchLossTier
		if req.Won {
			tier = GetLootboxConfig().MatchWinTier
		}
		if lootbox, lootboxWrite, lboxErr := PrepareCreateLootbox(userID, tier); lboxErr == nil {
			pending.AddStorageWrite(lootboxWrite)
			result.Lootboxes = []notify.LootboxGrant{{
				ID:     lootbox.ID,
				Tier:   lootbox.Tier,
				Source: "token_exchange",
			}}
		}
	} else {
		// Bank tokens — no exchange yet.
		delta := postTokens - preTokens
		if delta > 0 {
			pending.AddWalletUpdate(userID, map[string]int64{walletKeyRoundTokens: delta})
		}
	}

	// --- Match history for rate limiting ---
	historyValue, _ := json.Marshal(MatchHistory{LastMatchTime: time.Now().UnixMilli()})
	pending.AddStorageWrite(&runtime.StorageWrite{
		Collection:      storageCollectionMatchHistory,
		Key:             storageKeyLastMatch,
		UserID:          userID,
		Value:           string(historyValue),
		PermissionRead:  0,
		PermissionWrite: 0,
	})

	// --- Phase 2: Atomic commit (XP + tokens + exchange + lootbox + history) ---
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("Match result commit failed: %v", err)
		return nil, fmt.Errorf("match reward commit failed: %w", err)
	}

	// StorageDelete cannot go in MultiUpdate; runs after commit.
	clearActiveMatch(ctx, nk, logger, userID)

	// --- Metadata: derived from pre-read + deltas — no second AccountGetId ---
	finalTokens := postTokens
	finalDrops := preDrops
	if willExchange {
		// Freeze finalTokens at threshold so UI sequence displays visual completion.
		finalDrops--
	}
	result.Meta = &notify.RewardMeta{
		DailyMatches:   notify.IntPtr(matchesToday),
		DropsRemaining: notify.IntPtr(int(finalDrops)),
		RoundTokens:    notify.IntPtr(int(finalTokens)),
	}

	return result, nil
}

// processDeferredWinBonus is a no-op under the round-token economy.
//
// Gold win bonus is removed. Round tokens are computed symmetrically: each player earns
// tokens from their own round history at submission time — there is no deferred per-player
// top-up. The caller still invokes this function on the "ok" consensus path; returning
// nil, nil causes the notification send to be skipped cleanly.
func processDeferredWinBonus(_ context.Context, _ runtime.NakamaModule, _ runtime.Logger, _ string, _ bool) (*notify.RewardPayload, error) {
	return nil, nil
}

// preparePlayerXP applies diminishing returns and returns deferred progression writes.
// Note: PrepareExperience operates on pets and classes, whereas this handles player level directly.
func preparePlayerXP(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, xpAmount int) (int, *PendingWrites, int, error) {
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

		// Skip leveling if tree is unconfigured.
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
		return 0, nil, matchesToday, err
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

	return resultLevel, pending, matchesToday, nil
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

// MatchConfig holds match reward configuration.
// Gold fields are removed — token economy replaces per-match gold payouts.
type MatchConfig struct {
	WinXP               int `json:"win_xp"`
	LossXP              int `json:"loss_xp"`
	TokensPerRoundWin   int `json:"tokens_per_round_win"`  // Half-units; default 2 = 1.0 token
	TokensPerRoundLoss  int `json:"tokens_per_round_loss"` // Half-units; default 1 = 0.5 token
	TokensPerSoloRound  int `json:"tokens_per_solo_round"` // Half-units; default 1 = 0.5 token
	TokenExchangeThresh int `json:"token_exchange_thresh"` // Default 6 = 3.0 tokens trigger
	TokenRoundCap       int `json:"token_round_cap"`       // Only rounds 1..N earn tokens; default 3
}

var matchConfig *MatchConfig

func GetMatchConfig() *MatchConfig {
	if matchConfig == nil {
		matchConfig = &MatchConfig{
			WinXP:               100,
			LossXP:              25,
			TokensPerRoundWin:   2, // 1.0 token
			TokensPerRoundLoss:  1, // 0.5 token
			TokensPerSoloRound:  1, // 0.5 token per round regardless of outcome
			TokenExchangeThresh: 6, // 3.0 tokens
			TokenRoundCap:       3, // rounds 4+ earn nothing
		}
	}
	return matchConfig
}

// maxRoundsPerMatch is a hard server-side ceiling on round counts.
// No legitimate match format has more rounds than this; guards against inflated
// token claims when the Rounds array is absent (legacy client or empty payload).
const maxRoundsPerMatch = 10

// computeTokensEarned returns half-token units earned for this match.
// Pure function: no I/O. 1 full token = 2 units, 0.5 token = 1 unit.
//
// Token schedule: only rounds 1..TokenRoundCap earn tokens (e.g. first 3 rounds).
// When req.Rounds is present (normal path), each round's RoundNumber gates eligibility.
// When req.Rounds is absent (legacy/solo fallback), counts are used with the cap as a ceiling.
//
// Two security caps are always applied:
//  1. Relative cap: earned can't exceed a clean sweep (all-wins at TokensPerRoundWin rate).
//  2. Absolute cap: earned can't exceed maxRoundsPerMatch * TokensPerRoundWin regardless
//     of the Rounds array — closes the empty-array inflation attack.
func computeTokensEarned(req *MatchResultRequest, isSolo bool, cfg *MatchConfig) int {
	var earned int

	if len(req.Rounds) > 0 {
		// Preferred path: iterate round history, honour cap by RoundNumber.
		for _, r := range req.Rounds {
			if r.RoundNumber < 1 || r.RoundNumber > cfg.TokenRoundCap {
				continue // rounds outside the earning window contribute nothing
			}
			if isSolo {
				earned += cfg.TokensPerSoloRound
			} else if r.PlayerWon {
				earned += cfg.TokensPerRoundWin
			} else {
				earned += cfg.TokensPerRoundLoss
			}
		}
	} else {
		// Fallback: no round detail — cap eligible rounds at TokenRoundCap.
		won := req.RoundsWon
		lost := req.RoundsLost
		if won+lost > cfg.TokenRoundCap {
			// Trim excess rounds proportionally (won-first to be conservative).
			excess := (won + lost) - cfg.TokenRoundCap
			if lost >= excess {
				lost -= excess
			} else {
				excess -= lost
				lost = 0
				won -= excess
				if won < 0 {
					won = 0
				}
			}
		}
		if isSolo {
			earned = (won + lost) * cfg.TokensPerSoloRound
		} else {
			earned = won*cfg.TokensPerRoundWin + lost*cfg.TokensPerRoundLoss
		}
	}

	// Relative cap: can't exceed a clean sweep of TokenRoundCap rounds at win rate.
	if sweepMax := cfg.TokenRoundCap * cfg.TokensPerRoundWin; earned > sweepMax {
		earned = sweepMax
	}
	// Absolute cap: independent of request data — closes empty-Rounds inflation.
	if absMax := maxRoundsPerMatch * cfg.TokensPerRoundWin; earned > absMax {
		earned = absMax
	}
	return earned
}

// validateRounds checks round history plausibility and self-heals count mismatches.
// Not a hard security gate — the client is trusted for round detail.
// Purpose: catch integration bugs and flag suspiciously fast rounds for log analysis.
func validateRounds(req *MatchResultRequest, logger runtime.Logger) {
	if len(req.Rounds) == 0 {
		return // Legacy client or solo fallback — skip silently
	}
	derivedWon, derivedLost := 0, 0
	for _, r := range req.Rounds {
		if r.PlayerWon {
			derivedWon++
		} else {
			derivedLost++
		}
		if r.DurationSec < 5 {
			logger.Warn("[match_result] Suspiciously short round %d: %ds (match %s)",
				r.RoundNumber, r.DurationSec, req.MatchID)
		}
	}
	if derivedWon != req.RoundsWon || derivedLost != req.RoundsLost {
		logger.Warn("[match_result] Round count mismatch: claimed %d/%d, derived %d/%d (match %s) — correcting from Rounds array",
			req.RoundsWon, req.RoundsLost, derivedWon, derivedLost, req.MatchID)
		req.RoundsWon = derivedWon
		req.RoundsLost = derivedLost
	}
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
