package items

import (
	"encoding/json"
	"fmt"
)

type GameDataStruct struct {
	Pets        map[uint32]*Pet       `json:"pets"`
	Classes     map[uint32]*Class     `json:"classes"`
	Backgrounds map[uint32]Background `json:"backgrounds"`
	PieceStyles map[uint32]PieceStyle `json:"piece_styles"`
	LevelTrees  map[string]LevelTree  `json:"level_trees"`
}

type Pet struct {
	Name          string   `json:"name"`
	SpriteCount   int      `json:"spriteCount"`
	AbilityIDs    []uint32 `json:"abilityIds"`
	AbilitySet    map[uint32]struct{}
	BackgroundIDs []uint32 `json:"backgroundIds"`
	StyleIDs      []uint32 `json:"styleIds"`
	LevelTreeName string   `json:"levelTreeName"`
	BaseAttack         int    `json:"baseAttack"`
	AttackScalePercent int    `json:"attackScalePercent"`
	BaseHealth         int    `json:"baseHealth"`
	HealthScalePercent int    `json:"healthScalePercent"`
}

type Class struct {
	Name          string   `json:"name"`
	SpriteCount   int      `json:"spriteCount"`
	AbilityIDs    []uint32 `json:"abilityIds"`
	AbilitySet    map[uint32]struct{}
	BackgroundIDs []uint32 `json:"backgroundIds"`
	StyleIDs      []uint32 `json:"styleIds"`
	LevelTreeName string   `json:"levelTreeName"`
	BaseAttack         int    `json:"baseAttack"`
	AttackScalePercent int    `json:"attackScalePercent"`
	BaseHealth         int    `json:"baseHealth"`
	HealthScalePercent int    `json:"healthScalePercent"`
}

type Background struct {
	Name string `json:"name"`
}

type PieceStyle struct {
	Name string `json:"name"`
}

type LevelTree struct {
	MaxLevel            int    `json:"max_level"`
	BaseXP              int    `json:"base_xp"`
	LevelThresholds     []int  `json:"level_thresholds"`
	RewardedLevels      []int  `json:"rewarded_levels"`
	UpgradeCostCurrency string `json:"upgrade_cost_currency"`
	CostPerUpgrade      int    `json:"cost_per_upgrade"`
	XpPerUpgrade        int    `json:"xp_per_upgrade"`
	Rewards             map[string]struct {
		Gold        string `json:"gold,omitempty"`
		Gems        string `json:"gems,omitempty"`
		Abilities   string `json:"abilities,omitempty"`
		Backgrounds string `json:"backgrounds,omitempty"`
		PieceStyles string `json:"piece_styles,omitempty"`
		Sprites     string `json:"sprites,omitempty"`
	} `json:"rewards"`
}

const (
	storageCollectionInventory = "inventory"
	storageKeyPet              = "pets"         // [0,1,2]
	storageKeyClass            = "classes"      // [0,1,2]
	storageKeyBackground       = "backgrounds"  // [0,1,2,3]
	storageKeyPieceStyle       = "piece_styles" // [0]
	storageKeyPlayer           = "player"       // Singleton — ID 0 is always the local player

	storageCollectionEquipment   = "equipment"
	storageCollectionProgression = "progression"
)

const (
	ProgressionKeyPet    = "pet_"
	ProgressionKeyClass  = "class_"
	ProgressionKeyPlayer = "player_"
)

type ItemProgression struct {
	Level int `json:"level"`
	Exp   int `json:"xp"`

	EquippedAbility int `json:"ea"`
	EquippedSprite  int `json:"es"`

	UnlockedAbilityIndices ClaimedIndices `json:"au"`
	UnlockedSpriteIndices  []uint32       `json:"su"`
	BackgroundsUnlocked    int            `json:"bu"`
	PieceStylesUnlocked    int            `json:"pu"`

	UnclaimedRewards []int `json:"ur,omitempty"`

	Version string `json:"-"`
}

// ClaimedIndices is []int32 with migration from old int format
type ClaimedIndices []int32

// UnmarshalJSON handles migration from old AbilitiesUnlocked int format
func (c *ClaimedIndices) UnmarshalJSON(data []byte) error {
	// Try array first (new format)
	var arr []int32
	if err := json.Unmarshal(data, &arr); err == nil {
		if arr == nil {
			arr = []int32{}
		}
		*c = ClaimedIndices(arr)
		return nil
	}

	// Try int (old format) — migrate
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("ClaimedIndices: expected int or array, got %s", string(data))
	}

	if n <= 0 {
		*c = ClaimedIndices{}
		return nil
	}

	result := make([]int32, n)
	for i := range result {
		result[i] = int32(i)
	}
	*c = ClaimedIndices(result)
	return nil
}

