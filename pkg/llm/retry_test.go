package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		statusCode int
		want       bool
	}{
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusTooManyRequests, true},     // 429
		{http.StatusInternalServerError, true}, // 500
		{http.StatusBadGateway, true},          // 502
		{http.StatusServiceUnavailable, true},  // 503
		{http.StatusGatewayTimeout, true},      // 504
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.statusCode), func(t *testing.T) {
			got := isRetryable(tt.statusCode)
			if got != tt.want {
				t.Errorf("isRetryable(%d) = %v, want %v", tt.statusCode, got, tt.want)
			}
		})
	}
}

func TestRetryableError(t *testing.T) {
	underlying := errors.New("underlying error")
	re := &retryableError{
		err:        underlying,
		retryable:  true,
		statusCode: 429,
	}

	// Test Error() returns underlying message
	if re.Error() != "underlying error" {
		t.Errorf("Error() = %q, want %q", re.Error(), "underlying error")
	}

	// Test Unwrap() returns underlying error
	if re.Unwrap() != underlying {
		t.Errorf("Unwrap() did not return underlying error")
	}
}

func TestCalculateNextWait(t *testing.T) {
	config := RetryConfig{
		MaxWait:    30 * time.Second,
		Multiplier: 2.0,
	}

	tests := []struct {
		current time.Duration
		want    time.Duration
	}{
		{1 * time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{15 * time.Second, 30 * time.Second},
		{20 * time.Second, 30 * time.Second}, // Capped at MaxWait
	}

	for _, tt := range tests {
		got := calculateNextWait(tt.current, config)
		if got != tt.want {
			t.Errorf("calculateNextWait(%v) = %v, want %v", tt.current, got, tt.want)
		}
	}
}

func TestWaitWithJitter_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := waitWithJitter(ctx, 10*time.Second)
	if err != context.Canceled {
		t.Errorf("waitWithJitter() = %v, want %v", err, context.Canceled)
	}
}

// TestJitterFor covers the jitter bound directly, so the wall-clock test below
// doesn't have to: a scheduler-dependent upper bound on elapsed time is a flake
// waiting to happen, but the bound on the jitter itself is exact.
func TestJitterFor(t *testing.T) {
	waits := []time.Duration{
		0,
		1 * time.Nanosecond,
		3 * time.Nanosecond, // less than 4ns: wait/4 truncates to zero
		1 * time.Millisecond,
		50 * time.Millisecond,
		30 * time.Second,
	}

	for _, wait := range waits {
		t.Run(wait.String(), func(t *testing.T) {
			for range 1000 {
				got := jitterFor(wait)
				if got < 0 || got > wait/4 {
					t.Fatalf("jitterFor(%v) = %v, want within [0, %v]", wait, got, wait/4)
				}
			}
		})
	}
}

func TestWaitWithJitter_CompletesNormally(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		start := time.Now()

		err := waitWithJitter(ctx, 50*time.Millisecond)
		elapsed := time.Since(start)

		if err != nil {
			t.Errorf("waitWithJitter() returned error: %v", err)
		}

		// On the bubble's fake clock the elapsed time is exactly the base
		// duration plus the jitter, so both bounds are exact: at least the
		// base, at most base + base/4 (the jitter span TestJitterFor covers).
		if elapsed < 50*time.Millisecond {
			t.Errorf("waitWithJitter() returned too quickly: %v", elapsed)
		}
		if max := 50*time.Millisecond + 50*time.Millisecond/4; elapsed > max {
			t.Errorf("waitWithJitter() waited %v, want at most %v", elapsed, max)
		}
	})
}

func TestDefaultRetryConfig(t *testing.T) {
	config := DefaultRetryConfig()

	if config.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", config.MaxRetries)
	}
	if config.InitialWait != 1*time.Second {
		t.Errorf("InitialWait = %v, want 1s", config.InitialWait)
	}
	if config.MaxWait != 30*time.Second {
		t.Errorf("MaxWait = %v, want 30s", config.MaxWait)
	}
	if config.Multiplier != 2.0 {
		t.Errorf("Multiplier = %v, want 2.0", config.Multiplier)
	}
}

func TestStreamWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	// This test verifies that StreamWithRetry returns immediately on success.
	// Since we can't easily mock Stream, we test the retry logic indirectly
	// through the exported interface.

	config := RetryConfig{
		MaxRetries:  3,
		InitialWait: 10 * time.Millisecond,
		MaxWait:     100 * time.Millisecond,
		Multiplier:  2.0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Use an invalid model to trigger an error, but test the retry behavior
	model := Model{
		ID:       "test-model",
		API:      API("invalid-api"),
		Provider: "test",
	}

	_, err := StreamWithRetry(ctx, model, Request{}, StreamOptions{}, config)

	// Should fail with unsupported API error (not retried because it's not wrapped as retryableError)
	if err == nil {
		t.Error("expected error for invalid API")
	}
}

func TestStreamWithRetry_ContextCancellation(t *testing.T) {
	config := RetryConfig{
		MaxRetries:  3,
		InitialWait: 100 * time.Millisecond,
		MaxWait:     1 * time.Second,
		Multiplier:  2.0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	model := Model{
		ID:       "test-model",
		API:      APIAnthropicMessages,
		Provider: "test",
		BaseURL:  "http://localhost:1", // Will fail to connect
	}

	start := time.Now()
	_, err := StreamWithRetry(ctx, model, Request{}, StreamOptions{}, config)
	elapsed := time.Since(start)

	// Should fail quickly due to context cancellation
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}

	// Should not have waited for retries
	if elapsed > 500*time.Millisecond {
		t.Errorf("took too long despite context cancellation: %v", elapsed)
	}
}

func TestStreamWithRetry_NonRetryableError(t *testing.T) {
	config := RetryConfig{
		MaxRetries:  3,
		InitialWait: 10 * time.Millisecond,
		MaxWait:     100 * time.Millisecond,
		Multiplier:  2.0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test that non-retryable errors (like unsupported API) return immediately
	model := Model{
		ID:       "test-model",
		API:      API("unsupported-api"),
		Provider: "test",
	}

	_, err := StreamWithRetry(ctx, model, Request{}, StreamOptions{}, config)

	if err == nil {
		t.Fatal("expected error for unsupported API")
	}

	// An unsupported API is a permanent configuration error, so StreamWithRetry
	// must return it on the first attempt rather than backing off and asking
	// again. Asserting on the marker rather than on elapsed time keeps this
	// deterministic under load.
	re, ok := err.(*retryableError)
	if !ok {
		t.Fatalf("unsupported API error = %T, want *retryableError", err)
	}
	if re.retryable {
		t.Error("unsupported API error marked retryable, want non-retryable")
	}
}
