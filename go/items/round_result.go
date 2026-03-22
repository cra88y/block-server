package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

// round_records: permanent per-round records keyed "{match_id}_round_{n}".
// Not deleted at submit — orphaned records (no match_result) reveal crashes/abandons.
const storageCollectionRoundRecords = "round_records"

// RoundRecord is a permanent per-round commit keyed (userID, match_id, round_number).
// Idempotent: second write to the same key returns the original TokensGranted.
type RoundRecord struct {
	MatchID       string `json:"match_id"`
	RoundNumber   int    `json:"round_number"`
	PlayerWon     bool   `json:"player_won"`
	TokensGranted int    `json:"tokens_granted"` // what was actually banked; returned on idempotent replay
	DurationMs    int64  `json:"duration_ms"`    // anti-cheat telemetry
	Score         int    `json:"score"`          // anti-cheat telemetry
	GrantedAt     int64  `json:"granted_at"`     // unix ms, audit trail
}

// RoundResultRequest is sent by the client after each round completes.
// IsSolo is derived from activeMatch.OpponentID — not passed by the client.
type RoundResultRequest struct {
	MatchID     string `json:"match_id"`
	RoundNumber int    `json:"round_number"` // 1-indexed; rounds outside TokenRoundCap earn 0
	PlayerWon   bool   `json:"player_won"`   // determines token rate (win > loss)
	DurationMs  int64  `json:"duration_ms"`  // telemetry only, not used for grant logic
	Score       int    `json:"score"`        // telemetry only
}

// RoundResultResponse is returned to the client to enable display reconciliation.
type RoundResultResponse struct {
	TokensGranted  int  `json:"tokens_granted"`   // 0 on idempotent replay or if drops exhausted
	RunningBalance int  `json:"running_balance"`  // server's authoritative wallet token count
	DropsRemaining int  `json:"drops_remaining"`  // if 0, client should clamp future grant predictions
	Acknowledged   bool `json:"acknowledged"`     // true = new record banked; false = idempotent replay
}

func roundRecordKey(matchID string, roundNumber int) string {
	return fmt.Sprintf("%s_round_%d", matchID, roundNumber)
}

// RpcReportRoundResult banks tokens for a completed round.
// Idempotent by (match_id, round_number). Bank-only — exchange fires at submit_match_result.
func RpcReportRoundResult(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", errors.ErrNoUserIdFound
	}

	var req RoundResultRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Error("[RoundResult] Failed to unmarshal: %v", err)
		return "", errors.ErrUnmarshal
	}

	if req.MatchID == "" || req.RoundNumber < 1 {
		return "", errors.ErrInvalidInput
	}

	// --- Idempotency check: return existing grant if already recorded ---
	recordKey := roundRecordKey(req.MatchID, req.RoundNumber)
	existing, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionRoundRecords,
		Key:        recordKey,
		UserID:     userID,
	}})
	if err != nil {
		logger.Error("[RoundResult] Storage read failed for user %s: %v", userID, err)
		return "", errors.ErrCouldNotReadStorage
	}
	if len(existing) > 0 {
		var record RoundRecord
		if json.Unmarshal([]byte(existing[0].Value), &record) == nil {
			logger.Info("[RoundResult] Idempotent replay for user %s match %s round %d (granted %d tokens previously)",
				userID, req.MatchID, req.RoundNumber, record.TokensGranted)
			return marshalRoundResponse(ctx, nk, logger, userID, record.TokensGranted, false)
		}
	}

	// ErrMatchTooShort is acceptable here — a round can complete before minMatchDurationMs.
	activeMatch, err := validateActiveMatch(ctx, nk, logger, userID, req.MatchID)
	if err != nil && err != errors.ErrMatchTooShort {
		logger.Warn("[RoundResult] Session validation failed for user %s: %v", userID, err)
		return "", err
	}
	if activeMatch == nil {
		return "", errors.ErrNoActiveMatch
	}


	cfg := GetEconomyConfig()
	isSolo := activeMatch.OpponentID == ""

	tokensGranted := 0
	if req.RoundNumber <= cfg.TokenRoundCap {
		if isSolo {
			if req.PlayerWon {
				tokensGranted = cfg.TokensPerSoloRound
			} else {
				tokensGranted = 0
			}
		} else if req.PlayerWon {
			tokensGranted = cfg.TokensPerRoundWin
		} else {
			tokensGranted = cfg.TokensPerRoundLoss
		}
	}

	// Grant 0 if drops exhausted — no phantom banking.
	if tokensGranted > 0 {
		if account, err := nk.AccountGetId(ctx, userID); err == nil {
			var wallet map[string]int64
			if json.Unmarshal([]byte(account.Wallet), &wallet) == nil {
				if wallet[walletKeyDropsLeft] <= 0 {
					tokensGranted = 0
					logger.Info("[RoundResult] User %s has no drops left — round %d grants 0 tokens", userID, req.RoundNumber)
				}
			}
		}
	}

	// Bank-only: no exchange threshold check. Exchange fires at submit_match_result.
	record := RoundRecord{
		MatchID:       req.MatchID,
		RoundNumber:   req.RoundNumber,
		PlayerWon:     req.PlayerWon,
		TokensGranted: tokensGranted,
		DurationMs:    req.DurationMs,
		Score:         req.Score,
		GrantedAt:     time.Now().UnixMilli(),
	}
	recordValue, err := json.Marshal(record)
	if err != nil {
		return "", errors.ErrMarshal
	}

	pending := NewPendingWrites()
	pending.AddStorageWrite(&runtime.StorageWrite{
		Collection:      storageCollectionRoundRecords,
		Key:             recordKey,
		UserID:          userID,
		Value:           string(recordValue),
		PermissionRead:  0,
		PermissionWrite: 0,
	})
	if tokensGranted > 0 {
		pending.AddWalletUpdate(userID, map[string]int64{
			walletKeyRoundTokens: int64(tokensGranted),
		})
	}

	if err := CommitPendingWrites(ctx, nk, logger, pending); err != nil {
		logger.Error("[RoundResult] Commit failed for user %s round %d: %v", userID, req.RoundNumber, err)
		return "", fmt.Errorf("round result commit failed: %w", err)
	}

	logger.Info("[RoundResult] Banked %d tokens for user %s match %s round %d (won=%v, solo=%v)",
		tokensGranted, userID, req.MatchID, req.RoundNumber, req.PlayerWon, isSolo)

	return marshalRoundResponse(ctx, nk, logger, userID, tokensGranted, true)
}

