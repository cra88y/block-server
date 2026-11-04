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
	storageCollectionSettlement  = "match_settlement"
	systemUserID                 = "00000000-0000-0000-0000-000000000000"
)

type MatchSettlement struct {
	MatchID    string             `json:"match_id"`
	Status     ConsensusResult    `json:"status"`
	CreatedAt  int64              `json:"created_at"`
	SubmitterA MatchResultRecord  `json:"submitter_a"`
	SubmitterB *MatchResultRecord `json:"submitter_b,omitempty"`
	WinnerID   string             `json:"winner_id,omitempty"`
}

type ConsensusResult string

const (
	ConsensusResultOk         ConsensusResult = "ok"
	ConsensusResultForfeitWin ConsensusResult = "forfeit_win"
	ConsensusResultPending    ConsensusResult = "pending"
	ConsensusResultResolved   ConsensusResult = "resolved"
	ConsensusResultConflict   ConsensusResult = "conflict"
)

type ActiveMatch struct {
	MatchID      string        `json:"match_id"`
	StartTime    int64         `json:"start_time"`
	OpponentID   string        `json:"opponent_id,omitempty"`
	ClassID      *uint32       `json:"class_id,omitempty"`
	PetID        *uint32       `json:"pet_id,omitempty"`
	TokensBanked int           `json:"tokens_banked"`
	Rounds       []RoundRecord `json:"rounds,omitempty"`
	Version      string        `json:"-"`
}

// MatchResultRecord stores a player's claimed result for consensus
type MatchResultRecord struct {
	UserID      string  `json:"user_id"`
	ClaimedWin  bool    `json:"claimed_win"`
	Score       int     `json:"score"`
	ClassID     *uint32 `json:"class_id,omitempty"`
	PetID       *uint32 `json:"pet_id,omitempty"`
	SubmittedAt int64   `json:"submitted_at"`
	Resolved    bool    `json:"resolved"` // True when this player was the second submitter and resolved consensus
}

