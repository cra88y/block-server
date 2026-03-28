package items

import (
	"context"
	"database/sql"
	"encoding/json"

	blockerrors "block-server/errors"
	"block-server/notify"

	"github.com/heroiclabs/nakama-common/runtime"
)

// GameInviteRequest is the payload for the send_game_invite RPC.
// CROSS-REPO CONTRACT: field names must match client SendGameInvitePayload JSON tags.
// Client: scripts/models/SocialTypes.cs → SendGameInvitePayload("target_id", "match_id")
type GameInviteRequest struct {
	TargetUserID string `json:"target_id"`
	MatchID      string `json:"match_id"`
}

// RpcSendGameInvite delivers a match invitation notification to a target player.
//
// Flow:
//
//	Caller (inviter) creates/joins a private Nakama match first,
//	then calls this RPC with the match ID.
//	Target receives a CodeSocial (5) notification → InviteService on client.
//	Target calls JoinMatchById(matchId) to accept.
//
// Registered as: "send_game_invite"
func RpcSendGameInvite(
	ctx context.Context,
	logger runtime.Logger,
	db *sql.DB,
	nk runtime.NakamaModule,
	payload string,
) (string, error) {
	// ── Authenticate sender ───────────────────────────────────────────────────
	senderID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || senderID == "" {
		return "", blockerrors.ErrNoUserIdFound
	}

	// ── Parse request ─────────────────────────────────────────────────────────
	var req GameInviteRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.WithFields(map[string]interface{}{
			"sender":  senderID,
			"payload": payload,
			"error":   err.Error(),
		}).Error("send_game_invite: unmarshal failed")
		return "", blockerrors.ErrUnmarshal
	}

	if req.TargetUserID == "" {
		return "", blockerrors.ErrInvalidInviteTarget
	}
	if req.MatchID == "" {
		return "", blockerrors.ErrInviteMissingMatch
	}
	if req.TargetUserID == senderID {
		return "", blockerrors.ErrInvalidInput // can't invite yourself
	}

	// ── Validate target exists ────────────────────────────────────────────────
	targets, err := nk.UsersGetId(ctx, []string{req.TargetUserID}, nil)
	if err != nil || len(targets) == 0 {
		logger.WithFields(map[string]interface{}{
			"sender": senderID,
			"target": req.TargetUserID,
		}).Warn("send_game_invite: target user not found")
		return "", blockerrors.ErrInvalidInviteTarget
	}

	// ── Validate sender is actually in the claimed match ─────────────────────
	// Prevents spoofed invites with arbitrary match IDs.
	// Reads sender's active_match storage (same collection used by submit_match_result).
	// Soft validation: if storage read fails, we allow the invite through rather than
	// blocking a legitimate player due to a transient storage error.
	// Design: no friendship gate — inviting a non-friend (e.g. post-match) is intentional.
	activeMatchObjs, readErr := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: storageCollectionActiveMatch,
		Key:        storageKeyCurrentMatch,
		UserID:     senderID,
	}})
	if readErr == nil && len(activeMatchObjs) > 0 {
		var activeMatch ActiveMatch
		if json.Unmarshal([]byte(activeMatchObjs[0].Value), &activeMatch) == nil {
			if activeMatch.MatchID != req.MatchID {
				logger.WithFields(map[string]interface{}{
					"sender":           senderID,
					"claimed_match_id": req.MatchID,
					"active_match_id":  activeMatch.MatchID,
				}).Warn("send_game_invite: match_id mismatch — sender not in claimed match")
				return "", blockerrors.ErrInviteMissingMatch
			}
		}
	}

	// ── Deduplicate: purge prior challenge notifications from this sender → target ─
	// Each challenge attempt generates a fresh match_id UUID, so client-side
	// match_id deduplication misses stale notifications. Delete them server-side
	// before sending the new one so the target only ever sees one challenge per sender.
	if existing, _, listErr := nk.NotificationsList(ctx, req.TargetUserID, 20, ""); listErr == nil {
		var staleIDs []string
		for _, notif := range existing {
			if int(notif.Code) != notify.CodeSocial || notif.SenderId != senderID {
				continue
			}
			var nc map[string]interface{}
			if json.Unmarshal([]byte(notif.Content), &nc) == nil {
				if _, hasMID := nc["match_id"]; hasMID {
					staleIDs = append(staleIDs, notif.Id)
				}
			}
		}
		if len(staleIDs) > 0 {
			if delErr := nk.NotificationsDeleteId(ctx, req.TargetUserID, staleIDs); delErr != nil {
				logger.WithFields(map[string]interface{}{
					"sender": senderID, "target": req.TargetUserID,
				}).Warn("send_game_invite: failed to clear stale challenge notifications")
			} else {
				logger.WithFields(map[string]interface{}{
					"sender": senderID, "target": req.TargetUserID, "cleared": len(staleIDs),
				}).Info("send_game_invite: cleared stale challenge notifications")
			}
		}
	}

	// ── Resolve sender display name ───────────────────────────────────────────
	senderName := senderID
	senders, err := nk.UsersGetId(ctx, []string{senderID}, nil)
	if err == nil && len(senders) > 0 {
		if senders[0].DisplayName != "" {
			senderName = senders[0].DisplayName
		} else if senders[0].Username != "" {
			senderName = senders[0].Username
		}
	}

	// ── Send notification ─────────────────────────────────────────────────────
	// Uses CodeSocial = 5 — matching client ServerNotifyCode.Social.
	// CROSS-REPO CONTRACT: must match blockjitsu/scripts/services/notify/ServerNotifyTypes.cs
	// Content parsed in InviteService.OnServerNotification on the client.
	content := map[string]interface{}{
		"match_id":    req.MatchID,
		"sender_id":   senderID,
		"sender_name": senderName,
		"action":      "join_match",
	}

	if err := nk.NotificationSend(
		ctx,
		req.TargetUserID,
		senderName+" challenged you!",
		content,
		notify.CodeSocial, // 5
		senderID,          // non-empty → InviteService distinguishes from system toasts
		true,              // persistent — survives to inbox until client deletes it
	); err != nil {
		logger.WithFields(map[string]interface{}{
			"sender":   senderID,
			"target":   req.TargetUserID,
			"match_id": req.MatchID,
			"error":    err.Error(),
		}).Error("send_game_invite: notification send failed")
		return "", blockerrors.ErrInternalError
	}

	logger.WithFields(map[string]interface{}{
		"sender":   senderID,
		"target":   req.TargetUserID,
		"match_id": req.MatchID,
	}).Info("send_game_invite: invite sent")

	return `{"success": true}`, nil
}

