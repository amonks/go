package llm

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestProcessAnthropicStream_ReportsCacheUsage(t *testing.T) {
	stream := strings.Join([]string{
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"cache_creation_input_tokens\":4,\"cache_read_input_tokens\":6}}}",
		"data: {\"type\":\"message_stop\"}",
		"",
	}, "\n")

	body := io.NopCloser(strings.NewReader(stream))
	model := Model{ID: "claude", API: APIAnthropicMessages, Provider: "test"}

	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)

	processAnthropicStream(context.Background(), body, model, events, done, errCh)

	for range events {
	}

	select {
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	default:
	}

	msg := <-done
	if msg.Usage.CacheRead != 6 {
		t.Fatalf("Usage.CacheRead=%d, want %d", msg.Usage.CacheRead, 6)
	}
	if msg.Usage.CacheWrite != 4 {
		t.Fatalf("Usage.CacheWrite=%d, want %d", msg.Usage.CacheWrite, 4)
	}
	if msg.Usage.Total != 12 {
		t.Fatalf("Usage.Total=%d, want %d", msg.Usage.Total, 12)
	}
}

func TestConvertMessagesToAnthropic_MergesToolResults(t *testing.T) {
	// Simulate a conversation with parallel tool calls:
	// User asks -> Assistant makes 2 tool calls -> Both results come back
	messages := []Message{
		UserMessage{
			Role: "user",
			Content: []ContentBlock{
				TextContent{Type: "text", Text: "Please read two files"},
			},
			Timestamp: time.Now(),
		},
		AssistantMessage{
			Role: "assistant",
			Content: []ContentBlock{
				TextContent{Type: "text", Text: "I'll read both files."},
				ToolCall{Type: "toolCall", ID: "tool_1", Name: "read", Arguments: map[string]any{"path": "/a.txt"}},
				ToolCall{Type: "toolCall", ID: "tool_2", Name: "read", Arguments: map[string]any{"path": "/b.txt"}},
			},
			Timestamp: time.Now(),
		},
		ToolResultMessage{
			Role:       "toolResult",
			ToolCallID: "tool_1",
			ToolName:   "read",
			Content:    []ContentBlock{TextContent{Type: "text", Text: "content of a.txt"}},
			Timestamp:  time.Now(),
		},
		ToolResultMessage{
			Role:       "toolResult",
			ToolCallID: "tool_2",
			ToolName:   "read",
			Content:    []ContentBlock{TextContent{Type: "text", Text: "content of b.txt"}},
			Timestamp:  time.Now(),
		},
	}

	result := convertMessagesToAnthropic(messages)

	// Should have 3 messages: user, assistant, user (with merged tool results)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	// First message: user
	if result[0].Role != "user" {
		t.Errorf("expected first message role 'user', got %q", result[0].Role)
	}

	// Second message: assistant with text and tool calls
	if result[1].Role != "assistant" {
		t.Errorf("expected second message role 'assistant', got %q", result[1].Role)
	}
	if len(result[1].Content) != 3 {
		t.Errorf("expected 3 content blocks in assistant message, got %d", len(result[1].Content))
	}

	// Third message: user with BOTH tool results merged
	if result[2].Role != "user" {
		t.Errorf("expected third message role 'user', got %q", result[2].Role)
	}
	if len(result[2].Content) != 2 {
		t.Fatalf("expected 2 tool_result blocks in merged message, got %d", len(result[2].Content))
	}

	// Verify both tool results are present
	if result[2].Content[0].Type != "tool_result" {
		t.Errorf("expected first content type 'tool_result', got %q", result[2].Content[0].Type)
	}
	if result[2].Content[0].ToolUseID != "tool_1" {
		t.Errorf("expected first tool_use_id 'tool_1', got %q", result[2].Content[0].ToolUseID)
	}
	if result[2].Content[1].Type != "tool_result" {
		t.Errorf("expected second content type 'tool_result', got %q", result[2].Content[1].Type)
	}
	if result[2].Content[1].ToolUseID != "tool_2" {
		t.Errorf("expected second tool_use_id 'tool_2', got %q", result[2].Content[1].ToolUseID)
	}
}