// HasAbility checks if the player has unlocked the ability at the given index
func (p *ItemProgression) HasAbility(index int) bool {
	for _, idx := range p.UnlockedAbilityIndices {
		if int(idx) == index {
			return true
		}
	}
	return false
}

type AbilityEquipRequest struct {
	ItemID    uint32 `json:"id"`
	AbilityID uint32 `json:"ability_id"`
}

type EquipmentResponse struct {
	Pet        uint32 `json:"pet"`
	Class      uint32 `json:"class"`
	Background uint32 `json:"background"`
	PieceStyle uint32 `json:"piece_style"`
}

type InventoryResponse struct {
	Pets        []uint32 `json:"pets"`
	Classes     []uint32 `json:"classes"`
	Backgrounds []uint32 `json:"backgrounds"`
	PieceStyles []uint32 `json:"piece_styles"`
}

type ProgressionResponse struct {
	Pets    map[uint32]ItemProgression `json:"pets"`
	Classes map[uint32]ItemProgression `json:"classes"`
}

type InventoryData struct {
	Items []uint32 `json:"items"`
}

type EquipmentData struct {
	ID uint32 `json:"id"`
}

type PetTreatRequest struct {
	PetID uint32 `json:"pet_id"`
	Count int    `json:"count"` // number of treats to use in one atomic call; defaults to 1
}

// RoundResult is one player's self-reported round outcome, embedded in MatchResultRequest.Rounds[].
// The server cross-validates this against RoundRecord (written by report_round_result) —
// discrepancies between the two streams are the primary audit signal.
type RoundResult struct {
	RoundNumber int   `json:"round"`
	PlayerWon   bool  `json:"player_won"`
	Survived    bool  `json:"survived"`    // true if player health > 0 at round end
	DurationMs  int64 `json:"duration_ms"` // milliseconds; matches RoundRecord.DurationMs for direct comparison
}

// Match Result Types
type MatchResultRequest struct {
	MatchID           string        `json:"match_id"`
	Won               bool          `json:"won"`
	FinalScore        int           `json:"final_score"`
	OpponentScore     int           `json:"opponent_score"`
	MatchDurationSec  int           `json:"match_duration_sec"`
	EquippedPetID     uint32        `json:"equipped_pet_id"`
	EquippedClassID   uint32        `json:"equipped_class_id"`
	OpponentPetID     uint32        `json:"opponent_pet_id,omitempty"`
	OpponentClassID   uint32        `json:"opponent_class_id,omitempty"`
	RoundsWon         int           `json:"rounds_won"`
	RoundsLost        int           `json:"rounds_lost"`
	Rounds            []RoundResult `json:"rounds"`             // Per-round history; server validates plausibility
	OpponentForfeited bool          `json:"opponent_forfeited"` // Whether the opponent forfeited the match
	AbilitiesCast     int           `json:"abilities_cast"`
	APM               int           `json:"apm"`
	PiecesPlaced      int           `json:"pieces_placed"`
	TowerHeight       int           `json:"tower_height"`
	OpponentName      string        `json:"opponent_name,omitempty"`
}

// ─── Leaderboard & Competitive System ───────────────────────────────────────

const (
	// Leaderboard IDs — must match LeaderboardCreate calls in InitModule.
	// solo_* : BEST operator — highest single-run score wins.
	// 1v1_*  : INCREMENT operator — win count accumulates, resets per cadence.
	//
	// Season boards (no auto-reset): manually wiped at major balance patches / season boundaries.
	// Weekly boards:                 auto-reset Monday midnight UTC.
	LeaderboardSoloSeason = "solo_season" // primary season score board; wipe on patch/season boundary
	LeaderboardSoloWeekly = "solo_weekly" // primary weekly competitive surface
	Leaderboard1v1Season  = "1v1_season"  // season win count
	Leaderboard1v1Weekly  = "1v1_weekly"
)

const (
	storageCollectionCompetitiveStats = "competitive_stats"
	storageKeyStats                   = "stats"
	storageCollectionMatchHistory     = "match_history"
	maxMatchHistoryPerUser            = 100

	// Schema versions — bump on breaking struct changes.
	PlayerStatsSchema       = 1
	MatchHistoryEntrySchema = 1
)

// PlayerStats is the competitive aggregate for a single player.
// Collection: competitive_stats, Key: "stats", UserID: playerID.
// OCC-protected: Version is read from storage and written back.
// Rating is ELO-ready from day one; computation is stubbed until matchmaking is skill-based.
type PlayerStats struct {
	Schema        int    `json:"schema"` // always PlayerStatsSchema
	Rating        int    `json:"rating"` // ELO rating; initialised to 1000
	PeakRating    int    `json:"peak_rating"`
	Wins          int    `json:"wins"`
	Losses        int    `json:"losses"`
	MatchesPlayed int    `json:"matches_played"`
	BestSoloScore int    `json:"best_solo_score"`
	SeasonID      string `json:"season_id,omitempty"` // set when seasons are introduced
	UpdatedAt     int64  `json:"updated_at"`
	Version       string `json:"-"` // OCC version key from storage; not serialised to JSON
}

