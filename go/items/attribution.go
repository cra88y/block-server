package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"block-server/errors"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionAttributionQueue = "attribution_queue"
	StorageCollectionUserAttribution  = "user_attribution"
	StorageKeyAttributionProfile      = "profile"
	maxAttributionTokenLength         = 4096
	minAttributionTokenLength         = 10
	maxAttributionAttempts            = 5
	attributionTtlSeconds             = 86400 // 24 hours
	attributionWorkerCount            = 8
	attributionQueueSize              = 1024
)

var appleHttpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	},
}

var (
	attributionQueueChan = make(chan AttributionQueueItem, attributionQueueSize)
	globalLogger         runtime.Logger
	globalNk             runtime.NakamaModule
)

type AttributionQueueItem struct {
	UserID    string `json:"user_id"`
	Token     string `json:"token"`
	Platform  string `json:"platform"`
	Attempts  int    `json:"attempts"`
	CreatedAt int64  `json:"created_at"`
}

type UserAttributionRecord struct {
	Status         string `json:"status"` // "attributed", "organic", "expired", "invalid"
	CampaignID     int64  `json:"campaign_id,omitempty"`
	AdGroupID      int64  `json:"ad_group_id,omitempty"`
	KeywordID      int64  `json:"keyword_id,omitempty"`
	AdID           int64  `json:"ad_id,omitempty"`
	OrgID          int64  `json:"org_id,omitempty"`
	ConversionType string `json:"conversion_type,omitempty"`
	CountryCode    string `json:"country_code,omitempty"`
	ResolvedAt     int64  `json:"resolved_at"`
}

type SubmitAttributionRequest struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
}

type AppleAttributionResponse struct {
	Attribution     bool   `json:"attribution"`
	OrgID           int64  `json:"orgId"`
	CampaignID      int64  `json:"campaignId"`
	ConversionType  string `json:"conversionType"`
	ClickDate       string `json:"clickDate"`
	AdGroupID       int64  `json:"adGroupId"`
	CountryOrRegion string `json:"countryOrRegion"`
	KeywordID       int64  `json:"keywordId"`
	AdID            int64  `json:"adId"`
}

// StartAttributionWorkers initializes dedicated worker goroutines decoupled from the ephemeral InitModule context.
func StartAttributionWorkers(logger runtime.Logger, nk runtime.NakamaModule) {
	globalLogger = logger
	globalNk = nk
	bgCtx := context.Background()
	for i := 0; i < attributionWorkerCount; i++ {
		go func(workerID int) {
			for item := range attributionQueueChan {
				processAttributionItem(bgCtx, logger, nk, item)
			}
		}(i)
	}
}

// InitAttributionQueue performs a one-shot paginated recovery sweep at startup to repopulate in-memory workers.
func InitAttributionQueue(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) {
	cursor := ""
	recoveredCount := 0
	for {
		objects, nextCursor, err := nk.StorageList(ctx, "", "", StorageCollectionAttributionQueue, 100, cursor)
		if err != nil {
			logger.Error("Failed to list attribution_queue on boot: %v", err)
			break
		}
		if len(objects) == 0 {
			break
		}

		for _, obj := range objects {
			var item AttributionQueueItem
			if err := json.Unmarshal([]byte(obj.Value), &item); err == nil {
				DispatchAttributionTask(item)
				recoveredCount++
			}
		}

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
		time.Sleep(20 * time.Millisecond) // Paces recovery to avoid worker channel saturation
	}

	if recoveredCount > 0 {
		logger.Info("Recovered %d pending attribution tasks into worker queue.", recoveredCount)
	}
}

// DispatchAttributionTask non-blockingly pushes a task to the worker channel,
// or schedules a delayed re-dispatch if the channel buffer is saturated under a viral surge.
func DispatchAttributionTask(task AttributionQueueItem) {
	select {
	case attributionQueueChan <- task:
	default:
		// Saturated channel under traffic spike. Item is already durable on disk in attribution_queue.
		time.AfterFunc(15*time.Second, func() {
			select {
			case attributionQueueChan <- task:
			default:
				// Channel still full; item remains on disk for next recovery or retry cycle
			}
		})
	}
}

func RpcSubmitAttributionToken(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", errors.ErrNoUserIdFound
	}

	var req SubmitAttributionRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Warn("Malformed submit_attribution_token payload: %v", err)
		return "", errors.ErrUnmarshal
	}

	if err := EnqueueAttributionToken(ctx, logger, nk, userID, req.Token, req.Platform); err != nil {
		return "", err
	}

	return `{"success": true}`, nil
}

// EnqueueAttributionToken handles deduplication, durable persistence, and non-blocking worker dispatch.
func EnqueueAttributionToken(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, token string, platform string) error {
	trimmedToken := strings.TrimSpace(token)
	if len(trimmedToken) < minAttributionTokenLength || len(trimmedToken) > maxAttributionTokenLength {
		return errors.ErrInvalidInput
	}
	if platform == "" {
		platform = "ios"
	}

	// Idempotency: skip if already resolved or pending
	existing, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: StorageCollectionUserAttribution, Key: StorageKeyAttributionProfile, UserID: userID},
		{Collection: StorageCollectionAttributionQueue, Key: userID, UserID: userID},
	})
	if err == nil && len(existing) > 0 {
		return nil
	}

	queueItem := AttributionQueueItem{
		UserID:    userID,
		Token:     trimmedToken,
		Platform:  platform,
		Attempts:  0,
		CreatedAt: time.Now().Unix(),
	}

	itemBytes, err := json.Marshal(queueItem)
	if err != nil {
		return errors.ErrMarshal
	}

	// Durable persistence in Nakama Storage
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionAttributionQueue,
			Key:             userID,
			UserID:          userID,
			Value:           string(itemBytes),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	})
	if err != nil {
		logger.Error("Failed to write to attribution_queue for user %s: %v", userID, err)
		return errors.ErrCouldNotWriteStorage
	}

	DispatchAttributionTask(queueItem)
	return nil
}

