package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
)

func googleTestModel() Model {
	return Model{ID: "gemini-3.8-flash", API: APIGoogleGenerateContent, Provider: "google", MaxTokens: 65536}
}

func TestGoogleRequest(t *testing.T) {
	model := googleTestModel()
	schema := GenerateSchema(struct {
		Answer string  `json:"answer"`
		Note   *string `json:"note"`
	}{})
	before, _ := json.Marshal(schema)
	req, err := convertToGoogleRequest(model, Request{
		System:   []SystemBlock{{Text: "One"}, {Text: "Two"}},
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "Look"}, ImageContent{Data: "aW1hZ2U=", MimeType: "image/png"}}}},
		Tools: []Tool{{Name: "lookup", Description: "Look up", Parameters: struct {
			Query string `json:"query"`
		}{}}},
		ToolChoice:   "lookup",
		OutputSchema: schema,
	}, StreamOptions{MaxTokens: new(1200), ThinkingLevel: ThinkingLow})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	config := got["generationConfig"].(map[string]any)
	if config["maxOutputTokens"] != float64(1200) {
		t.Fatalf("config: %s", b)
	}
	thinking := config["thinkingConfig"].(map[string]any)
	if thinking["thinkingLevel"] != "LOW" || thinking["thinkingBudget"] != nil {
		t.Fatalf("thinking: %s", b)
	}
	format := config["responseFormat"].(map[string]any)["text"].(map[string]any)
	if format["mimeType"] != "APPLICATION_JSON" {
		t.Fatalf("format: %s", b)
	}
	output := format["schema"].(map[string]any)
	if output["additionalProperties"] != false || !reflect.DeepEqual(output["required"], []any{"answer"}) {
		t.Fatalf("output: %s", b)
	}
	after, _ := json.Marshal(schema)
	if string(before) != string(after) {
		t.Fatal("mutated caller schema")
	}
	if !strings.Contains(string(b), `"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}`) {
		t.Fatalf("image: %s", b)
	}
	if !strings.Contains(string(b), `"allowedFunctionNames":["lookup"]`) || !strings.Contains(string(b), `"mode":"ANY"`) {
		t.Fatalf("tool choice: %s", b)
	}
	if strings.Contains(string(b), `"streamFunctionCallArguments":true`) {
		t.Fatalf("unexpected partial-argument protocol: %s", b)
	}
}

