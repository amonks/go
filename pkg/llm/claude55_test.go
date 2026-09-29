package llm

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestClaude55Requests(t *testing.T) {
	for _, id := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		t.Run(id, func(t *testing.T) {
			model := Model{ID: id, API: APIAnthropicMessages, MaxTokens: 128000}
			if model.SupportsToolChoice() {
				t.Error("forced tools must not be advertised")
			}
			for _, level := range []ThinkingLevel{"", ThinkingOff, ThinkingHigh, ThinkingXHigh, ThinkingMax} {
				req, err := convertToAnthropicRequest(model, Request{}, StreamOptions{ThinkingLevel: level})
				if err != nil {
					t.Fatal(err)
				}
				if level == ThinkingOff && id == "claude-sonnet-5-5" {
					if req.Thinking == nil || req.Thinking.Type != "between_tools" {
						t.Fatalf("off = %+v", req.Thinking)
					}
					b, _ := json.Marshal(req.Thinking)
					if string(b) != `{"type":"between_tools"}` {
						t.Errorf("between_tools takes no other fields: %s", b)
					}
				} else {
					b, _ := json.Marshal(req.Thinking)
					if string(b) != `{"type":"adaptive","display":"summarized"}` {
						t.Errorf("thinking = %s", b)
					}
				}
				if level == ThinkingMax && (req.OutputConfig == nil || req.OutputConfig.Effort != "max" || !model.SupportsThinkingLevel(level)) {
					t.Error("max effort must be supported and sent")
				}
				if _, err := convertToAnthropicRequest(model, Request{ToolChoice: "answer"}, StreamOptions{ThinkingLevel: level}); err == nil {
					t.Errorf("forced tool accepted at %q", level)
				}
			}
			if model.SupportsThinkingLevel(ThinkingOff) != (id == "claude-sonnet-5-5") {
				t.Error("wrong off capability")
			}
		})
	}
}

func TestClaude55ThinkingReplay(t *testing.T) {
	model := Model{ID: "claude-opus-5-5", API: APIAnthropicMessages, Provider: "anthropic", MaxTokens: 128000}
	stream := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-state"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"redacted_thinking","data":"redacted-state"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call1","name":"bash"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"pwd\"}"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		`data: {"type":"message_stop"}`, ""}, "\n")
	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errs := make(chan error, 1)
	processAnthropicStream(context.Background(), io.NopCloser(strings.NewReader(stream)), model, events, done, errs)
	msg := <-done
	otherProvider, otherEndpoint := model, model
	otherProvider.Provider = "other"
	otherEndpoint.BaseURL = "https://other.example"
	for _, target := range []Model{model, otherProvider, otherEndpoint, {ID: "claude-sonnet-5-5", API: model.API, Provider: model.Provider, MaxTokens: model.MaxTokens}} {
		req, err := convertToAnthropicRequest(target, Request{Messages: []Message{msg}}, StreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(req.Messages)
		if strings.Contains(string(b), "signed-state") != (target.ID == model.ID && target.Provider == model.Provider && target.BaseURL == model.BaseURL) {
			t.Errorf("replay on %s: %s", target.ID, b)
		}
		if target.ID == model.ID && target.Provider == model.Provider && target.BaseURL == model.BaseURL && !strings.Contains(string(b), `"thinking":""`) {
			t.Errorf("empty thinking must replay: %s", b)
		}
		if strings.Contains(string(b), "redacted-state") != (target.ID == model.ID && target.Provider == model.Provider && target.BaseURL == model.BaseURL) {
			t.Errorf("redacted replay on %s: %s", target.ID, b)
		}
		if !strings.Contains(string(b), `"command":"pwd"`) {
			t.Errorf("tool lost: %s", b)
		}
	}
}
