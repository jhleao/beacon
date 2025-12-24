package httpdeliver_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"beacon/internal/httpdeliver"
	"beacon/internal/outbox"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// testLogger returns a discard logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// allowLocalhostPolicy returns SSRF policy JSON that allows localhost for testing.
func allowLocalhostPolicy() json.RawMessage {
	return json.RawMessage(`{"allow_private":true}`)
}

func TestClient_Deliver_Success(t *testing.T) {
	var receivedBody []byte
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := httpdeliver.NewClient(nil, testLogger())
	dest := outbox.Destination{
		ID:         uuid.New(),
		URL:        server.URL,
		Method:     "POST",
		TimeoutMs:  5000,
		Headers:    map[string]string{"X-Custom": "test-value"},
		SSRFPolicy: allowLocalhostPolicy(),
	}
	event := outbox.Event{
		ID:             uuid.New(),
		SubscriptionID: uuid.New(),
		Payload:        []byte(`{"data":"test"}`),
		Attempts:       1,
	}

	statusCode, _, err := client.Deliver(context.Background(), dest, event)

	assert.NoError(t, err)
	assert.NotNil(t, statusCode)
	assert.Equal(t, 200, *statusCode)

	// Verify headers sent
	assert.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))
	assert.Equal(t, "Beacon/1.0", receivedHeaders.Get("User-Agent"))
	assert.Equal(t, event.ID.String(), receivedHeaders.Get("Beacon-Event-Id"))
	assert.Equal(t, "1", receivedHeaders.Get("Beacon-Attempt"))
	assert.Equal(t, "test-value", receivedHeaders.Get("X-Custom"))

	// Verify body
	assert.Equal(t, `{"data":"test"}`, string(receivedBody))
}

func TestClient_Deliver_WithSigning(t *testing.T) {
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(200)
	}))
	defer server.Close()

	secret := []byte("test-hmac-secret")
	client := httpdeliver.NewClient(secret, testLogger())

	dest := outbox.Destination{
		ID:         uuid.New(),
		URL:        server.URL,
		Method:     "POST",
		TimeoutMs:  5000,
		SSRFPolicy: allowLocalhostPolicy(),
	}
	event := outbox.Event{
		ID:      uuid.New(),
		Payload: []byte(`{"data":"test"}`),
	}

	client.Deliver(context.Background(), dest, event)

	// Verify signing headers present
	assert.NotEmpty(t, receivedHeaders.Get("Beacon-Timestamp"))
	assert.NotEmpty(t, receivedHeaders.Get("Beacon-Signature"))
}

func TestClient_Deliver_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("Service Unavailable"))
	}))
	defer server.Close()

	client := httpdeliver.NewClient(nil, testLogger())
	dest := outbox.Destination{URL: server.URL, Method: "POST", TimeoutMs: 5000, SSRFPolicy: allowLocalhostPolicy()}
	event := outbox.Event{ID: uuid.New(), Payload: []byte(`{}`)}

	statusCode, _, err := client.Deliver(context.Background(), dest, event)

	assert.NoError(t, err) // No error, just non-2xx status
	assert.Equal(t, 503, *statusCode)
}

func TestClient_Deliver_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Longer than timeout
		w.WriteHeader(200)
	}))
	defer server.Close()

	client := httpdeliver.NewClient(nil, testLogger())
	dest := outbox.Destination{URL: server.URL, Method: "POST", TimeoutMs: 100, SSRFPolicy: allowLocalhostPolicy()}
	event := outbox.Event{ID: uuid.New(), Payload: []byte(`{}`)}

	statusCode, _, err := client.Deliver(context.Background(), dest, event)

	assert.Error(t, err)
	assert.Nil(t, statusCode)
	assert.Contains(t, err.Error(), "deadline exceeded")
}

func TestClient_Deliver_ConnectionRefused(t *testing.T) {
	client := httpdeliver.NewClient(nil, testLogger())
	dest := outbox.Destination{
		URL:        "http://127.0.0.1:59999", // Nothing listening
		Method:     "POST",
		TimeoutMs:  1000,
		SSRFPolicy: allowLocalhostPolicy(),
	}
	event := outbox.Event{ID: uuid.New(), Payload: []byte(`{}`)}

	statusCode, _, err := client.Deliver(context.Background(), dest, event)

	assert.Error(t, err)
	assert.Nil(t, statusCode)
}

func TestClient_Deliver_NoRedirectFollow(t *testing.T) {
	redirectCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectCount++
		if redirectCount == 1 {
			http.Redirect(w, r, "/redirected", http.StatusFound)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()

	client := httpdeliver.NewClient(nil, testLogger())
	dest := outbox.Destination{URL: server.URL, Method: "POST", TimeoutMs: 5000, SSRFPolicy: allowLocalhostPolicy()}
	event := outbox.Event{ID: uuid.New(), Payload: []byte(`{}`)}

	statusCode, _, err := client.Deliver(context.Background(), dest, event)

	// Should get 302, not follow redirect
	assert.NoError(t, err)
	assert.Equal(t, 302, *statusCode)
	assert.Equal(t, 1, redirectCount, "should not follow redirects")
}

func TestClient_Deliver_ResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "abc123")
		w.Header().Set("X-Rate-Limit", "100")
		w.WriteHeader(200)
	}))
	defer server.Close()

	client := httpdeliver.NewClient(nil, testLogger())
	dest := outbox.Destination{URL: server.URL, Method: "POST", TimeoutMs: 5000, SSRFPolicy: allowLocalhostPolicy()}
	event := outbox.Event{ID: uuid.New(), Payload: []byte(`{}`)}

	_, respHeaders, err := client.Deliver(context.Background(), dest, event)

	assert.NoError(t, err)
	assert.Equal(t, "abc123", respHeaders["X-Request-Id"])
	assert.Equal(t, "100", respHeaders["X-Rate-Limit"])
}