// marshalRoundResponse reads current wallet state and builds the response.
// Called for both new grants and idempotent replays.
func marshalRoundResponse(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, tokensGranted int, acknowledged bool) (string, error) {
	runningBalance := 0
	dropsRemaining := 0

	if account, err := nk.AccountGetId(ctx, userID); err == nil {
		var wallet map[string]int64
		if json.Unmarshal([]byte(account.Wallet), &wallet) == nil {
			runningBalance = int(wallet[walletKeyRoundTokens])
			dropsRemaining = int(wallet[walletKeyDropsLeft])
		}
	} else {
		logger.Warn("[RoundResult] Could not read wallet for response: %v", err)
	}

	resp := RoundResultResponse{
		TokensGranted:  tokensGranted,
		RunningBalance: runningBalance,
		DropsRemaining: dropsRemaining,
		Acknowledged:   acknowledged,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(b), nil
}

// ReadRoundRecordsTotal returns total tokens banked by report_round_result for this match.
// Returns 0 on error — safe fallback to computeTokensEarned.
func ReadRoundRecordsTotal(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID, matchID string, maxRounds int) int {
	if maxRounds < 1 {
		return 0
	}

	// Build batch read for all possible round keys in one call.
	reads := make([]*runtime.StorageRead, maxRounds)
	for i := 0; i < maxRounds; i++ {
		reads[i] = &runtime.StorageRead{
			Collection: storageCollectionRoundRecords,
			Key:        roundRecordKey(matchID, i+1),
			UserID:     userID,
		}
	}

	objects, err := nk.StorageRead(ctx, reads)
	if err != nil {
		logger.Warn("[RoundResult] ReadRoundRecordsTotal failed for user %s match %s: %v — falling back to computeTokensEarned", userID, matchID, err)
		return 0
	}

	total := 0
	for _, obj := range objects {
		var record RoundRecord
		if json.Unmarshal([]byte(obj.Value), &record) == nil {
			total += record.TokensGranted
		}
	}

	if total > 0 {
		logger.Info("[RoundResult] ReadRoundRecordsTotal: %d rounds found, %d tokens already banked for user %s match %s",
			len(objects), total, userID, matchID)
	}
	return total
}

// buildRoundRecordReads constructs a batch-read slice for all round records up to maxRounds.
// Used by validateRounds in match_result.go for the cross-stream audit.
func buildRoundRecordReads(matchID, userID string, maxRounds int) []*runtime.StorageRead {
	reads := make([]*runtime.StorageRead, maxRounds)
	for i := range reads {
		reads[i] = &runtime.StorageRead{
			Collection: storageCollectionRoundRecords,
			Key:        roundRecordKey(matchID, i+1),
			UserID:     userID,
		}
	}
	return reads
}
