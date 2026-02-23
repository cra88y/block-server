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

	// MECE Reward Domains
	Inventory   *InventoryDelta   `json:"inventory,omitempty"`
	Wallet      *WalletDelta      `json:"wallet,omitempty"`
	Progression *ProgressionDelta `json:"progression,omitempty"`
	Lootboxes   []LootboxGrant    `json:"lootboxes,omitempty"`

	// Meta (non-reward feedback)
	Meta *RewardMeta `json:"meta,omitempty"`
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

// XP and level-ups. Driven strictly by the server.
type ProgressionDelta struct {
	XpGranted      *int               `json:"xp_granted,omitempty"`
	XpBase         *int               `json:"xp_base,omitempty"` // Before diminishing
	NewPlayerLevel *int               `json:"new_player_level,omitempty"`
	NewPetLevel    *int               `json:"new_pet_level,omitempty"`
	NewClassLevel  *int               `json:"new_class_level,omitempty"`
	Unlocks        []ProgressionUnlock `json:"unlocks,omitempty"`
}

// ProgressionUnlock represents an ability/sprite unlock from level-up.
type ProgressionUnlock struct {
	System string `json:"system"`  // pet, class
	ItemID uint32 `json:"item_id"` // Which pet/class
	Type   string `json:"type"`    // ability, sprite
	Count  int    `json:"count"`
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
	// Display in UI as value / 2.0. Exchange threshold is 6 (= 3.0 tokens).
	RoundTokens  *int `json:"round_tokens,omitempty"`
	TokensEarned *int `json:"tokens_earned,omitempty"`
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
