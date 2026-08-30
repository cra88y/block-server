package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"block-server/errors"
	"block-server/notify"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Solo executes BEST operator writes. 1v1 executes INCREMENT operator on win only.
func writeLeaderboardRecords(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, req *MatchResultRequest, isSolo bool, actualWon bool) []notify.CompetitiveBoardState {
	var globalBoard, weeklyBoard string
	var score, subscore int64

	if isSolo {
		globalBoard = LeaderboardSoloSeason
		weeklyBoard = LeaderboardSoloWeekly
		score = int64(req.FinalScore)
		subscore = int64(req.MatchDurationSec)
	} else {
		globalBoard = Leaderboard1v1Season
		weeklyBoard = Leaderboard1v1Weekly
		score = 1
		subscore = 0
	}

	metadata := map[string]interface{}{
		"mode":     map[bool]string{true: "solo", false: "1v1"}[isSolo],
		"match_id": req.MatchID,
		"class_id": req.EquippedClassID,
		"pet_id":   req.EquippedPetID,
	}

	username := ""
	if users, err := nk.UsersGetId(ctx, []string{userID}, nil); err == nil && len(users) > 0 {
		username = users[0].Username
	}

	shouldWrite := isSolo || actualWon
	globalState := processBoard(ctx, nk, logger, globalBoard, userID, username, score, subscore, metadata, isSolo, shouldWrite)
	weeklyState := processBoard(ctx, nk, logger, weeklyBoard, userID, username, score, subscore, metadata, isSolo, shouldWrite)

	return []notify.CompetitiveBoardState{globalState, weeklyState}
}

// resolveCeremony is the single authoritative function that classifies a match outcome
// into a ceremony context string. The client reads this directly — no client-side re-derivation.
// All 10 MECE states are covered. Order of checks is significant (most specific first).
func resolveCeremony(rank, prevRank, prevScore, matchScore int64, rival *notify.CompetitiveTarget, isSolo bool, actualWon bool) string {
	isChampion := rank == 1
	// New champion: just reached Rank 1 from a non-Rank-1 position
	isNewChampion := isChampion && prevRank != 1

	if isNewChampion {
		return "new_champion"
	}

	if isChampion {
		if isSolo {
			if matchScore > prevScore {
				return "rechamp_beat"
			}
			// Boiling point: within 10% of own record but didn't beat it (solo only)
			if prevScore > 0 && float64(matchScore)/float64(prevScore) > 0.90 {
				return "rechamp_boiling"
			}
			return "rechamp_idle"
		} else {
			// 1v1 Champion: won the match -> beat record (+1 win); lost the match -> idle
			if actualWon {
				return "rechamp_beat"
			}
			return "rechamp_idle"
		}
	}

	// Challenger branch — has a rival above them (or just passed one)
	if rival != nil {
		if prevRank > 0 && rank > 0 && (prevRank - rank) > 0 {
			return "overtake"
		}
		if isSolo {
			// Boiling point: within 10% of rival's score but didn't pass (solo only)
			if rival.Score > 0 && float64(matchScore)/float64(rival.Score) > 0.90 {
				return "boiling_point"
			}
			if prevScore == 0 && matchScore > 0 {
				return "first_match"
			}
			if matchScore > prevScore {
				return "personal_best"
			}
			return "normal_loss"
		} else {
			// 1v1 Challenger
			if prevScore == 0 && actualWon {
				return "first_match"
			}
			if actualWon {
				return "personal_best"
			}
			return "normal_loss"
		}
	}

	// No rival above them and not champion
	if prevScore == 0 && ((isSolo && matchScore > 0) || actualWon) {
		return "first_match"
	}
	if isSolo && matchScore > prevScore {
		return "personal_best"
	}
	if !isSolo && actualWon {
		return "personal_best"
	}
	return "unranked"
}

