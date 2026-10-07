package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHaiku55ThinkingAndForcedTools(t *testing.T) {
	model := Model{ID: "claude-haiku-5-5", API: APIAnthropicMessages, MaxTokens: 128000}
	if !model.SupportsToolChoice() {
		t.Fatal("Haiku 5.5 supports forced tools")
	}
	for _, tt := range []struct {
		level    ThinkingLevel
		thinking string
		effort   string
	}{
		{"", `{"type":"adaptive","display":"summarized"}`, ""},
		{ThinkingOff, `{"type":"disabled"}`, ""},
		{ThinkingMinimal, `{"type":"adaptive","display":"summarized"}`, "low"},
		{ThinkingLow, `{"type":"adaptive","display":"summarized"}`, "low"},
		{ThinkingMedium, `{"type":"adaptive","display":"summarized"}`, "medium"},
		{ThinkingHigh, `{"type":"adaptive","display":"summarized"}`, "high"},
		{ThinkingXHigh, `{"type":"adaptive","display":"summarized"}`, "xhigh"},
		{ThinkingMax, `{"type":"adaptive","display":"summarized"}`, "max"},
	} {
		t.Run(string(tt.level), func(t *testing.T) {
			if !model.SupportsThinkingLevel(tt.level) {
				t.Errorf("thinking level %q must be supported", tt.level)
			}
			for _, forced := range []string{"", "answer"} {
				req, err := convertToAnthropicRequest(model, Request{ToolChoice: forced}, StreamOptions{ThinkingLevel: tt.level})
				if err != nil {
					t.Fatalf("forced tool %q: %v", forced, err)
				}
				thinking, err := json.Marshal(req.Thinking)
				if err != nil {
					t.Fatal(err)
				}
				if string(thinking) != tt.thinking {
					t.Errorf("forced tool %q: thinking = %s, want %s", forced, thinking, tt.thinking)
				}
				effort := ""
				if req.OutputConfig != nil {
					effort = req.OutputConfig.Effort
				}
				if effort != tt.effort {
					t.Errorf("forced tool %q: effort = %q, want %q", forced, effort, tt.effort)
				}
				if forced != "" && (req.ToolChoice == nil || req.ToolChoice.Type != "tool" || req.ToolChoice.Name != forced) {
					t.Errorf("forced tool lost: %+v", req.ToolChoice)
				}
				if req.MaxTokens != model.MaxTokens || req.Temperature != nil {
					t.Errorf("request changed the output cap or added sampling: %+v", req)
				}
			}
		})
	}
}

func TestHaiku55StructuredOutput(t *testing.T) {
	model := Model{ID: "claude-haiku-5-5", API: APIAnthropicMessages, MaxTokens: 128000}
	if !model.SupportsStructuredOutput() {
		t.Error("Haiku 5.5 supports native structured output")
	}
	for _, level := range []ThinkingLevel{ThinkingOff, ThinkingHigh} {
		req, err := convertToAnthropicRequest(model, Request{OutputSchema: GenerateSchema(outputFixture{})}, StreamOptions{ThinkingLevel: level})
		if err != nil {
			t.Fatal(err)
		}
		if len(req.Tools) != 0 || req.ToolChoice != nil {
			t.Fatal("native output added a synthetic tool")
		}
		if req.OutputConfig == nil {
			t.Fatal("missing output_config")
		}
		format, ok := req.OutputConfig.Format.(map[string]any)
		if !ok || format["type"] != "json_schema" || format["schema"] == nil {
			t.Errorf("missing native JSON schema: %+v", req.OutputConfig.Format)
		}
		if level == ThinkingHigh && (req.Thinking == nil || req.Thinking.Type != "adaptive" || req.OutputConfig.Effort != "high") {
			t.Error("native output lost adaptive thinking or effort")
		}
		if level == ThinkingOff && (req.Thinking == nil || req.Thinking.Type != "disabled" || req.OutputConfig.Effort != "") {
			t.Error("disabled thinking must leave effort at the provider default")
		}
	}
}

func TestHaiku55Temperature(t *testing.T) {
	model := Model{ID: "claude-haiku-5-5", API: APIAnthropicMessages, MaxTokens: 128000}
	for _, level := range []ThinkingLevel{"", ThinkingOff, ThinkingHigh, ThinkingMax} {
		for _, temperature := range []float64{0, 0.5, 1} {
			req, err := convertToAnthropicRequest(model, Request{}, StreamOptions{ThinkingLevel: level, Temperature: &temperature})
			if temperature == 1 {
				if err != nil {
					t.Fatalf("default temperature at %q: %v", level, err)
				}
				if req.Temperature != nil && *req.Temperature != 1 {
					t.Errorf("default temperature at %q changed to %v", level, *req.Temperature)
				}
			} else if err == nil || !strings.Contains(err.Error(), "temperature") {
				t.Errorf("nondefault temperature %v at %q: got %v, want a temperature error", temperature, level, err)
			}
		}
	}
}
