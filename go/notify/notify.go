// Package notify provides unified notification types and helpers for server-to-client communication.
// This schema mirrors the client's RewardPayload structure
package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Notification codes matching client ServerNotifyCode enum
// MUST remain aligned with blockjitsu/scripts/services/notify/ServerNotifyTypes.cs
const (
	CodeSystem        = 0   // System messages / fallback toast
	CodeToast         = 1   // Simple toast notifications
	CodeReward        = 2   // Reward ceremonies (lootbox, level-up)
	CodeCenterMessage = 3   // Center flyout message
	CodeWallet        = 4   // Wallet/currency updates
	CodeSocial        = 5   // Friend activity
	CodeMatchmaking   = 6   // Matchmaking/lobby events
	CodeDailyRefresh  = 7   // Daily/weekly refresh events
	CodeAnnouncement  = 8   // Maintenance/server announcements
	CodeDevice        = 100 // Single-device enforcement
)

// RewardPayload is the unified reward schema for all delivery channels.
// Domains are MECE - each maps to a player state bucket.
type RewardPayload struct {
	// Identity
	RewardID  string `json:"reward_id"`
	CreatedAt int64  `json:"created_at"`

	// Context
	Source     string            `json:"source,omitempty"`      // match, lootbox, level_up, daily
	ReasonKey  string            `json:"reason_key,omitempty"`  // Localization key
	ReasonArgs map[string]string `json:"reason_args,omitempty"` // Localization args

	// Action (optional deep link)
	Action        string `json:"action,omitempty"`
	ActionPayload string `json:"action_payload,omitempty"`

	// LeaderboardRank is the player's resulting rank after this match's leaderboard write.
	// 0 (omitted) means the write failed or the mode doesn't write a board (e.g. 1v1 loss).
	LeaderboardRank int `json:"leaderboard_rank,omitempty"`

	// LeaderboardRankDelta is the change in position on the season board.
	// Negative = climbed (better rank). Positive = dropped. 0 = unchanged or first placement.
	// Only set when LeaderboardRank > 0 and a previous rank existed.
	LeaderboardRankDelta int `json:"leaderboard_rank_delta,omitempty"`

	// BoardId is the canonical leaderboard ID this rank applies to
	// (solo_season, solo_weekly, 1v1_season, 1v1_weekly).
	BoardId string `json:"board_id,omitempty"`

	// MECE Reward Domains
	Inventory        *InventoryDelta   `json:"inventory,omitempty"`
	Wallet           *WalletDelta      `json:"wallet,omitempty"`
	Progression      *ProgressionDelta `json:"progression,omitempty"`
	Lootboxes        []LootboxGrant    `json:"lootboxes,omitempty"`
	DuplicateGrants  []DuplicateGrant  `json:"duplicate_grants,omitempty"`

	// Meta (non-reward feedback)
	Meta        *RewardMeta `json:"meta,omitempty"`
	DisplayTier string      `json:"display_tier,omitempty"`
}

// DuplicateGrant represents an item that was rolled but already owned, converted to currency.
type DuplicateGrant struct {
	ItemID           uint32 `json:"item_id"`
	Type             string `json:"type"` // pet, class, background, piece_style
	FallbackCurrency string `json:"fallback_currency"` // gold, gems
	FallbackAmount   int    `json:"fallback_amount"`
}

// Client-side inventory state must be add-only. No removals.
type InventoryDelta struct {
	Items []ItemGrant `json:"items"`
}

// ItemGrant represents a single item granted.
type ItemGrant struct {
	ID   uint32 `json:"id"`
	Type string `json:"type"` // pet, class, background, piece_style
}

// Discrete currency changes rather than absolute totals.
// Allows multiple parallel matches to claim rewards without race conditions.
type WalletDelta struct {
	Gold   int `json:"gold,omitempty"`
	Gems   int `json:"gems,omitempty"`
	Treats int `json:"treats,omitempty"`
}

// TierState represents the current state of a progression reward tier
type TierState struct {
	Status     string `json:"s"`
	UnlockedAt int64  `json:"ua,omitempty"`
	ClaimedAt  int64  `json:"ca,omitempty"`
}