type NotifyMatchStartRequest struct {
	MatchID    string  `json:"match_id"`
	OpponentID string  `json:"opponent_id,omitempty"`
	ClassID    *uint32 `json:"class_id,omitempty"`
	PetID      *uint32 `json:"pet_id,omitempty"`
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

	// Overwrite active match lock. Player must still satisfy minMatchDurationMs.
	activeMatch := ActiveMatch{
		MatchID:    req.MatchID,
		StartTime:  time.Now().UnixMilli(),
		OpponentID: req.OpponentID,
		ClassID:    req.ClassID,
		PetID:      req.PetID,
		Rounds:     make([]RoundRecord, 0),
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

	// Idempotency check
	cacheObj, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: "match_results_cache",
		Key:        "latest_match_result",
		UserID:     userID,
	}})
	if err == nil && len(cacheObj) > 0 {
		var cacheEntry MatchResultCacheEntry
		if err := json.Unmarshal([]byte(cacheObj[0].Value), &cacheEntry); err == nil && cacheEntry.MatchID == req.MatchID {
			logger.Info("Returning cached reward payload for match %s user %s", req.MatchID, userID)
			return string(cacheEntry.Payload), nil
		}
	}

	activeMatch, err := validateActiveMatch(ctx, nk, logger, userID, req.MatchID)
	if err != nil {
		roundsPlayed := req.RoundsWon + req.RoundsLost
		if err == errors.ErrMatchTooShort && (roundsPlayed >= 1 || req.OpponentForfeited) && activeMatch != nil {
			logger.Info("Match %s: duration short but %d round(s) completed (or forfeit) — proceeding", req.MatchID, roundsPlayed)
		} else if err == errors.ErrMatchTooShort {
			logger.Warn("Match too short for user %s: %v", userID, err)
			errorPayload := notify.NewRewardPayload("match")
			errorPayload.Meta = &notify.RewardMeta{ErrorCode: errorCodeMatchTooShort}
			respBytes, marshalErr := json.Marshal(errorPayload)
			if marshalErr != nil {
				return "", marshalErr
			}
			return string(respBytes), nil
		} else if err == errors.ErrStaleMatchExpired && activeMatch != nil {
			logger.Warn("Stale match expired for user %s (match %s, opponent %s)", userID, req.MatchID, activeMatch.OpponentID)

			if activeMatch.OpponentID != "" {
				staleNote := notify.NewRewardPayload("match")
				staleNote.Meta = &notify.RewardMeta{ErrorCode: errorCodeStaleMatch}
				staleNote.ReasonKey = "reward.match.stale_resolved"
				go func(oppID string) {
					if sendErr := notify.SendReward(context.Background(), nk, oppID, staleNote); sendErr != nil {
						logger.Warn("Failed to send stale-match notification to opponent %s: %v", oppID, sendErr)
					}
				}(activeMatch.OpponentID)
			}

			clearActiveMatch(ctx, nk, logger, userID)

			errorPayload := notify.NewRewardPayload("match")
			errorPayload.Meta = &notify.RewardMeta{ErrorCode: errorCodeStaleMatch}
			respBytes, marshalErr := json.Marshal(errorPayload)
			if marshalErr != nil {
				return "", marshalErr
			}
			return string(respBytes), nil
		} else {
			logger.Warn("Match validation failed for user %s: %v", userID, err)
			return "", err
		}
	}

	validateRounds(ctx, nk, &req, userID, logger, activeMatch)

	// Consensus check (unified path: solo short-circuits in resolveMatchConsensus)
	oppRecord, consensusResult, err := resolveMatchConsensus(ctx, nk, logger, userID, activeMatch, &req)
	if err != nil {
		logger.Warn("Consensus check failed for user %s: %v", userID, err)
		return "", err
	}

	isSolo := activeMatch.OpponentID == ""
	actualWon := req.Won
	var opponentIDForDeferred string
	var opponentWonForDeferred bool

	var oppClassID *uint32
	var oppPetID *uint32
	if !isSolo && activeMatch.OpponentID != "" {
		if oppRecord != nil && oppRecord.ClassID != nil {
			oppClassID = oppRecord.ClassID
			oppPetID = oppRecord.PetID
		} else if oppActive, getErr := getActiveMatch(ctx, nk, activeMatch.OpponentID); getErr == nil && oppActive != nil && oppActive.MatchID == req.MatchID {
			oppClassID = oppActive.ClassID
			oppPetID = oppActive.PetID
		}
	}

	switch consensusResult {
	case ConsensusResultPending:
		actualWon = false
		logger.Info("Match %s: user %s is first submitter, granting participation rewards", req.MatchID, userID)

		// Notify opponent that their match result is expected — this triggers
		// match teardown on the opponent's client if P2P resolution failed.
		if activeMatch.OpponentID != "" {
			opponentNote := notify.NewRewardPayload("match")
			opponentNote.Meta = &notify.RewardMeta{ErrorCode: errorCodeOpponentSubmitted}
			opponentNote.ReasonArgs = map[string]string{
				"opponent_claimed_win": fmt.Sprintf("%v", req.Won),
			}
			go func(oppID string) {
				if sendErr := notify.SendReward(context.Background(), nk, oppID, opponentNote); sendErr != nil {
					logger.Warn("Failed to send opponent-submitted notification to %s: %v", oppID, sendErr)
				}
			}(activeMatch.OpponentID)
		}

	case ConsensusResultResolved:
		actualWon = false
		logger.Info("Match %s: user %s arrived late, rewards already resolved by opponent", req.MatchID, userID)

	case ConsensusResultConflict:
		logger.Warn("Match %s: Both players claimed victory. Voiding win for user %s", req.MatchID, userID)
		actualWon = false

	case ConsensusResultOk, ConsensusResultForfeitWin:
		actualWon = req.Won
		if consensusResult == ConsensusResultOk && activeMatch.OpponentID != "" {
			opponentIDForDeferred = activeMatch.OpponentID
			opponentWonForDeferred = !req.Won
		}
		if consensusResult == ConsensusResultForfeitWin && activeMatch.OpponentID != "" {
			abandonMap := map[string]interface{}{
				"match_id":    req.MatchID,
				"reporter_id": userID,
				"reason":      "forfeit_win_claimed",
			}
			if oppClassID != nil {
				abandonMap["local_class_id"] = *oppClassID
			}
			if oppPetID != nil {
				abandonMap["local_pet_id"] = *oppPetID
			}
			abandonData, _ := json.Marshal(abandonMap)
			abandonEvent := TelemetryEvent{
				EventType: "match_abandoned",
				Timestamp: float64(time.Now().UnixNano()) / 1e9,
				Data:      string(abandonData),
			}
			go processTelemetryEvent(context.Background(), logger, db, nk, activeMatch.OpponentID, abandonEvent)
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
	result, err := processMatchRewards(ctx, nk, logger, userID, &req, isSolo, activeMatch)
	if err == nil && (consensusResult == ConsensusResultOk || consensusResult == ConsensusResultForfeitWin || isSolo) {
		// Emit authoritative telemetry metric (match_completed) only upon terminal consensus or solo
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

			telemetryMap := map[string]interface{}{
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
				"AbilitiesCast":   req.AbilitiesCast,
				"APM":             req.APM,
				"PiecesPlaced":    req.PiecesPlaced,
				"LocalClassId":    req.EquippedClassID,
				"LocalPetId":      req.EquippedPetID,
			}

			// Only emit opponent/symmetric class analytics in 1v1 multiplayer when opponent loadout is verified
			if !isSolo && oppClassID != nil {
				telemetryMap["OpponentClassId"] = *oppClassID
				if oppPetID != nil {
					telemetryMap["OpponentPetId"] = *oppPetID
				}
				if actualWon {
					telemetryMap["WinnerClassId"] = req.EquippedClassID
					telemetryMap["LoserClassId"] = *oppClassID
				} else {
					telemetryMap["WinnerClassId"] = *oppClassID
					telemetryMap["LoserClassId"] = req.EquippedClassID
				}
			}

			telemetryData, _ := json.Marshal(telemetryMap)

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

	// Second submitter: push deferred XP win bonus to first submitter
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

	// Synchronous: Write leaderboard records. Non-fatal on err.
	result.Competitive = writeLeaderboardRecords(ctx, nk, logger, userID, &req, isSolo, actualWon)

	// Synchronous history write must precede cache write.
	// Prevents transient failures from being permanently masked by the idempotency cache.
	historyWrite, historyPrepErr := PrepareMatchHistoryWrite(ctx, nk, userID, &req, isSolo, actualWon, activeMatch.OpponentID)
	if historyPrepErr != nil {
		logger.Warn("Match history prepare failed for user %s match %s: %v — proceeding without history", userID, req.MatchID, historyPrepErr)
	} else {
		historyPending := NewPendingWrites()
		historyPending.AddStorageWrite(historyWrite)
		if commitErr := CommitPendingWrites(ctx, nk, logger, historyPending); commitErr != nil {
			logger.Warn("Match history commit failed for user %s match %s: %v — history may be missing", userID, req.MatchID, commitErr)
			// Non-fatal: proceed so rewards are returned. History loss is logged.
		}
	}

	// ASYNC: Update competitive stats (OCC-tolerant).
	reqCopy := req
	go func() {
		bgCtx := context.Background()
		statsWrite, err := PreparePlayerStatsUpdate(bgCtx, nk, userID, &reqCopy, isSolo, actualWon)
		if err != nil {
			logger.Warn("Player stats prepare failed for user %s match %s: %v", userID, reqCopy.MatchID, err)
			return
		}
		statsPending := NewPendingWrites()
		statsPending.AddStorageWrite(statsWrite)
		if err := CommitPendingWrites(bgCtx, nk, logger, statsPending); err != nil {
			logger.Warn("Player stats commit failed for user %s match %s (OCC or transient): %v", userID, reqCopy.MatchID, err)
		}
	}()

	respBytes, err := json.Marshal(result)
	if err != nil {
		logger.Error("Failed to marshal match reward response: %v", err)
		return "", errors.ErrMarshal
	}

	// Atomic idempotency commit
	cacheEntry := MatchResultCacheEntry{
		MatchID: req.MatchID,
		Payload: respBytes,
	}
	cacheBytes, _ := json.Marshal(cacheEntry)
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      "match_results_cache",
		Key:             "latest_match_result",
		UserID:          userID,
		Value:           string(cacheBytes),
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

	telemetryData, _ := json.Marshal(map[string]interface{}{
		"match_id":  req.MatchID,
		"won":       req.Won,
		"xp_earned": xpAmount,
	})
	telemetryEvent := TelemetryEvent{
		EventType: "match_result_verified",
		Timestamp: float64(time.Now().Unix()),
		Data:      string(telemetryData),
	}
	processTelemetryEvent(context.Background(), logger, db, nk, userID, telemetryEvent)

	return string(respBytes), nil
}

const (
	// minMatchDurationMs: floor gate for anti-farming.
	minMatchDurationMs = 10000 // 10 seconds

	// maxMatchDurationMs: 1v1 stale session ceiling.
	maxMatchDurationMs = 600000 // 10 minutes

	// maxSoloMatchDurationMs: solo stale session ceiling.
	// Use a 1-hour ceiling purely to clean up sessions from crashed/uninstalled clients.
	maxSoloMatchDurationMs = 60 * 60 * 1000 // 1 hour

	// errorCode constants: set in RewardMeta.ErrorCode when a validation gate rejects the match.
	// The client routes to distinct UI messages. Empty string = normal processing.
	errorCodeMatchTooShort     = "MATCH_TOO_SHORT"
	errorCodeStaleMatch        = "STALE_MATCH"
	errorCodeOpponentSubmitted = "OPPONENT_SUBMITTED"
)

func getActiveMatch(ctx context.Context, nk runtime.NakamaModule, userID string) (*ActiveMatch, error) {
	if userID == "" {
		return nil, errors.ErrInvalidInput
	}
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
	activeMatch.Version = objects[0].Version
	return &activeMatch, nil
}

func validateActiveMatch(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, matchID string) (*ActiveMatch, error) {
	activeMatch, err := getActiveMatch(ctx, nk, userID)
	if err != nil {
		return nil, err
	}

	if activeMatch.MatchID != matchID {
		return nil, errors.ErrMatchIDMismatch
	}

	if time.Now().UnixMilli()-activeMatch.StartTime < minMatchDurationMs {
		// Return the activeMatch alongside the error so the caller can apply semantic override.
		// If the caller has round records proving meaningful play, it may proceed despite short duration.
		return activeMatch, errors.ErrMatchTooShort
	}

	// Apply a mode-specific stale-session ceiling.
	// Solo: generous cap (marathon sessions are valid). Multiplayer: tight cap (consensus enforces short matches).
	maxDuration := int64(maxMatchDurationMs)
	if activeMatch.OpponentID == "" {
		maxDuration = int64(maxSoloMatchDurationMs)
	}
	if time.Now().UnixMilli()-activeMatch.StartTime > maxDuration {
		// Return activeMatch alongside error so caller can notify opponent before cleanup.
		return activeMatch, errors.ErrStaleMatchExpired
	}

	return activeMatch, nil
}

// Write-first single-resolution consensus.
// Writing our claim before reading the opponent's claim collapses the TOCTOU window.
//
// States:
//
//	pending     : First submitter. Participation-only.
//	ok          : Second submitter. Full rewards + deferred bonus to first submitter.
//	forfeit_win : Opponent abandoned. Full rewards immediately.
//	resolved    : Late arrival (opponent resolved). Participation-only.
//	conflict    : Both claimed win. Both downgraded.
func resolveMatchConsensus(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, activeMatch *ActiveMatch, req *MatchResultRequest) (*MatchResultRecord, ConsensusResult, error) {
	if activeMatch == nil || activeMatch.OpponentID == "" {
		return nil, ConsensusResultOk, nil // Solo — no consensus needed, caller handles isSolo reward reduction
	}

	myRecord := MatchResultRecord{
		UserID:      userID,
		ClaimedWin:  req.Won,
		Score:       req.FinalScore,
		ClassID:     &req.EquippedClassID,
		PetID:       &req.EquippedPetID,
		SubmittedAt: time.Now().UnixMilli(),
		Resolved:    false,
	}

	// Read canonical settlement record
	records, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionSettlement,
		Key:        req.MatchID,
		UserID:     systemUserID,
	}})
	if err != nil {
		logger.Error("StorageRead failed for settlement %s: %v", req.MatchID, err)
		return nil, "", err
	}

	now := time.Now().UnixMilli()

	// Phase 1: Settlement record does not yet exist -> First submitter
	if len(records) == 0 {
		status := ConsensusResultPending
		winnerID := ""

		// Forfeit guard: Only allow immediate forfeit win if match duration >= 15s to prevent tick-0 spoofing
		if req.OpponentForfeited && req.Won {
			durationMs := now - activeMatch.StartTime
			if activeMatch.StartTime > 0 && durationMs >= 15000 {
				status = ConsensusResultForfeitWin
				winnerID = userID
				myRecord.Resolved = true
			} else {
				logger.Warn("Premature forfeit claim for match %s (duration %dms < 15s floor); demoting to pending", req.MatchID, durationMs)
			}
		}

		settlement := MatchSettlement{
			MatchID:    req.MatchID,
			Status:     status,
			CreatedAt:  now,
			SubmitterA: myRecord,
			WinnerID:   winnerID,
		}
		bytes, _ := json.Marshal(settlement)

		// Atomic create: Version: "*" ensures insert fails if opponent writes simultaneously
		_, writeErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      storageCollectionSettlement,
			Key:             req.MatchID,
			UserID:          systemUserID,
			Value:           string(bytes),
			Version:         "*",
			PermissionRead:  0,
			PermissionWrite: 0,
		}})
		if writeErr == nil {
			return nil, status, nil
		}

		// Write collision: Opponent created the row at the exact same millisecond. Re-read row below.
		records, err = nk.StorageRead(ctx, []*runtime.StorageRead{{
			Collection: storageCollectionSettlement,
			Key:        req.MatchID,
			UserID:     systemUserID,
		}})
		if err != nil || len(records) == 0 {
			return nil, "", fmt.Errorf("concurrent settlement read failed for match %s", req.MatchID)
		}
	}

	// Phase 2: Settlement record exists -> Second submitter or terminal state
	var settlement MatchSettlement
	if err := json.Unmarshal([]byte(records[0].Value), &settlement); err != nil {
		logger.Error("Corrupt settlement JSON for match %s: %v", req.MatchID, err)
		return nil, "", err
	}
	existingVersion := records[0].Version

	// Terminal state: match already resolved
	if settlement.Status == ConsensusResultOk || settlement.Status == ConsensusResultResolved || settlement.Status == ConsensusResultForfeitWin {
		opp := &settlement.SubmitterA
		if settlement.SubmitterA.UserID == userID && settlement.SubmitterB != nil {
			opp = settlement.SubmitterB
		}
		return opp, ConsensusResultResolved, nil
	}

	// Resubmission by original submitter (e.g. promoting pending to forfeit after grace timeout)
	if settlement.SubmitterA.UserID == userID {
		if req.OpponentForfeited && req.Won && (now-settlement.CreatedAt >= 15000) {
			settlement.Status = ConsensusResultForfeitWin
			settlement.WinnerID = userID
			settlement.SubmitterA.Resolved = true
			bytes, _ := json.Marshal(settlement)
			_, writeErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
				Collection:      storageCollectionSettlement,
				Key:             req.MatchID,
				UserID:          systemUserID,
				Value:           string(bytes),
				Version:         existingVersion,
				PermissionRead:  0,
				PermissionWrite: 0,
			}})
			if writeErr != nil {
				logger.Warn("CAS version collision promoting forfeit for match %s: %v, assuming terminal", req.MatchID, writeErr)
				return nil, ConsensusResultResolved, nil
			}
			return nil, ConsensusResultForfeitWin, nil
		}
		return nil, ConsensusResultPending, nil
	}

	// Caller is Submitter B (the second distinct submitter)
	oppRecord := settlement.SubmitterA

	// Conflict check: Both claimed victory
	if req.Won && oppRecord.ClaimedWin {
		logger.Warn("CONFLICT: Match %s - both %s and %s claimed victory", req.MatchID, userID, oppRecord.UserID)
		settlement.Status = ConsensusResultConflict
		settlement.SubmitterB = &myRecord
		bytes, _ := json.Marshal(settlement)
		if _, writeErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      storageCollectionSettlement,
			Key:             req.MatchID,
			UserID:          systemUserID,
			Value:           string(bytes),
			Version:         existingVersion,
			PermissionRead:  0,
			PermissionWrite: 0,
		}}); writeErr != nil {
			logger.Warn("CAS collision writing conflict settlement for match %s: %v", req.MatchID, writeErr)
		}
		return &oppRecord, ConsensusResultConflict, nil
	}

	// Consensual resolution
	winnerID := userID
	if oppRecord.ClaimedWin {
		winnerID = oppRecord.UserID
	}

	myRecord.Resolved = true
	settlement.Status = ConsensusResultOk
	settlement.SubmitterB = &myRecord
	settlement.WinnerID = winnerID
	bytes, _ := json.Marshal(settlement)

	// Optimistic CAS: write with expected existingVersion
	_, writeErr := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionSettlement,
		Key:             req.MatchID,
		UserID:          systemUserID,
		Value:           string(bytes),
		Version:         existingVersion,
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	if writeErr != nil {
		logger.Warn("CAS version collision resolving match %s: %v, assuming terminal", req.MatchID, writeErr)
		return &oppRecord, ConsensusResultResolved, nil
	}

	return &oppRecord, ConsensusResultOk, nil
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

