// Package retry provides exponential backoff and retry logic for webhook delivery.
package retry

import (
	"math"
	"math/rand"
	"net"
	"strings"
	"time"
)

const (
	// BaseDelay is the initial retry delay.
	BaseDelay = 1 * time.Second

	// MaxDelay is the maximum delay between retries.
	MaxDelay = 10 * time.Minute

	// MaxAttempts is the maximum number of delivery attempts before dead-lettering.
	MaxAttempts = 10

	// JitterRatio is the fraction of delay to randomize.
	JitterRatio = 0.2
)

// NextDelay calculates the next retry delay using exponential backoff with jitter.
// Formula: min(MaxDelay, BaseDelay * 2^(attempt-1)) ± jitter
func NextDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	// Calculate exponential delay
	delay := float64(BaseDelay) * math.Pow(2, float64(attempt-1))

	// Cap at MaxDelay
	if delay > float64(MaxDelay) {
		delay = float64(MaxDelay)
	}

	// Add jitter: ±JitterRatio of the delay
	jitter := delay * JitterRatio
	delay = delay - jitter + (rand.Float64() * 2 * jitter)

	return time.Duration(delay)
}

// ShouldRetry returns true if the event should be retried based on attempt count.
func ShouldRetry(attempt int) bool {
	return attempt < MaxAttempts
}

// IsRetryableError determines if an error is transient and worth retrying.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// Network errors are retryable
	if _, ok := err.(net.Error); ok {
		return true
	}

	// Common transient error patterns
	retryablePatterns := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"deadline exceeded",
		"context deadline exceeded",
		"context canceled",
		"no such host",
		"network is unreachable",
		"i/o timeout",
		"temporary failure",
		"EOF",
		"GOAWAY",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(strings.ToLower(errStr), strings.ToLower(pattern)) {
			return true
		}
	}

	return false
}

// IsRetryableStatus determines if an HTTP status code warrants retry.
func IsRetryableStatus(statusCode int) bool {
	switch statusCode {
	case 408: // Request Timeout
		return true
	case 429: // Too Many Requests
		return true
	case 500: // Internal Server Error
		return true
	case 502: // Bad Gateway
		return true
	case 503: // Service Unavailable
		return true
	case 504: // Gateway Timeout
		return true
	default:
		return false
	}
}

// IsPermanentFailure returns true if the failure should not be retried.
func IsPermanentFailure(statusCode int) bool {
	// 4xx errors (except 408, 429) are permanent
	if statusCode >= 400 && statusCode < 500 {
		return statusCode != 408 && statusCode != 429
	}
	return false
}

// Schedule calculates the visible_at time for the next retry.
func Schedule(attempt int) time.Time {
	return time.Now().Add(NextDelay(attempt))
}