func processBoard(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, boardId, userID, username string, score, subscore int64, metadata map[string]interface{}, isSolo bool, shouldWrite bool) notify.CompetitiveBoardState {
	var prevRank int64
	var prevScore int64
	_, prevRecords, _, _, prevErr := nk.LeaderboardRecordsList(ctx, boardId, []string{userID}, 1, "", 0)
	if prevErr == nil {
		for _, r := range prevRecords {
			if r.OwnerId == userID {
				prevRank = r.Rank
				prevScore = r.Score
				break
			}
		}
	}

	rank := prevRank
	actualScore := prevScore
	if shouldWrite {
		record, err := nk.LeaderboardRecordWrite(ctx, boardId, userID, username, score, subscore, metadata, nil)
		if err != nil {
			logger.Warn("Failed to write %s for user %s: %v", boardId, userID, err)
		} else if record != nil {
			rank = record.Rank
			actualScore = record.Score
		}
	}

	delta := 0
	if prevRank > 0 && rank > 0 {
		delta = int(prevRank - rank) // positive = climbed
	}

	state := notify.CompetitiveBoardState{
		BoardID:       boardId,
		IsScoreBased:  isSolo,
		RankCurrent:   int(rank),
		RankDelta:     delta,
		ScoreCurrent:  actualScore,
		ScorePrevious: prevScore,
	}

	if rank > 1 {
		haystack, err := nk.LeaderboardRecordsHaystack(ctx, boardId, userID, 10, "", 0)
		if err == nil && haystack != nil {
			var closestRival *api.LeaderboardRecord
			if delta > 0 { // Overtake: find the highest score now strictly below us
				for _, r := range haystack.Records {
					if r.Rank > rank && r.OwnerId != userID {
						closestRival = r
						break // Haystack is rank-ascending; first entry > rank is the immediate rival below
					}
				}
			} else { // Normal chase: find the lowest rank strictly above us
				for _, r := range haystack.Records {
					if r.Rank < rank && r.OwnerId != userID {
						closestRival = r
					}
				}
			}
			if closestRival != nil {
				scoreBaseline := actualScore
				if isSolo {
					scoreBaseline = score // Evaluate gap against this specific match run score
				}
				state.NextTarget = &notify.CompetitiveTarget{
					UserID:     closestRival.OwnerId,
					Username:   closestRival.Username.GetValue(),
					Rank:       int(closestRival.Rank),
					Score:      closestRival.Score,
					ScoreDelta: closestRival.Score - scoreBaseline,
				}
			}
		}
	}

	// Resolve ceremony using the score from this match (score) rather than post-write board score
	matchScore := score
	if !isSolo && !shouldWrite {
		matchScore = 0
	}
	state.CeremonyContext = resolveCeremony(rank, prevRank, prevScore, matchScore, state.NextTarget, isSolo, shouldWrite)

	return state
}

func leaderboardEntryFromRecord(r *api.LeaderboardRecord) LeaderboardEntry {
	return LeaderboardEntry{
		UserID:   r.OwnerId,
		Username: r.Username.GetValue(), // *wrapperspb.StringValue — GetValue() returns "" if nil
		Score:    r.Score,
		Subscore: r.Subscore,
		Rank:     r.Rank,
		Metadata: r.Metadata,
	}
}

func RpcGetLeaderboard(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		return "", errors.ErrNoUserIdFound
	}

	var req LeaderboardRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", errors.ErrUnmarshal
	}
	if req.BoardID == "" {
		return "", errors.ErrInvalidInput
	}

	limit := req.Limit
	if limit < 1 || limit > 100 {
		limit = 20
	}

	// Force inject caller ID to guarantee their localized record is returned regardless of top-N window boundary.
	ownerIDs := []string{userID}

	records, ownerRecords, nextCursor, prevCursor, err := nk.LeaderboardRecordsList(ctx, req.BoardID, ownerIDs, limit, req.Cursor, 0)
	if err != nil {
		logger.Error("Failed to list %s: %v", req.BoardID, err)
		return "", errors.ErrCouldNotReadStorage
	}

	resp := LeaderboardResponse{
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
		Entries:    make([]LeaderboardEntry, 0, len(records)),
	}
	for _, r := range records {
		resp.Entries = append(resp.Entries, leaderboardEntryFromRecord(r))
	}

	for _, r := range ownerRecords {
		if r.OwnerId == userID {
			entry := leaderboardEntryFromRecord(r)
			resp.MyEntry = &entry
			break
		}
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(b), nil
}

