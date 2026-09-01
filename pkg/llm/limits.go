package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// outputLimit resolves the output cap a request sends: MaxTokens when
// the caller named one, the model's ceiling when it asked for MaxOutput,
// else the model's ceiling if known and 0 — no cap, the provider's own
// default — if not. Naming a number and asking for the ceiling at once
// is a contradiction, as is asking for a ceiling nobody knows.
func (opts StreamOptions) outputLimit(model Model) (int, error) {
	switch {
	case opts.MaxTokens != nil && opts.MaxOutput:
		return 0, errors.New("llm: MaxTokens and MaxOutput are exclusive")
	case opts.MaxTokens != nil:
		return *opts.MaxTokens, nil
	case opts.MaxOutput && model.MaxTokens == 0:
		return 0, fmt.Errorf("llm: MaxOutput asked of %s, whose output ceiling is unknown", model.ID)
	default:
		return model.MaxTokens, nil
	}
}

// Limits is what a provider publishes about a model's size: the
// context window and the output ceiling, in tokens.
type Limits struct {
	ContextWindow int
	MaxTokens     int
}

// ModelLimits asks the provider for the model's published limits, so
// that Model.ContextWindow and Model.MaxTokens can carry the provider's
// figures rather than ones kept by hand. Only the Anthropic Models API
// (GET /v1/models/{id}) answers today; the OpenAI-style APIs publish no
// per-model output ceiling, and their max_tokens is optional anyway, so
// for them this reports ErrNoLimits and the request omits the field.
func ModelLimits(ctx context.Context, model Model) (Limits, error) {
	if model.API != APIAnthropicMessages {
		return Limits{}, ErrNoLimits
	}
	baseURL := model.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/v1/models/"+model.ID, nil)
	if err != nil {
		return Limits{}, err
	}
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	if model.APIKey != "" {
		req.Header.Set("x-api-key", model.APIKey)
	}
	resp, err := limitsClient.Do(req)
	if err != nil {
		return Limits{}, fmt.Errorf("model limits: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Limits{}, fmt.Errorf("model limits: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Limits{}, fmt.Errorf("model limits: anthropic API error (status %d): %s", resp.StatusCode, body)
	}
	var out struct {
		MaxInputTokens int `json:"max_input_tokens"`
		MaxTokens      int `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Limits{}, fmt.Errorf("model limits: decode: %w", err)
	}
	if out.MaxTokens == 0 {
		return Limits{}, fmt.Errorf("model limits: %s publishes no max_tokens", model.ID)
	}
	return Limits{ContextWindow: out.MaxInputTokens, MaxTokens: out.MaxTokens}, nil
}

// limitsClient bounds the metadata GET; unlike a completion stream, it
// has no business taking long.
var limitsClient = newHTTPClient(30 * time.Second)

// ErrNoLimits reports a provider that publishes no per-model limits.
var ErrNoLimits = errors.New("llm: provider publishes no model limits")
