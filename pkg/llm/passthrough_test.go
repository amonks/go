package llm

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"reflect"
	"strings"
	"testing"
)

type streamProcessor func(context.Context, io.ReadCloser, Model, chan<- StreamEvent, chan<- AssistantMessage, chan<- error)

func runStream(t *testing.T, process streamProcessor, model Model, stream string) AssistantMessage {
	t.Helper()
	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errs := make(chan error, 1)
	process(context.Background(), io.NopCloser(strings.NewReader(stream)), model, events, done, errs)
	select {
	case err := <-errs:
		t.Fatalf("stream failed: %v", err)
	default:
	}
	return <-done
}

// What a model writes is read as v1 read it: a tool call's repeated key leaves
// its last value, whatever the types, and a bad byte reads as U+FFFD, rather
// than the arguments quietly vanishing or the event being skipped.
func TestAnthropicStreamReadsWhatModelsWrite(t *testing.T) {
	msg := runStream(t, processAnthropicStream, Model{ID: "m", API: APIAnthropicMessages}, strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"b` + "\xff" + `d"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c1","name":"bash"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1,\"a\":\"x\",\"o\":{\"k\":1},\"o\":{\"j\":2}}"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		`data: {"type":"message_stop"}`, ""}, "\n"))
	if text, _ := msg.Content[0].(TextContent); text.Text != "b\uFFFDd" {
		t.Errorf("text = %q", text.Text)
	}
	call, _ := msg.Content[1].(ToolCall)
	want := map[string]any{"a": "x", "o": map[string]any{"j": float64(2)}}
	if !reflect.DeepEqual(call.Arguments, want) {
		t.Errorf("arguments = %#v, want %#v", call.Arguments, want)
	}
}

// A call cut off mid-JSON has no arguments, not whatever decoded before the
// cut: a caller must not run half a command.
func TestOpenAIToolCallCutOffHasNoArguments(t *testing.T) {
	msg := runStream(t, processOpenAIStream, Model{ID: "m", API: APIOpenAICompletions}, strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"bash","arguments":"{\"a\":1,\"command\":"}}]},"index":0}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"length","index":0}]}`,
		`data: [DONE]`, ""}, "\n"))
	if len(msg.Content) != 1 {
		t.Fatalf("content = %#v", msg.Content)
	}
	if call, _ := msg.Content[0].(ToolCall); call.Arguments != nil {
		t.Errorf("arguments = %#v, want none", call.Arguments)
	}
}

// A history is replayed whole on every turn, so what a provider or a tool put
// in it can't fail the request that carries it.
func TestRequestsCarryHistoryWhole(t *testing.T) {
	model := Model{ID: "m", API: APIAnthropicMessages, Provider: "p", MaxTokens: 1000}
	signed := jsontext.Value(`{"type":"thinking","thinking":"t","thinking":"t2","signature":"s` + "\xff" + `"}`)
	req, err := convertToAnthropicRequest(model, Request{Messages: []Message{
		AssistantMessage{Role: "assistant", Content: []ContentBlock{OpaqueContent{Type: "opaque", API: APIAnthropicMessages, Provider: "p", Model: "m", Data: signed}}},
		ToolResultMessage{ToolCallID: "c1", Content: []ContentBlock{TextContent{Text: "out\xffput"}}},
	}}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(req, json.Deterministic(true), Passthrough)
	if err != nil {
		t.Fatal(err)
	}
	if !jsontext.Value(body).IsValid() {
		t.Errorf("request isn't valid JSON: %q", body)
	}
	if !strings.Contains(string(body), `"thinking":"t2"`) {
		t.Errorf("the signed thinking block was dropped from the replay: %s", body)
	}
}

// A structured answer is the same text the stream read leniently, so a repeated
// name doesn't fail it here: the last value stands and validation judges that.
func TestStructuredAnswerReadsRepeatedNames(t *testing.T) {
	type answer struct {
		N int64  `json:"n"`
		S string `json:"s"`
	}
	msg := AssistantMessage{Role: "assistant", StopReason: StopReasonEnd, Content: []ContentBlock{
		TextContent{Type: "text", Text: `{"n":"not yet","n":12345678901234567,"s":"a","s":"b"}`},
	}}
	var got answer
	if err := DecodeOutput(msg, &got); err != nil {
		t.Fatal(err)
	}
	if got.N != 12345678901234567 || got.S != "b" {
		t.Errorf("answer = %+v", got)
	}
}

// A tool call's arguments are replayed on every turn; a bad byte in them mustn't
// blank them to "", which a provider would refuse on every retry.
func TestReplayedToolCallsCarryArgumentsHoldingRawBytes(t *testing.T) {
	const want = `{"command":"cat b` + "\uFFFD" + `d"}`
	messages := []Message{AssistantMessage{Role: "assistant", Content: []ContentBlock{
		ToolCall{Type: "toolCall", ID: "c1", Name: "bash", Arguments: map[string]any{"command": "cat b\xffd"}},
	}}}
	completions := convertMessagesToOpenAI(nil, messages)
	if len(completions) != 1 || len(completions[0].ToolCalls) != 1 {
		t.Fatalf("completions = %#v", completions)
	}
	if got := completions[0].ToolCalls[0].Function.Arguments; got != want {
		t.Errorf("completions arguments = %q, want %q", got, want)
	}
	responses, _ := convertMessagesToResponsesInputForModel(Model{}, messages).([]any)
	if len(responses) != 1 {
		t.Fatalf("responses = %#v", responses)
	}
	item, _ := responses[0].(map[string]any)
	if got := item["arguments"]; got != want {
		t.Errorf("responses arguments = %q, want %q", got, want)
	}
}