// Idempotent via match_results_cache.
// ExchangesLeft limits daily lootbox generation; 6 RoundTokens (half-units) exchange for 1 lootbox.
// A single AccountGetId pre-read prevents wallet TOCTOU during reward generation.
// Solo match XP is halved to prevent farming.
func processMatchRewards(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, req *MatchResultRequest, isSolo bool, activeMatch *ActiveMatch) (*notify.RewardPayload, error) {
	cfg := GetEconomyConfig()
	pending := NewPendingWrites()

	result := notify.NewRewardPayload("match")
	result.ReasonKey = "reward.match.complete"
	result.Progression = &notify.ProgressionDelta{}

	// --- Daily Journey ---
	var dj DailyJourney
	var djVersion string
	djObjects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: storageCollectionProgression,
			Key:        ProgressionKeyDailyJourney,
			UserID:     userID,
		},
	})
	nowUTC := time.Now().UTC()
	midnightUTC := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	if err == nil && len(djObjects) > 0 {
		if err := json.Unmarshal([]byte(djObjects[0].Value), &dj); err == nil {
			djVersion = djObjects[0].Version
		}
	} else {
		dj = DailyJourney{
			DailyMatches:       0,
			DailyWarmupClaimed: false,
			ResetUnix:          midnightUTC.Unix(),
		}
	}

	// Reset if reset_unix is before today's midnight
	CheckAndResetDailyJourney(&dj)

	// Increment daily match count
	dj.DailyMatches++

	// Check daily warmup completion
	warmupGoal := cfg.DailyMatchesWarmupGoal
	if warmupGoal <= 0 {
		warmupGoal = 1
	}
	if dj.DailyMatches >= warmupGoal && !dj.DailyWarmupClaimed {
		dj.DailyWarmupClaimed = true
		tier := cfg.DailyMatchesWarmupLootboxTier
		if tier == "" {
			tier = "standard"
		}
		if lootbox, lootboxWrite, lboxErr := PrepareCreateLootbox(userID, tier); lboxErr == nil {
			pending.AddStorageWrite(lootboxWrite)
			result.Lootboxes = append(result.Lootboxes, notify.LootboxGrant{
				ID:     lootbox.ID,
				Tier:   lootbox.Tier,
				Source: "daily_warmup",
			})
			logger.Info("[DailyJourney] Granted warmup lootbox of tier %s to user %s", tier, userID)
		} else {
			logger.Error("[DailyJourney] Failed to prepare warmup lootbox for user %s: %v", userID, lboxErr)
		}
	}

	// Note: Serialization and AddStorageWrite for dj is moved to the end of the token exchange loop.

	// --- Pre-read token state ---
	var preTokens int64 = int64(dj.RoundTokens)
	var preExchanges int64 = int64(dj.ExchangesLeft)

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

	playerLevelUp, xpPending, err := preparePlayerXP(ctx, nk, logger, userID, xpAmount, dj.DailyMatches)
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
	tokensBanked := 0
	if activeMatch != nil {
		tokensBanked = activeMatch.TokensBanked
	}

	var tokensEarned int
	var postTokens int64
	if tokensBanked > 0 {
		postTokens = preTokens
		logger.Info("Match %s: %d tokens pre-banked, skipping delta (audit_confirmed)", req.MatchID, tokensBanked)
	} else {
		// Fallback: no round records — network failure, legacy client, or pre-Phase2 solo.
		tokensEarned = computeTokensEarned(req, isSolo, cfg)
		if preExchanges <= 0 {
			tokensEarned = 0
		}
		postTokens = preTokens + int64(tokensEarned)
		if tokensEarned > 0 {
			logger.Info("Match %s: no round records, granting %d tokens (audit_unconfirmed)", req.MatchID, tokensEarned)
		}
	}

	// Token -> Lootbox Exchange Loop
	thresh := int64(cfg.TokenExchangeThresh)
	finalTokens := postTokens
	finalExchanges := preExchanges
	exchangesMade := 0

	for finalTokens >= thresh && finalExchanges >= 1 {
		finalTokens -= thresh
		finalExchanges--
		exchangesMade++
		
		dj.ExchangesLeft--

		tier := GetLootboxConfig().MatchLossTier
		if req.Won {
			tier = GetLootboxConfig().MatchWinTier
		}

		if lootbox, lootboxWrite, lboxErr := PrepareCreateLootbox(userID, tier); lboxErr == nil {
			pending.AddStorageWrite(lootboxWrite)
			result.Lootboxes = append(result.Lootboxes, notify.LootboxGrant{
				ID:     lootbox.ID,
				Tier:   lootbox.Tier,
				Source: "token_exchange",
			})
		}
	}

	// Serialize and prepare write for daily journey (includes match counts, warmup, tokens, and updated ExchangesLeft)
	if finalExchanges <= 0 && finalTokens > thresh {
		finalTokens = thresh
	}
	dj.RoundTokens = int(finalTokens)

	djBytes, _ := json.Marshal(dj)
	pending.AddStorageWrite(&runtime.StorageWrite{
		Collection:      storageCollectionProgression,
		Key:             ProgressionKeyDailyJourney,
		UserID:          userID,
		Value:           string(djBytes),
		Version:         djVersion,
		PermissionRead:  2,
		PermissionWrite: 0,
	})

	// --- Phase 2: Atomic commit (XP + tokens + exchange + lootbox) ---
	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("Match result commit failed: %v", err)
		return nil, errors.ErrMatchRewardCommit
	}

	// StorageDelete cannot go in MultiUpdate; runs after commit.
	clearActiveMatch(ctx, nk, logger, userID)

	// --- Metadata: derived from final state — no second AccountGetId ---
	// RoundTokens always reflects the real wallet balance. The client detects
	// exchange via ExchangesMade > 0 and computes the animation path locally.

	effectiveEarned := tokensEarned
	if tokensBanked > 0 {
		effectiveEarned = tokensBanked
	}
	result.Meta = &notify.RewardMeta{
		DailyMatches:    notify.IntPtr(dj.DailyMatches),
		ExchangesLeft:   notify.IntPtr(int(finalExchanges)),
		RoundTokens:     notify.IntPtr(int(finalTokens)), // always real balance
		TokensEarned:    notify.IntPtr(effectiveEarned),
		ExchangesMade:   exchangesMade,
		CarryOverTokens: nil,
	}
	result.Economy = &notify.EconomyState{
		ExchangesLeft:  notify.IntPtr(int(finalExchanges)),
		RoundTokens:    notify.IntPtr(int(finalTokens)),
		TokensEarned:   notify.IntPtr(effectiveEarned),
		ExchangesMade:  exchangesMade,
	}
	// If an exchange occurred, expose carry-over so the client can snap to real balance
	// after the exchange animation. The client uses ExchangesMade > 0 to detect
	// the exchange event and CarryOverTokens for the post-snap value.
	if exchangesMade > 0 {
		result.Meta.CarryOverTokens = notify.IntPtr(int(finalTokens))
		result.Economy.CarryOverTokens = notify.IntPtr(int(finalTokens))
	}

	return result, nil
}

