package llm

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestProcessResponsesStream_FunctionCallArgumentsDeltaUsesItemID(t *testing.T) {
	// This test exercises the Responses API streaming shape where
	// response.function_call_arguments.delta events carry item_id/output_index
	// instead of embedding an "item" object.

	stream := strings.Join([]string{
		"data: {\"type\":\"response.created\"}",
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_123\",\"call_id\":\"call_123\",\"name\":\"bash\",\"arguments\":\"\"}}",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_123\",\"output_index\":0,\"delta\":\"{\\\"command\\\":\\\"touch hello-world.txt\\\"}\"}",
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_123\",\"call_id\":\"call_123\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"touch hello-world.txt\\\"}\"}}",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":3}}}}",
		"",
	}, "\n")

	body := io.NopCloser(strings.NewReader(stream))

	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)

	model := Model{ID: "gpt-5.2", API: APIOpenAIResponses, Provider: "test"}

	processResponsesStream(context.Background(), body, model, events, done, errCh)

	// Drain events; verify the final message contains a ToolCall with parsed arguments.
	for range events {
	}

	select {
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	default:
	}

	msg := <-done
	if msg.Usage.CacheRead != 3 {
		t.Fatalf("Usage.CacheRead=%d, want %d", msg.Usage.CacheRead, 3)
	}
	if msg.StopReason != StopReasonToolUse {
		t.Fatalf("StopReason=%q, want %q", msg.StopReason, StopReasonToolUse)
	}

	var found ToolCall
	for _, block := range msg.Content {
		if tc, ok := block.(ToolCall); ok {
			found = tc
			break
		}
	}
	if found.Name != "bash" {
		t.Fatalf("tool name=%q, want %q", found.Name, "bash")
	}
	if found.ID != "call_123" {
		t.Fatalf("tool call id=%q, want %q", found.ID, "call_123")
	}
	cmd, _ := found.Arguments["command"].(string)
	if cmd != "touch hello-world.txt" {
		t.Fatalf("arguments.command=%q, want %q", cmd, "touch hello-world.txt")
	}
}
