package llm

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

type outputFixture struct {
	Title   string `json:"title"`
	Kind    string `json:"kind" jsonschema:"enum=movie,enum=tv"`
	Details *struct {
		Count int `json:"count"`
	} `json:"details"`
	Tags []string `json:"tags"`
}

func TestNativeOutputRequests(t *testing.T) {
	schema := GenerateSchema(outputFixture{})
	before, _ := json.Marshal(schema)
	req := Request{OutputSchema: schema}
	for _, api := range []API{APIAnthropicMessages, APIOpenAIResponses, APIOpenAICompletions} {
		t.Run(string(api), func(t *testing.T) {
			model := Model{API: api, ID: "gpt-6-astra", MaxTokens: 32000}
			var request any
			var err error
			switch api {
			case APIAnthropicMessages:
				model.ID = "claude-opus-5-5"
				request, err = convertToAnthropicRequest(model, req, StreamOptions{ThinkingLevel: ThinkingHigh})
			case APIOpenAIResponses:
				request, err = convertToResponsesRequest(model, req, StreamOptions{ThinkingLevel: ThinkingHigh})
			case APIOpenAICompletions:
				request, err = convertToOpenAIRequest(model, req, StreamOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(request)
			var body map[string]any
			json.Unmarshal(data, &body)
			if _, ok := body["tools"]; ok {
				t.Fatal("structured output added fake tools")
			}
			var format map[string]any
			switch api {
			case APIAnthropicMessages:
				config := body["output_config"].(map[string]any)
				if config["effort"] != "high" {
					t.Fatal("lost effort")
				}
				format = config["format"].(map[string]any)
			case APIOpenAIResponses:
				format = body["text"].(map[string]any)["format"].(map[string]any)
			case APIOpenAICompletions:
				format = body["response_format"].(map[string]any)["json_schema"].(map[string]any)
			}
			wire := format["schema"].(map[string]any)
			if wire["additionalProperties"] != false {
				t.Fatal("object is not closed")
			}
			details := wire["properties"].(map[string]any)["details"].(map[string]any)
			if api != APIAnthropicMessages {
				if len(wire["required"].([]any)) != 4 {
					t.Fatal("OpenAI needs every property required")
				}
				if len(details["anyOf"].([]any)) != 2 {
					t.Fatal("optional property is not nullable")
				}
			}
		})
	}
	after, _ := json.Marshal(schema)
	if string(before) != string(after) {
		t.Fatal("mutated caller schema")
	}
}

func TestDecodeOutput(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		reason     StopReason
		valid      bool
	}{
		{"complete", `{"title":"A","kind":"movie","tags":[]}`, StopReasonEnd, true},
		{"nullable", `{"title":"A","kind":"tv","tags":[],"details":null}`, StopReasonEnd, true},
		{"nested", `{"title":"A","kind":"tv","tags":[],"details":{"count":2}}`, StopReasonEnd, true},
		{"missing", `{"kind":"tv","tags":[]}`, StopReasonEnd, false},
		{"missing nested", `{"title":"A","kind":"tv","tags":[],"details":{}}`, StopReasonEnd, false},
		{"enum", `{"title":"A","kind":"other","tags":[]}`, StopReasonEnd, false},
		{"unknown", `{"title":"A","kind":"tv","tags":[],"extra":1}`, StopReasonEnd, false},
		{"wrong type", `{"title":5,"kind":"tv","tags":[]}`, StopReasonEnd, false},
		{"null required", `{"title":null,"kind":"tv","tags":[]}`, StopReasonEnd, false},
		{"wrong array item", `{"title":"A","kind":"tv","tags":[1]}`, StopReasonEnd, false},
		{"truncated", `{"title":"A","kind":"tv","tags":[]}`, StopReasonMaxTokens, false},
		{"refusal", `{"title":"A","kind":"tv","tags":[]}`, StopReasonRefusal, false},
		{"unfinished", `{"title":"A","kind":"tv","tags":[]}`, "", false},
		{"trailing", `{"title":"A","kind":"tv","tags":[]} {}`, StopReasonEnd, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var dst outputFixture
			err := DecodeOutput(AssistantMessage{StopReason: tt.reason, Content: []ContentBlock{TextContent{Text: tt.text}}}, &dst)
			if (err == nil) != tt.valid {
				t.Fatalf("err=%v valid=%v", err, tt.valid)
			}
		})
	}
	// Absent and null optionals decode the same way even into a reused destination.
	dst := struct {
		Value string `json:"value" jsonschema:"optional"`
	}{Value: "stale"}
	if err := DecodeOutput(AssistantMessage{StopReason: StopReasonEnd, Content: []ContentBlock{TextContent{Text: `{"value":null}`}}}, &dst); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dst, struct {
		Value string `json:"value" jsonschema:"optional"`
	}{}) {
		t.Fatal(dst)
	}
}

