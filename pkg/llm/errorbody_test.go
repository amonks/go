package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// An error answer is read under a bound, whatever the adapter. The body
// becomes the error's text, which callers log and hold, and what
// answers at a base URL is not always the provider: an error document
// is a few hundred bytes, and a megabyte of anything else is not one to
// carry around. The start of the body, which is where an error document
// says what it is, comes through as it was sent.
func TestErrorBodiesAreReadUnderABound(t *testing.T) {
	const reason = `{"error":{"message":"the reason this request failed"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(reason + strings.Repeat("x", 2<<20)))
	}))
	defer srv.Close()

	ctx := context.Background()
	for _, api := range []API{APIAnthropicMessages, APIOpenAICompletions, APIOpenAIResponses, APIGoogleGenerateContent} {
		t.Run(string(api), func(t *testing.T) {
			m := Model{ID: "model-x", API: api, Provider: "test", BaseURL: srv.URL, APIKey: "k", MaxTokens: 100}
			_, err := Stream(ctx, m, Request{}, StreamOptions{})
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), "(status 400): "+reason) {
				t.Errorf("the error lost the body's start: %.120s", err)
			}
			if len(err.Error()) > errorBodyLimit+256 {
				t.Errorf("the error carries %d bytes of a 2MiB body, want at most %d", len(err.Error()), errorBodyLimit)
			}
			if !Rejected(err) {
				t.Errorf("a 400 is no longer Rejected: %.120s", err)
			}
		})
	}
	t.Run("model limits", func(t *testing.T) {
		m := Model{ID: "model-x", API: APIAnthropicMessages, BaseURL: srv.URL}
		_, err := ModelLimits(ctx, m)
		if err == nil {
			t.Fatal("no error")
		}
		if !strings.Contains(err.Error(), "(status 400): "+reason) {
			t.Errorf("the error lost the body's start: %.120s", err)
		}
		if len(err.Error()) > errorBodyLimit+256 {
			t.Errorf("the error carries %d bytes of a 2MiB body, want at most %d", len(err.Error()), errorBodyLimit)
		}
	})
}

// A request this package refuses before it sends anything fails the
// same way on every attempt, so StreamWithRetry returns it at once: on
// the bubble's clock, no backoff has been waited out.
func TestStreamWithRetryDoesNotRetryALocalRefusal(t *testing.T) {
	n := 1024
	for _, api := range []API{APIAnthropicMessages, APIOpenAICompletions, APIOpenAIResponses, APIGoogleGenerateContent} {
		t.Run(string(api), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Naming a number and asking for the ceiling at once is a
				// contradiction no adapter sends.
				opts := StreamOptions{MaxTokens: &n, MaxOutput: true}
				m := Model{ID: "model-x", API: api, Provider: "test", BaseURL: "http://unreachable.invalid", APIKey: "k", MaxTokens: 100}
				start := time.Now()
				_, err := StreamWithRetry(context.Background(), m, Request{}, opts, DefaultRetryConfig())
				if err == nil || !strings.Contains(err.Error(), "exclusive") {
					t.Fatalf("err = %v, want the refusal", err)
				}
				if waited := time.Since(start); waited != 0 {
					t.Errorf("waited %s retrying a request that is refused before it is sent", waited)
				}
			})
		})
	}
}