// MatchHistoryEntry is a single match record, written after each completed match.
// Collection: match_history, Key: matchID+"_"+userID, UserID: playerID.
// Append-only and idempotent (same key overwrites with equivalent data).
// Rating and RatingDelta are nil until ELO is active — nil != 0.
type MatchHistoryEntry struct {
	Schema      int    `json:"schema"` // always MatchHistoryEntrySchema
	MatchID     string `json:"match_id"`
	Mode        string `json:"mode"`  // "solo" | "1v1"
	Score        int    `json:"score"` // FinalScore
	OpponentID   string `json:"opponent_id,omitempty"`
	OpponentName string `json:"opponent_name,omitempty"`
	Won          bool   `json:"won"`
	MyPetID         uint32 `json:"my_pet_id,omitempty"`
	MyClassID       uint32 `json:"my_class_id,omitempty"`
	OpponentPetID   uint32 `json:"opponent_pet_id"`
	OpponentClassID uint32 `json:"opponent_class_id"`

	AbilitiesCast int `json:"abilities_cast"`
	APM           int `json:"apm"`

	RoundsWon    int    `json:"rounds_won"`
	RoundsLost   int    `json:"rounds_lost"`
	DurationSec  int    `json:"duration_sec"`
	PiecesPlaced int    `json:"pieces_placed"`
	TowerHeight  int    `json:"tower_height"`
	Rating       *int   `json:"rating,omitempty"`       // player rating at match time; nil until ELO
	RatingDelta  *int   `json:"rating_delta,omitempty"` // ELO delta applied; nil until ELO
	PlayedAt     int64  `json:"played_at"`
}

// ─── RPC request/response types ─────────────────────────────────────────────

// LeaderboardRequest fetches a board's top entries + the caller's own record.
type LeaderboardRequest struct {
	BoardID string `json:"board_id"`
	Limit   int    `json:"limit,omitempty"`  // default 20, max 100
	Cursor  string `json:"cursor,omitempty"` // empty = start from top
}

// LeaderboardEntry is one row returned from a leaderboard query.
type LeaderboardEntry struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Score    int64  `json:"score"`
	Subscore int64  `json:"subscore"`
	Rank     int64  `json:"rank"`
	Metadata string `json:"metadata,omitempty"`
}

// LeaderboardResponse is returned by get_leaderboard and get_friends_leaderboard.
// MyEntry is the caller's own record; nil if they have no entry on this board.
type LeaderboardResponse struct {
	Entries    []LeaderboardEntry `json:"entries"`
	MyEntry    *LeaderboardEntry  `json:"my_entry,omitempty"`
	NextCursor string             `json:"next_cursor,omitempty"`
	PrevCursor string             `json:"prev_cursor,omitempty"`
}

// FriendsLeaderboardRequest fetches a board filtered to the caller's friends + self.
type FriendsLeaderboardRequest struct {
	BoardID string `json:"board_id"`
	Limit   int    `json:"limit,omitempty"` // default 50, max 50
}

// PlayerStatsRequest fetches competitive stats for a user.
// Omitting UserID returns the calling user's own stats.
type PlayerStatsRequest struct {
	UserID string `json:"user_id,omitempty"`
}

// MatchHistoryRequest fetches paginated match history for the calling user.
type MatchHistoryRequest struct {
	Limit  int    `json:"limit,omitempty"` // default 20, max maxMatchHistoryPerUser
	Cursor string `json:"cursor,omitempty"`
}

// MatchHistoryResponse is returned by get_match_history.
type MatchHistoryResponse struct {
	Entries    []MatchHistoryEntry `json:"entries"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

// GetUsersLoadoutsPayload is the request payload for get_users_loadouts
type GetUsersLoadoutsPayload struct {
	UserIDs []string `json:"user_ids"`
}

// PlayerLoadout represents the authoritative stats for a player entering a match
type PlayerLoadout struct {
	PetID          uint32 `json:"pet_id"`
	PetLevel       int    `json:"pet_level"`
	PetAbilityID   uint32 `json:"pet_ability_id"`
	ClassID        uint32 `json:"class_id"`
	ClassLevel     int    `json:"class_level"`
	ClassAbilityID uint32 `json:"class_ability_id"`
	ThemeID        uint32 `json:"theme_id"`
	BackgroundID   uint32 `json:"background_id"`
}