func TestStructuredCapabilityIndependentOfForcedTools(t *testing.T) {
	m := Model{API: APIAnthropicMessages, ID: "claude-opus-5-5"}
	if !m.SupportsStructuredOutput() || m.SupportsToolChoice() {
		t.Fatal("schema output incorrectly coupled to forced tools")
	}
	if (Model{API: APIOpenAIResponses, ID: "unrecognized"}).SupportsStructuredOutput() {
		t.Fatal("unknown model promised native output")
	}
}

func TestOutputStreamCompletionStates(t *testing.T) {
	type processor func(context.Context, io.ReadCloser, Model, chan<- StreamEvent, chan<- AssistantMessage, chan<- error)
	for _, tt := range []struct {
		name      string
		process   processor
		stream    string
		reason    StopReason
		wantError bool
	}{
		{"anthropic refusal", processAnthropicStream, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"}}\ndata: {\"type\":\"message_stop\"}\n", StopReasonRefusal, false},
		{"anthropic eof", processAnthropicStream, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n", "", true},
		{"completions refusal", processOpenAIStream, "data: {\"choices\":[{\"delta\":{\"refusal\":\"No\"},\"finish_reason\":\"stop\"}]}\ndata: [DONE]\n", StopReasonRefusal, false},
		{"completions filtered", processOpenAIStream, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\ndata: [DONE]\n", StopReasonRefusal, false},
		{"completions eof", processOpenAIStream, "data: {\"choices\":[{\"delta\":{\"content\":\"{}\"}}]}\n", "", true},
		{"responses refusal delta", processResponsesStream, "data: {\"type\":\"response.refusal.delta\",\"item_id\":\"a\",\"delta\":\"No\"}\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n", StopReasonRefusal, false},
		{"responses refusal terminal", processResponsesStream, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"No\"}]}]}}\n", StopReasonRefusal, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := make(chan StreamEvent, 30)
			done := make(chan AssistantMessage, 1)
			errs := make(chan error, 1)
			tt.process(context.Background(), io.NopCloser(strings.NewReader(tt.stream)), Model{}, events, done, errs)
			select {
			case err := <-errs:
				if !tt.wantError {
					t.Fatal(err)
				}
			case msg := <-done:
				if tt.wantError || msg.StopReason != tt.reason {
					t.Fatalf("reason=%q want=%q error=%v", msg.StopReason, tt.reason, tt.wantError)
				}
			default:
				t.Fatal("no result")
			}
		})
	}
}

func TestNativeOutputRejectsUnsupportedSchemaAndModel(t *testing.T) {
	for _, schema := range []*Schema{
		{Type: "string"},
		{Type: "object"},
		{Type: "object", Properties: map[string]*Schema{"child": nil}},
		{Type: "object", Properties: map[string]*Schema{}, Required: []string{"missing"}},
	} {
		if _, err := convertToResponsesRequest(Model{ID: "gpt-6-astra", API: APIOpenAIResponses}, Request{OutputSchema: schema}, StreamOptions{}); err == nil {
			t.Fatalf("accepted invalid schema %+v", schema)
		}
	}
	if _, err := convertToResponsesRequest(Model{ID: "unknown", API: APIOpenAIResponses}, Request{OutputSchema: GenerateSchema(outputFixture{})}, StreamOptions{}); err == nil {
		t.Fatal("accepted undocumented native output")
	}
}

func TestOutputSchemaNestedAndToolIsolation(t *testing.T) {
	schema := GenerateSchema(outputFixture{})
	req, err := convertToResponsesRequest(Model{ID: "gpt-6-astra", API: APIOpenAIResponses}, Request{OutputSchema: schema, Tools: []Tool{{Name: "real", Parameters: schema}}}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req.Tools[0].Parameters, schema) {
		t.Fatal("altered tool schema")
	}
	wire, err := outputSchema(schema, true)
	if err != nil {
		t.Fatal(err)
	}
	nested := wire["properties"].(map[string]any)["details"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)
	if nested["additionalProperties"] != false {
		t.Fatal("nested object must be closed")
	}
	if !reflect.DeepEqual(nested["required"], []string{"count"}) {
		t.Fatal(nested)
	}
}

func TestOutputIntegerIntegralityBoundedByLiteral(t *testing.T) {
	for _, tt := range []struct {
		number  string
		integer bool
	}{
		{"0", true}, {"-0.000e-99999999999999999999", true},
		{"1e99999999999999999999", true}, {"1e-99999999999999999999", false},
		{"12.3e1", true}, {"12.3e0", false}, {"1200e-2", true}, {"1200e-3", false},
		{"1.00", true}, {"-0.001e3", true}, {"-0.001e2", false}, {"3E+20", true},
	} {
		t.Run(tt.number, func(t *testing.T) {
			if got := jsonNumberIsInteger(tt.number); got != tt.integer {
				t.Fatalf("got %v want %v", got, tt.integer)
			}
		})
	}
}

func TestStructuredOutputKnownFamilies(t *testing.T) {
	for _, tt := range []struct {
		id        string
		api       API
		supported bool
	}{
		{"o1", APIOpenAIResponses, true},
		{"o1-2024-12-17", APIOpenAIResponses, true},
		{"o1-2024-09-12", APIOpenAIResponses, false},
		{"o1-preview", APIOpenAIResponses, false},
		{"gpt-4o-2024-05-13", APIOpenAIResponses, false},
		{"gpt-4o-2024-08-06", APIOpenAIResponses, true},
		{"claude-opus-5-5", APIAnthropicMessages, true},
		{"claude-opus-5-99", APIAnthropicMessages, false},
		{"claude-haiku-4-5-20251001", APIAnthropicMessages, true},
	} {
		t.Run(tt.id, func(t *testing.T) {
			if got := (Model{ID: tt.id, API: tt.api}).SupportsStructuredOutput(); got != tt.supported {
				t.Fatalf("support %v want %v", got, tt.supported)
			}
		})
	}
}

func TestDecodeOutputIntegerRepresentations(t *testing.T) {
	type result struct {
		Number int64 `json:"number"`
		Nested []struct {
			Number uint64 `json:"number"`
		} `json:"nested"`
	}
	for _, number := range []string{"2e3", "2000.0", "200000e-2"} {
		var dst result
		msg := AssistantMessage{StopReason: StopReasonEnd, Content: []ContentBlock{TextContent{Text: `{"number":` + number + `,"nested":[{"number":18446744073709551615.0}]}`}}}
		if err := DecodeOutput(msg, &dst); err != nil {
			t.Fatalf("%s: %v", number, err)
		}
		if dst.Number != 2000 || dst.Nested[0].Number != ^uint64(0) {
			t.Fatal(dst)
		}
	}
	for _, number := range []string{"1e9999999999999999", "9223372036854775808", "-9223372036854775809"} {
		var dst result
		msg := AssistantMessage{StopReason: StopReasonEnd, Content: []ContentBlock{TextContent{Text: `{"number":` + number + `,"nested":[]}`}}}
		if err := DecodeOutput(msg, &dst); err == nil {
			t.Fatalf("accepted out-of-range integer %s", number)
		}
	}
}

func TestUnknownProviderStopsKeepReason(t *testing.T) {
	for _, tt := range []struct {
		name, stream, reason string
		process              func(context.Context, io.ReadCloser, Model, chan<- StreamEvent, chan<- AssistantMessage, chan<- error)
	}{
		{"anthropic", `data: {"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"}}` + "\n" + `data: {"type":"message_stop"}` + "\n", "model_context_window_exceeded", processAnthropicStream},
		{"completions", `data: {"choices":[{"finish_reason":"future_stop"}]}` + "\ndata: [DONE]\n", "future_stop", processOpenAIStream},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := make(chan StreamEvent, 20)
			done := make(chan AssistantMessage, 1)
			errs := make(chan error, 1)
			tt.process(context.Background(), io.NopCloser(strings.NewReader(tt.stream)), Model{}, events, done, errs)
			msg, err := newStreamHandle(events, done, errs).Wait()
			if err != nil {
				t.Fatal(err)
			}
			if msg.StopReason != StopReasonError || !strings.Contains(msg.ErrorMessage, tt.reason) {
				t.Fatalf("lost provider reason: %+v", msg)
			}
		})
	}
}