// processDeferredWinBonus credits the differential between victory and participation rewards to the first submitter
func processDeferredWinBonus(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, opponentID string, opponentWon bool) (*notify.RewardPayload, error) {
	if !opponentWon || opponentID == "" {
		return nil, nil
	}

	cfg := GetEconomyConfig()
	winDiffXP := cfg.WinXP - cfg.LossXP
	if winDiffXP <= 0 {
		return nil, nil
	}

	playerLevelUp, xpPending, err := preparePlayerXP(ctx, nk, logger, opponentID, winDiffXP, 1)
	if err != nil {
		logger.Warn("Failed to prepare deferred win XP for %s: %v", opponentID, err)
		return nil, err
	}

	if err := CommitPendingWrites(ctx, nk, logger, xpPending); err != nil {
		logger.Error("Failed committing deferred win XP for opponent %s: %v", opponentID, err)
		return nil, err
	}

	payload := notify.NewRewardPayload("match")
	payload.ReasonKey = "reward.match.win_bonus"
	payload.Progression = &notify.ProgressionDelta{
		XpGranted: notify.IntPtr(winDiffXP),
	}
	if playerLevelUp > 0 {
		payload.Progression.NewPlayerLevel = notify.IntPtr(playerLevelUp)
	}

	return payload, nil
}