func TestConvertMessagesToAnthropic_ExcludesThinkingBlocks(t *testing.T) {
	messages := []Message{
		AssistantMessage{
			Role: "assistant",
			Content: []ContentBlock{
				ThinkingContent{Type: "thinking", Thinking: "Let me think about this..."},
				TextContent{Type: "text", Text: "Here's my answer."},
			},
			Timestamp: time.Now(),
		},
	}

	result := convertMessagesToAnthropic(messages)

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}

	// Should only have the text content, not the thinking content
	if len(result[0].Content) != 1 {
		t.Fatalf("expected 1 content block (thinking excluded), got %d", len(result[0].Content))
	}

	if result[0].Content[0].Type != "text" {
		t.Errorf("expected content type 'text', got %q", result[0].Content[0].Type)
	}
	if result[0].Content[0].Text != "Here's my answer." {
		t.Errorf("expected text 'Here's my answer.', got %q", result[0].Content[0].Text)
	}
}

func TestConvertMessagesToAnthropic_ToolResultsWithInterleaved(t *testing.T) {
	// Test that tool results separated by other messages are NOT merged
	messages := []Message{
		UserMessage{
			Role:      "user",
			Content:   []ContentBlock{TextContent{Type: "text", Text: "first question"}},
			Timestamp: time.Now(),
		},
		AssistantMessage{
			Role: "assistant",
			Content: []ContentBlock{
				ToolCall{Type: "toolCall", ID: "tool_1", Name: "read", Arguments: map[string]any{}},
			},
			Timestamp: time.Now(),
		},
		ToolResultMessage{
			Role:       "toolResult",
			ToolCallID: "tool_1",
			Content:    []ContentBlock{TextContent{Type: "text", Text: "result 1"}},
			Timestamp:  time.Now(),
		},
		AssistantMessage{
			Role: "assistant",
			Content: []ContentBlock{
				TextContent{Type: "text", Text: "Got it. Let me do another."},
				ToolCall{Type: "toolCall", ID: "tool_2", Name: "read", Arguments: map[string]any{}},
			},
			Timestamp: time.Now(),
		},
		ToolResultMessage{
			Role:       "toolResult",
			ToolCallID: "tool_2",
			Content:    []ContentBlock{TextContent{Type: "text", Text: "result 2"}},
			Timestamp:  time.Now(),
		},
	}

	result := convertMessagesToAnthropic(messages)

	// Should have 5 messages: user, assistant, user(tool1), assistant, user(tool2)
	if len(result) != 5 {
		t.Fatalf("expected 5 messages (tool results not merged due to interleaving), got %d", len(result))
	}

	// Verify the tool results are in separate messages
	if result[2].Content[0].ToolUseID != "tool_1" {
		t.Errorf("expected tool_use_id 'tool_1' in message 3")
	}
	if result[4].Content[0].ToolUseID != "tool_2" {
		t.Errorf("expected tool_use_id 'tool_2' in message 5")
	}
}