func TestGoogleCapabilities(t *testing.T) {
	for _, tc := range []struct {
		id     string
		levels []ThinkingLevel
	}{
		{"gemini-2.5-pro", []ThinkingLevel{"", ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-2.5-flash", []ThinkingLevel{"", ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-2.5-flash-lite", []ThinkingLevel{"", ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3-flash-preview", []ThinkingLevel{"", ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3.1-pro-preview", []ThinkingLevel{"", ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3.1-pro-preview-customtools", []ThinkingLevel{"", ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3.1-flash-lite", []ThinkingLevel{"", ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3.1-flash-lite-preview", []ThinkingLevel{"", ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3.5-flash", []ThinkingLevel{"", ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3.5-flash-lite", []ThinkingLevel{"", ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3.6-flash", []ThinkingLevel{"", ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3.7-flash", []ThinkingLevel{"", ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-3.8-flash", []ThinkingLevel{"", ThinkingLow, ThinkingMedium, ThinkingHigh}},
		{"gemini-4-argon", []ThinkingLevel{"", ThinkingLow, ThinkingMedium, ThinkingHigh}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			m := googleTestModel()
			m.ID = tc.id
			if got := m.ThinkingLevels(); !reflect.DeepEqual(got, tc.levels) {
				t.Fatalf("levels=%v, want %v", got, tc.levels)
			}
			if !m.SupportsStructuredOutput() || !m.SupportsToolChoice() {
				t.Fatal("missing documented capability")
			}
			for _, level := range tc.levels {
				if _, err := convertToGoogleRequest(m, Request{}, StreamOptions{ThinkingLevel: level}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	for _, id := range []string{"gemini-unknown", "gemini-3.8-flash-image", "gemini-4-argon-next"} {
		m := googleTestModel()
		m.ID = id
		if m.SupportsStructuredOutput() || m.SupportsToolChoice() || m.SupportsThinkingLevel(ThinkingLow) {
			t.Fatalf("unknown model %s promised capabilities", id)
		}
	}
	m := googleTestModel()
	for _, level := range []ThinkingLevel{ThinkingOff, ThinkingMinimal, ThinkingXHigh, ThinkingMax} {
		if _, err := convertToGoogleRequest(m, Request{}, StreamOptions{ThinkingLevel: level}); err == nil {
			t.Fatalf("accepted unsupported %q", level)
		}
	}
	m.ID = "gemini-2.5-flash"
	req, err := convertToGoogleRequest(m, Request{}, StreamOptions{ThinkingLevel: ThinkingOff})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	if !strings.Contains(string(b), `"thinkingBudget":0`) {
		t.Fatalf("off omitted: %s", b)
	}
	if _, err := convertToGoogleRequest(m, Request{Tools: []Tool{{Name: "lookup"}}, OutputSchema: GenerateSchema(struct{ Answer string }{})}, StreamOptions{}); err == nil {
		t.Fatal("2.5 cannot combine tools and output schema")
	}
}

func runGoogleStream(t *testing.T, model Model, chunks ...string) (AssistantMessage, []StreamEvent, error) {
	t.Helper()
	var data strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&data, "data: %s\n\n", chunk)
	}
	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)
	processGoogleStream(context.Background(), io.NopCloser(strings.NewReader(data.String())), model, events, done, errCh)
	var received []StreamEvent
	for e := range events {
		received = append(received, e)
	}
	msg, err := newStreamHandle(events, done, errCh).Wait()
	return msg, received, err
}

func TestGoogleStreamTextThinkingUsageAndSignatures(t *testing.T) {
	m := googleTestModel()
	m.Cost = Cost{Input: 1, Output: 2, CacheRead: .1}
	msg, events, err := runGoogleStream(t, m,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Consider ","thought":true}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"carefully","thought":true},{"text":"Hello "}]}}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":30}}`,
		`{"candidates":[{"content":{"parts":[{"text":"world"},{"text":"","thoughtSignature":"c2ln"}]},"finishReason":"STOP"}]}`,
		`{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":30,"candidatesTokenCount":7,"thoughtsTokenCount":13,"toolUsePromptTokenCount":2,"totalTokenCount":122}}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != StopReasonEnd || msg.Usage.Input != 72 || msg.Usage.Output != 20 || msg.Usage.CacheRead != 30 || msg.Usage.Total != 122 {
		t.Fatalf("message: %+v", msg)
	}
	if msg.Usage.Cost.Total == 0 {
		t.Fatal("cost missing")
	}
	var text, thoughts string
	for _, b := range msg.Content {
		switch b := b.(type) {
		case TextContent:
			text += b.Text
		case ThinkingContent:
			thoughts += b.Thinking
		}
	}
	if text != "Hello world" || thoughts != "Consider carefully" {
		t.Fatalf("text=%q thoughts=%q", text, thoughts)
	}
	if _, ok := events[0].(StartEvent); !ok {
		t.Fatalf("first event=%T", events[0])
	}
	request, err := convertToGoogleRequest(m, Request{Messages: []Message{msg}}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(request)
	if strings.Count(string(wire), "Hello ") != 1 || !strings.Contains(string(wire), `"text":"","thoughtSignature":"c2ln"`) {
		t.Fatalf("replay: %s", wire)
	}
	for _, change := range []func(*Model){func(m *Model) { m.ID = "gemini-3.7-flash" }, func(m *Model) { m.Provider = "other" }, func(m *Model) { m.BaseURL = "https://other.example" }} {
		other := m
		change(&other)
		r, err := convertToGoogleRequest(other, Request{Messages: []Message{msg}}, StreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "c2ln") || strings.Contains(string(b), "carefully") || !strings.Contains(string(b), "Hello world") {
			t.Fatalf("foreign replay: %s", b)
		}
	}
}

func TestGoogleParallelToolsAndImageResults(t *testing.T) {
	m := googleTestModel()
	msg, _, err := runGoogleStream(t, m, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"look","args":{"at":"one"}},"thoughtSignature":"c2ln"},{"functionCall":{"id":"native-two","name":"look","args":{"at":"two"}}}]},"finishReason":"STOP"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var calls []ToolCall
	for _, b := range msg.Content {
		if call, ok := b.(ToolCall); ok {
			calls = append(calls, call)
		}
	}
	if msg.StopReason != StopReasonToolUse || len(calls) != 2 || calls[0].ID == "" || calls[0].ID == calls[1].ID || calls[1].ID != "native-two" {
		t.Fatalf("calls: %+v", msg)
	}
	req, err := convertToGoogleRequest(m, Request{Messages: []Message{msg,
		ToolResultMessage{ToolCallID: calls[0].ID, ToolName: "look", Content: []ContentBlock{TextContent{Text: "first"}, ImageContent{Data: "aW1hZ2U=", MimeType: "image/png"}}},
		ToolResultMessage{ToolCallID: calls[1].ID, ToolName: "look", IsError: true, Content: []ContentBlock{TextContent{Text: "missing"}}},
	}}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	var got struct {
		Contents []struct {
			Role  string
			Parts []map[string]any
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Contents) != 2 || got.Contents[0].Role != "model" || len(got.Contents[0].Parts) != 2 || got.Contents[1].Role != "user" || len(got.Contents[1].Parts) != 2 {
		t.Fatalf("parallel ordering: %s", b)
	}
	first := got.Contents[1].Parts[0]["functionResponse"].(map[string]any)
	second := got.Contents[1].Parts[1]["functionResponse"].(map[string]any)
	if first["id"] != nil || second["id"] != "native-two" {
		t.Fatalf("native ids: %s", b)
	}
	if len(first["parts"].([]any)) != 1 || second["response"].(map[string]any)["error"] != "missing" {
		t.Fatalf("results: %s", b)
	}
}

func TestGoogleStreamTerminalContracts(t *testing.T) {
	for _, tc := range []struct {
		name, chunk string
		reason      StopReason
		errorText   string
	}{
		{"limit", `{"candidates":[{"finishReason":"MAX_TOKENS"}]}`, StopReasonMaxTokens, ""},
		{"safety", `{"candidates":[{"finishReason":"SAFETY","finishMessage":"blocked"}]}`, StopReasonRefusal, ""},
		{"prompt block", `{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"blocked prompt"}}`, StopReasonRefusal, ""},
		{"provider error", `{"error":{"code":429,"message":"busy","status":"RESOURCE_EXHAUSTED"}}`, StopReasonError, "busy"},
		{"missing terminal", `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`, StopReasonError, "terminal"},
		{"invalid JSON", `{`, StopReasonError, "decode"},
		{"bad function", `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`, StopReasonError, "MALFORMED_FUNCTION_CALL"},
		{"future reason", `{"candidates":[{"finishReason":"FUTURE"}]}`, StopReasonError, "FUTURE"},
		{"partial args", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","partialArgs":[{"jsonPath":"$.x","stringValue":"y"}],"willContinue":true}}]}}]}`, StopReasonError, "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, _, err := runGoogleStream(t, googleTestModel(), `{"usageMetadata":{"promptTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":7}}`, tc.chunk)
			if msg.StopReason != tc.reason || msg.Usage.Input != 5 || msg.Usage.Output != 2 {
				t.Fatalf("message=%+v error=%v", msg, err)
			}
			if tc.errorText == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatalf("error=%v want %q", err, tc.errorText)
			}
		})
	}
}

func TestGoogleHTTPAndLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "secret" || r.URL.Query().Get("key") != "" {
			t.Error("credentials not in header")
		}
		switch r.URL.Path {
		case "/v1beta/models/gemini-3.8-flash:streamGenerateContent":
			if r.Method != "POST" || r.URL.Query().Get("alt") != "sse" || r.Header.Get("User-Agent") != "test-agent" || r.Header.Get("session_id") != "session" {
				t.Errorf("request=%+v", r)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
		case "/v1beta/models/gemini-3.8-flash":
			fmt.Fprint(w, `{"name":"models/gemini-3.8-flash","supportedGenerationMethods":["generateContent"],"inputTokenLimit":1048576,"outputTokenLimit":65536}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := googleTestModel()
	m.BaseURL = srv.URL
	m.APIKey = "secret"
	h, err := Stream(context.Background(), m, Request{Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}}}, StreamOptions{UserAgent: "test-agent", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := h.Wait()
	if err != nil || msg.StopReason != StopReasonEnd {
		t.Fatalf("message=%+v err=%v", msg, err)
	}
	limits, err := ModelLimits(context.Background(), m)
	if err != nil || limits != (Limits{ContextWindow: 1048576, MaxTokens: 65536}) {
		t.Fatalf("limits=%+v err=%v", limits, err)
	}
	m.ID = "missing"
	if _, err := ModelLimits(context.Background(), m); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("missing model: %v", err)
	}
}

func TestGoogleModelLimitsValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		missing    bool
	}{
		{"empty", `{}`, 200, false},
		{"wrong model", `{"name":"models/other","inputTokenLimit":10,"outputTokenLimit":10,"supportedGenerationMethods":["generateContent"]}`, 200, false},
		{"wrong method", `{"name":"models/gemini-3.8-flash","inputTokenLimit":10,"outputTokenLimit":10,"supportedGenerationMethods":["embedContent"]}`, 200, true},
		{"missing method", `{"name":"models/gemini-3.8-flash","inputTokenLimit":10,"outputTokenLimit":10}`, 200, false},
		{"missing limit", `{"name":"models/gemini-3.8-flash","inputTokenLimit":10,"supportedGenerationMethods":["generateContent"]}`, 200, false},
		{"not found", `private provider details`, 404, true},
		{"provider error", `private provider details`, 500, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			m := googleTestModel()
			m.BaseURL = srv.URL
			_, err := ModelLimits(context.Background(), m)
			if err == nil || errors.Is(err, ErrModelNotFound) != tc.missing || strings.Contains(err.Error(), "private") {
				t.Fatalf("err=%v missing=%v", err, tc.missing)
			}
		})
	}
}

func TestGoogleDoesNotForwardCredentialsOnRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed a redirect with API credentials") }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	m := googleTestModel()
	m.BaseURL = source.URL
	m.APIKey = "private-key"
	if _, err := ModelLimits(context.Background(), m); err == nil {
		t.Fatal("metadata accepted redirect")
	}
	if _, err := Stream(context.Background(), m, Request{}, StreamOptions{}); err == nil {
		t.Fatal("stream accepted redirect")
	}
}

func TestGoogleTransferToolHistory(t *testing.T) {
	msg := AssistantMessage{Content: []ContentBlock{
		ThinkingContent{Thinking: "display only"},
		OpaqueContent{API: APIOpenAIResponses, Data: json.RawMessage(`{"encrypted_content":"private"}`)},
		TextContent{Text: "A preamble"},
		ToolCall{ID: "external", Name: "look", Arguments: map[string]any{"at": "one"}},
	}}
	m := googleTestModel()
	req, err := convertToGoogleRequest(m, Request{Messages: []Message{msg, ToolResultMessage{ToolCallID: "external", Content: []ContentBlock{TextContent{Text: "okay"}}}}}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "display only") || !strings.Contains(string(b), "skip_thought_signature_validator") || !strings.Contains(string(b), `"name":"look"`) {
		t.Fatalf("request=%s", b)
	}
}

func TestGoogle25ToolImagesFollowAllResults(t *testing.T) {
	m := googleTestModel()
	m.ID = "gemini-2.5-flash"
	req, err := convertToGoogleRequest(m, Request{Messages: []Message{
		AssistantMessage{Content: []ContentBlock{ToolCall{ID: "1", Name: "look"}, ToolCall{ID: "2", Name: "look"}}},
		ToolResultMessage{ToolCallID: "1", Content: []ContentBlock{ImageContent{MimeType: "image/png", Data: "b25l"}}},
		ToolResultMessage{ToolCallID: "2", Content: []ContentBlock{ImageContent{MimeType: "image/png", Data: "dHdv"}}},
	}}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req)
	var wire struct {
		Contents []struct{ Parts []map[string]any }
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	parts := wire.Contents[1].Parts
	if len(parts) != 6 || parts[0]["functionResponse"] == nil || parts[1]["functionResponse"] == nil || parts[3]["inlineData"] == nil || parts[5]["inlineData"] == nil {
		t.Fatalf("request=%s", b)
	}
}

func TestGoogleSSEFramingAndCancellation(t *testing.T) {
	for _, data := range []string{
		"data:{\"candidates\":[{\"finishReason\":\"STOP\"}]}",
		": keepalive\r\nevent: message\r\ndata: {\"candidates\":\r\ndata: [{\"finishReason\":\"STOP\"}]}\r\n\r\n",
	} {
		events := make(chan StreamEvent, 100)
		done := make(chan AssistantMessage, 1)
		errCh := make(chan error, 1)
		processGoogleStream(context.Background(), io.NopCloser(strings.NewReader(data)), googleTestModel(), events, done, errCh)
		if msg, err := newStreamHandle(events, done, errCh).Wait(); err != nil || msg.StopReason != StopReasonEnd {
			t.Fatalf("message=%+v err=%v", msg, err)
		}
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		events := make(chan StreamEvent)
		done := make(chan AssistantMessage, 1)
		errCh := make(chan error, 1)
		go processGoogleStream(ctx, io.NopCloser(strings.NewReader("")), googleTestModel(), events, done, errCh)
		synctest.Wait()
		cancel()
		msg, err := newStreamHandle(events, done, errCh).Wait()
		if !errors.Is(err, context.Canceled) || msg.StopReason != StopReasonAborted {
			t.Fatalf("message=%+v err=%v", msg, err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		events := make(chan StreamEvent, 1)
		done := make(chan AssistantMessage, 1)
		errCh := make(chan error, 1)
		data := `data: {"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"c2ln"}]},"finishReason":"STOP"}]}`
		go processGoogleStream(ctx, io.NopCloser(strings.NewReader(data)), googleTestModel(), events, done, errCh)
		synctest.Wait() // DoneEvent is blocked behind StartEvent.
		cancel()
		synctest.Wait()
		msg, err := newStreamHandle(events, done, errCh).Wait()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		count := 0
		for _, block := range msg.Content {
			if _, ok := block.(OpaqueContent); ok {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("opaque blocks=%d want 1", count)
		}
	})
}

func TestGoogleRejectsEditedSignedCalls(t *testing.T) {
	m := googleTestModel()
	for _, edit := range []func(*ToolCall){
		func(call *ToolCall) { call.Name = "changed" },
		func(call *ToolCall) { call.Arguments = map[string]any{"at": "changed"} },
	} {
		msg, _, err := runGoogleStream(t, m, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"look","args":{"at":"one"}},"thoughtSignature":"c2ln"}]},"finishReason":"STOP"}]}`)
		if err != nil {
			t.Fatal(err)
		}
		for i, block := range msg.Content {
			if call, ok := block.(ToolCall); ok {
				edit(&call)
				msg.Content[i] = call
			}
		}
		if _, err := convertToGoogleRequest(m, Request{Messages: []Message{msg}}, StreamOptions{}); err == nil || !strings.Contains(err.Error(), "match") {
			t.Fatalf("edited call: %v", err)
		}
	}
}

func TestGoogleOutputLimitDoesNotChangeThinkingBudget(t *testing.T) {
	m := googleTestModel()
	m.ID = "gemini-2.5-pro"
	req, err := convertToGoogleRequest(m, Request{}, StreamOptions{ThinkingLevel: ThinkingHigh, MaxTokens: new(100)})
	if err != nil {
		t.Fatal(err)
	}
	if req.GenerationConfig.MaxOutputTokens != 100 || *req.GenerationConfig.ThinkingConfig.ThinkingBudget != 32768 {
		t.Fatalf("config=%+v", req.GenerationConfig)
	}
}

func TestGoogleNoArgumentToolReplay(t *testing.T) {
	m := googleTestModel()
	msg, _, err := runGoogleStream(t, m, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"now"},"thoughtSignature":"c2ln"}]},"finishReason":"STOP"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := convertToGoogleRequest(m, Request{Messages: []Message{msg}}, StreamOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestGoogleConfigurationFailuresArePermanent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Model, *Request, *StreamOptions)
	}{
		{"missing key", func(m *Model, _ *Request, _ *StreamOptions) { m.APIKey = "" }},
		{"unsupported thinking", func(_ *Model, _ *Request, o *StreamOptions) { o.ThinkingLevel = ThinkingOff }},
		{"invalid schema", func(_ *Model, r *Request, _ *StreamOptions) { r.OutputSchema = &Schema{Type: "object"} }},
		{"invalid URL", func(m *Model, _ *Request, _ *StreamOptions) { m.BaseURL = ":" }},
		{"unserializable history", func(_ *Model, r *Request, _ *StreamOptions) {
			r.Messages = []Message{AssistantMessage{Content: []ContentBlock{ToolCall{ID: "call", Name: "f", Arguments: map[string]any{"bad": make(chan int)}}}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := googleTestModel()
			m.APIKey = "key"
			var req Request
			var opts StreamOptions
			tc.change(&m, &req, &opts)
			_, err := Stream(context.Background(), m, req, opts)
			re, ok := errors.AsType[*retryableError](err)
			if !ok || re.retryable {
				t.Fatalf("error=%v permanent=%v", err, ok)
			}
		})
	}
}

func TestGoogleStreamRejectsAmbiguousCandidatesAndCalls(t *testing.T) {
	for _, payload := range []string{
		`{"candidates":[{"content":{"role":"user","parts":[{"text":"wrong role"}]},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"first"}]}},{"content":{"parts":[{"text":"second"}]},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"duplicate","name":"f","args":{}}},{"functionCall":{"id":"duplicate","name":"f","args":{}}}]},"finishReason":"STOP"}]}`,
	} {
		if _, _, err := runGoogleStream(t, googleTestModel(), payload); err == nil {
			t.Fatalf("accepted ambiguous response: %s", payload)
		}
	}
}

func googleWeatherCalls(t *testing.T, model Model, nativeIDs [2]string) (AssistantMessage, []ToolCall) {
	t.Helper()
	parts := []googlePart{
		{FunctionCall: &googleFunctionCall{ID: nativeIDs[0], Name: "weather", Args: map[string]any{"city": "Paris"}}, ThoughtSignature: "c2ln"},
		{FunctionCall: &googleFunctionCall{ID: nativeIDs[1], Name: "weather", Args: map[string]any{"city": "London"}}},
	}
	data, err := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": parts}, "finishReason": "STOP"}}})
	if err != nil {
		t.Fatal(err)
	}
	msg, _, err := runGoogleStream(t, model, string(data))
	if err != nil {
		t.Fatal(err)
	}
	var calls []ToolCall
	for _, block := range msg.Content {
		if call, ok := block.(ToolCall); ok {
			calls = append(calls, call)
		}
	}
	return msg, calls
}

func TestGoogleReordersPositionalToolResultsWithTheirImages(t *testing.T) {
	for _, modelID := range []string{"gemini-2.5-flash", "gemini-3.8-flash"} {
		for _, ids := range [][2]string{{"", ""}, {"native-paris", "native-london"}, {"", "native-london"}, {"native-paris", ""}} {
			t.Run(modelID+"/"+strings.Join(ids[:], "/"), func(t *testing.T) {
				m := googleTestModel()
				m.ID = modelID
				msg, calls := googleWeatherCalls(t, m, ids)
				results := []Message{msg,
					ToolResultMessage{ToolCallID: calls[1].ID, ToolName: "weather", Content: []ContentBlock{TextContent{Text: "12C"}, ImageContent{MimeType: "image/png", Data: "london-image"}}},
					ToolResultMessage{ToolCallID: calls[0].ID, ToolName: "weather", Content: []ContentBlock{TextContent{Text: "20C"}, ImageContent{MimeType: "image/png", Data: "paris-image"}}},
				}
				req, err := convertToGoogleRequest(m, Request{Messages: results}, StreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				wire, err := json.Marshal(req)
				if err != nil {
					t.Fatal(err)
				}
				var decoded struct {
					Contents []struct{ Parts []googlePart }
				}
				if err := json.Unmarshal(wire, &decoded); err != nil {
					t.Fatal(err)
				}
				first, second := 0, 1
				if ids[0] != "" && ids[1] != "" {
					first, second = 1, 0
				}
				outputs := [2]string{"20C", "12C"}
				images := [2]string{"paris-image", "london-image"}
				parts := decoded.Contents[1].Parts
				for position, source := range []int{first, second} {
					response := parts[position].FunctionResponse
					if response == nil || response.ID != ids[source] || response.Response["output"] != outputs[source] {
						t.Fatalf("result %d lost call association: %s", position, wire)
					}
					if googleBudgetModel(modelID) {
						label := parts[2+2*position].Text
						if label == nil || !strings.Contains(*label, calls[source].ID) || parts[3+2*position].InlineData.Data != images[source] {
							t.Fatalf("image %d lost call association: %s", position, wire)
						}
					} else if len(response.Parts) != 1 || response.Parts[0].InlineData.Data != images[source] {
						t.Fatalf("image %d lost call association: %s", position, wire)
					}
				}
				if decoded.Contents[0].Parts[0].FunctionCall.Args["city"] != "Paris" || decoded.Contents[0].Parts[1].FunctionCall.Args["city"] != "London" {
					t.Fatalf("changed signed call order: %s", wire)
				}
			})
		}
	}
}

func TestGoogleRejectsAmbiguousPositionalToolResultBatches(t *testing.T) {
	m := googleTestModel()
	msg, calls := googleWeatherCalls(t, m, [2]string{})
	paris := ToolResultMessage{ToolCallID: calls[0].ID, ToolName: "weather", Content: []ContentBlock{TextContent{Text: "20C"}}}
	london := ToolResultMessage{ToolCallID: calls[1].ID, ToolName: "weather", Content: []ContentBlock{TextContent{Text: "12C"}}}
	for _, tc := range []struct {
		name     string
		messages []Message
	}{
		{"incomplete", []Message{msg, london}},
		{"duplicate", []Message{msg, london, london}},
		{"unknown call", []Message{msg, london, ToolResultMessage{ToolCallID: "unknown", ToolName: "weather"}}},
		{"wrong name", []Message{msg, london, ToolResultMessage{ToolCallID: paris.ToolCallID, ToolName: "other"}}},
		{"split batch", []Message{msg, london, UserMessage{Content: []ContentBlock{TextContent{Text: "continue"}}}, paris}},
		{"repeated batch", []Message{msg, paris, london, UserMessage{Content: []ContentBlock{TextContent{Text: "continue"}}}, paris, london}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := convertToGoogleRequest(m, Request{Messages: tc.messages}, StreamOptions{}); err == nil {
				t.Fatal("accepted ambiguous result batch")
			}
		})
	}
	other, otherCalls := googleWeatherCalls(t, m, [2]string{})
	if _, err := convertToGoogleRequest(m, Request{Messages: []Message{msg, other, paris, london}}, StreamOptions{}); err == nil {
		t.Fatal("matched results to an earlier assistant turn")
	}
	if _, err := convertToGoogleRequest(m, Request{Messages: []Message{msg, other, paris, ToolResultMessage{ToolCallID: otherCalls[1].ID, ToolName: "weather"}}}, StreamOptions{}); err == nil {
		t.Fatal("accepted results spanning assistant turns")
	}
}

func TestGooglePositionalResultsBecomeIdentifiedAcrossReplayScopes(t *testing.T) {
	m := googleTestModel()
	msg, calls := googleWeatherCalls(t, m, [2]string{})
	for _, change := range []func(*Model){
		func(m *Model) { m.ID = "gemini-3.7-flash" },
		func(m *Model) { m.Provider = "another" },
		func(m *Model) { m.BaseURL = "https://another.example" },
	} {
		other := m
		change(&other)
		req, err := convertToGoogleRequest(other, Request{Messages: []Message{msg,
			ToolResultMessage{ToolCallID: calls[1].ID, ToolName: "weather", Content: []ContentBlock{TextContent{Text: "12C"}}},
			ToolResultMessage{ToolCallID: calls[0].ID, ToolName: "weather", Content: []ContentBlock{TextContent{Text: "20C"}}},
		}}, StreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(req)
		var decoded struct {
			Contents []struct{ Parts []googlePart }
		}
		if err := json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Contents[0].Parts[0].FunctionCall.ID != calls[0].ID || decoded.Contents[1].Parts[0].FunctionResponse.ID != calls[1].ID || decoded.Contents[1].Parts[0].FunctionResponse.Response["output"] != "12C" {
			t.Fatalf("imported IDs lost association: %s", wire)
		}
		if strings.Contains(string(wire), "c2ln") {
			t.Fatalf("replayed foreign signature: %s", wire)
		}
	}
}

func TestGoogleArgonRejectsUnsupportedContinuationBudget(t *testing.T) {
	m := googleTestModel()
	m.ID = "gemini-4-argon"
	m.MaxTokens = 32768
	for _, tc := range []struct {
		name string
		opts StreamOptions
		want int
	}{
		{"metadata default", StreamOptions{}, 32768},
		{"metadata maximum", StreamOptions{MaxOutput: true}, 32768},
		{"explicit streaming cap", StreamOptions{MaxTokens: new(65536)}, 65536},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := convertToGoogleRequest(m, Request{}, tc.opts)
			if err != nil || req.GenerationConfig.MaxOutputTokens != tc.want {
				t.Fatalf("output cap=%d err=%v", req.GenerationConfig.MaxOutputTokens, err)
			}
		})
	}
	if _, err := convertToGoogleRequest(m, Request{}, StreamOptions{MaxTokens: new(65537)}); err == nil || !strings.Contains(err.Error(), "continuation") {
		t.Fatalf("continuation request: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("sent a paid request requiring unsupported continuation")
	}))
	defer srv.Close()
	m.BaseURL = srv.URL
	m.APIKey = "key"
	_, err := Stream(context.Background(), m, Request{}, StreamOptions{MaxTokens: new(65537)})
	re, ok := errors.AsType[*retryableError](err)
	if !ok || re.retryable {
		t.Fatalf("continuation limit error must be permanent: %v", err)
	}
}