func TestStreamFailureRetainsPartialUsage(t *testing.T) {
	for _, tt := range []struct {
		name, stream string
		process      func(context.Context, io.ReadCloser, Model, chan<- StreamEvent, chan<- AssistantMessage, chan<- error)
	}{
		{"anthropic", `data: {"type":"message_start","message":{"usage":{"input_tokens":31,"output_tokens":2}}}` + "\n", processAnthropicStream},
		{"completions", `data: {"usage":{"prompt_tokens":31,"completion_tokens":2,"total_tokens":33}}` + "\n", processOpenAIStream},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := make(chan StreamEvent, 20)
			done := make(chan AssistantMessage, 1)
			errs := make(chan error, 1)
			model := Model{ID: "test", Cost: Cost{Input: 1, Output: 1}}
			tt.process(context.Background(), io.NopCloser(strings.NewReader(tt.stream)), model, events, done, errs)
			// A normal streaming consumer may already have read every event before
			// Wait; the partial result must not depend on Wait draining ErrorEvent.
			for range events {
			}
			msg, err := newStreamHandle(events, done, errs).Wait()
			if err == nil || msg.Model != "test" || msg.Usage.Input != 31 || msg.Usage.Output != 2 || msg.Usage.Cost.Total == 0 {
				t.Fatalf("msg=%+v err=%v", msg, err)
			}
		})
	}
}

