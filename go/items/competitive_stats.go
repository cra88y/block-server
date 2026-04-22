package items

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// GetOrCreatePlayerStats reads the player's competitive stats document.
// Returns initialised defaults (not yet written) if no document exists.
// The Version field is populated from the storage object for OCC use.
func GetOrCreatePlayerStats(ctx context.Context, nk runtime.NakamaModule, userID string) (*PlayerStats, error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionCompetitiveStats,
		Key:        storageKeyStats,
		UserID:     userID,
	}})
	if err != nil {
		return nil, err
	}

	if len(objects) == 0 {
		// Empty Version → StorageWrite create-only semantics (no overwrite risk).
		return &PlayerStats{
			Schema:     PlayerStatsSchema,
			Rating:     1000,
			PeakRating: 1000,
			UpdatedAt:  time.Now().UnixMilli(),
		}, nil
	}

	var stats PlayerStats
	if err := json.Unmarshal([]byte(objects[0].Value), &stats); err != nil {
		return nil, err
	}
	stats.Version = objects[0].Version
	return &stats, nil
}

// MatchHistoryExists is the idempotency gate for the stats+history goroutine.
// If a history record exists, stats were already incremented — skip both.
func MatchHistoryExists(ctx context.Context, nk runtime.NakamaModule, userID, matchID string) bool {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionMatchHistory,
		Key:        matchID + "_" + userID,
		UserID:     userID,
	}})
	if err != nil {
		// On read error, assume not exists — goroutine will attempt to write.
		// Worst case: a duplicate write occurs. Both writes are idempotent (same key, same data).
		return false
	}
	return len(objects) > 0
}

// PreparePlayerStatsUpdate builds a storage write for updated player stats without committing.
// Returns the write operation to be batched into a PendingWrites collection.
func PreparePlayerStatsUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, req *MatchResultRequest, isSolo bool, won bool) (*runtime.StorageWrite, error) {
	stats, err := GetOrCreatePlayerStats(ctx, nk, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to read player stats for %s: %w", userID, err)
	}

	stats.MatchesPlayed++
	if won {
		stats.Wins++
	} else {
		stats.Losses++
	}
	if isSolo && req.FinalScore > stats.BestSoloScore {
		stats.BestSoloScore = req.FinalScore
	}
	stats.UpdatedAt = time.Now().UnixMilli()

	value, err := json.Marshal(stats)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal player stats for %s: %w", userID, err)
	}

	return &runtime.StorageWrite{
		Collection:      storageCollectionCompetitiveStats,
		Key:             storageKeyStats,
		UserID:          userID,
		Value:           string(value),
		Version:         stats.Version, // OCC: empty string = create-only; populated = update-only
		PermissionRead:  2,             // Public read — profile cards can display opponent stats
		PermissionWrite: 0,             // Server-only write
	}, nil
}

// PrepareMatchHistoryEntry builds a storage write for match history without committing.
// Returns the write operation to be batched into a PendingWrites collection.
func PrepareMatchHistoryEntry(userID string, req *MatchResultRequest, isSolo bool, won bool, opponentID string) (*runtime.StorageWrite, error) {
	mode := "1v1"
	if isSolo {
		mode = "solo"
	}

	entry := MatchHistoryEntry{
		Schema:      MatchHistoryEntrySchema,
		MatchID:     req.MatchID,
		Mode:         mode,
		Score:        req.FinalScore,
		OpponentID:   opponentID,
		OpponentName: req.OpponentName,
		Won:          won,
		MyPetID:         req.EquippedPetID,
		MyClassID:       req.EquippedClassID,
		OpponentPetID:   req.OpponentPetID,
		OpponentClassID: req.OpponentClassID,
		AbilitiesCast:   req.AbilitiesCast,
		APM:             req.APM,
		RoundsWon:       req.RoundsWon,
		RoundsLost:      req.RoundsLost,
		DurationSec:  req.MatchDurationSec,
		PiecesPlaced: req.PiecesPlaced,
		TowerHeight:  req.TowerHeight,
		PlayedAt:     time.Now().UnixMilli(),
	}

	value, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal match history for user %s match %s: %w", userID, req.MatchID, err)
	}

	return &runtime.StorageWrite{
		Collection:      storageCollectionMatchHistory,
		Key:             req.MatchID + "_" + userID,
		UserID:          userID,
		Value:           string(value),
		PermissionRead:  1, // Owner-only: match history is personal data
		PermissionWrite: 0,
	}, nil
}

// UpdatePlayerStatsAndHistory writes competitive stats and match history.
// Stats and history are committed in separate operations so that an OCC conflict
// on stats (stale version field) never silently prevents the history record from landing.
// History has no version field and is therefore conflict-free.
func UpdatePlayerStatsAndHistory(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, req *MatchResultRequest, isSolo bool, won bool, opponentID string) error {

	// --- Stats write (may fail on OCC conflict; non-fatal) ---
	statsWrite, err := PreparePlayerStatsUpdate(ctx, nk, userID, req, isSolo, won)
	if err != nil {
		logger.Warn("[competitive] stats prepare failed for user %s match %s: %v", userID, req.MatchID, err)
	} else if statsWrite != nil {
		statsPending := NewPendingWrites()
		statsPending.AddStorageWrite(statsWrite)
		if err := CommitPendingWrites(ctx, nk, logger, statsPending); err != nil {
			// OCC conflict or transient error — log and continue. Off-by-one is acceptable.
			logger.Warn("[competitive] stats commit failed for user %s match %s (OCC or transient): %v", userID, req.MatchID, err)
		}
	}

	// --- History write (independent commit; no version field — conflict-free) ---
	historyWrite, err := PrepareMatchHistoryEntry(userID, req, isSolo, won, opponentID)
	if err != nil {
		logger.Warn("[competitive] history prepare failed for user %s match %s: %v", userID, req.MatchID, err)
		return err
	}

	historyPending := NewPendingWrites()
	historyPending.AddStorageWrite(historyWrite)
	if err := CommitPendingWrites(ctx, nk, logger, historyPending); err != nil {
		logger.Warn("[competitive] history commit failed for user %s match %s: %v", userID, req.MatchID, err)
		return err
	}

	return nil
}

