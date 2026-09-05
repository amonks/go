package llm

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"
)

func TestOpenAICachedUsageIsExclusive(t *testing.T) {
	for _, api := range []API{APIOpenAIResponses, APIOpenAICompletions} {
		t.Run(string(api), func(t *testing.T) {
			events := make(chan StreamEvent, 100)
			done := make(chan AssistantMessage, 1)
			errs := make(chan error, 1)
			model := Model{Cost: Cost{Input: 2, CacheRead: .2, Output: 10}}
			if api == APIOpenAIResponses {
				processResponsesStream(context.Background(), io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100,"input_tokens_details":{"cached_tokens":800}}}}`+"\n")), model, events, done, errs)
			} else {
				processOpenAIStream(context.Background(), io.NopCloser(strings.NewReader(`data: {"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100,"prompt_tokens_details":{"cached_tokens":800}}}`+"\ndata: [DONE]\n")), model, events, done, errs)
			}
			msg := <-done
			if msg.Usage.Input != 200 || msg.Usage.CacheRead != 800 || msg.Usage.Total != 1100 || math.Abs(msg.Usage.Cost.Total-.00156) > 1e-10 {
				t.Fatalf("usage=%+v", msg.Usage)
			}
		})
	}
}

func TestResponsesRefusalIsVisible(t *testing.T) {
	for _, tc := range []struct{ name, stream string }{
		{"completed", `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_1","content":[{"type":"refusal","refusal":"I cannot do that."}]}]}}` + "\n"},
		{"stream delta", `data: {"type":"response.refusal.delta","item_id":"msg_1","delta":"I cannot "}` + "\n" + `data: {"type":"response.refusal.delta","item_id":"msg_1","delta":"do that."}` + "\n" + `data: {"type":"response.refusal.done","item_id":"msg_1","refusal":"I cannot do that."}` + "\n" + `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n"},
		{"stream done", `data: {"type":"response.refusal.done","item_id":"msg_1","refusal":"I cannot do that."}` + "\n" + `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make(chan StreamEvent, 100)
			done := make(chan AssistantMessage, 1)
			errs := make(chan error, 1)
			processResponsesStream(context.Background(), io.NopCloser(strings.NewReader(tc.stream)), Model{}, events, done, errs)
			msg := <-done
			if len(msg.Content) != 1 {
				t.Fatalf("content=%v", msg.Content)
			}
			text, ok := msg.Content[0].(TextContent)
			if !ok || text.Text != "I cannot do that." {
				t.Fatalf("content=%v", msg.Content)
			}
		})
	}
}

func TestResponsesStreamPreservesMessagePhase(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"msg_1","type":"message","phase":"commentary"}}`,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"First."}`,
		`data: {"type":"response.output_item.done","item":{"id":"msg_1","type":"message","phase":"commentary","content":[{"type":"output_text","text":"First."}]}}`,
		`data: {"type":"response.output_item.added","item":{"id":"msg_2","type":"message","phase":"future_phase"}}`,
		`data: {"type":"response.output_text.delta","item_id":"msg_2","delta":"Second."}`,
		`data: {"type":"response.output_item.done","item":{"id":"msg_2","type":"message","phase":"future_phase","content":[{"type":"output_text","text":"Second."}]}}`,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n")
	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errs := make(chan error, 1)
	model := Model{ID: "gpt-5.6-sol", API: APIOpenAIResponses, Provider: "openai"}
	processResponsesStream(context.Background(), io.NopCloser(strings.NewReader(stream)), model, events, done, errs)
	msg := <-done
	if len(msg.Content) != 2 {
		t.Fatalf("message boundaries=%v", msg.Content)
	}
	input := convertMessagesToResponsesInputForModel(model, []Message{msg}).([]any)
	for i, phase := range []string{"commentary", "future_phase"} {
		if input[i].(map[string]any)["phase"] != phase {
			t.Fatalf("phase replay=%v", input)
		}
	}
	for name, value := range map[string]any{"chat": convertMessagesToOpenAI(nil, []Message{msg}), "anthropic": convertMessagesToAnthropic([]Message{msg})} {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "future_phase") || !strings.Contains(string(b), "Second.") {
			t.Fatalf("%s replay=%s", name, b)
		}
	}
	for _, different := range []Model{{ID: model.ID, Provider: "other"}, {ID: model.ID, Provider: model.Provider, BaseURL: "https://other.example"}} {
		b, err := json.Marshal(convertMessagesToResponsesInputForModel(different, []Message{msg}))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "phase") || !strings.Contains(string(b), "Second.") {
			t.Fatalf("cross-scope replay=%s", b)
		}
	}
}
