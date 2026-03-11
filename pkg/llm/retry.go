package llm

import (
	"context"
	"math/rand"
	"net/http"
	"time"
)

// RetryConfig configures retry behavior.
type RetryConfig struct {
	MaxRetries  int           // Maximum number of retries (0 = no retries)
	InitialWait time.Duration // Initial wait time before first retry
	MaxWait     time.Duration // Maximum wait time between retries
	Multiplier  float64       // Multiplier for exponential backoff
}

// DefaultRetryConfig returns the default retry configuration.
// Retries up to 5 times (6 total attempts) with exponential backoff.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:  5,
		InitialWait: 1 * time.Second,
		MaxWait:     30 * time.Second,
		Multiplier:  2.0,
	}
}

// isRetryable returns true if the HTTP status code indicates a retryable error.
func isRetryable(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, // 429 - Rate limit
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	default:
		return false
	}
}

// retryableError wraps an error with a flag indicating if it's retryable.
type retryableError struct {
	err        error
	retryable  bool
	statusCode int
}

func (e *retryableError) Error() string {
	return e.err.Error()
}

func (e *retryableError) Unwrap() error {
	return e.err
}

// waitWithJitter waits for the specified duration with some jitter.
func waitWithJitter(ctx context.Context, wait time.Duration) error {
	// Add up to 25% jitter
	jitter := time.Duration(rand.Int63n(int64(wait / 4)))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait + jitter):
		return nil
	}
}

// calculateNextWait calculates the next wait time for exponential backoff.
func calculateNextWait(current time.Duration, config RetryConfig) time.Duration {
	next := min(time.Duration(float64(current)*config.Multiplier), config.MaxWait)
	return next
}

// StreamWithRetry wraps Stream with automatic retry for transient errors.
// It retries on network errors and rate limits with exponential backoff.
//
// Retry behavior:
//   - Network errors (connection refused, timeout, DNS failure): always retried
//   - HTTP 429 (rate limit), 500, 502, 503, 504: retried
//   - HTTP 4xx (except 429): not retried (client errors)
//   - Context cancellation: stops retrying immediately
func StreamWithRetry(ctx context.Context, model Model, req Request, opts StreamOptions, config RetryConfig) (*StreamHandle, error) {
	var lastErr error
	wait := config.InitialWait

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retry
			if err := waitWithJitter(ctx, wait); err != nil {
				return nil, err
			}
			wait = calculateNextWait(wait, config)
		}

		handle, err := Stream(ctx, model, req, opts)
		if err == nil {
			return handle, nil
		}

		lastErr = err

		// Check if error is explicitly marked as non-retryable
		if re, ok := err.(*retryableError); ok {
			if !re.retryable {
				return nil, err
			}
		}

		// Check for context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	return nil, lastErr
}
