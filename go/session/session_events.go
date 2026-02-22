// Package session handles session-start grants and single-device enforcement.
package session

import (
	"context"
	"database/sql"
	"time"

	"block-server/items"
	"block-server/notify"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	streamModeNotification = 0
)

// RegisterSessionEvents wires session lifecycle hooks. Call once from InitModule.
func RegisterSessionEvents(db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	if err := initializer.RegisterEventSessionStart(eventSessionStartFunc(nk)); err != nil {
		return err
	}
	if err := initializer.RegisterEventSessionEnd(eventSessionEndFunc(db)); err != nil {
		return err
	}

	return nil
}

// eventSessionEndFunc stamps last_online_time_unix on disconnect. 1s deadline guards against reconnect storms.
func eventSessionEndFunc(db *sql.DB) func(context.Context, runtime.Logger, *api.Event) {
	return func(ctx context.Context, logger runtime.Logger, evt *api.Event) {
		userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
		if !ok {
			logger.Error("context did not contain user ID.")
			return
		}

		// Restrict the time allowed with the DB operation so we can fail fast in a stampeding herd scenario.
		ctx2, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()
		query := `
UPDATE
    users AS u
SET
    metadata
        = u.metadata
        || jsonb_build_object('last_online_time_unix', extract('epoch' FROM now())::BIGINT)
WHERE
    id = $1;
`
		_, err := db.ExecContext(ctx2, query, userID)
		if err != nil && err != context.DeadlineExceeded {
			logger.WithField("err", err).Error("db.ExecContext last online update error.")
			return
		}
	}
}

// eventSessionStartFunc claims daily drops, verifies progression, and kicks duplicate sessions.
func eventSessionStartFunc(nk runtime.NakamaModule) func(context.Context, runtime.Logger, *api.Event) {
	return func(ctx context.Context, logger runtime.Logger, evt *api.Event) {

		userID, err := items.GetUserIDFromContext(ctx, logger)
		if err != nil {
			logger.WithField("err", err).Error("Error getting UserID")
			return
		}
		if err := items.TryClaimDailyDrops(ctx, logger, nk); err != nil {
			logger.WithField("err", err).Warn("daily drops claim failed")
		}

		if err := items.GiveAllItemsToUser(ctx, nk, logger, userID); err != nil {
			logger.WithField("err", err).Error("failed to give all items to user on session start")
		}

		report, err := items.VerifyAndFixUserProgression(ctx, nk, logger, userID)
		if err != nil {
			logger.WithField("err", err).Error("progression verification failed")
		} else if report.TotalFixed > 0 {
			logger.WithField("report", report).Info("progression verification completed with repairs")
		}

		sessionID, ok := ctx.Value(runtime.RUNTIME_CTX_SESSION_ID).(string)
		if !ok {
			logger.Error("context did not contain session ID.")
			return
		}

		// Fetch all live presences for this user on their private notification stream.
		presences, err := nk.StreamUserList(streamModeNotification, userID, "", "", true, true)
		if err != nil {
			logger.WithField("err", err).Error("nk.StreamUserList error.")
			return
		}

		notifications := []*runtime.NotificationSend{
			{
				Code: notify.CodeDevice,
				Content: map[string]interface{}{
					"kicked_by": sessionID,
				},
				Persistent: false,
				Sender:     userID,
				Subject:    "Another device is already in use.",
				UserID:     userID,
			},
		}
		for _, presence := range presences {
			if presence.GetUserId() == userID && presence.GetSessionId() == sessionID {
				// Ignore our current socket connection.
				continue
			}

			// Use closure to ensure cancel is always called
			func() {
				ctx2, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()

				if err := nk.NotificationsSend(ctx2, notifications); err != nil {
					logger.WithField("err", err).Error("nk.NotificationsSend error.")
					return
				}

				// Force disconnect the socket for the user's other game client.
				if err := nk.SessionDisconnect(ctx2, presence.GetSessionId()); err != nil {
					logger.WithField("err", err).Error("nk.SessionDisconnect error.")
				}
			}()
		}
	}
}