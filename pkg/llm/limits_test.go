package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOutputLimit(t *testing.T) {
	known := Model{ID: "m", MaxTokens: 128000}
	unknown := Model{ID: "m"}
	cases := []struct {
		name  string
		opts  StreamOptions
		model Model
		want  int
		err   string
	}{
		{"number named", StreamOptions{MaxTokens: new(300)}, known, 300, ""},
		{"ceiling asked", StreamOptions{MaxOutput: true}, known, 128000, ""},
		{"nothing said, ceiling known", StreamOptions{}, known, 128000, ""},
		{"nothing said, ceiling unknown", StreamOptions{}, unknown, 0, ""},
		{"ceiling asked but unknown", StreamOptions{MaxOutput: true}, unknown, 0, "ceiling is unknown"},
		{"both", StreamOptions{MaxTokens: new(1), MaxOutput: true}, known, 0, "exclusive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.opts.outputLimit(c.model)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("= %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

// Anthropic requires max_tokens, and the package invents none: with no
// caller preference and no known ceiling the request is refused before
// it is sent, not sent with a made-up number.
func TestAnthropicRequestNeedsACeiling(t *testing.T) {
	_, err := convertToAnthropicRequest(Model{ID: "m"}, Request{}, StreamOptions{})
	if err == nil || !strings.Contains(err.Error(), "requires max_tokens") {
		t.Fatalf("err = %v", err)
	}
	r, err := convertToAnthropicRequest(Model{ID: "m", MaxTokens: 64000}, Request{}, StreamOptions{MaxOutput: true})
	if err != nil || r.MaxTokens != 64000 {
		t.Fatalf("= %d, %v", r.MaxTokens, err)
	}
}

// OpenAI's field is optional, so an unknown ceiling is simply omitted.
func TestOpenAIRequestOmitsUnknownCeiling(t *testing.T) {
	r, err := convertToOpenAIRequest(Model{ID: "m"}, Request{}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "max_tokens") {
		t.Fatalf("max_tokens sent: %s", b)
	}
	rr, err := convertToResponsesRequest(Model{ID: "m", MaxTokens: 100}, Request{}, StreamOptions{MaxOutput: true})
	if err != nil || rr.MaxOutputTokens != 100 {
		t.Fatalf("= %d, %v", rr.MaxOutputTokens, err)
	}
}

func TestModelLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models/claude-x" || r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			http.Error(w, "bad request "+r.URL.Path, 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "claude-x", "max_input_tokens": 1000000, "max_tokens": 128000})
	}))
	defer srv.Close()
	got, err := ModelLimits(context.Background(), Model{ID: "claude-x", API: APIAnthropicMessages, BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if got != (Limits{ContextWindow: 1000000, MaxTokens: 128000}) {
		t.Fatalf("= %+v", got)
	}
	if _, err := ModelLimits(context.Background(), Model{ID: "nope", API: APIAnthropicMessages, BaseURL: srv.URL}); err == nil {
		t.Fatal("unknown model: want error")
	}
	if _, err := ModelLimits(context.Background(), Model{ID: "gpt", API: APIOpenAIResponses}); err != ErrNoLimits {
		t.Fatalf("openai: err = %v, want ErrNoLimits", err)
	}
}
