package llm

import (
	"context"
	"errors"
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

// Rejected reports whether err is the provider rejecting the request as
// sent: 400 (invalid — a conversation past the context window, say) or
// 413 (too large). It will not succeed until the request or the account
// changes: providers answer some account conditions 400 too (a spend
// limit, a retention setting), so a caller decides how long to keep
// trying. Rate limits and outages pass, and 401, 403 and 404 are never
// about the request itself, so none of those count.
func Rejected(err error) bool {
	var re *retryableError
	if !errors.As(err, &re) {
		return false
	}
	switch re.statusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
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
	retryAfter time.Duration
}

func (e *retryableError) Error() string {
	return e.err.Error()
}

func (e *retryableError) Unwrap() error {
	return e.err
}

// jitterFor returns a random offset in [0, wait/4] to spread retries out.
// Waits shorter than 4ns get no jitter, since there is no room for any.
func jitterFor(wait time.Duration) time.Duration {
	span := int64(wait / 4)
	if span <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(span + 1))
}

// waitWithJitter waits for the specified duration with some jitter.
func waitWithJitter(ctx context.Context, wait time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait + jitterFor(wait)):
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
//   - Unsupported model.API: not retried (permanent configuration error)
//   - A request refused before it is sent (options that contradict each
//     other, content an adapter cannot encode): not retried, since it is
//     refused the same way every time
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
		re, marked := err.(*retryableError)
		if marked && !re.retryable {
			return nil, err
		}

		// Check for context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// An error no adapter marked never left this process: the request
		// was refused before it was sent, and is refused the same way on
		// every attempt. Only what an adapter marked retryable — a
		// transport failure, a status that passes on its own — is asked
		// again.
		if !marked {
			return nil, err
		}
	}

	return nil, lastErr
}