func TestConvertMessagesToAnthropic_EmptyAssistantMessageExcluded(t *testing.T) {
	// If an assistant message only had thinking content, it should be excluded
	messages := []Message{
		UserMessage{
			Role:      "user",
			Content:   []ContentBlock{TextContent{Type: "text", Text: "question"}},
			Timestamp: time.Now(),
		},
		AssistantMessage{
			Role: "assistant",
			Content: []ContentBlock{
				ThinkingContent{Type: "thinking", Thinking: "thinking only..."},
			},
			Timestamp: time.Now(),
		},
	}

	result := convertMessagesToAnthropic(messages)

	// Should only have the user message since assistant message becomes empty
	if len(result) != 1 {
		t.Fatalf("expected 1 message (empty assistant excluded), got %d", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", result[0].Role)
	}
}

// A forced tool and extended thinking are mutually exclusive at the
// API: the pair is refused here, with the reason, rather than by the
// provider's 400. On an adaptive family an unset level is thinking.
func TestConvertToAnthropicRequest_ForcedToolRefusesThinking(t *testing.T) {
	req := Request{
		Messages:   []Message{UserMessage{Role: "user", Content: []ContentBlock{TextContent{Type: "text", Text: "hello"}}, Timestamp: time.Now()}},
		Tools:      []Tool{{Name: "set_title", Description: "record the title"}},
		ToolChoice: "set_title",
	}
	for name, tc := range map[string]struct {
		model string
		opts  StreamOptions
		ok    bool
	}{
		"pre-adaptive, unset":     {"claude-haiku-4-5", StreamOptions{}, true},
		"pre-adaptive, off":       {"claude-haiku-4-5", StreamOptions{ThinkingLevel: ThinkingOff}, true},
		"pre-adaptive, thinking":  {"claude-haiku-4-5", StreamOptions{ThinkingLevel: ThinkingHigh}, false},
		"adaptive, off":           {"claude-sonnet-5", StreamOptions{ThinkingLevel: ThinkingOff}, true},
		"adaptive, unset":         {"claude-sonnet-5", StreamOptions{}, false},
		"adaptive, thinking":      {"claude-sonnet-5", StreamOptions{ThinkingLevel: ThinkingLow}, false},
		"always-thinking, off":    {"claude-fable-5", StreamOptions{ThinkingLevel: ThinkingOff}, false},
		"always-thinking, unset":  {"claude-fable-5", StreamOptions{}, false},
	} {
		model := Model{ID: tc.model, MaxTokens: testCeiling}
		_, err := convertToAnthropicRequest(model, req, tc.opts)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
}

func TestConvertToAnthropicRequest_ForcesToolChoice(t *testing.T) {
	req := Request{
		Messages: []Message{
			UserMessage{
				Role:      "user",
				Content:   []ContentBlock{TextContent{Type: "text", Text: "hello"}},
				Timestamp: time.Now(),
			},
		},
		Tools:      []Tool{{Name: "recommend", Description: "pick movies"}},
		ToolChoice: "recommend",
	}

	anthropicReq := mustAnthropicRequest(t, Model{ID: "claude"}, req, StreamOptions{})

	if anthropicReq.ToolChoice == nil {
		t.Fatal("expected tool_choice to be set when Request.ToolChoice is non-empty")
	}
	if anthropicReq.ToolChoice.Type != "tool" {
		t.Errorf("tool_choice type = %q, want %q", anthropicReq.ToolChoice.Type, "tool")
	}
	if anthropicReq.ToolChoice.Name != "recommend" {
		t.Errorf("tool_choice name = %q, want %q", anthropicReq.ToolChoice.Name, "recommend")
	}
}

func TestConvertToAnthropicRequest_NoToolChoiceByDefault(t *testing.T) {
	req := Request{
		Messages: []Message{
			UserMessage{
				Role:      "user",
				Content:   []ContentBlock{TextContent{Type: "text", Text: "hello"}},
				Timestamp: time.Now(),
			},
		},
		Tools: []Tool{{Name: "recommend"}},
	}

	anthropicReq := mustAnthropicRequest(t, Model{ID: "claude"}, req, StreamOptions{})
	if anthropicReq.ToolChoice != nil {
		t.Errorf("expected no tool_choice by default, got %+v", anthropicReq.ToolChoice)
	}
}

func TestConvertToAnthropicRequest_AddsCacheControlMarkers(t *testing.T) {
	req := Request{
		System: []SystemBlock{{Text: "system prompt", CacheBreakpoint: true}},
		Messages: []Message{
			UserMessage{
				Role: "user",
				Content: []ContentBlock{
					TextContent{Type: "text", Text: "hello"},
				},
				Timestamp: time.Now(),
			},
		},
		Tools: []Tool{
			{
				Name:        "read",
				Description: "read file",
				Parameters: struct {
					Path string `json:"path"`
				}{},
			},
		},
	}

	t.Run("cache retention", func(t *testing.T) {
		anthropicReq := mustAnthropicRequest(t, Model{ID: "claude"}, req, StreamOptions{CacheRetention: CacheShort})

		if len(anthropicReq.System) == 0 || anthropicReq.System[len(anthropicReq.System)-1].CacheControl == nil {
			t.Fatalf("expected cache_control on system prompt")
		}
		if len(anthropicReq.Tools) == 0 || anthropicReq.Tools[len(anthropicReq.Tools)-1].CacheControl == nil {
			t.Fatalf("expected cache_control on tool definitions")
		}

		foundUser := false
		for _, msg := range anthropicReq.Messages {
			if msg.Role != "user" {
				continue
			}
			if len(msg.Content) == 0 {
				continue
			}
			foundUser = true
			if msg.Content[len(msg.Content)-1].CacheControl == nil {
				t.Fatalf("expected cache_control on last user message content")
			}
			break
		}
		if !foundUser {
			t.Fatal("expected a user message to receive cache_control")
		}
	})

	t.Run("default retention", func(t *testing.T) {
		anthropicReq := mustAnthropicRequest(t, Model{ID: "claude"}, req, StreamOptions{})

		if len(anthropicReq.System) > 0 && anthropicReq.System[len(anthropicReq.System)-1].CacheControl != nil {
			t.Fatal("expected no cache_control on system prompt")
		}
		if len(anthropicReq.Tools) > 0 && anthropicReq.Tools[len(anthropicReq.Tools)-1].CacheControl != nil {
			t.Fatal("expected no cache_control on tool definitions")
		}
		for _, msg := range anthropicReq.Messages {
			if msg.Role != "user" || len(msg.Content) == 0 {
				continue
			}
			if msg.Content[len(msg.Content)-1].CacheControl != nil {
				t.Fatal("expected no cache_control on user message content")
			}
			break
		}
	})
}

func TestConvertToAnthropicRequest_AdaptiveThinkingModels(t *testing.T) {
	req := Request{
		Messages: []Message{
			UserMessage{Role: "user", Content: []ContentBlock{TextContent{Type: "text", Text: "hi"}}},
		},
	}

	t.Run("claude 5 era models use adaptive thinking with effort", func(t *testing.T) {
		for _, id := range []string{
			"claude-fable-5",
			"claude-mythos-5",
			"claude-opus-5",
			"claude-sonnet-5",
			"claude-opus-4-6",
			"claude-opus-4-7",
			"claude-opus-4-8",
			"claude-sonnet-4-6",
		} {
			anthropicReq := mustAnthropicRequest(t, Model{ID: id}, req, StreamOptions{ThinkingLevel: ThinkingMedium})

			if anthropicReq.Thinking == nil || anthropicReq.Thinking.Type != "adaptive" {
				t.Fatalf("%s: expected thinking type adaptive, got %+v", id, anthropicReq.Thinking)
			}
			if anthropicReq.Thinking.BudgetTokens != 0 {
				t.Fatalf("%s: expected no budget_tokens, got %d", id, anthropicReq.Thinking.BudgetTokens)
			}
			if anthropicReq.OutputConfig == nil || anthropicReq.OutputConfig.Effort != "medium" {
				t.Fatalf("%s: expected output_config effort medium, got %+v", id, anthropicReq.OutputConfig)
			}
			if anthropicReq.Temperature != nil {
				t.Fatalf("%s: expected no temperature (rejected on this model family), got %v", id, *anthropicReq.Temperature)
			}
		}
	})

	t.Run("budget_tokens omitted from adaptive thinking JSON", func(t *testing.T) {
		anthropicReq := mustAnthropicRequest(t, Model{ID: "claude-sonnet-5"}, req, StreamOptions{ThinkingLevel: ThinkingMedium})
		body, err := json.Marshal(anthropicReq)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "budget_tokens") {
			t.Fatalf("expected budget_tokens absent from request JSON, got %s", body)
		}
	})

	t.Run("effort levels map from thinking levels", func(t *testing.T) {
		for level, want := range map[ThinkingLevel]string{
			ThinkingMinimal: "low",
			ThinkingLow:     "low",
			ThinkingMedium:  "medium",
			ThinkingHigh:    "high",
			ThinkingXHigh:   "xhigh",
		} {
			anthropicReq := mustAnthropicRequest(t, Model{ID: "claude-sonnet-5"}, req, StreamOptions{ThinkingLevel: level})
			if anthropicReq.OutputConfig == nil || anthropicReq.OutputConfig.Effort != want {
				t.Fatalf("level %s: expected effort %q, got %+v", level, want, anthropicReq.OutputConfig)
			}
		}
	})

	t.Run("older models keep enabled thinking with budget", func(t *testing.T) {
		for _, id := range []string{"claude-haiku-4-5", "claude-sonnet-4-5", "claude-opus-4-5", "claude"} {
			anthropicReq := mustAnthropicRequest(t, Model{ID: id}, req, StreamOptions{ThinkingLevel: ThinkingMedium})

			if anthropicReq.Thinking == nil || anthropicReq.Thinking.Type != "enabled" {
				t.Fatalf("%s: expected thinking type enabled, got %+v", id, anthropicReq.Thinking)
			}
			if anthropicReq.Thinking.BudgetTokens != 10000 {
				t.Fatalf("%s: expected budget 10000, got %d", id, anthropicReq.Thinking.BudgetTokens)
			}
			if anthropicReq.OutputConfig != nil {
				t.Fatalf("%s: expected no output_config, got %+v", id, anthropicReq.OutputConfig)
			}
			if anthropicReq.Temperature == nil || *anthropicReq.Temperature != 1 {
				t.Fatalf("%s: expected temperature 1 for enabled thinking, got %v", id, anthropicReq.Temperature)
			}
		}
	})

	t.Run("thinking off disables thinking and sends no output_config", func(t *testing.T) {
		anthropicReq := mustAnthropicRequest(t, Model{ID: "claude-sonnet-5"}, req, StreamOptions{ThinkingLevel: ThinkingOff})
		if anthropicReq.Thinking == nil || anthropicReq.Thinking.Type != "disabled" {
			t.Fatalf("expected thinking type disabled, got %+v", anthropicReq.Thinking)
		}
		if anthropicReq.OutputConfig != nil {
			t.Fatalf("expected no output_config, got %+v", anthropicReq.OutputConfig)
		}
	})
}

func TestProcessAnthropicStream_UnknownBlockTypeDoesNotShiftIndices(t *testing.T) {
	// A block type the parser doesn't recognize (e.g. redacted_thinking)
	// must not desync the server's content indices from our accumulated
	// content — the tool call at index 1 must still get its arguments.
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"recommend"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"recommendations\":"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"[{\"number\":3,\"reason\":\"x\"}]}"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	body := io.NopCloser(strings.NewReader(stream))
	model := Model{ID: "claude-sonnet-5", API: APIAnthropicMessages, Provider: "test"}

	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)

	go processAnthropicStream(context.Background(), body, model, events, done, errCh)
	for range events {
	}

	select {
	case msg := <-done:
		var tool *ToolCall
		for _, block := range msg.Content {
			if tc, ok := block.(ToolCall); ok {
				tool = &tc
			}
		}
		if tool == nil {
			t.Fatalf("expected a tool call in content, got %#v", msg.Content)
		}
		recs, ok := tool.Arguments["recommendations"].([]any)
		if !ok || len(recs) != 1 {
			t.Fatalf("expected parsed tool arguments, got %#v", tool.Arguments)
		}
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	}
}

