package retry_test

import (
	"errors"
	"testing"
	"time"

	"beacon/internal/dispatcher/retry"

	"github.com/stretchr/testify/assert"
)

func TestNextDelay_ExponentialGrowth(t *testing.T) {
	// First attempt should be around BaseDelay
	delay1 := retry.NextDelay(1)
	assert.True(t, delay1 >= 800*time.Millisecond && delay1 <= 1200*time.Millisecond,
		"first delay should be around 1s, got %v", delay1)

	// Second attempt should be around 2s
	delay2 := retry.NextDelay(2)
	assert.True(t, delay2 >= 1600*time.Millisecond && delay2 <= 2400*time.Millisecond,
		"second delay should be around 2s, got %v", delay2)

	// Third attempt should be around 4s
	delay3 := retry.NextDelay(3)
	assert.True(t, delay3 >= 3200*time.Millisecond && delay3 <= 4800*time.Millisecond,
		"third delay should be around 4s, got %v", delay3)
}

func TestNextDelay_CapsAtMaxDelay(t *testing.T) {
	// Very high attempt should cap at MaxDelay
	delay := retry.NextDelay(100)
	assert.True(t, delay <= retry.MaxDelay+time.Duration(float64(retry.MaxDelay)*retry.JitterRatio),
		"delay should not exceed MaxDelay + jitter, got %v", delay)
}

func TestNextDelay_HandlesZero(t *testing.T) {
	delay := retry.NextDelay(0)
	assert.True(t, delay > 0, "delay should be positive")
}

func TestShouldRetry(t *testing.T) {
	assert.True(t, retry.ShouldRetry(1))
	assert.True(t, retry.ShouldRetry(5))
	assert.True(t, retry.ShouldRetry(9))
	assert.False(t, retry.ShouldRetry(10))
	assert.False(t, retry.ShouldRetry(11))
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"connection refused", errors.New("connection refused"), true},
		{"connection reset", errors.New("connection reset by peer"), true},
		{"timeout", errors.New("i/o timeout"), true},
		{"context deadline exceeded", errors.New("context deadline exceeded"), true},
		{"random error", errors.New("something random happened"), false},
		{"EOF", errors.New("EOF"), true},
		{"GOAWAY", errors.New("received GOAWAY frame"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := retry.IsRetryableError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsRetryableStatus(t *testing.T) {
	retryable := []int{408, 429, 500, 502, 503, 504}
	for _, code := range retryable {
		assert.True(t, retry.IsRetryableStatus(code), "status %d should be retryable", code)
	}

	notRetryable := []int{200, 201, 400, 401, 403, 404, 405}
	for _, code := range notRetryable {
		assert.False(t, retry.IsRetryableStatus(code), "status %d should not be retryable", code)
	}
}

func TestIsPermanentFailure(t *testing.T) {
	permanent := []int{400, 401, 403, 404, 405}
	for _, code := range permanent {
		assert.True(t, retry.IsPermanentFailure(code), "status %d should be permanent", code)
	}

	notPermanent := []int{200, 408, 429, 500, 502, 503}
	for _, code := range notPermanent {
		assert.False(t, retry.IsPermanentFailure(code), "status %d should not be permanent", code)
	}
}

func TestSchedule(t *testing.T) {
	before := time.Now()
	scheduled := retry.Schedule(1)
	after := time.Now()

	assert.True(t, scheduled.After(before), "scheduled time should be after now")
	assert.True(t, scheduled.Before(after.Add(2*time.Second)), "scheduled time should be within reasonable range")
}
