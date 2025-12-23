package httpdeliver_test

import (
	"context"
	"testing"

	"beacon/internal/httpdeliver"

	"github.com/stretchr/testify/assert"
)

func TestSSRFGuard_BlocksPrivateIPs(t *testing.T) {
	guard := httpdeliver.NewSSRFGuard()
	ctx := context.Background()

	blockedURLs := []string{
		"http://127.0.0.1/webhook",
		"http://10.0.0.1/webhook",
		"http://10.255.255.255/webhook",
		"http://172.16.0.1/webhook",
		"http://172.31.255.255/webhook",
		"http://192.168.1.1/webhook",
		"http://169.254.169.254/latest/meta-data/", // AWS metadata
	}

	for _, url := range blockedURLs {
		_, err := guard.CheckURL(ctx, url)
		assert.Error(t, err, "should block %s", url)
	}
}

func TestSSRFGuard_AllowsPublicIPs(t *testing.T) {
	guard := httpdeliver.NewSSRFGuard()
	ctx := context.Background()

	allowedURLs := []string{
		"https://8.8.8.8/webhook",
		"https://1.1.1.1/webhook",
	}

	for _, url := range allowedURLs {
		safe, err := guard.CheckURL(ctx, url)
		assert.NoError(t, err, "should allow %s", url)
		assert.Equal(t, url, safe)
	}
}

func TestSSRFGuard_RejectsInvalidSchemes(t *testing.T) {
	guard := httpdeliver.NewSSRFGuard()
	ctx := context.Background()

	invalidURLs := []string{
		"ftp://example.com/file",
		"file:///etc/passwd",
		"gopher://localhost/",
	}

	for _, url := range invalidURLs {
		_, err := guard.CheckURL(ctx, url)
		assert.Error(t, err, "should reject %s", url)
	}
}

func TestSSRFGuard_WithPolicy_AllowPrivate(t *testing.T) {
	guard := httpdeliver.NewSSRFGuard()
	ctx := context.Background()

	policy := httpdeliver.SSRFPolicy{AllowPrivate: true}
	guardWithPolicy := guard.WithPolicy(policy)

	// Now private IPs should be allowed
	safe, err := guardWithPolicy.CheckURL(ctx, "http://10.0.1.50/webhook")
	assert.NoError(t, err)
	assert.Equal(t, "http://10.0.1.50/webhook", safe)
}

func TestSSRFGuard_WithPolicy_AllowedHosts(t *testing.T) {
	guard := httpdeliver.NewSSRFGuard()
	ctx := context.Background()

	policy := httpdeliver.SSRFPolicy{
		AllowedHosts: []string{"10.0.1.50"},
	}
	guardWithPolicy := guard.WithPolicy(policy)

	// Specific allowed host should work
	safe, err := guardWithPolicy.CheckURL(ctx, "http://10.0.1.50/webhook")
	assert.NoError(t, err)
	assert.Equal(t, "http://10.0.1.50/webhook", safe)

	// Other private IPs still blocked
	_, err = guardWithPolicy.CheckURL(ctx, "http://10.0.1.51/webhook")
	assert.Error(t, err)
}

func TestSSRFGuard_InvalidURL(t *testing.T) {
	guard := httpdeliver.NewSSRFGuard()
	ctx := context.Background()

	_, err := guard.CheckURL(ctx, "not-a-url")
	assert.Error(t, err)
}

func TestParseSSRFPolicy(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected *httpdeliver.SSRFPolicy
	}{
		{"empty", "", nil},
		{"null", "null", nil},
		{"empty object", "{}", nil},
		{"allow private", `{"allow_private":true}`, &httpdeliver.SSRFPolicy{AllowPrivate: true}},
		{"allowed hosts", `{"allowed_hosts":["a.com"]}`, &httpdeliver.SSRFPolicy{AllowedHosts: []string{"a.com"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := httpdeliver.ParseSSRFPolicy([]byte(tt.input))
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, tt.expected.AllowPrivate, result.AllowPrivate)
				assert.Equal(t, tt.expected.AllowedHosts, result.AllowedHosts)
			}
		})
	}
}
