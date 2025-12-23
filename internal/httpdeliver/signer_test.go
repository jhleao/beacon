package httpdeliver_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"beacon/internal/httpdeliver"

	"github.com/stretchr/testify/assert"
)

func TestSigner_Sign(t *testing.T) {
	signer := httpdeliver.NewSigner()
	secret := []byte("test-secret")
	timestamp := time.Unix(1700000000, 0)
	body := []byte(`{"event":"test"}`)

	signature := signer.Sign(secret, timestamp, body)

	// Verify manually
	expected := computeSignature(secret, timestamp, body)
	assert.Equal(t, expected, signature)
}

func TestSigner_Headers(t *testing.T) {
	signer := httpdeliver.NewSigner()
	secret := []byte("test-secret")
	body := []byte(`{"event":"test"}`)

	headers := signer.Headers(secret, body)

	assert.Contains(t, headers, "Beacon-Timestamp")
	assert.Contains(t, headers, "Beacon-Signature")

	// Timestamp should be recent
	ts := headers["Beacon-Timestamp"]
	assert.NotEmpty(t, ts)

	// Signature should be hex-encoded
	sig := headers["Beacon-Signature"]
	_, err := hex.DecodeString(sig)
	assert.NoError(t, err, "signature should be valid hex")
}

func TestSigner_Deterministic(t *testing.T) {
	signer := httpdeliver.NewSigner()
	secret := []byte("test-secret")
	timestamp := time.Unix(1700000000, 0)
	body := []byte(`{"event":"test"}`)

	sig1 := signer.Sign(secret, timestamp, body)
	sig2 := signer.Sign(secret, timestamp, body)

	assert.Equal(t, sig1, sig2, "same inputs should produce same signature")
}

func TestSigner_DifferentSecretsProduceDifferentSignatures(t *testing.T) {
	signer := httpdeliver.NewSigner()
	timestamp := time.Unix(1700000000, 0)
	body := []byte(`{"event":"test"}`)

	sig1 := signer.Sign([]byte("secret-1"), timestamp, body)
	sig2 := signer.Sign([]byte("secret-2"), timestamp, body)

	assert.NotEqual(t, sig1, sig2)
}

func TestSigner_Verify(t *testing.T) {
	signer := httpdeliver.NewSigner()
	secret := []byte("test-secret")
	timestamp := time.Unix(1700000000, 0)
	body := []byte(`{"event":"test"}`)

	signature := signer.Sign(secret, timestamp, body)

	assert.True(t, signer.Verify(secret, timestamp, body, signature))
	assert.False(t, signer.Verify(secret, timestamp, body, "wrong-signature"))
	assert.False(t, signer.Verify([]byte("wrong-secret"), timestamp, body, signature))
}

// Helper to compute expected signature
func computeSignature(secret []byte, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(fmt.Sprintf("%d.", timestamp.Unix())))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
