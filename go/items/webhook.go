package items

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Apple Root CA - G3 SHA-256 fingerprint (https://www.apple.com/certificateauthority/AppleRootCA-G3.cer).
const appleRootCAG3Fingerprint = "63343abfb89a6a03ebb57e9b3f5fa7be7c4f5c756f3017b3a8c488c3653e9179"

type appleJWSHeader struct {
	Alg string   `json:"alg"`
	X5c []string `json:"x5c"`
}

func verifyAppleJWS(jws string, dst interface{}) error {
	return verifyAppleJWSWithRoot(jws, appleRootCAG3Fingerprint, time.Now(), dst)
}

func verifyAppleJWSWithRoot(jws string, expectedRootFP string, verifyTime time.Time, dst interface{}) error {
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		return fmt.Errorf("invalid JWS format")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("decode JWS header: %w", err)
	}
	var hdr appleJWSHeader
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return fmt.Errorf("unmarshal JWS header: %w", err)
	}
	if hdr.Alg != "ES256" {
		return fmt.Errorf("unexpected JWS alg: %s", hdr.Alg)
	}
	if len(hdr.X5c) < 2 {
		return fmt.Errorf("insufficient x5c chain length: %d", len(hdr.X5c))
	}

	certs := make([]*x509.Certificate, len(hdr.X5c))
	for i, b64 := range hdr.X5c {
		der, decErr := base64.StdEncoding.DecodeString(b64)
		if decErr != nil {
			return fmt.Errorf("decode x5c[%d]: %w", i, decErr)
		}
		cert, parseErr := x509.ParseCertificate(der)
		if parseErr != nil {
			return fmt.Errorf("parse x5c[%d]: %w", i, parseErr)
		}
		certs[i] = cert
	}

	rootCert := certs[len(certs)-1]
	rootSum := sha256.Sum256(rootCert.Raw)
	if hex.EncodeToString(rootSum[:]) != expectedRootFP {
		return fmt.Errorf("x5c root does not match expected Apple Root CA G3 pin")
	}

	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	intermediates := x509.NewCertPool()
	for _, c := range certs[1 : len(certs)-1] {
		intermediates.AddCert(c)
	}

	if _, err := certs[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   verifyTime,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return fmt.Errorf("x5c chain verification failed: %w", err)
	}

	pubKey, ok := certs[0].PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("leaf certificate public key is not ECDSA")
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("decode JWS signature: %w", err)
	}
	if len(sigBytes) != 64 {
		return fmt.Errorf("unexpected ES256 signature length: %d", len(sigBytes))
	}

	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])
	if !ecdsa.Verify(pubKey, digest[:], r, s) {
		return fmt.Errorf("ES256 signature verification failed")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("decode JWS payload: %w", err)
	}
	if err := json.Unmarshal(payloadBytes, dst); err != nil {
		return fmt.Errorf("unmarshal JWS payload: %w", err)
	}
	return nil
}

// HandleAppleS2SWebhook processes incoming Apple App Store Server Notifications (V2).
// Registered in main.go via initializer.RegisterRpc (requires ?http_key=...&unwrap; client user sessions are rejected).
func HandleAppleS2SWebhook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	return handleAppleS2SWebhook(ctx, logger, payload, verifyAppleJWS, func(userID, origTxID string) error {
		return RevokeIAPPurchaseInternal(ctx, logger, db, nk, userID, origTxID, "apple_s2s_refund")
	})
}

func handleAppleS2SWebhook(
	ctx context.Context,
	logger runtime.Logger,
	payload string,
	verifyFn func(string, interface{}) error,
	revokeFn func(userID, origTxID string) error,
) (string, error) {
	if uid, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); ok && uid != "" {
		return "", runtime.NewError("forbidden", 7)
	}

	var req struct {
		SignedPayload string `json:"signedPayload"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		logger.Error("Failed to unmarshal Apple webhook payload: %v", err)
		return "", runtime.NewError("invalid JSON", 3)
	}

	if req.SignedPayload == "" {
		return "", runtime.NewError("missing signedPayload", 3)
	}

	var notification struct {
		NotificationType string `json:"notificationType"`
		Data             struct {
			SignedTransactionInfo string `json:"signedTransactionInfo"`
		} `json:"data"`
	}
	if err := verifyFn(req.SignedPayload, &notification); err != nil {
		logger.Warn("Apple webhook outer JWS verification failed: %v", err)
		return "", runtime.NewError("invalid JWS signature", 3)
	}

	if notification.NotificationType != "REFUND" {
		return `{"success":true}`, nil
	}

	var txInfo struct {
		AppAccountToken       string `json:"appAccountToken"`
		BundleId              string `json:"bundleId"`
		OriginalTransactionId string `json:"originalTransactionId"`
	}
	if err := verifyFn(notification.Data.SignedTransactionInfo, &txInfo); err != nil {
		logger.Warn("Apple webhook transaction JWS verification failed: %v", err)
		return "", runtime.NewError("invalid transaction JWS signature", 3)
	}

	if expectedBundleID := os.Getenv("APPLE_BUNDLE_ID"); expectedBundleID != "" && !strings.EqualFold(txInfo.BundleId, expectedBundleID) {
		logger.Warn("Apple webhook bundleId mismatch: got=%s expected=%s", txInfo.BundleId, expectedBundleID)
		return "", runtime.NewError("bundleId mismatch", 3)
	}

	if txInfo.AppAccountToken == "" || txInfo.OriginalTransactionId == "" {
		logger.Warn("Apple webhook refund missing required identifiers. origTx=%s", txInfo.OriginalTransactionId)
		return `{"success":true}`, nil
	}

	if err := revokeFn(txInfo.AppAccountToken, txInfo.OriginalTransactionId); err != nil {
		return "", err
	}

	logger.Info("Processed Apple S2S Refund for origTx=%s user=%s", txInfo.OriginalTransactionId, txInfo.AppAccountToken)
	return `{"success":true}`, nil
}