// preparePlayerXP applies diminishing returns and returns deferred progression writes.
// Note: PrepareExperience operates on pets and classes, whereas this handles player level directly.
func preparePlayerXP(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, xpAmount int, matchesToday int) (int, *PendingWrites, error) {
	const treeName = "player_level"
	const playerItemID = uint32(0)

	pending := NewPendingWrites()

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
		return 0, nil, err
	}

	if progWrite != nil {
		pending.AddStorageWrite(progWrite)
	}

	// Level-up rewards
	if resultLevel > oldLevel {
		mutator := NewInventoryMutator()
		for lvl := oldLevel + 1; lvl <= resultLevel; lvl++ {
			levelRewards, mutations, err := PrepareLevelRewards(ctx, nk, logger, userID, treeName, lvl, "player", playerItemID, mutator)
			if err != nil {
				logger.Warn("Failed to prepare player level %d rewards: %v", lvl, err)
				continue
			}

			// Player progression has no abilities/sprites to grant, so we ignore mutations
			_ = mutations

			pending.Merge(levelRewards)
		}
		invPending, err := mutator.CompileWrites(ctx, nk, logger, userID)
		if err == nil && invPending != nil {
			pending.Merge(invPending)
		}
	}

	return resultLevel, pending, nil
}


// EconomyConfig holds match reward and token exchange configuration.
type EconomyConfig struct {
	WinXP                         int    `json:"win_xp"`
	LossXP                        int    `json:"loss_xp"`
	TokensPerRoundWin             int    `json:"tokens_per_round_win"`  // Half-units; default 2 = 1.0 token
	TokensPerRoundLoss            int    `json:"tokens_per_round_loss"` // Half-units; default 1 = 0.5 token
	TokensPerSoloRound            int    `json:"tokens_per_solo_round"` // Half-units; default 1 = 0.5 token
	TokenExchangeThresh           int    `json:"token_exchange_thresh"` // Default 6 = 3.0 tokens trigger
	TokenRoundCap                 int    `json:"token_round_cap"`       // Only rounds 1..N earn tokens; default 3
	TokenExchangeLootboxTier      string `json:"token_exchange_lootbox_tier"`
	TokenExchangesPerDay          int    `json:"token_exchanges_per_day"`
	DailyMatchesWarmupGoal        int    `json:"daily_matches_warmup_goal"`
	DailyMatchesWarmupLootboxTier string `json:"daily_matches_warmup_lootbox_tier"`
}

