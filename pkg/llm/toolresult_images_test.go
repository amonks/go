package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A tool result may carry images beside text (ToolResultMessage.Content is
// []ContentBlock). Each adapter has to deliver them in the form its API
// accepts: Anthropic tool_result content as a block array; the OpenAI APIs
// take no images in tool output, so the images ride a synthetic user
// message immediately after, naming the call they came from.

func imageToolResult(callID string) ToolResultMessage {
	return ToolResultMessage{
		Role:       "toolResult",
		ToolCallID: callID,
		ToolName:   "view",
		Content: []ContentBlock{
			TextContent{Type: "text", Text: "viewing chart.png"},
			ImageContent{Type: "image", Data: "aWJ5dGVz", MimeType: "image/png"},
		},
		Timestamp: time.Now(),
	}
}

func textToolResult(callID, text string) ToolResultMessage {
	return ToolResultMessage{
		Role:       "toolResult",
		ToolCallID: callID,
		ToolName:   "bash",
		Content:    []ContentBlock{TextContent{Type: "text", Text: text}},
		Timestamp:  time.Now(),
	}
}

func TestConvertMessagesToAnthropic_ToolResultImageBecomesBlockArray(t *testing.T) {
	result := convertMessagesToAnthropic([]Message{imageToolResult("tool_1")})

	if len(result) != 1 || result[0].Role != "user" {
		t.Fatalf("expected one merged user message, got %+v", result)
	}
	tr := result[0].Content[0]
	if tr.Type != "tool_result" || tr.ToolUseID != "tool_1" {
		t.Fatalf("expected tool_result for tool_1, got %+v", tr)
	}
	blocks, ok := tr.Content.([]anthropicContent)
	if !ok {
		t.Fatalf("expected tool_result content to be a block array, got %T", tr.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "viewing chart.png" {
		t.Errorf("expected leading text block, got %+v", blocks[0])
	}
	if blocks[1].Type != "image" || blocks[1].Source == nil ||
		blocks[1].Source.MediaType != "image/png" || blocks[1].Source.Data != "aWJ5dGVz" {
		t.Errorf("expected image block with base64 source, got %+v", blocks[1])
	}
}

func TestConvertMessagesToAnthropic_TextOnlyToolResultStaysString(t *testing.T) {
	result := convertMessagesToAnthropic([]Message{textToolResult("tool_1", "plain output")})

	tr := result[0].Content[0]
	s, ok := tr.Content.(string)
	if !ok || s != "plain output" {
		t.Fatalf("expected plain string content, got %T %v", tr.Content, tr.Content)
	}
}

// The wire shape: a string stays a JSON string, a block array marshals as an
// array, and an empty tool result omits the content key entirely (the shape
// the API has always been sent).
func TestAnthropicToolResultContentWireShape(t *testing.T) {
	cases := []struct {
		name string
		msg  ToolResultMessage
		want string
	}{
		{"text", textToolResult("t1", "out"), `"content":"out"`},
		{"image", imageToolResult("t1"), `"content":[`},
	}
	for _, tc := range cases {
		result := convertMessagesToAnthropic([]Message{tc.msg})
		raw, err := json.Marshal(result[0].Content[0])
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		if !strings.Contains(string(raw), tc.want) {
			t.Errorf("%s: expected %s in %s", tc.name, tc.want, raw)
		}
	}

	empty := textToolResult("t1", "")
	result := convertMessagesToAnthropic([]Message{empty})
	raw, err := json.Marshal(result[0].Content[0])
	if err != nil {
		t.Fatalf("empty: marshal: %v", err)
	}
	if strings.Contains(string(raw), `"content"`) {
		t.Errorf("empty tool result should omit content key, got %s", raw)
	}
}

// An image-only result must not carry an empty text block — the API
// rejects them.
func TestConvertMessagesToAnthropic_ImageOnlyToolResultHasNoEmptyTextBlock(t *testing.T) {
	msg := imageToolResult("tool_1")
	msg.Content = msg.Content[1:] // drop the text block
	result := convertMessagesToAnthropic([]Message{msg})

	blocks, ok := result[0].Content[0].Content.([]anthropicContent)
	if !ok {
		t.Fatalf("expected block array, got %T", result[0].Content[0].Content)
	}
	if len(blocks) != 1 || blocks[0].Type != "image" {
		t.Fatalf("expected a lone image block, got %+v", blocks)
	}
}

func TestConvertMessagesToOpenAI_ToolResultImageRidesFollowupUserMessage(t *testing.T) {
	result := convertMessagesToOpenAI(nil, []Message{imageToolResult("tool_1")})

	if len(result) != 2 {
		t.Fatalf("expected tool message + follow-up user message, got %d: %+v", len(result), result)
	}
	if result[0].Role != "tool" || result[0].ToolCallID != "tool_1" {
		t.Fatalf("expected tool message first, got %+v", result[0])
	}
	if s, ok := result[0].Content.(string); !ok || s != "viewing chart.png" {
		t.Errorf("expected tool message to keep the text output, got %v", result[0].Content)
	}
	if result[1].Role != "user" {
		t.Fatalf("expected follow-up user message, got %+v", result[1])
	}
	parts, ok := result[1].Content.([]openAIContentPart)
	if !ok {
		t.Fatalf("expected content parts, got %T", result[1].Content)
	}
	var sawText, sawImage bool
	for _, p := range parts {
		switch p.Type {
		case "text":
			if strings.Contains(p.Text, "tool_1") {
				sawText = true
			}
		case "image_url":
			if p.ImageURL != nil && strings.HasPrefix(p.ImageURL.URL, "data:image/png;base64,") {
				sawImage = true
			}
		}
	}
	if !sawText || !sawImage {
		t.Errorf("expected a text part naming the call and an image part, got %+v", parts)
	}
}

func TestConvertMessagesToOpenAI_TextOnlyToolResultUnchanged(t *testing.T) {
	result := convertMessagesToOpenAI(nil, []Message{textToolResult("tool_1", "plain output")})
	if len(result) != 1 {
		t.Fatalf("expected one tool message, got %d", len(result))
	}
	if s, ok := result[0].Content.(string); !ok || s != "plain output" {
		t.Errorf("expected plain string content, got %v", result[0].Content)
	}
}

func TestConvertMessagesToResponsesInput_ToolResultImageRidesFollowupUserItem(t *testing.T) {
	items, ok := convertMessagesToResponsesInput([]Message{imageToolResult("tool_1")}).([]any)
	if !ok {
		t.Fatal("expected structured items")
	}
	if len(items) != 2 {
		t.Fatalf("expected function_call_output + follow-up user item, got %d: %+v", len(items), items)
	}
	out, ok := items[0].(map[string]any)
	if !ok || out["type"] != "function_call_output" || out["call_id"] != "tool_1" {
		t.Fatalf("expected function_call_output first, got %+v", items[0])
	}
	if out["output"] != "viewing chart.png" {
		t.Errorf("expected output to keep the text, got %v", out["output"])
	}
	user, ok := items[1].(map[string]any)
	if !ok || user["role"] != "user" {
		t.Fatalf("expected follow-up user item, got %+v", items[1])
	}
	parts, ok := user["content"].([]responsesContentPart)
	if !ok {
		t.Fatalf("expected content parts, got %T", user["content"])
	}
	var sawText, sawImage bool
	for _, p := range parts {
		switch p.Type {
		case "input_text":
			if strings.Contains(p.Text, "tool_1") {
				sawText = true
			}
		case "input_image":
			if strings.HasPrefix(p.ImageURL, "data:image/png;base64,") {
				sawImage = true
			}
		}
	}
	if !sawText || !sawImage {
		t.Errorf("expected a text part naming the call and an image part, got %+v", parts)
	}
}

func TestConvertMessagesToResponsesInput_TextOnlyToolResultUnchanged(t *testing.T) {
	items, ok := convertMessagesToResponsesInput([]Message{textToolResult("tool_1", "plain output")}).([]any)
	if !ok {
		t.Fatal("expected structured items")
	}
	if len(items) != 1 {
		t.Fatalf("expected one function_call_output, got %d", len(items))
	}
}

// The flush waits for the whole run of tool results: an assistant
// turn's parallel calls must keep their outputs adjacent, so both
// images ride one user message after both results — on both OpenAI
// paths.
func TestOpenAIToolResultImagesFlushAfterTheWholeRun(t *testing.T) {
	messages := []Message{
		AssistantMessage{Role: "assistant", Content: []ContentBlock{
			ToolCall{Type: "toolCall", ID: "tool_1", Name: "view", Arguments: map[string]any{}},
			ToolCall{Type: "toolCall", ID: "tool_2", Name: "view", Arguments: map[string]any{}},
		}},
		imageToolResult("tool_1"),
		imageToolResult("tool_2"),
	}

	chat := convertMessagesToOpenAI(nil, messages)
	var roles []string
	for _, m := range chat {
		roles = append(roles, m.Role)
	}
	want := []string{"assistant", "tool", "tool", "user"}
	if fmt.Sprint(roles) != fmt.Sprint(want) {
		t.Errorf("chat roles = %v, want %v", roles, want)
	}
	if parts, ok := chat[3].Content.([]openAIContentPart); !ok || len(parts) != 4 {
		t.Errorf("follow-up = %+v", chat[3].Content)
	}

	items := convertMessagesToResponsesInput(messages).([]any)
	var kinds []string
	for _, it := range items {
		m := it.(map[string]any)
		if k, ok := m["type"].(string); ok {
			kinds = append(kinds, k)
		} else {
			kinds = append(kinds, m["role"].(string))
		}
	}
	wantKinds := []string{"function_call", "function_call", "function_call_output", "function_call_output", "user"}
	if fmt.Sprint(kinds) != fmt.Sprint(wantKinds) {
		t.Errorf("responses items = %v, want %v", kinds, wantKinds)
	}
}
