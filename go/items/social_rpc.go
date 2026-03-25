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
//   Caller (inviter) creates/joins a private Nakama match first,
//   then calls this RPC with the match ID.
//   Target receives a CodeSocial (5) notification → InviteService on client.
//   Target calls JoinMatchById(matchId) to accept.
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