// CancelInviteRequest is the payload for the cancel_game_invite RPC.
// CROSS-REPO CONTRACT: field names must match client CancelGameInvitePayload JSON tags.
// Client: scripts/models/SocialTypes.cs → CancelGameInvitePayload("target_id", "match_id")
type CancelInviteRequest struct {
	TargetUserID string `json:"target_id"`
	MatchID      string `json:"match_id"`
}

// RpcCancelGameInvite notifies the target player that a previously sent challenge
// has been revoked by the sender.
//
// Registered as: "cancel_game_invite"
func RpcCancelGameInvite(
	ctx context.Context,
	logger runtime.Logger,
	db *sql.DB,
	nk runtime.NakamaModule,
	payload string,
) (string, error) {
	// ── Authenticate sender ───────────────────────────────────────────────────
	senderID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || senderID == "" {
		return "", blockerrors.ErrNoUserIdFound
	}

	// ── Parse request ─────────────────────────────────────────────────────────
	var req CancelInviteRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.WithFields(map[string]interface{}{
			"sender":  senderID,
			"payload": payload,
			"error":   err.Error(),
		}).Error("cancel_game_invite: unmarshal failed")
		return "", blockerrors.ErrUnmarshal
	}

	if req.TargetUserID == "" {
		return "", blockerrors.ErrInvalidInviteTarget
	}
	if req.MatchID == "" {
		return "", blockerrors.ErrInviteMissingMatch
	}

	// ── Purge the original challenge notification from target's inbox ─────────
	// The challenge was sent as a persistent notification. Delete it server-side
	// so it won't be hydrated on next login. Also send a cancel notification
	// in case the target is currently online (the notification replay + delete
	// pipeline will handle offline targets on their next connect).
	if existing, _, listErr := nk.NotificationsList(ctx, req.TargetUserID, 20, ""); listErr == nil {
		var toDelete []string
		for _, notif := range existing {
			if notif.SenderId != senderID {
				continue
			}
			var nc map[string]interface{}
			if json.Unmarshal([]byte(notif.Content), &nc) == nil {
				if mid, hasMID := nc["match_id"].(string); hasMID && mid == req.MatchID {
					toDelete = append(toDelete, notif.Id)
				}
			}
		}
		if len(toDelete) > 0 {
			if delErr := nk.NotificationsDeleteId(ctx, req.TargetUserID, toDelete); delErr != nil {
				logger.WithFields(map[string]interface{}{
					"sender": senderID, "target": req.TargetUserID,
				}).Warn("cancel_game_invite: failed to delete original challenge notification")
			}
		}
	}

	// ── Resolve sender display name ───────────────────────────────────────────
	senderName := senderID
	if senders, err := nk.UsersGetId(ctx, []string{senderID}, nil); err == nil && len(senders) > 0 {
		if senders[0].DisplayName != "" {
			senderName = senders[0].DisplayName
		} else if senders[0].Username != "" {
			senderName = senders[0].Username
		}
	}

	// ── Send cancellation notification ────────────────────────────────────────
	// Non-persistent — it only matters if the target is currently online.
	// The action "cancel_invite" is handled by InboxService.OnLiveNotification
	// to silently remove the matching inbox item.
	content := map[string]interface{}{
		"match_id":    req.MatchID,
		"sender_id":   senderID,
		"sender_name": senderName,
		"action":      "cancel_invite",
	}

	if err := nk.NotificationSend(
		ctx,
		req.TargetUserID,
		senderName+" cancelled a challenge.",
		content,
		notify.CodeSocial, // 5 — same channel, filtered by action
		senderID,
		false, // NOT persistent — don't hoard cancellation notices
	); err != nil {
		logger.WithFields(map[string]interface{}{
			"sender":   senderID,
			"target":   req.TargetUserID,
			"match_id": req.MatchID,
			"error":    err.Error(),
		}).Error("cancel_game_invite: cancellation notification send failed")
		return "", blockerrors.ErrInternalError
	}

	logger.WithFields(map[string]interface{}{
		"sender":   senderID,
		"target":   req.TargetUserID,
		"match_id": req.MatchID,
	}).Info("cancel_game_invite: cancellation sent")

	return `{"success": true}`, nil
}

