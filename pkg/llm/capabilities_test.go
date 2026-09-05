package llm

import "testing"

func TestSupportsToolChoiceMatchesAnthropicAdapter(t *testing.T) {
	for _, id := range []string{"claude-haiku-4-5", "claude-sonnet-5", "claude-opus-5", "claude-fable-5", "claude-mythos-5-20260801"} {
		model := Model{ID: id, API: APIAnthropicMessages, MaxTokens: 1024}
		_, err := convertToAnthropicRequest(model, Request{ToolChoice: "answer"}, StreamOptions{ThinkingLevel: ThinkingOff})
		if model.SupportsToolChoice() != (err == nil) {
			t.Errorf("%s: supports=%v, adapter=%v", id, model.SupportsToolChoice(), err)
		}
	}
}
