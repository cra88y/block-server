package items

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

type stubLogger struct{}

func (s *stubLogger) Debug(string, ...interface{})                    {}
func (s *stubLogger) Info(string, ...interface{})                     {}
func (s *stubLogger) Warn(string, ...interface{})                     {}
func (s *stubLogger) Error(string, ...interface{})                    {}
func (s *stubLogger) WithField(string, interface{}) runtime.Logger    { return s }
func (s *stubLogger) WithFields(map[string]interface{}) runtime.Logger { return s }
func (s *stubLogger) Fields() map[string]interface{}                  { return nil }

func buildTestChainAndSignJWS(t *testing.T, payloadObj interface{}) (string, string) {
	t.Helper()
	now := time.Now().Add(-time.Hour)

	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Root CA"},
		NotBefore:             now,
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	rootCert, _ := x509.ParseCertificate(rootDER)

	interKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	interTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Test Inter CA"},
		NotBefore:             now,
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, rootCert, &interKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	interCert, _ := x509.ParseCertificate(interDER)

	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{CommonName: "Test Leaf"},
		NotBefore:             now,
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, interCert, &leafKey.PublicKey, interKey)
	if err != nil {
		t.Fatal(err)
	}

	hdrBytes, _ := json.Marshal(appleJWSHeader{
		Alg: "ES256",
		X5c: []string{
			base64.StdEncoding.EncodeToString(leafDER),
			base64.StdEncoding.EncodeToString(interDER),
			base64.StdEncoding.EncodeToString(rootDER),
		},
	})
	payBytes, _ := json.Marshal(payloadObj)
	signingInput := base64.RawURLEncoding.EncodeToString(hdrBytes) + "." + base64.RawURLEncoding.EncodeToString(payBytes)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, leafKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	rBytes, sBytes := r.Bytes(), s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)

	rootFP := sha256.Sum256(rootDER)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), hex.EncodeToString(rootFP[:])
}

func TestVerifyAppleJWS(t *testing.T) {
	jws, rootFP := buildTestChainAndSignJWS(t, map[string]string{
		"notificationType": "REFUND",
	})

	var out map[string]string
	if err := verifyAppleJWSWithRoot(jws, rootFP, time.Now(), &out); err != nil {
		t.Fatalf("expected valid JWS, got %v", err)
	}
	if out["notificationType"] != "REFUND" {
		t.Fatalf("unexpected decoded payload: %v", out)
	}

	// Wrong root pin must fail
	if err := verifyAppleJWSWithRoot(jws, strings.Repeat("0", 64), time.Now(), &out); err == nil {
		t.Fatal("expected root pin mismatch failure")
	}

	// Tampered payload must fail signature check
	parts := strings.Split(jws, ".")
	tamperedPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"notificationType":"CONSUMPTION_REQUEST"}`))
	tampered := parts[0] + "." + tamperedPayload + "." + parts[2]
	if err := verifyAppleJWSWithRoot(tampered, rootFP, time.Now(), &out); err == nil {
		t.Fatal("expected signature verification failure on tampered payload")
	}
}

func TestHandleAppleS2SWebhookSecurityAndRefund(t *testing.T) {
	logger := &stubLogger{}

	// 1. Authenticated client JWT session must be rejected before JSON/crypto parsing
	jwtCtx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "player-user-id")
	if _, err := HandleAppleS2SWebhook(jwtCtx, logger, nil, nil, `{"signedPayload":"a.b.c"}`); err == nil {
		t.Fatal("expected client JWT caller to be rejected")
	}

	// 2. Missing signedPayload must be rejected
	if _, err := HandleAppleS2SWebhook(context.Background(), logger, nil, nil, `{}`); err == nil {
		t.Fatal("expected rejection when signedPayload is missing")
	}

	// 3. Unverified JWS against real Apple Root CA G3 pin must be rejected
	selfSignedJWS, _ := buildTestChainAndSignJWS(t, map[string]string{"notificationType": "REFUND"})
	selfSignedBody, _ := json.Marshal(map[string]string{"signedPayload": selfSignedJWS})
	if _, err := HandleAppleS2SWebhook(context.Background(), logger, nil, nil, string(selfSignedBody)); err == nil {
		t.Fatal("expected unpinned test root JWS to be rejected by HandleAppleS2SWebhook")
	}

	// 3. Verified REFUND chain triggers revokeFn cleanly with BundleId check
	txJWS, rootFP := buildTestChainAndSignJWS(t, map[string]string{
		"appAccountToken":       "user-123",
		"bundleId":              "com.wrong.bundle",
		"originalTransactionId": "orig-tx-999",
	})
	outerJWS, _ := buildTestChainAndSignJWS(t, map[string]interface{}{
		"notificationType": "REFUND",
		"data":             map[string]string{"signedTransactionInfo": txJWS},
	})
	// Outer uses its own test chain; verifyFn allows explicit test root or matching root
	verifyEither := func(jws string, dst interface{}) error {
		if err := verifyAppleJWSWithRoot(jws, rootFP, time.Now(), dst); err == nil {
			return nil
		}
		// Decode outer cert root hash from this test helper's chain
		parts := strings.Split(outerJWS, ".")
		var hdr appleJWSHeader
		hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
		_ = json.Unmarshal(hb, &hdr)
		der, _ := base64.StdEncoding.DecodeString(hdr.X5c[len(hdr.X5c)-1])
		fp := sha256.Sum256(der)
		return verifyAppleJWSWithRoot(jws, hex.EncodeToString(fp[:]), time.Now(), dst)
	}
	body, _ := json.Marshal(map[string]string{"signedPayload": outerJWS})

	t.Setenv("APPLE_BUNDLE_ID", "com.blockjitsu.app")
	var revokedUser, revokedTx string
	if _, err := handleAppleS2SWebhook(context.Background(), logger, string(body), verifyEither, func(u, tx string) error {
		revokedUser, revokedTx = u, tx
		return nil
	}); err == nil || revokedTx != "" {
		t.Fatalf("expected bundleId mismatch to abort revocation, got err=%v revokedTx=%q", err, revokedTx)
	}

	t.Setenv("APPLE_BUNDLE_ID", "com.wrong.bundle")
	if res, err := handleAppleS2SWebhook(context.Background(), logger, string(body), verifyEither, func(u, tx string) error {
		revokedUser, revokedTx = u, tx
		return nil
	}); err != nil || !strings.Contains(res, `"success":true`) {
		t.Fatalf("expected valid refund invocation, got res=%q err=%v", res, err)
	}
	if revokedUser != "user-123" || revokedTx != "orig-tx-999" {
		t.Fatalf("unexpected revoke args: user=%q tx=%q", revokedUser, revokedTx)
	}
}