// XP and level-ups. Driven strictly by the server.
type ProgressionDelta struct {
	XpGranted           *int                 `json:"xp_granted,omitempty"`
	XpBase              *int                 `json:"xp_base,omitempty"` // Before diminishing
	NewPlayerLevel      *int                 `json:"new_player_level,omitempty"`
	NewPetLevel         *int                 `json:"new_pet_level,omitempty"`
	NewClassLevel       *int                 `json:"new_class_level,omitempty"`
	NewUnclaimedRewards []int                `json:"new_unclaimed_rewards,omitempty"`
	UpdatedTierStates   map[string]TierState `json:"updated_tier_states,omitempty"`
	Unlocks             []ProgressionUnlock  `json:"unlocks,omitempty"`
}

// ProgressionUnlock represents an ability/sprite unlock from level-up.
type ProgressionUnlock struct {
	System  string   `json:"system"`  // pet, class
	ItemID  uint32   `json:"item_id"` // Which pet/class
	Type    string   `json:"type"`    // ability, sprite
	Indices []uint32 `json:"indices"` // Position-based indices
}

// Grants a sealed lootbox. Contents remain a mystery until the player opens it.
type LootboxGrant struct {
	ID     string `json:"id"`
	Tier   string `json:"tier"`             // standard, premium, legendary
	Source string `json:"source,omitempty"` // match_drop, purchase, level_up
}

// RewardMeta contains non-reward feedback.
type RewardMeta struct {
	DropsRemaining  *int   `json:"drops_remaining,omitempty"`
	NextDropRefresh *int64 `json:"next_drop_refresh,omitempty"`
	DailyMatches    *int   `json:"daily_matches,omitempty"`
	// RoundTokens is the player's current half-unit token balance after this match.
	// Always reflects the real wallet state. Display in UI as value / 2.0.
	// Exchange threshold is 6 (= 3.0 tokens).
	// When an exchange occurs, ExchangesMade > 0 and CarryOverTokens contains the remainder.
	// The client reads ExchangesMade to trigger the exchange animation.
	RoundTokens  *int `json:"round_tokens,omitempty"`
	TokensEarned *int `json:"tokens_earned,omitempty"`
	// CarryOverTokens is the balance remaining AFTER exchange deduction.
	// Only set when an exchange occurred (ExchangesMade > 0).
	CarryOverTokens *int `json:"carry_over_tokens,omitempty"`
	// ExchangesMade is the count of token→lootbox exchanges that occurred this match.
	// > 0 means the player earned at least one lootbox from token exchange.
	// The client uses this to trigger the exchange animation sequence.
	ExchangesMade int `json:"exchanges_made,omitempty"`
	// ErrorCode is set when the match result was rejected by a server validation gate.
	// Non-empty means no rewards were processed. Known values: MATCH_TOO_SHORT.
	// The client routes to distinct UI messages based on this code.
	ErrorCode string `json:"error_code,omitempty"`
}

// NewRewardPayload creates a new RewardPayload with generated ID and timestamp.
func NewRewardPayload(source string) *RewardPayload {
	return &RewardPayload{
		RewardID:  generateID(),
		CreatedAt: time.Now().UnixMilli(),
		Source:    source,
	}
}

// generateID creates a random 12-character hex string.
func generateID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// IntPtr is a helper to create pointer to int.
func IntPtr(v int) *int {
	return &v
}

// Int64Ptr is a helper to create pointer to int64.
func Int64Ptr(v int64) *int64 {
	return &v
}

// Helper to marshal and ship a RewardPayload down to the client.
func SendReward(ctx context.Context, nk runtime.NakamaModule, userID string, payload *RewardPayload) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("reward marshal: %w", err)
	}
	var content map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &content); err != nil {
		return fmt.Errorf("reward unmarshal: %w", err)
	}
	return nk.NotificationSend(ctx, userID, "Reward!", content, CodeReward, "", true)
}

// SendToast sends a simple toast notification.
func SendToast(ctx context.Context, nk runtime.NakamaModule, userID, message string) error {
	content := map[string]interface{}{
		"message": message,
	}
	return nk.NotificationSend(ctx, userID, message, content, CodeToast, "", false)
}

// SendCenterMessage sends a center flyout message.
func SendCenterMessage(ctx context.Context, nk runtime.NakamaModule, userID, message string, duration float64) error {
	content := map[string]interface{}{
		"message":  message,
		"duration": duration,
	}
	return nk.NotificationSend(ctx, userID, message, content, CodeCenterMessage, "", false)
}

// SendAnnouncement sends a persistent server announcement.
func SendAnnouncement(ctx context.Context, nk runtime.NakamaModule, userID, title, body string) error {
	content := map[string]interface{}{
		"title": title,
		"body":  body,
	}
	return nk.NotificationSend(ctx, userID, title, content, CodeAnnouncement, "", true)
}
