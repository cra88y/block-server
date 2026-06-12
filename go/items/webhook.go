package items

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

// HandleAppleS2SWebhook processes incoming Apple App Store Server Notifications (V2).
// Registered in main.go via initializer.RegisterRpc.
func HandleAppleS2SWebhook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var req struct {
		SignedPayload string `json:"signedPayload"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Error("Failed to unmarshal Apple webhook payload: %v", err)
		return "", err
	}

	if req.SignedPayload == "" {
		return "", runtime.NewError("missing signedPayload", 400)
	}

	parts := strings.Split(req.SignedPayload, ".")
	if len(parts) != 3 {
		return "", runtime.NewError("invalid JWS", 400)
	}

	decodedBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}

	var notification struct {
		NotificationType string `json:"notificationType"`
		Data             struct {
			SignedTransactionInfo string `json:"signedTransactionInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(decodedBytes, &notification); err != nil {
		return "", err
	}

	if notification.NotificationType != "REFUND" {
		return `{"success":true}`, nil
	}

	txParts := strings.Split(notification.Data.SignedTransactionInfo, ".")
	if len(txParts) != 3 {
		return "", runtime.NewError("invalid transaction JWS", 400)
	}

	txBytes, err := base64.RawURLEncoding.DecodeString(txParts[1])
	if err != nil {
		return "", err
	}

	var txInfo struct {
		AppAccountToken       string `json:"appAccountToken"`
		OriginalTransactionId string `json:"originalTransactionId"`
	}
	if err := json.Unmarshal(txBytes, &txInfo); err != nil {
		return "", err
	}

	if txInfo.AppAccountToken == "" || txInfo.OriginalTransactionId == "" {
		logger.Warn("Apple webhook refund missing required identifiers. origTx=%s", txInfo.OriginalTransactionId)
		return `{"success":true}`, nil
	}

	// TODO: Verify JWS ECDSA signatures using Apple's Root CA

	revokeReq := struct {
		OriginalTransactionId string `json:"original_transaction_id"`
		RevocationReason      string `json:"revocation_reason"`
		UserId                string `json:"user_id"`
	}{
		OriginalTransactionId: txInfo.OriginalTransactionId,
		RevocationReason:      "apple_s2s_refund",
		UserId:                txInfo.AppAccountToken,
	}

	revokeBytes, _ := json.Marshal(revokeReq)

	_, err = RpcRevokeIAPPurchase(ctx, logger, db, nk, string(revokeBytes))
	if err != nil {
		return "", err
	}

	logger.Info("Processed Apple S2S Refund for origTx=%s user=%s", txInfo.OriginalTransactionId, txInfo.AppAccountToken)
	return `{"success":true}`, nil
}