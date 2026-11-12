package llm

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"strings"
	"testing"
)

// zeroArgHistory is a conversation whose assistant called a tool taking
// no arguments, as replayed on the next turn. args is the call's
// Arguments: an empty map as a provider parsed it, or nil as a stored
// history whose wire encoding dropped the empty object reads back.
func zeroArgHistory(args map[string]any) Request {
	return Request{Messages: []Message{
		UserMessage{Role: "user", Content: []ContentBlock{
			TextContent{Type: "text", Text: "what is running?"},
			ImageContent{Type: "image", Data: "aGk=", MimeType: "image/png"},
		}},
		AssistantMessage{Role: "assistant", Content: []ContentBlock{
			TextContent{Type: "text", Text: "checking"},
			ToolCall{Type: "toolCall", ID: "c1", Name: "tasks", Arguments: args},
		}},
		ToolResultMessage{Role: "toolResult", ToolCallID: "c1", ToolName: "tasks", Content: []ContentBlock{TextContent{Type: "text", Text: "nothing"}}},
	}}
}

// A tool that takes no arguments is called with an empty object, and its
// call replays as one on every provider — including from a history that
// stored no arguments at all. Anthropic refuses a tool_use without
// input ("tool_use.input: Field required"), the request failing forever
// once the call is in the history; the OpenAI and Google wires each
// carry the empty object in their own shape.
func TestZeroArgumentToolCallReplaysOnEveryProvider(t *testing.T) {
	encode := func(t *testing.T, v any) []byte {
		t.Helper()
		b, err := json.Marshal(v, json.Deterministic(true), Passthrough)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, args := range []struct {
		name string
		args map[string]any
	}{{"empty", map[string]any{}}, {"absent", nil}} {
		t.Run(args.name, func(t *testing.T) {
			req := zeroArgHistory(args.args)

			t.Run("anthropic", func(t *testing.T) {
				converted, err := convertToAnthropicRequest(Model{ID: "claude-haiku-5-5", API: APIAnthropicMessages, Provider: "anthropic", MaxTokens: 64000}, req, StreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Messages []struct {
						Content []jsontext.Value `json:"content"`
					} `json:"messages"`
				}
				if err := json.Unmarshal(encode(t, converted), &wire); err != nil {
					t.Fatal(err)
				}
				var calls []string
				for _, m := range wire.Messages {
					for _, block := range m.Content {
						var probe map[string]jsontext.Value
						if err := json.Unmarshal(block, &probe); err != nil {
							t.Fatal(err)
						}
						var typ string
						_ = json.Unmarshal(probe["type"], &typ)
						if typ == "tool_use" {
							calls = append(calls, string(block))
							continue
						}
						// Only a tool_use carries input: text, image, and
						// tool_result blocks share the struct and must not.
						if _, ok := probe["input"]; ok {
							t.Errorf("%s block carries input: %s", typ, block)
						}
					}
				}
				want := []string{`{"type":"tool_use","id":"c1","name":"tasks","input":{}}`}
				if !reflect.DeepEqual(calls, want) {
					t.Errorf("tool_use = %v, want %v", calls, want)
				}
			})

			t.Run("openai completions", func(t *testing.T) {
				converted, err := convertToOpenAIRequest(Model{ID: "gpt-test", API: APIOpenAICompletions, Provider: "openai"}, req, StreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Messages []struct {
						ToolCalls []jsontext.Value `json:"tool_calls"`
					} `json:"messages"`
				}
				if err := json.Unmarshal(encode(t, converted), &wire); err != nil {
					t.Fatal(err)
				}
				var calls []string
				for _, m := range wire.Messages {
					for _, c := range m.ToolCalls {
						calls = append(calls, string(c))
					}
				}
				want := []string{`{"index":0,"id":"c1","type":"function","function":{"name":"tasks","arguments":"{}"}}`}
				if !reflect.DeepEqual(calls, want) {
					t.Errorf("tool_calls = %v, want %v", calls, want)
				}
			})

			t.Run("openai responses", func(t *testing.T) {
				converted, err := convertToResponsesRequest(Model{ID: "gpt-test", API: APIOpenAIResponses, Provider: "openai"}, req, StreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Input []jsontext.Value `json:"input"`
				}
				if err := json.Unmarshal(encode(t, converted), &wire); err != nil {
					t.Fatal(err)
				}
				var calls []string
				for _, item := range wire.Input {
					if strings.Contains(string(item), `"type":"function_call"`) {
						calls = append(calls, string(item))
					}
				}
				want := []string{`{"arguments":"{}","call_id":"c1","name":"tasks","type":"function_call"}`}
				if !reflect.DeepEqual(calls, want) {
					t.Errorf("function_call items = %v, want %v", calls, want)
				}
			})

			t.Run("google", func(t *testing.T) {
				converted, err := convertToGoogleRequest(Model{ID: "gemini-3.8-flash", API: APIGoogleGenerateContent, Provider: "google", MaxTokens: 65536}, req, StreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Contents []struct {
						Parts []jsontext.Value `json:"parts"`
					} `json:"contents"`
				}
				if err := json.Unmarshal(encode(t, converted), &wire); err != nil {
					t.Fatal(err)
				}
				var calls []string
				for _, c := range wire.Contents {
					for _, p := range c.Parts {
						if strings.Contains(string(p), `"functionCall"`) {
							calls = append(calls, string(p))
						}
					}
				}
				want := []string{`{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"id":"c1","name":"tasks","args":{}}}`}
				if !reflect.DeepEqual(calls, want) {
					t.Errorf("functionCall parts = %v, want %v", calls, want)
				}
			})
		})
	}
}

// A completed tool_use whose input streamed as no JSON — the start
// block's `input: {}` and an empty input_json_delta, or no delta at
// all — is a call with the empty object, the same as one whose delta
// was "{}": not the nil a caller reads as arguments that never arrived.
// A call cut off mid-JSON still has none.
func TestAnthropicStreamZeroArgumentToolCall(t *testing.T) {
	model := Model{ID: "claude-haiku-5-5", API: APIAnthropicMessages, Provider: "anthropic"}
	stream := func(stop string, deltas ...string) string {
		lines := []string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c1","name":"tasks","input":{}}}`,
		}
		for _, d := range deltas {
			partial, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(partial)+`}}`)
		}
		lines = append(lines,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"`+stop+`"}}`,
			`data: {"type":"message_stop"}`, "")
		return strings.Join(lines, "\n")
	}
	for _, tc := range []struct {
		name   string
		stream string
		want   map[string]any
	}{
		{"empty delta", stream("tool_use", ""), map[string]any{}},
		{"no delta", stream("tool_use"), map[string]any{}},
		{"empty object delta", stream("tool_use", "{}"), map[string]any{}},
		{"cut mid-JSON", stream("max_tokens", `{"verbose":`), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := runStream(t, processAnthropicStream, model, tc.stream)
			if len(msg.Content) != 1 {
				t.Fatalf("content = %#v", msg.Content)
			}
			call, ok := msg.Content[0].(ToolCall)
			if !ok {
				t.Fatalf("content = %#v", msg.Content)
			}
			if !reflect.DeepEqual(call.Arguments, tc.want) { // DeepEqual tells nil from empty
				t.Errorf("arguments = %#v, want %#v", call.Arguments, tc.want)
			}
		})
	}
}
