package llm

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordingTransport struct {
	requests []*http.Request
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.requests = append(rt.requests, req)
	return http.DefaultTransport.RoundTrip(req)
}

// TestTransportCarriesEveryRequest pins the injection seam: requests go
// through whatever Transport holds at call time, not whatever it held
// at package init. A package-level client captured at init is exactly
// the regression that would silently drop a host's instrumentation.
func TestTransportCarriesEveryRequest(t *testing.T) {
	if Transport != nil {
		t.Fatalf("default Transport = %v, want nil (net/http's own default)", Transport)
	}
	recorder := &recordingTransport{}
	Transport = recorder
	t.Cleanup(func() { Transport = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.MarshalWrite(w, map[string]any{"id": "claude-x", "max_input_tokens": 1000, "max_tokens": 100})
	}))
	defer srv.Close()
	if _, err := ModelLimits(context.Background(), Model{ID: "claude-x", API: APIAnthropicMessages, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("recorded %d requests through Transport, want 1", len(recorder.requests))
	}
}

// TestNoRequestFollowsARedirect pins the package's one redirect rule:
// whatever the adapter, a 3xx from the configured endpoint is answered
// as an error, never followed. net/http strips Authorization on a
// cross-host hop but not x-api-key or x-goog-api-key, and a 307 or 308
// re-sends the prompt body; no provider API needs a redirect.
func TestNoRequestFollowsARedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("followed a redirect: %s %s (x-api-key %q, authorization %q)", r.Method, r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("Authorization"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	ctx := context.Background()
	req := Request{Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "private prompt"}}}}}
	for _, api := range []API{APIAnthropicMessages, APIOpenAICompletions, APIOpenAIResponses, APIGoogleGenerateContent} {
		t.Run(string(api), func(t *testing.T) {
			m := Model{ID: "model-x", API: api, Provider: "test", BaseURL: source.URL, APIKey: "private-key", MaxTokens: 100}
			if h, err := Stream(ctx, m, req, StreamOptions{}); err == nil {
				if _, err := h.Wait(); err == nil {
					t.Fatal("stream accepted a redirect")
				}
			}
			if _, err := ModelLimits(ctx, m); err == nil {
				t.Fatal("metadata accepted a redirect")
			}
		})
	}
	t.Run(string(APITypeSafeSystemOne), func(t *testing.T) {
		m := Model{ID: "jev-latest", API: APITypeSafeSystemOne, BaseURL: source.URL, APIKey: "private-key"}
		if _, err := Decide(ctx, m, decisionFixture()); err == nil {
			t.Fatal("decision accepted a redirect")
		}
	})
}