func RpcGetFriendsLeaderboard(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		return "", errors.ErrNoUserIdFound
	}

	var req FriendsLeaderboardRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", errors.ErrUnmarshal
	}
	if req.BoardID == "" {
		return "", errors.ErrInvalidInput
	}

	limit := req.Limit
	if limit < 1 || limit > 50 {
		limit = 50
	}

	// State 0 isolates mutual friends. Fallback to self-only guarantees UI population on Nakama API fault.
	ownerIDs := []string{userID}
	state := 0
	friends, _, err := nk.FriendsList(ctx, userID, 1000, &state, "")
	if err != nil {
		logger.Warn("Failed to fetch friends for %s, returning self-only: %v", userID, err)
	} else {
		for _, f := range friends {
			if f.User != nil && f.User.Id != "" {
				ownerIDs = append(ownerIDs, f.User.Id)
			}
		}
	}

	// List returns global top-N in `records` and owner-filtered in `ownerRecords`. Discard `records` to enforce mutual-friends boundary.
	_, ownerRecords, _, _, err := nk.LeaderboardRecordsList(ctx, req.BoardID, ownerIDs, limit, "", 0)
	if err != nil {
		logger.Error("Failed to list friends leaderboard %s: %v", req.BoardID, err)
		return "", errors.ErrCouldNotReadStorage
	}

	resp := LeaderboardResponse{
		Entries: make([]LeaderboardEntry, 0, len(ownerRecords)),
	}
	// ownerRecords order is non-deterministic; client delegates sorting.
	for _, r := range ownerRecords {
		entry := leaderboardEntryFromRecord(r)
		if r.OwnerId == userID {
			copy := entry
			resp.MyEntry = &copy
		} else {
			resp.Entries = append(resp.Entries, entry)
		}
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(b), nil
}

func RpcGetPlayerStats(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	callerUserID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		return "", errors.ErrNoUserIdFound
	}

	targetUserID := callerUserID
	if payload != "" && payload != "{}" && payload != "null" {
		var req PlayerStatsRequest
		if jsonErr := json.Unmarshal([]byte(payload), &req); jsonErr == nil && req.UserID != "" {
			targetUserID = req.UserID
		}
	}

	stats, err := GetOrCreatePlayerStats(ctx, nk, targetUserID)
	if err != nil {
		logger.Error("Failed to read player stats for %s: %v", targetUserID, err)
		return "", errors.ErrCouldNotReadStorage
	}

	b, err := json.Marshal(stats)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(b), nil
}

// Storage API returns alphabetical keys; client assumes chronological sort responsibility.
func RpcGetMatchHistory(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		return "", errors.ErrNoUserIdFound
	}

	var req MatchHistoryRequest
	if payload != "" && payload != "{}" && payload != "null" {
		// Ignore unmarshal faults to permit malformed payload fallback to defaults.
		json.Unmarshal([]byte(payload), &req) //nolint:errcheck
	}

	limit := req.Limit
	if limit < 1 || limit > maxMatchHistoryPerUser {
		limit = 20
	}

	offset := 0
	if req.Cursor != "" {
		fmt.Sscanf(req.Cursor, "%d", &offset)
	}

	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionMatchHistory,
		Key:        "history",
		UserID:     userID,
	}})
	if err != nil {
		logger.Error("Failed to read match history for %s: %v", userID, err)
		return "", errors.ErrCouldNotReadStorage
	}

	resp := MatchHistoryResponse{
		Entries: make([]MatchHistoryEntry, 0),
	}

	if len(objects) > 0 {
		var doc MatchHistoryDocument
		if json.Unmarshal([]byte(objects[0].Value), &doc) == nil {
			start := offset
			end := offset + limit
			if start < len(doc.Matches) {
				if end > len(doc.Matches) {
					end = len(doc.Matches)
				} else {
					resp.NextCursor = fmt.Sprintf("%d", end)
				}
				resp.Entries = doc.Matches[start:end]
			}
		}
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(b), nil
}
