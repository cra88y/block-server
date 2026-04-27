package items

import (
	"context"
	"database/sql"
	"encoding/json"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Writes match result to leaderboards synchronously and returns the season rank.
// Solo: BEST operator (writes always). 1v1: INCREMENT operator (writes on win only).
func writeLeaderboardRecords(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, req *MatchResultRequest, isSolo bool, actualWon bool) int {
	var globalBoard, weeklyBoard string
	var score, subscore int64

	if isSolo {
		globalBoard = LeaderboardSoloSeason
		weeklyBoard = LeaderboardSoloWeekly
		score = int64(req.FinalScore)
		subscore = int64(req.MatchDurationSec)
	} else {
		if !actualWon {
			return 0
		}
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

	// Fetch username for leaderboard record
	username := ""
	if users, err := nk.UsersGetId(ctx, []string{userID}, nil); err == nil && len(users) > 0 {
		username = users[0].Username
	}

	// Write global board — capture rank from this record.
	var rank int64
	record, err := nk.LeaderboardRecordWrite(ctx, globalBoard, userID, username, score, subscore, metadata, nil)
	if err != nil {
		logger.Warn("[leaderboard] Failed to write %s for user %s: %v", globalBoard, userID, err)
	} else if record != nil {
		rank = record.Rank
	}

	// Write weekly board — rank not captured (global is the canonical rank surface).
	if _, err := nk.LeaderboardRecordWrite(ctx, weeklyBoard, userID, username, score, subscore, metadata, nil); err != nil {
		logger.Warn("[leaderboard] Failed to write %s for user %s: %v", weeklyBoard, userID, err)
	}

	return int(rank)
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

// Fetches top entries from a board and the caller's own record.
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

	// Always include the caller's own userID in ownerIDs so their record is
	// returned in ownerRecords even if they aren't in the top-N window.
	ownerIDs := []string{userID}

	records, ownerRecords, nextCursor, prevCursor, err := nk.LeaderboardRecordsList(ctx, req.BoardID, ownerIDs, limit, req.Cursor, 0)
	if err != nil {
		logger.Error("[leaderboard] Failed to list %s: %v", req.BoardID, err)
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

// Fetches a board filtered to the caller's mutual friends and self.
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

	// Fetch mutual friends (state = 0). Falls back to self-only on error.
	ownerIDs := []string{userID}
	state := 0
	friends, _, err := nk.FriendsList(ctx, userID, 1000, &state, "")
	if err != nil {
		logger.Warn("[leaderboard] Failed to fetch friends for %s, returning self-only: %v", userID, err)
	} else {
		for _, f := range friends {
			if f.User != nil && f.User.Id != "" {
				ownerIDs = append(ownerIDs, f.User.Id)
			}
		}
	}

	// ownerRecords contains the friends-filtered records (Nakama filters by ownerIDs).
	// records would be the global top-N — we do NOT want that here.
	_, ownerRecords, _, _, err := nk.LeaderboardRecordsList(ctx, req.BoardID, ownerIDs, limit, "", 0)
	if err != nil {
		logger.Error("[leaderboard] Failed to list friends leaderboard %s: %v", req.BoardID, err)
		return "", errors.ErrCouldNotReadStorage
	}

	resp := LeaderboardResponse{
		Entries: make([]LeaderboardEntry, 0, len(ownerRecords)),
	}
	// Separate caller's own entry from friend entries.
	// ownerRecords order is not guaranteed — client must sort by Rank.
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

// Fetches public competitive stats. Omitting user_id returns the caller's stats.
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
		logger.Error("[competitive] Failed to read player stats for %s: %v", targetUserID, err)
		return "", errors.ErrCouldNotReadStorage
	}

	b, err := json.Marshal(stats)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(b), nil
}

// Fetches paginated match history for the caller. Ordered alphabetically; client sorts chronologically.
func RpcGetMatchHistory(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := GetUserIDFromContext(ctx, logger)
	if err != nil {
		return "", errors.ErrNoUserIdFound
	}

	var req MatchHistoryRequest
	if payload != "" && payload != "{}" && payload != "null" {
		// Ignore unmarshal errors — use defaults on malformed input
		json.Unmarshal([]byte(payload), &req) //nolint:errcheck
	}

	limit := req.Limit
	if limit < 1 || limit > maxMatchHistoryPerUser {
		limit = 20
	}

	objects, nextCursor, err := nk.StorageList(ctx, "", userID, storageCollectionMatchHistory, limit, req.Cursor)
	if err != nil {
		logger.Error("[competitive] Failed to list match history for %s: %v", userID, err)
		return "", errors.ErrCouldNotReadStorage
	}

	resp := MatchHistoryResponse{
		NextCursor: nextCursor,
		Entries:    make([]MatchHistoryEntry, 0, len(objects)),
	}
	for _, obj := range objects {
		var entry MatchHistoryEntry
		if json.Unmarshal([]byte(obj.Value), &entry) == nil {
			resp.Entries = append(resp.Entries, entry)
		}
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return "", errors.ErrMarshal
	}
	return string(b), nil
}
