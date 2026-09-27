package llm

// Decision calls are a separate operation from chat: they evaluate a shared
// state against closed questions, without messages, tools, or generated text.
// The first adapter is TypeSafe's System One API. These types deliberately
// preserve its primitives; another provider needs an explicit adapter and
// capability checks, not an assumption that every decision API is compatible.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const APITypeSafeSystemOne API = "typesafe-systemone"

type DecisionType string

const (
	DecisionNoul   DecisionType = "noul"
	DecisionChoice DecisionType = "choice"
	DecisionScore  DecisionType = "score"
)

// DecisionQuestion's instructions accept a string, object, or array. Criteria
// are a choice-name map (choice), ordered level descriptions (score), or an
// optional true/false map (noul). Descriptions may themselves be structured JSON.
type DecisionQuestion struct {
	Type         DecisionType `json:"type"`
	Instructions any          `json:"instructions"`
	Criteria     any          `json:"criteria,omitempty"`
}

type DecisionRequest struct {
	State     any                         `json:"state"`
	Questions map[string]DecisionQuestion `json:"questions"`
}

// Pointers distinguish a zero answer/confidence from a missing field. Noul is
// a probability, not a boolean; Score is a weighted value, not an integer.
type DecisionAnswer struct {
	Type          DecisionType       `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        *string            `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Legend        map[string]any     `json:"legend,omitempty"`
}

type DecisionUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type DecisionResponse struct {
	Model   string                    `json:"model"`
	Answers map[string]DecisionAnswer `json:"answers"`
	Usage   DecisionUsage             `json:"usage"`
}

func description(v any) bool {
	switch v.(type) {
	case string, map[string]any, []any:
		return true
	}
	return false
}

// normalized validates the JSON shape too, so direct Go callers and decoded
// gateway requests obey the same rules (including typed maps and slices).
func (r DecisionRequest) normalized() (DecisionRequest, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return r, fmt.Errorf("decision request: %w", err)
	}
	var n DecisionRequest
	if err = json.Unmarshal(b, &n); err != nil {
		return r, err
	}
	if !description(n.State) {
		return r, fmt.Errorf("decision state must be a string, object, or array")
	}
	if len(n.Questions) == 0 {
		return r, fmt.Errorf("decision questions are required")
	}
	for id, q := range n.Questions {
		if !description(q.Instructions) {
			return r, fmt.Errorf("question %q: instructions must be a string, object, or array", id)
		}
		switch q.Type {
		case DecisionNoul, DecisionChoice:
			if q.Type == DecisionNoul && q.Criteria == nil {
				continue
			}
			criteria, ok := q.Criteria.(map[string]any)
			if !ok || (q.Type == DecisionChoice && (len(criteria) == 0 || len(criteria) > 255)) {
				return r, fmt.Errorf("question %q: invalid criteria map", id)
			}
			for key, value := range criteria {
				if q.Type == DecisionNoul && key != "true" && key != "false" {
					return r, fmt.Errorf("question %q: noul criteria must use true/false", id)
				}
				if !description(value) && !(q.Type == DecisionChoice && value == nil) {
					return r, fmt.Errorf("question %q: invalid criterion description", id)
				}
			}
		case DecisionScore:
			criteria, ok := q.Criteria.([]any)
			if !ok || len(criteria) < 2 || len(criteria) > 10 {
				return r, fmt.Errorf("question %q: score requires 2–10 levels", id)
			}
			for _, value := range criteria {
				if !description(value) {
					return r, fmt.Errorf("question %q: invalid level description", id)
				}
			}
		default:
			return r, fmt.Errorf("question %q: unsupported decision type %q", id, q.Type)
		}
	}
	return n, nil
}

func (r DecisionRequest) Validate() error { _, err := r.normalized(); return err }

func probability(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }

func (r DecisionResponse) validate(req DecisionRequest) error {
	if r.Model == "" || len(r.Answers) != len(req.Questions) || r.Usage.InputTokens < 0 || r.Usage.OutputTokens < 0 {
		return fmt.Errorf("invalid decision response metadata")
	}
	for id, q := range req.Questions {
		a, ok := r.Answers[id]
		if !ok || a.Type != q.Type {
			return fmt.Errorf("missing or mismatched decision answer %q", id)
		}
		if a.Type == DecisionNoul {
			if a.Noul == nil || !probability(*a.Noul) {
				return fmt.Errorf("invalid noul answer %q", id)
			}
			continue
		}
		keys := map[string]bool{}
		if q.Type == DecisionChoice {
			for k := range q.Criteria.(map[string]any) {
				keys[k] = true
			}
			if a.Choice == nil || !keys[*a.Choice] {
				return fmt.Errorf("invalid choice answer %q", id)
			}
		} else {
			for i := range q.Criteria.([]any) {
				keys[strconv.Itoa(i)] = true
			}
			if a.Score == nil || math.IsNaN(*a.Score) || *a.Score < 0 || *a.Score > float64(len(keys)-1) || len(a.Legend) != len(keys) {
				return fmt.Errorf("invalid score answer %q", id)
			}
			for k := range keys {
				if _, ok := a.Legend[k]; !ok {
					return fmt.Errorf("incomplete score legend %q", id)
				}
			}
		}
		if a.Confidence == nil || !probability(*a.Confidence) || len(a.Probabilities) != len(keys) {
			return fmt.Errorf("invalid decision distribution %q", id)
		}
		total := 0.0
		for k := range keys {
			p, ok := a.Probabilities[k]
			if !ok || !probability(p) {
				return fmt.Errorf("invalid decision probability %q", id)
			}
			total += p
		}
		if math.Abs(total-1) > 0.01 {
			return fmt.Errorf("invalid decision probability sum %q", id)
		}
	}
	return nil
}

