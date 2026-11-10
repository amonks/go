package llm

import (
	"context"
	"encoding/json/v2"
	"io"
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
				if string(b) != `{"type":"adaptive","display":"summarized"}` {
					t.Errorf("%q: thinking = %s", tt.level, b)
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
