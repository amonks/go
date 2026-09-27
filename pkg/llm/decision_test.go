package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func decisionFixture() DecisionRequest {
	return DecisionRequest{State: map[string]any{"message": "Please refund me"}, Questions: map[string]DecisionQuestion{
		"urgent":   {Type: DecisionNoul, Instructions: "Is it urgent?"},
		"team":     {Type: DecisionChoice, Instructions: "Which team?", Criteria: map[string]any{"billing": nil, "sales": "New sales"}},
		"severity": {Type: DecisionScore, Instructions: "Severity?", Criteria: []any{"low", "high"}},
	}}
}

const decisionResponseJSON = `{"model":"jev-1.13.0","answers":{"urgent":{"type":"noul","noul":0},"team":{"type":"choice","choice":"billing","probabilities":{"billing":1,"sales":0},"confidence":1},"severity":{"type":"score","score":0,"legend":{"0":"low","1":"high"},"probabilities":{"0":1,"1":0},"confidence":1}},"usage":{"input_tokens":120,"output_tokens":30}}`

func TestDecideWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("bad request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["model"]) != `"jev-latest"` || len(body) != 3 {
			t.Errorf("body=%s", body)
		}
		w.Write([]byte(decisionResponseJSON))
	}))
	defer srv.Close()
	got, err := Decide(context.Background(), Model{ID: "jev-latest", API: APITypeSafeSystemOne, BaseURL: srv.URL + "/v1", APIKey: "secret"}, decisionFixture())
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "jev-1.13.0" || got.Usage.InputTokens != 120 || got.Answers["urgent"].Noul == nil || *got.Answers["urgent"].Noul != 0 || *got.Answers["severity"].Score != 0 {
		t.Fatalf("result=%+v", got)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"noul":0`) || !strings.Contains(string(raw), `"confidence":1`) {
		t.Fatal(string(raw))
	}
}

func TestDecideValidationAndErrors(t *testing.T) {
	for _, modify := range []func(*DecisionRequest){
		func(r *DecisionRequest) { r.State = nil },
		func(r *DecisionRequest) { r.State = 42 },
		func(r *DecisionRequest) { r.Questions = nil },
		func(r *DecisionRequest) { r.Questions["urgent"] = DecisionQuestion{Type: "text", Instructions: "x"} },
		func(r *DecisionRequest) {
			r.Questions["team"] = DecisionQuestion{Type: DecisionChoice, Instructions: "x", Criteria: []string{"a", "b"}}
		},
		func(r *DecisionRequest) {
			r.Questions["severity"] = DecisionQuestion{Type: DecisionScore, Instructions: "x", Criteria: []string{"a"}}
		},
	} {
		r := decisionFixture()
		modify(&r)
		if r.Validate() == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	for _, status := range []int{401, 422, 429, 529} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte("secret prompt key"))
			}))
			defer srv.Close()
			_, err := Decide(context.Background(), Model{ID: "jev-latest", API: APITypeSafeSystemOne, BaseURL: srv.URL, APIKey: "secret"}, decisionFixture())
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDecideRejectsIncompleteResponse(t *testing.T) {
	for _, body := range []string{`{}`, strings.Replace(decisionResponseJSON, `"noul":0`, `"noul":2`, 1), strings.Replace(decisionResponseJSON, `"noul":0`, `"wrong":0`, 1), strings.Replace(decisionResponseJSON, `"input_tokens":120`, `"input_tokens":-1`, 1)} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		_, err := Decide(context.Background(), Model{ID: "jev-latest", API: APITypeSafeSystemOne, BaseURL: srv.URL, APIKey: "secret"}, decisionFixture())
		srv.Close()
		if err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestDecideRetryAndCancellation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(529)
			return
		}
		w.Write([]byte(decisionResponseJSON))
	}))
	defer srv.Close()
	model := Model{ID: "jev-latest", API: APITypeSafeSystemOne, BaseURL: srv.URL, APIKey: "secret"}
	cfg := RetryConfig{MaxRetries: 1, InitialWait: time.Nanosecond, MaxWait: time.Nanosecond, Multiplier: 1}
	if _, err := DecideWithRetry(context.Background(), model, decisionFixture(), cfg); err != nil || calls != 2 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecideWithRetry(ctx, model, decisionFixture(), cfg); err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
}

// Fake time keeps a provider Retry-After from slowing the suite, while proving
// that both forms are honored and a deadline interrupts the backoff.
func TestDecisionRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, header := range []string{"5", time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)} {
			if got := decisionRetryAfter(header); got != 5*time.Second {
				t.Fatalf("Retry-After %q = %v", header, got)
			}
		}
		old := Transport
		defer func() { Transport = old }()
		calls := 0
		Transport = decisionTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"5"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := DecideWithRetry(ctx, Model{ID: "jev-latest", API: APITypeSafeSystemOne, APIKey: "test"}, decisionFixture(), DefaultRetryConfig())
		if err != context.DeadlineExceeded || calls != 1 {
			t.Fatalf("calls=%d error=%v", calls, err)
		}
	})
}

type decisionTransport func(*http.Request) (*http.Response, error)

func (f decisionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
