package items

import (
	"context"
	"encoding/json"
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
			Schema:    PlayerStatsSchema,
			Rating:    1000,
			PeakRating: 1000,
			UpdatedAt: time.Now().UnixMilli(),
		}, nil
	}

	var stats PlayerStats
	if err := json.Unmarshal([]byte(objects[0].Value), &stats); err != nil {
		return nil, err
	}
	stats.Version = objects[0].Version
	return &stats, nil
}

// UpdatePlayerStats increments win/loss counters and best solo score.
// OCC-protected via the Version captured by GetOrCreatePlayerStats.
// On conflict (rare retry race), logs and continues — off-by-one is acceptable at launch scale.
func UpdatePlayerStats(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, req *MatchResultRequest, isSolo bool, won bool) {
	stats, err := GetOrCreatePlayerStats(ctx, nk, userID)
	if err != nil {
		logger.Warn("[competitive] Failed to read player stats for %s: %v", userID, err)
		return
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
		logger.Warn("[competitive] Failed to marshal player stats for %s: %v", userID, err)
		return
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionCompetitiveStats,
		Key:             storageKeyStats,
		UserID:          userID,
		Value:           string(value),
		Version:         stats.Version, // OCC: empty string = create-only; populated = update-only
		PermissionRead:  2,             // Public read — profile cards can display opponent stats
		PermissionWrite: 0,             // Server-only write
	}})
	if err != nil {
		// OCC conflict expected on retry races — stat will be off by one match.
		logger.Warn("[competitive] Failed to write player stats for %s (OCC conflict or write error): %v", userID, err)
	}
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

// AppendMatchHistory writes a single match record.
// Key is matchID+"_"+userID — idempotent: re-write on retry produces equivalent data.
// OpponentID is passed explicitly (activeMatch.OpponentID is cleared before this runs).
func AppendMatchHistory(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, req *MatchResultRequest, isSolo bool, won bool, opponentID string) {
	mode := "1v1"
	if isSolo {
		mode = "solo"
	}

	entry := MatchHistoryEntry{
		Schema:      MatchHistoryEntrySchema,
		MatchID:     req.MatchID,
		Mode:        mode,
		Score:       req.FinalScore,
		OpponentID:  opponentID,
		Won:         won,
		RoundsWon:   req.RoundsWon,
		RoundsLost:  req.RoundsLost,
		DurationSec: req.MatchDurationSec,
		// Rating and RatingDelta intentionally nil until ELO is active.
		PlayedAt: time.Now().UnixMilli(),
	}

	value, err := json.Marshal(entry)
	if err != nil {
		logger.Warn("[competitive] Failed to marshal match history for user %s match %s: %v", userID, req.MatchID, err)
		return
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      storageCollectionMatchHistory,
		Key:             req.MatchID + "_" + userID,
		UserID:          userID,
		Value:           string(value),
		PermissionRead:  1, // Owner-only: match history is personal data
		PermissionWrite: 0,
	}})
	if err != nil {
		logger.Warn("[competitive] Failed to write match history for user %s match %s: %v", userID, req.MatchID, err)
	}
}