// Thinking is on by default on the adaptive-thinking families, and its
// tokens come out of max_tokens, so omitting the parameter is not the
// same as turning thinking off. ThinkingOff has to say so explicitly —
// on the families that accept it.
func TestConvertToAnthropicRequest_ThinkingOff(t *testing.T) {
	for _, tc := range []struct {
		model string
		level ThinkingLevel
		want  string // "" means the request carries no thinking parameter
	}{
		{"claude-sonnet-5", ThinkingOff, "disabled"},
		{"claude-opus-5", ThinkingOff, "disabled"},
		{"claude-sonnet-4-6", ThinkingOff, "disabled"},
		// Unset is the model's own default, which the agent loop wants.
		{"claude-sonnet-5", "", ""},
		// Omitting thinking already means off before the 4.6 families.
		{"claude-haiku-4-5", ThinkingOff, ""},
		// Fable and Mythos think unconditionally and 400 on "disabled".
		{"claude-fable-5", ThinkingOff, ""},
		{"claude-mythos-5", ThinkingOff, ""},
		// A level still asks for thinking.
		{"claude-sonnet-5", ThinkingLow, "adaptive"},
	} {
		req := mustAnthropicRequest(t,
			Model{ID: tc.model, API: APIAnthropicMessages},
			Request{},
			StreamOptions{ThinkingLevel: tc.level},
		)
		got := ""
		if req.Thinking != nil {
			got = req.Thinking.Type
		}
		if got != tc.want {
			t.Errorf("%s with thinking %q: type %q, want %q", tc.model, tc.level, got, tc.want)
		}
	}
}

// testCeiling is an arbitrary output ceiling for a model under test.
const testCeiling = 5000

// mustAnthropicRequest converts on behalf of tests that are about
// something other than the output cap, standing in a ceiling for a
// model that names none so the required max_tokens is satisfied.
func mustAnthropicRequest(t *testing.T, model Model, req Request, opts StreamOptions) anthropicRequest {
	t.Helper()
	if model.MaxTokens == 0 && opts.MaxTokens == nil {
		model.MaxTokens = testCeiling
	}
	out, err := convertToAnthropicRequest(model, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
