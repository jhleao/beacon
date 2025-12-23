package httpdeliver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// Signer creates HMAC signatures for request authentication.
type Signer struct{}

// NewSigner creates a new Signer.
func NewSigner() *Signer {
	return &Signer{}
}

// Sign generates an HMAC-SHA256 signature over "{timestamp}.{body}".
func (s *Signer) Sign(secret []byte, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(fmt.Sprintf("%d.", timestamp.Unix())))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Headers returns the signing headers to add to a request.
func (s *Signer) Headers(secret []byte, body []byte) map[string]string {
	timestamp := time.Now()
	signature := s.Sign(secret, timestamp, body)

	return map[string]string{
		"Beacon-Timestamp": strconv.FormatInt(timestamp.Unix(), 10),
		"Beacon-Signature": signature,
	}
}

// Verify checks if a signature is valid (used by receivers).
func (s *Signer) Verify(secret []byte, timestamp time.Time, body []byte, signature string) bool {
	expected := s.Sign(secret, timestamp, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}
