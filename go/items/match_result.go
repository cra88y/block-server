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
	storageCollectionActiveMatch = "active_match"
	storageKeyCurrentMatch       = "current"
)

type ActiveMatch struct {
	MatchID    string `json:"match_id"`
	StartTime  int64  `json:"start_time"`
	OpponentID string `json:"opponent_id,omitempty"`
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

	// Unconditionally overwrite any existing active match lock.
	// This gracefully handles players abandoning a match mid-game and starting a new one.
	// Since we record a fresh StartTime, the player must still satisfy minMatchDurationMs
	// for the new match, ensuring security is preserved.

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

	var req MatchResultRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Error("Failed to unmarshal match result: %v", err)
		return "", errors.ErrUnmarshal
	}

	// Bypass rate limits for duplicate submissions
	cacheObj, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: "match_results_cache",
		Key:        req.MatchID + "_" + userID,
		UserID:     userID,
	}})
	if err == nil && len(cacheObj) > 0 {
		logger.Info("Returning cached reward payload for match %s user %s", req.MatchID, userID)
		return cacheObj[0].Value, nil
	}

	// Validate round history (logging + self-healing only; not a hard rejection gate).
	validateRounds(ctx, nk, &req, userID, logger)

	// Validate against Active Match (Security)
	activeMatch, err := validateActiveMatch(ctx, nk, logger, userID, req.MatchID)
	if err != nil {
		// Semantic override: a completed round proves meaningful play regardless of wall-clock time.
		// BRIDGE: req.RoundsWon/Lost are client-provided. report_round_result replaces this with server records.
		roundsPlayed := req.RoundsWon + req.RoundsLost
		if err == errors.ErrMatchTooShort && roundsPlayed >= 1 && activeMatch != nil {
			logger.Info("Match %s: duration short but %d round(s) completed — proceeding", req.MatchID, roundsPlayed)
		} else {
			logger.Warn("Match validation failed for user %s: %v", userID, err)
			return "", err
		}
	}

	// Consensus check (unified path: solo short-circuits in resolveMatchConsensus)
	consensusResult, err := resolveMatchConsensus(ctx, nk, logger, userID, activeMatch.OpponentID, req.MatchID, req.Won, req.FinalScore, req.OpponentForfeited)
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

	case "ok", "forfeit_win":
		// Second submitter (or unilateral forfeit winner): grant ourselves full rewards
		actualWon = req.Won
		if consensusResult == "ok" && activeMatch.OpponentID != "" {
			opponentIDForDeferred = activeMatch.OpponentID
			// Only push deferred if this was a normal "ok" consensus
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
	if err == nil {
		// Emit authoritative telemetry metric (match_completed)
		go func() {
			winnerID := activeMatch.OpponentID
			loserID := userID
			winnerScore := req.OpponentScore
			loserScore := req.FinalScore

			if actualWon {
				winnerID = userID
				loserID = activeMatch.OpponentID
				winnerScore = req.FinalScore
				loserScore = req.OpponentScore
			}

			gameMode := "1v1"
			if isSolo {
				loserID = "unknown"
				gameMode = "solo"
			}

			telemetryData, _ := json.Marshal(map[string]interface{}{
				"$type":           "MatchCompletedMetric",
				"MatchId":         req.MatchID,
				"WinnerId":        winnerID,
				"LoserId":         loserID,
				"WinnerScore":     winnerScore,
				"LoserScore":      loserScore,
				"DurationSeconds": float64(req.MatchDurationSec),
				"GameMode":        gameMode,
				"RoundsWon":       req.RoundsWon,
				"RoundsLost":      req.RoundsLost,
			})

			telemetryEvent := TelemetryEvent{
				EventType: "match_completed",
				Timestamp: float64(time.Now().UnixNano()) / 1e9,
				Data:      string(telemetryData),
			}

			if telErr := processTelemetryEvent(context.Background(), logger, db, nk, userID, telemetryEvent); telErr != nil {
				logger.Warn("Failed to record match_completed telemetry: %v", telErr)
			}
		}()
	}

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

	// SYNCHRONOUS: Write leaderboard records and capture rank for response payload.
	// Must occur before marshaling — LeaderboardRank is included in the cached response.
	// Failure is non-fatal: logs and returns rank=0 (omitted from response via omitempty).
	leaderboardRank := writeLeaderboardRecords(ctx, nk, logger, userID, &req, isSolo, actualWon)
	if leaderboardRank > 0 {
		result.LeaderboardRank = leaderboardRank
	}

	// ASYNC: Update competitive stats + append match history.
	// Self-idempotency gate: if history already exists, stats were already written — skip both.
	// Goroutine captures explicit copies (reqCopy, opponentIDCopy) to avoid data races.
	reqCopy := req
	opponentIDCopy := activeMatch.OpponentID
	go func() {
		bgCtx := context.Background()
		if MatchHistoryExists(bgCtx, nk, userID, reqCopy.MatchID) {
			logger.Info("[competitive] stats+history already processed for user %s match %s — skipping goroutine", userID, reqCopy.MatchID)
			return
		}
		AppendMatchHistory(bgCtx, nk, logger, userID, &reqCopy, isSolo, actualWon, opponentIDCopy)
		UpdatePlayerStats(bgCtx, nk, logger, userID, &reqCopy, isSolo, actualWon)
	}()

	respBytes, err := json.Marshal(result)
	if err != nil {
		logger.Error("Failed to marshal match reward response: %v", err)
		return "", errors.ErrMarshal
	}

	// Cache the exact response so future identical requests don't double-process rewards
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      "match_results_cache",
		Key:             req.MatchID + "_" + userID,
		UserID:          userID,
		Value:           string(respBytes),
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	if err != nil {
		logger.Warn("Failed to cache match result for user %s match %s: %v", userID, req.MatchID, err)
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
	// minMatchDurationMs: server-enforced floor. Any match shorter than this is rejected.
	// This is the primary anti-farming gate — no wall-clock inter-submission cooldown needed.
	minMatchDurationMs = 10000 // 10 seconds

	// maxMatchDurationMs: stale-session ceiling for 1v1 matches.
	// No competitive match physically lasts longer than 10 minutes; beyond this the session is abandoned.
	maxMatchDurationMs = 600000 // 10 minutes

	// maxSoloMatchDurationMs: stale-session ceiling for solo matches.
	// Solo has no consensus deadline — a player can run long survival sessions.
	// Use a 1-hour ceiling purely to clean up sessions from crashed/uninstalled clients.
	maxSoloMatchDurationMs = 60 * 60 * 1000 // 1 hour

	storageCollectionResults = "match_results"
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
		return nil, errors.ErrNoActiveMatch
	}

	var activeMatch ActiveMatch
	if err := json.Unmarshal([]byte(objects[0].Value), &activeMatch); err != nil {
		return nil, errors.ErrUnmarshal
	}

	if activeMatch.MatchID != matchID {
		return nil, errors.ErrMatchIDMismatch
	}

	if time.Now().UnixMilli()-activeMatch.StartTime < minMatchDurationMs {
		// Return the activeMatch alongside the error so the caller can apply semantic override.
		// If the caller has round records proving meaningful play, it may proceed despite short duration.
		return &activeMatch, errors.ErrMatchTooShort
	}

	// Apply a mode-specific stale-session ceiling.
	// Solo: generous cap (marathon sessions are valid). Multiplayer: tight cap (consensus enforces short matches).
	maxDuration := int64(maxMatchDurationMs)
	if activeMatch.OpponentID == "" {
		maxDuration = int64(maxSoloMatchDurationMs)
	}
	if time.Now().UnixMilli()-activeMatch.StartTime > maxDuration {
		// Clear stale match state.
		clearActiveMatch(ctx, nk, logger, userID)
		return nil, errors.ErrStaleMatchExpired
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
//	forfeit_win — opponent abandoned. Caller grants full rewards immediately without waiting.
//	resolved — late arrival (opponent already set Resolved=true on their record). Participation-only.
//	conflict — both claimed win. Both downgraded.
func resolveMatchConsensus(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, opponentID string, matchID string, claimedWin bool, score int, opponentForfeited bool) (string, error) {
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

	// If opponent forfeited, bypass waiting for their claim and resolve unilaterally
	if opponentForfeited && claimedWin {
		logger.Info("Match %s: User %s claims victory via opponent forfeit. Bypassing pending state.", matchID, userID)
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
		return "forfeit_win", nil
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
	// TODO(ATOM-D1): After consensus resolves to "ok", compare both players' RoundRecord sets.
	// For every round N: playerA.round[N].PlayerWon == !playerB.round[N].PlayerWon.
	// Requires reading opponent storage with userID=opponentID (PermissionRead:0, valid from server context).
	// OpponentID is available as activeMatch.OpponentID. Do NOT implement until ATOM-D1 is scoped.
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

// Anti-farming: two gates.
// 1. validateActiveMatch: 10s floor, waived when rounds_played >= 1 (server records take over in Phase 2).
// 2. match_results_cache: idempotency key prevents double-reward on retries.

// processMatchRewards handles reward generation atomically.
//
// Lootbox economy: dropsLeft is a daily pool of lootbox slots (3/day, max 5).
// Round tokens are the key — 3 full tokens (6 half-units) exchange one slot for a lootbox.
// No lootbox is granted per-match directly; tokens must accumulate across matches.
//
// Pre-read pattern: one AccountGetId at the top covers token read and drop availability.
// isSolo: halves XP to prevent solo-match farming.
func processMatchRewards(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, req *MatchResultRequest, isSolo bool) (*notify.RewardPayload, error) {
	cfg := GetEconomyConfig()
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

	// If report_round_result banked tokens this match, preTokens already reflects them.
	// Skip computeTokensEarned to avoid double-grant. Fallback runs if no records exist.
	tokensBanked := ReadRoundRecordsTotal(ctx, nk, logger, userID, req.MatchID, cfg.TokenRoundCap)

	var tokensEarned int
	var postTokens int64
	var willExchange bool

	if tokensBanked > 0 {
		postTokens = preTokens
		willExchange = preTokens >= int64(cfg.TokenExchangeThresh) && preDrops >= 1
		logger.Info("Match %s: %d tokens pre-banked, skipping delta (audit_confirmed)", req.MatchID, tokensBanked)
	} else {
		// Fallback: no round records — network failure, legacy client, or pre-Phase2 solo.
		tokensEarned = computeTokensEarned(req, isSolo, cfg)
		if preDrops <= 0 {
			tokensEarned = 0
		}
		postTokens = preTokens + int64(tokensEarned)
		willExchange = postTokens >= int64(cfg.TokenExchangeThresh) && preDrops >= 1
		if tokensEarned > 0 {
			logger.Info("Match %s: no round records, granting %d tokens (audit_unconfirmed)", req.MatchID, tokensEarned)
		}
	}

	// If no slots remain, clamp at threshold (no infinite banking).
	if !willExchange && preDrops <= 0 && postTokens > int64(cfg.TokenExchangeThresh) {
		postTokens = int64(cfg.TokenExchangeThresh)
	}

	if willExchange {
		// Calculate the exact delta needed to exchange the threshold tokens.
		// If tokensEarned > 0, we must credit them simultaneously.
		walletDelta := int64(tokensEarned) - int64(cfg.TokenExchangeThresh)

		pending.AddWalletUpdate(userID, map[string]int64{
			walletKeyRoundTokens: walletDelta,
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
	} else if tokensEarned > 0 {
		delta := postTokens - preTokens
		if delta > 0 {
			pending.AddWalletUpdate(userID, map[string]int64{walletKeyRoundTokens: delta})
		}
	}

	// --- Phase 2: Atomic commit (XP + tokens + exchange + lootbox) ---
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("Match result commit failed: %v", err)
		return nil, errors.ErrMatchRewardCommit
	}

	// StorageDelete cannot go in MultiUpdate; runs after commit.
	clearActiveMatch(ctx, nk, logger, userID)

	// --- Metadata: derived from pre-read + deltas — no second AccountGetId ---
	finalTokens := postTokens
	finalDrops := preDrops
	if willExchange {
		// Freeze tokens at the threshold so the client UI animates cleanly to 100%
		finalDrops--
		finalTokens = int64(cfg.TokenExchangeThresh)
	}
	// In the pre-bank path tokensEarned==0 (no new grant was made here), but the
	// client end-screen needs the actual delta earned this match to animate correctly.
	// Use tokensBanked (already committed by report_round_result RPCs) as the display value.
	effectiveEarned := tokensEarned
	if tokensBanked > 0 {
		effectiveEarned = tokensBanked
	}
	result.Meta = &notify.RewardMeta{
		DailyMatches:   notify.IntPtr(matchesToday),
		DropsRemaining: notify.IntPtr(int(finalDrops)),
		RoundTokens:    notify.IntPtr(int(finalTokens)),
		TokensEarned:   notify.IntPtr(effectiveEarned),
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

// EconomyConfig holds match reward and token exchange configuration.
type EconomyConfig struct {
	WinXP               int `json:"win_xp"`
	LossXP              int `json:"loss_xp"`
	TokensPerRoundWin   int `json:"tokens_per_round_win"`  // Half-units; default 2 = 1.0 token
	TokensPerRoundLoss  int `json:"tokens_per_round_loss"` // Half-units; default 1 = 0.5 token
	TokensPerSoloRound  int `json:"tokens_per_solo_round"` // Half-units; default 1 = 0.5 token
	TokenExchangeThresh int `json:"token_exchange_thresh"` // Default 6 = 3.0 tokens trigger
	TokenRoundCap       int `json:"token_round_cap"`       // Only rounds 1..N earn tokens; default 3
}

var economyConfig *EconomyConfig

func GetEconomyConfig() *EconomyConfig {
	if economyConfig == nil {
		economyConfig = &EconomyConfig{
			WinXP:               100,
			LossXP:              25,
			TokensPerRoundWin:   2, // 1.0 token
			TokensPerRoundLoss:  1, // 0.5 token
			TokensPerSoloRound:  1, // 0.5 token per completed round
			TokenExchangeThresh: 6, // 3.0 tokens
			TokenRoundCap:       3, // rounds 4+ earn nothing
		}
	}
	return economyConfig
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
func computeTokensEarned(req *MatchResultRequest, isSolo bool, cfg *EconomyConfig) int {
	var earned int

	if len(req.Rounds) > 0 {
		// Preferred path: iterate round history, honour cap by RoundNumber.
		for _, r := range req.Rounds {
			if r.RoundNumber < 1 || r.RoundNumber > cfg.TokenRoundCap {
				continue // rounds outside the earning window contribute nothing
			}
			if !r.Survived {
				continue // died — no tokens
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
			earned = won * cfg.TokensPerSoloRound
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
// Also performs a cross-stream audit: compares the client's self-report against server
// RoundRecord objects written by report_round_result. Discrepancies are warn-only —
// no rewards are withheld in this pass.
func validateRounds(ctx context.Context, nk runtime.NakamaModule, req *MatchResultRequest, userID string, logger runtime.Logger) {
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
		if r.DurationMs < 5000 {
			logger.Warn("[match_result] Suspiciously short round %d: %dms (match %s)",
				r.RoundNumber, r.DurationMs, req.MatchID)
		}
	}

	// Cross-stream audit: compare client self-report against server RoundRecord objects.
	// Discrepancy = client tampered with their batch report after rounds resolved.
	cfg := GetEconomyConfig()
	serverRecords, readErr := nk.StorageRead(ctx, buildRoundRecordReads(req.MatchID, userID, cfg.TokenRoundCap))
	if readErr == nil {
		recordMap := make(map[int]RoundRecord, len(serverRecords))
		for _, obj := range serverRecords {
			var rec RoundRecord
			if json.Unmarshal([]byte(obj.Value), &rec) == nil {
				recordMap[rec.RoundNumber] = rec
			}
		}
		for _, r := range req.Rounds {
			rec, exists := recordMap[r.RoundNumber]
			if !exists {
				logger.Warn("[audit] Round %d in client report has no server record (match %s user %s) — possible fabrication",
					r.RoundNumber, req.MatchID, userID)
				continue
			}
			if rec.PlayerWon != r.PlayerWon {
				logger.Warn("[audit] STREAM_CONFLICT round %d: server record PlayerWon=%v, client claims PlayerWon=%v (match %s user %s)",
					r.RoundNumber, rec.PlayerWon, r.PlayerWon, req.MatchID, userID)
			}
			const durationDeltaMs = int64(3000) // ±3s tolerance for clock drift
			if r.DurationMs > 0 && rec.DurationMs > 0 {
				delta := r.DurationMs - rec.DurationMs
				if delta < 0 {
					delta = -delta
				}
				if delta > durationDeltaMs {
					logger.Warn("[audit] Duration delta round %d: server=%dms client=%dms delta=%dms (match %s user %s)",
						r.RoundNumber, rec.DurationMs, r.DurationMs, delta, req.MatchID, userID)
				}
			}
		}
	}

	if derivedWon != req.RoundsWon || derivedLost != req.RoundsLost {
		logger.Warn("[match_result] Round count mismatch: claimed %d/%d, derived %d/%d (match %s) — correcting from Rounds array",
			req.RoundsWon, req.RoundsLost, derivedWon, derivedLost, req.MatchID)
		req.RoundsWon = derivedWon
		req.RoundsLost = derivedLost
	}
}

// TODO move lootbox stuff out of here?
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
		return nil, errors.ErrLootboxWriteFailed
	}

	return lootbox, nil
}