var economyConfig *EconomyConfig

func GetEconomyConfig() *EconomyConfig {
	if economyConfig == nil {
		economyConfig = &EconomyConfig{
			WinXP:                         100,
			LossXP:                        25,
			TokensPerRoundWin:             2, // 1.0 token
			TokensPerRoundLoss:            1, // 0.5 token
			TokensPerSoloRound:            1, // 0.5 token per completed round
			TokenExchangeThresh:           6, // 3.0 tokens
			TokenRoundCap:                 3, // rounds 4+ earn nothing
			TokenExchangeLootboxTier:      "standard",
			TokenExchangesPerDay:          2,
			DailyMatchesWarmupGoal:        1,
			DailyMatchesWarmupLootboxTier: "standard",
		}
	}
	return economyConfig
}

// maxRoundsPerMatch is a hard server-side ceiling on round counts.
// No legitimate match format has more rounds than this; guards against inflated
// token claims when the Rounds array is absent (legacy client or empty payload).
const maxRoundsPerMatch = 99

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
func validateRounds(ctx context.Context, nk runtime.NakamaModule, req *MatchResultRequest, userID string, logger runtime.Logger, activeMatch *ActiveMatch) {
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
	if activeMatch != nil {
		recordMap := make(map[int]RoundRecord, len(activeMatch.Rounds))
		for _, rec := range activeMatch.Rounds {
			recordMap[rec.RoundNumber] = rec
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