// RpcDeclineGameInvite notifies the sender that their challenge was declined by the target.
// Initiated by the recipient (who received the invite). Sends a non-persistent notification
// with action "decline_invite" back to the original sender.
//
// Registered as: "decline_game_invite"
func RpcDeclineGameInvite(
	ctx context.Context,
	logger runtime.Logger,
	db *sql.DB,
	nk runtime.NakamaModule,
	payload string,
) (string, error) {
	// ── Authenticate decliner (the target who received the invite) ──────────
	declinerID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || declinerID == "" {
		return "", blockerrors.ErrNoUserIdFound
	}

	// ── Parse request ─────────────────────────────────────────────────────────
	var req CancelInviteRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.WithFields(map[string]interface{}{
			"decliner": declinerID,
			"payload":  payload,
			"error":    err.Error(),
		}).Error("decline_game_invite: unmarshal failed")
		return "", blockerrors.ErrUnmarshal
	}

	if req.TargetUserID == "" {
		return "", blockerrors.ErrInvalidInviteTarget
	}
	if req.MatchID == "" {
		return "", blockerrors.ErrInviteMissingMatch
	}

	// ── TargetUserID is the ORIGINAL SENDER ──────────────────────────────────
	senderID := req.TargetUserID

	// ── Purge the original challenge notification from decliner's own inbox ──
	if existing, _, listErr := nk.NotificationsList(ctx, declinerID, 20, ""); listErr == nil {
		var toDelete []string
		for _, notif := range existing {
			if notif.SenderId != senderID {
				continue
			}
			var nc map[string]interface{}
			if json.Unmarshal([]byte(notif.Content), &nc) == nil {
				if mid, hasMID := nc["match_id"].(string); hasMID && mid == req.MatchID {
					toDelete = append(toDelete, notif.Id)
				}
			}
		}
		if len(toDelete) > 0 {
			if delErr := nk.NotificationsDeleteId(ctx, declinerID, toDelete); delErr != nil {
				logger.WithFields(map[string]interface{}{
					"decliner": declinerID, "sender": senderID,
				}).Warn("decline_game_invite: failed to delete challenge from decliner inbox")
			}
		}
	}

	// ── Resolve decliner display name ────────────────────────────────────────
	declinerName := declinerID
	if users, err := nk.UsersGetId(ctx, []string{declinerID}, nil); err == nil && len(users) > 0 {
		if users[0].DisplayName != "" {
			declinerName = users[0].DisplayName
		} else if users[0].Username != "" {
			declinerName = users[0].Username
		}
	}

	// ── Send decline notification back to the ORIGINAL SENDER ────────────────
	content := map[string]interface{}{
		"match_id":    req.MatchID,
		"sender_id":   declinerID,
		"sender_name": declinerName,
		"action":      "decline_invite",
	}

	if err := nk.NotificationSend(
		ctx,
		senderID, // notify the original sender
		declinerName+" declined your challenge.",
		content,
		notify.CodeSocial, // 5
		declinerID,
		false, // NOT persistent — only matters if sender is online
	); err != nil {
		logger.WithFields(map[string]interface{}{
			"decliner": declinerID,
			"sender":   senderID,
			"match_id": req.MatchID,
			"error":    err.Error(),
		}).Error("decline_game_invite: notification send failed")
		return "", blockerrors.ErrInternalError
	}

	logger.WithFields(map[string]interface{}{
		"decliner": declinerID,
		"sender":   senderID,
		"match_id": req.MatchID,
	}).Info("decline_game_invite: decline sent")

	return `{"success": true}`, nil
}