type OutputEmbeddedFields struct {
	Title string `json:"title"`
}

func TestDecodeOutputRejectsFlattenedEmbeddedFields(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		dst        any
	}{
		{"embedded", `{"OutputEmbeddedFields":{"title":"answer"}}`, &struct{ OutputEmbeddedFields }{}},
		{"nested", `{"nested":{"OutputEmbeddedFields":{"title":"answer"}}}`, &struct {
			Nested struct{ OutputEmbeddedFields } `json:"nested"`
		}{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := DecodeOutput(AssistantMessage{StopReason: StopReasonEnd, Content: []ContentBlock{TextContent{Text: tt.text}}}, tt.dst)
			if err == nil || !strings.Contains(err.Error(), "embedded") {
				t.Fatalf("expected unsupported embedded field, got %v", err)
			}
		})
	}
	var named struct {
		OutputEmbeddedFields `json:"named"`
	}
	err := DecodeOutput(AssistantMessage{StopReason: StopReasonEnd, Content: []ContentBlock{TextContent{Text: `{"named":{"title":"answer"}}`}}}, &named)
	if err != nil || named.Title != "answer" {
		t.Fatalf("named embedded field: %+v, %v", named, err)
	}
}

func TestOutputFinalAnswerExcludesCommentary(t *testing.T) {
	schema := GenerateSchema(struct {
		Title string `json:"title"`
	}{})
	commentary := TextContent{Text: `{"title":"not the final answer"}`, Message: &MessageMetadata{Phase: "commentary"}}
	msg := AssistantMessage{StopReason: StopReasonEnd, Content: []ContentBlock{commentary}}
	if err := ValidateOutput(msg, schema); err == nil {
		t.Fatal("commentary counted as final output")
	}
	msg.Content = append(msg.Content, TextContent{Text: `{"title":"answer"}`, Message: &MessageMetadata{Phase: "final_answer"}})
	var dst struct {
		Title string `json:"title"`
	}
	if err := DecodeOutput(msg, &dst); err != nil || dst.Title != "answer" {
		t.Fatalf("got %+v err=%v", dst, err)
	}
}