func processAttributionItem(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, item AttributionQueueItem) {
	now := time.Now().Unix()
	if now-item.CreatedAt > attributionTtlSeconds {
		finalizeAttribution(ctx, logger, nk, item.UserID, UserAttributionRecord{
			Status:     "expired",
			ResolvedAt: now,
		})
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api-adservices.apple.com/api/v1/", strings.NewReader(item.Token))
	if err != nil {
		logger.Error("Failed to build Apple AdServices request for %s: %v", item.UserID, err)
		return
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := appleHttpClient.Do(req)
	if err != nil {
		logger.Warn("Network error calling Apple AdServices for %s: %v", item.UserID, err)
		item.Attempts++
		if item.Attempts < maxAttributionAttempts {
			persistQueueItem(ctx, logger, nk, item)
			scheduleRetry(item)
		} else {
			finalizeAttribution(ctx, logger, nk, item.UserID, UserAttributionRecord{
				Status:     "failed",
				ResolvedAt: now,
			})
		}
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var appleResp AppleAttributionResponse
		if err := json.Unmarshal(bodyBytes, &appleResp); err != nil {
			logger.Error("Failed to unmarshal Apple attribution response for %s: %v", item.UserID, err)
			return
		}

		if appleResp.Attribution {
			record := UserAttributionRecord{
				Status:         "attributed",
				CampaignID:     appleResp.CampaignID,
				AdGroupID:      appleResp.AdGroupID,
				KeywordID:      appleResp.KeywordID,
				AdID:           appleResp.AdID,
				OrgID:          appleResp.OrgID,
				ConversionType: appleResp.ConversionType,
				CountryCode:    appleResp.CountryOrRegion,
				ResolvedAt:     now,
			}
			finalizeAttribution(ctx, logger, nk, item.UserID, record)
			EmitServerTelemetry(logger, item.UserID, "attribution_resolved", record)
		} else {
			record := UserAttributionRecord{
				Status:     "organic",
				ResolvedAt: now,
			}
			finalizeAttribution(ctx, logger, nk, item.UserID, record)
			EmitServerTelemetry(logger, item.UserID, "attribution_organic", record)
		}

	case http.StatusNotFound:
		// Apple indexing lag (can take up to 24h): persist attempt count and reschedule
		item.Attempts++
		if item.Attempts < maxAttributionAttempts {
			persistQueueItem(ctx, logger, nk, item)
			scheduleRetry(item)
		} else {
			finalizeAttribution(ctx, logger, nk, item.UserID, UserAttributionRecord{
				Status:     "expired",
				ResolvedAt: now,
			})
		}

	case http.StatusBadRequest:
		finalizeAttribution(ctx, logger, nk, item.UserID, UserAttributionRecord{
			Status:     "invalid",
			ResolvedAt: now,
		})

	default:
		logger.Warn("Apple AdServices HTTP %d for user %s: %s", resp.StatusCode, item.UserID, string(bodyBytes))
		item.Attempts++
		if item.Attempts < maxAttributionAttempts {
			persistQueueItem(ctx, logger, nk, item)
			scheduleRetry(item)
		} else {
			finalizeAttribution(ctx, logger, nk, item.UserID, UserAttributionRecord{
				Status:     "failed",
				ResolvedAt: now,
			})
		}
	}
}

// scheduleRetry uses progressive backoff (5m -> 15m -> 1h -> 4h) via non-blocking time.AfterFunc.
func scheduleRetry(item AttributionQueueItem) {
	var delay time.Duration
	switch item.Attempts {
	case 1:
		delay = 5 * time.Minute
	case 2:
		delay = 15 * time.Minute
	case 3:
		delay = 1 * time.Hour
	default:
		delay = 4 * time.Hour
	}
	time.AfterFunc(delay, func() {
		DispatchAttributionTask(item)
	})
}

func persistQueueItem(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, item AttributionQueueItem) {
	itemBytes, err := json.Marshal(item)
	if err != nil {
		return
	}
	_, _ = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionAttributionQueue,
			Key:             item.UserID,
			UserID:          item.UserID,
			Value:           string(itemBytes),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	})
}

func finalizeAttribution(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, record UserAttributionRecord) {
	recordBytes, err := json.Marshal(record)
	if err != nil {
		logger.Error("Failed to marshal UserAttributionRecord for %s: %v", userID, err)
		return
	}

	// 1. Write permanent attribution record
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionUserAttribution,
			Key:             StorageKeyAttributionProfile,
			UserID:          userID,
			Value:           string(recordBytes),
			PermissionRead:  1, // Read permission for owner
			PermissionWrite: 0,
		},
	})
	if err != nil {
		logger.Error("Failed to write user_attribution for %s: %v", userID, err)
	}

	// 2. Drain self-cleaning queue entry
	err = nk.StorageDelete(ctx, []*runtime.StorageDelete{
		{
			Collection: StorageCollectionAttributionQueue,
			Key:        userID,
			UserID:     userID,
		},
	})
	if err != nil {
		logger.Warn("Failed to delete attribution_queue item for %s: %v", userID, err)
	}
}
