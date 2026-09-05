package llm

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestResponsesTerminalContract(t *testing.T) {
	for _, tc := range []struct{ name, stream, want string }{
		{"eof", `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n", "terminal"},
		{"malformed", "data: {broken}\n", "decode"},
		{"missing response", `data: {"type":"response.completed"}` + "\n", "terminal"},
		{"wrong status", `data: {"type":"response.completed","response":{"status":"failed"}}` + "\n", "status"},
		{"failed", `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"provider broke"}}}` + "\n", "provider broke"},
		{"error", `data: {"type":"error","code":"server_error","message":"stream broke"}` + "\n", "stream broke"},
		{"content filter", `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}` + "\n", "content_filter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make(chan StreamEvent, 100)
			done := make(chan AssistantMessage, 1)
			errs := make(chan error, 1)
			processResponsesStream(context.Background(), io.NopCloser(strings.NewReader(tc.stream)), Model{}, events, done, errs)
			select {
			case err := <-errs:
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v, want %s", err, tc.want)
				}
			default:
				t.Fatal("stream silently succeeded")
			}
			for e := range events {
				if _, ok := e.(DoneEvent); ok {
					t.Fatal("failure emitted DoneEvent")
				}
			}
		})
	}
}

func TestResponsesIncompletePreservesUsage(t *testing.T) {
	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errs := make(chan error, 1)
	stream := `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":3,"output_tokens":7,"total_tokens":10,"input_tokens_details":{"cached_tokens":2}}}}` + "\n"
	processResponsesStream(context.Background(), io.NopCloser(strings.NewReader(stream)), Model{}, events, done, errs)
	select {
	case m := <-done:
		if m.StopReason != StopReasonMaxTokens || m.Usage.Output != 7 || m.Usage.CacheRead != 2 {
			t.Fatalf("message=%+v", m)
		}
	default:
		t.Fatal("missing terminal message")
	}
}

func TestResponsesRequestStatelessAndThinking(t *testing.T) {
	for _, tc := range []struct {
		model  string
		level  ThinkingLevel
		effort string
		valid  bool
	}{
		{"gpt-5.6-sol", ThinkingOff, "none", true}, {"gpt-5.6-sol", ThinkingMedium, "medium", true}, {"gpt-5.6-sol", ThinkingMinimal, "", false}, {"gpt-6-astra", ThinkingOff, "", false}, {"gpt-6-astra", ThinkingHigh, "high", true}, {"gpt-4.1", ThinkingOff, "", true}, {"gpt-4.1", ThinkingHigh, "", false}, {"unknown", ThinkingOff, "", false},
	} {
		t.Run(tc.model+string(tc.level), func(t *testing.T) {
			req, err := convertToResponsesRequest(Model{ID: tc.model}, Request{}, StreamOptions{ThinkingLevel: tc.level})
			if !tc.valid {
				if err == nil {
					t.Fatal("unsupported effort accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(req)
			var v map[string]any
			json.Unmarshal(b, &v)
			if v["store"] != false {
				t.Fatalf("store=%v", v["store"])
			}
			if !strings.Contains(string(b), "reasoning.encrypted_content") {
				t.Fatal("missing encrypted reasoning request")
			}
			if tc.effort != "" && v["reasoning"].(map[string]any)["effort"] != tc.effort {
				t.Fatalf("reasoning=%v", v["reasoning"])
			}
		})
	}
}

func TestOpaqueResponsesStateDoesNotReachOtherProviders(t *testing.T) {
	messages := []Message{AssistantMessage{Content: []ContentBlock{OpaqueContent{API: APIOpenAIResponses, Model: "gpt-5.6-sol", Data: json.RawMessage(`{"type":"reasoning","encrypted_content":"secret-ciphertext"}`)}, TextContent{Text: "answer"}}}}
	for name, value := range map[string]any{"chat": convertMessagesToOpenAI(nil, messages), "anthropic": convertMessagesToAnthropic(messages)} {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "secret-ciphertext") {
			t.Fatalf("%s leaks opaque state", name)
		}
	}
}

func TestResponsesIncompleteToolArgumentsRemainUnexecutable(t *testing.T) {
	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errs := make(chan error, 1)
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup"}}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"query\":"}}`,
		`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"query\":"}],"usage":{"input_tokens":3,"output_tokens":7,"total_tokens":10}}}`,
		"",
	}, "\n")
	processResponsesStream(context.Background(), io.NopCloser(strings.NewReader(stream)), Model{}, events, done, errs)
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	select {
	case m := <-done:
		if m.StopReason != StopReasonMaxTokens || m.Usage.Output != 7 {
			t.Fatalf("message=%+v", m)
		}
		if len(m.Content) != 1 {
			t.Fatalf("content=%v", m.Content)
		}
		call, ok := m.Content[0].(ToolCall)
		if !ok || call.Arguments != nil || call.ID != "call_1" {
			t.Fatalf("call=%+v", call)
		}
	default:
		t.Fatal("missing terminal result")
	}
}

func TestResponsesCapabilitySnapshotsAndTemperature(t *testing.T) {
	for _, id := range []string{"gpt-5.6", "gpt-5.6-sol", "gpt-5.6-sol-2026-08-01", "gpt-4.1-mini"} {
		m := Model{ID: id, API: APIOpenAIResponses}
		if !m.SupportsToolChoice() || !m.SupportsThinkingLevel(ThinkingOff) {
			t.Errorf("%s missing documented capability", id)
		}
	}
	for _, id := range []string{"text-embedding-3-small", "gpt-5.6-unknown", "gpt-5.2-pro", "gpt-5.6-sol-made-up"} {
		m := Model{ID: id, API: APIOpenAIResponses}
		if m.SupportsToolChoice() || m.SupportsThinkingLevel(ThinkingOff) {
			t.Errorf("%s invented capability", id)
		}
	}
	temp := 0.5
	if _, err := convertToResponsesRequest(Model{ID: "gpt-6-astra"}, Request{}, StreamOptions{Temperature: &temp}); err == nil {
		t.Fatal("Astra sampling accepted")
	}
	if _, err := convertToResponsesRequest(Model{ID: "gpt-5.2"}, Request{}, StreamOptions{ThinkingLevel: ThinkingHigh, Temperature: &temp}); err == nil {
		t.Fatal("reasoning sampling accepted")
	}
}

func TestResponsesLocalValidationIsNotRetryable(t *testing.T) {
	_, err := streamOpenAIResponses(context.Background(), Model{ID: "gpt-6-astra"}, Request{}, StreamOptions{ThinkingLevel: ThinkingOff})
	retry, ok := err.(*retryableError)
	if !ok || retry.retryable {
		t.Fatalf("local validation can retry: %T %v", err, err)
	}
	if _, err := convertToResponsesRequest(Model{ID: "unsupported"}, Request{ToolChoice: "answer"}, StreamOptions{}); err == nil {
		t.Fatal("unknown forced-tool capability accepted")
	}
}
