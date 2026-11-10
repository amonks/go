package llm

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Fable line (and its Mythos twins) thinks unconditionally, takes
// every effort through max, and returns the notes it writes between
// tool calls as thinking blocks that are empty unless a display is
// asked for — so, as on Claude 5.5, every request asks for summaries.
// https://platform.claude.com/docs/en/models/fable-5-2/migration-guide
func TestFableRequests(t *testing.T) {
	for _, id := range []string{
		"claude-fable-5-2", "claude-mythos-5-2",
		"claude-fable-5-1", "claude-mythos-5-1",
		"claude-fable-5", "claude-mythos-5",
	} {
		t.Run(id, func(t *testing.T) {
			model := Model{ID: id, API: APIAnthropicMessages, MaxTokens: 128000}
			if !model.SupportsStructuredOutput() {
				t.Error("native structured output must be advertised")
			}
			if model.SupportsToolChoice() {
				t.Error("forced tools must not be advertised")
			}
			if model.SupportsThinkingLevel(ThinkingOff) {
				t.Error("thinking cannot be turned off")
			}
			for _, tt := range []struct {
				level  ThinkingLevel
				effort string
			}{
				{"", ""},
				{ThinkingOff, ""},
				{ThinkingLow, "low"},
				{ThinkingHigh, "high"},
				{ThinkingXHigh, "xhigh"},
				{ThinkingMax, "max"},
			} {
				if tt.level != ThinkingOff && !model.SupportsThinkingLevel(tt.level) {
					t.Errorf("thinking level %q must be supported", tt.level)
				}
				req, err := convertToAnthropicRequest(model, Request{}, StreamOptions{ThinkingLevel: tt.level})
				if err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(req.Thinking)
				want := `{"type":"adaptive","display":"summarized"}`
				if modelBindsEveryAccount(id) {
					want = `{"type":"adaptive","display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}}`
				}
				if string(b) != want {
					t.Errorf("%q: thinking = %s, want %s", tt.level, b, want)
				}
				effort := ""
				if req.OutputConfig != nil {
					effort = req.OutputConfig.Effort
				}
				if effort != tt.effort {
					t.Errorf("%q: effort = %q, want %q", tt.level, effort, tt.effort)
				}
				if req.Temperature != nil {
					t.Errorf("%q: sampling sent", tt.level)
				}
				if _, err := convertToAnthropicRequest(model, Request{ToolChoice: "answer"}, StreamOptions{ThinkingLevel: tt.level}); err == nil {
					t.Errorf("forced tool accepted at %q", tt.level)
				}
			}
			req, err := convertToAnthropicRequest(model, Request{OutputSchema: GenerateSchema(outputFixture{})}, StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if req.OutputConfig == nil || req.OutputConfig.Format == nil || req.ToolChoice != nil {
				t.Errorf("structured answer = %+v, tool choice %+v", req.OutputConfig, req.ToolChoice)
			}
		})
	}
}

// Fable 5.2's signed thinking replays to the model, provider, and
// endpoint that produced it, and nowhere else.
func TestFable52ThinkingReplay(t *testing.T) {
	testClaude55ThinkingReplay(t, "claude-fable-5-2")
}

// With summarized display, a thinking block carries text as well as a
// signature, and the API accepts it back only unchanged: the summary,
// streamed in pieces, must replay byte for byte beside its signature.
func TestSummarizedThinkingReplaysUnchanged(t *testing.T) {
	for _, id := range []string{"claude-fable-5-2", "claude-opus-5-5"} {
		t.Run(id, func(t *testing.T) {
			model := Model{ID: id, API: APIAnthropicMessages, Provider: "anthropic", MaxTokens: 128000}
			stream := strings.Join([]string{
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Listing the \"work\"space "}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first.\n"}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-state"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call1","name":"bash"}}`,
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
				`data: {"type":"content_block_stop","index":1}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
				`data: {"type":"message_stop"}`, ""}, "\n")
			events := make(chan StreamEvent, 100)
			done := make(chan AssistantMessage, 1)
			errs := make(chan error, 1)
			processAnthropicStream(context.Background(), io.NopCloser(strings.NewReader(stream)), model, events, done, errs)
			msg := <-done
			req, err := convertToAnthropicRequest(model, Request{Messages: []Message{msg, ToolResultMessage{
				ToolCallID: "call1",
				Content:    []ContentBlock{TextContent{Text: "a b"}},
			}}}, StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(req.Messages[0].Content[0], json.Deterministic(true))
			want := `{"type":"thinking","thinking":"Listing the \"work\"space first.\n","signature":"signed-state"}`
			if string(b) != want {
				t.Errorf("replayed block = %s, want %s", b, want)
			}
		})
	}
}

// Fable and Mythos 5.2, and Mythos 5.1, check every account's replayed
// thinking against the history it was produced in, and by default
// refuse a request whose system, tools, or earlier messages changed —
// a 400 no retry clears. Asking the API to drop the stale blocks
// instead keeps a conversation alive when its prompt changes (a deploy
// editing an agent's instructions), at the cost of re-thinking. The
// other models check only newer accounts and leave the field unset:
// setting it would turn the check on.
// https://platform.claude.com/docs/en/models/fable-5-2/migration-guide
func TestBlockBindingDropsStaleThinking(t *testing.T) {
	for id, binds := range map[string]bool{
		"claude-fable-5-2":  true,
		"claude-mythos-5-2": true,
		"claude-mythos-5-1": true,
		"claude-fable-5-1":  false,
		"claude-fable-5":    false,
		"claude-opus-5-5":   false,
		"claude-sonnet-5-5": false,
		"claude-haiku-5-5":  false,
	} {
		t.Run(id, func(t *testing.T) {
			var gotBeta string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBeta = r.Header.Get("anthropic-beta")
				_ = json.UnmarshalRead(r.Body, &gotBody)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
			}))
			defer srv.Close()
			model := Model{ID: id, API: APIAnthropicMessages, BaseURL: srv.URL, MaxTokens: 1024}
			h, err := Stream(context.Background(), model, Request{}, StreamOptions{CacheRetention: CacheShort})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = h.Wait()
			thinking, _ := gotBody["thinking"].(map[string]any)
			_, hasBinding := thinking["block_binding"]
			if hasBinding != binds {
				t.Errorf("block_binding sent = %v, want %v: %v", hasBinding, binds, thinking)
			}
			if strings.Contains(gotBeta, "thinking-binding-controls-2026-08-01") != binds {
				t.Errorf("anthropic-beta = %q", gotBeta)
			}
			if !strings.Contains(gotBeta, "prompt-caching-2024-07-31") {
				t.Errorf("caching beta lost: %q", gotBeta)
			}
		})
	}
}

// A request asking the API to drop stale thinking learns what it
// dropped from message_start; the count surfaces on the message, so a
// history edit shows up in the caller's events instead of only as
// quiet re-thinking. Entries of other types and unknown future ones
// are not drops.
func TestThinkingDroppedCounted(t *testing.T) {
	model := Model{ID: "claude-fable-5-2", API: APIAnthropicMessages, Provider: "anthropic", MaxTokens: 1024}
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":5,"output_tokens":1},"input_transformations":[` +
			`{"type":"thinking_dropped","path":"messages.1.content.0","reason":"prefix_binding_mismatch"},` +
			`{"type":"thinking_dropped","path":"messages.3.content.0","reason":"prefix_binding_mismatch"},` +
			`{"type":"thinking_mismatch_allowed","path":"messages.5.content.0","reason":"prefix_binding_mismatch"},` +
			`{"type":"something_new"}]}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`data: {"type":"message_stop"}`, ""}, "\n")
	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errs := make(chan error, 1)
	processAnthropicStream(context.Background(), io.NopCloser(strings.NewReader(stream)), model, events, done, errs)
	if msg := <-done; msg.ThinkingDropped != 2 {
		t.Errorf("ThinkingDropped = %d, want 2", msg.ThinkingDropped)
	}
}