// Decide performs one non-streaming evaluation. BaseURL is the API root,
// including /v1, matching the other adapters. No provider body is included in
// an error: validation errors can echo state or credentials back to the client.
func Decide(ctx context.Context, model Model, req DecisionRequest) (DecisionResponse, error) {
	permanent := func(err error) (DecisionResponse, error) { return DecisionResponse{}, &retryableError{err: err} }
	if err := ctx.Err(); err != nil {
		return DecisionResponse{}, err
	}
	if model.API != APITypeSafeSystemOne {
		return permanent(fmt.Errorf("unsupported decision API %q", model.API))
	}
	if model.APIKey == "" || model.ID == "" {
		return permanent(fmt.Errorf("decision model and API key are required"))
	}
	normalized, err := req.normalized()
	if err != nil {
		return permanent(err)
	}
	body, err := json.Marshal(struct {
		Model string `json:"model"`
		DecisionRequest
	}{model.ID, req})
	if err != nil {
		return permanent(err)
	}
	base := model.BaseURL
	if base == "" {
		base = "https://api.typesafe.ai/v1"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/systemone", bytes.NewReader(body))
	if err != nil {
		return permanent(fmt.Errorf("invalid decision endpoint"))
	}
	httpReq.Header.Set("Authorization", "Bearer "+model.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	client := newHTTPClient(time.Minute)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(httpReq)
	if err != nil {
		return DecisionResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return DecisionResponse{}, &retryableError{err: fmt.Errorf("typesafe: HTTP %d", res.StatusCode), retryable: isRetryable(res.StatusCode) || res.StatusCode == 529, statusCode: res.StatusCode, retryAfter: decisionRetryAfter(res.Header.Get("Retry-After"))}
	}
	var result DecisionResponse
	// A missing usage object must not silently turn a billable call into zero.
	var wire struct {
		Model   string                    `json:"model"`
		Answers map[string]DecisionAnswer `json:"answers"`
		Usage   *struct {
			Input  *int `json:"input_tokens"`
			Output *int `json:"output_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 8<<20))
	if err = decoder.Decode(&wire); err != nil {
		return permanent(fmt.Errorf("invalid typesafe response JSON"))
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return permanent(fmt.Errorf("invalid typesafe response trailer"))
	}
	if wire.Usage == nil || wire.Usage.Input == nil || wire.Usage.Output == nil {
		return permanent(fmt.Errorf("missing typesafe token usage"))
	}
	result = DecisionResponse{Model: wire.Model, Answers: wire.Answers, Usage: DecisionUsage{*wire.Usage.Input, *wire.Usage.Output}}
	if err = result.validate(normalized); err != nil {
		return permanent(err)
	}
	return result, nil
}

// decisionRetryAfter accepts both HTTP Retry-After forms. The caller's context
// bounds a long provider-requested delay; retrying earlier would violate it.
func decisionRetryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(at))
	}
	return 0
}

// DecideWithRetry retries transient HTTP and transport failures. Cancellation
// interrupts both requests and backoff; malformed answers are never retried.
func DecideWithRetry(ctx context.Context, model Model, req DecisionRequest, config RetryConfig) (DecisionResponse, error) {
	wait := config.InitialWait
	retryAfter := time.Duration(0)
	var lastErr error
	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := waitWithJitter(ctx, max(wait, retryAfter)); err != nil {
				return DecisionResponse{}, err
			}
			wait = calculateNextWait(wait, config)
		}
		result, err := Decide(ctx, model, req)
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return DecisionResponse{}, ctx.Err()
		}
		retryAfter = 0
		if re, ok := err.(*retryableError); ok {
			if !re.retryable {
				return DecisionResponse{}, err
			}
			retryAfter = re.retryAfter
		}
		lastErr = err
	}
	return DecisionResponse{}, lastErr
}
