package llm

import (
	"context"
	"encoding/json"
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
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "claude-x", "max_input_tokens": 1000, "max_tokens": 100})
	}))
	defer srv.Close()
	if _, err := ModelLimits(context.Background(), Model{ID: "claude-x", API: APIAnthropicMessages, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 1 {
		t.Fatalf("recorded %d requests through Transport, want 1", len(recorder.requests))
	}
}
