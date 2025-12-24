package httpdeliver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"beacon/internal/outbox"

	"github.com/google/uuid"
)

// Client delivers webhooks with security and reliability features.
type Client struct {
	httpClient *http.Client
	ssrfGuard  *SSRFGuard
	signer     *Signer
	hmacSecret []byte
	logger     *slog.Logger
}

// NewClient creates a Client with the global HMAC signing secret.
// Pass nil to disable request signing.
func NewClient(hmacSecret []byte, logger *slog.Logger) *Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			// Disable automatic redirects
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		ssrfGuard:  NewSSRFGuard(),
		signer:     NewSigner(),
		hmacSecret: hmacSecret,
		logger:     logger.With("component", "httpdeliver"),
	}
}

// Deliver sends an event to a destination.
// Returns: status code (nil if connection failed), response headers, error.
func (c *Client) Deliver(
	ctx context.Context,
	dest outbox.Destination,
	event outbox.Event,
) (*int, map[string]string, error) {
	startTime := time.Now()

	// 1. SSRF check
	var checker PolicyChecker = c.ssrfGuard
	if policy := ParseSSRFPolicy(dest.SSRFPolicy); policy != nil {
		checker = c.ssrfGuard.WithPolicy(*policy)
	}

	safeURL, err := checker.CheckURL(ctx, dest.URL)
	if err != nil {
		c.logger.Debug("SSRF check blocked URL",
			"url", dest.URL,
			"destination", dest.Name,
			"error", err,
		)
		return nil, nil, fmt.Errorf("SSRF blocked: %w", err)
	}

	c.logger.Debug("SSRF check passed",
		"url", safeURL,
		"destination", dest.Name,
	)

	// 2. Use payload directly (already JSON)
	body := []byte(event.Payload)

	// 3. Create request
	req, err := http.NewRequestWithContext(ctx, dest.Method, safeURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}

	// 4. Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Beacon/1.0")
	req.Header.Set("Beacon-Event-Id", event.ID.String())
	req.Header.Set("Beacon-Attempt", strconv.Itoa(event.Attempts))
	req.Header.Set("Beacon-Subscription-Id", event.SubscriptionID.String())

	// Custom headers from destination
	for k, v := range dest.Headers {
		req.Header.Set(k, v)
	}

	// 5. Add signature if global secret configured
	if len(c.hmacSecret) > 0 {
		sigHeaders := c.signer.Headers(c.hmacSecret, body)
		for k, v := range sigHeaders {
			req.Header.Set(k, v)
		}
		c.logger.Debug("added HMAC signature to request",
			"event_id", event.ID,
		)
	}

	// 6. Apply timeout
	timeout := time.Duration(dest.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req = req.WithContext(ctx)

	c.logger.Debug("sending HTTP request",
		"method", dest.Method,
		"url", safeURL,
		"event_id", event.ID,
		"attempt", event.Attempts,
		"timeout_ms", timeout.Milliseconds(),
		"payload_size", len(body),
	)

	// 7. Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Debug("HTTP request failed",
			"event_id", event.ID,
			"destination", dest.Name,
			"error", err,
			"duration_ms", time.Since(startTime).Milliseconds(),
		)
		return nil, nil, err
	}
	defer resp.Body.Close()

	// 8. Read response (limited to prevent memory exhaustion)
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))

	// 9. Extract response headers
	respHeaders := make(map[string]string)
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	c.logger.Debug("HTTP request completed",
		"event_id", event.ID,
		"destination", dest.Name,
		"status_code", resp.StatusCode,
		"duration_ms", time.Since(startTime).Milliseconds(),
	)

	return &resp.StatusCode, respHeaders, nil
}

// DeliverEvent is a helper that creates a minimal event for delivery testing.
func DeliverEvent(id uuid.UUID, subscriptionID uuid.UUID, payload []byte, attempts int) outbox.Event {
	return outbox.Event{
		ID:             id,
		SubscriptionID: subscriptionID,
		Payload:        payload,
		Attempts:       attempts,
	}
}
