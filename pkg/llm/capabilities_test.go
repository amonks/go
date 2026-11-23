package llm

import (
	"slices"
	"testing"
)

func TestSupportsToolChoiceMatchesAnthropicAdapter(t *testing.T) {
	for _, id := range []string{"claude-haiku-4-5", "claude-sonnet-5", "claude-opus-5", "claude-fable-5", "claude-mythos-5-20260801"} {
		model := Model{ID: id, API: APIAnthropicMessages, MaxTokens: 1024}
		_, err := convertToAnthropicRequest(model, Request{ToolChoice: "answer"}, StreamOptions{ThinkingLevel: ThinkingOff})
		if model.SupportsToolChoice() != (err == nil) {
			t.Errorf("%s: supports=%v, adapter=%v", id, model.SupportsToolChoice(), err)
		}
	}
}

// DeclaredModels is the roster a host walks to check it can serve every
// model the package declares (llmproxy's prices do), so it must be exactly
// the families the capability checks accept: each one, and its dated
// snapshots, takes native structured output, and an OpenAI family on the
// Responses API also declares its efforts.
func TestDeclaredModels(t *testing.T) {
	snapshot := map[API]string{
		APIAnthropicMessages:     "-20260101",
		APIOpenAIResponses:       "-2026-01-01",
		APIOpenAICompletions:     "-2026-01-01",
		APIGoogleGenerateContent: "",
	}
	for api, suffix := range snapshot {
		ids := DeclaredModels(api)
		if len(ids) == 0 || !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			t.Errorf("%s: DeclaredModels = %v, want a sorted roster without repeats", api, ids)
		}
		for _, id := range ids {
			for _, variant := range []string{id, id + suffix} {
				if !(Model{ID: variant, API: api}).SupportsStructuredOutput() {
					t.Errorf("%s: %s is declared but SupportsStructuredOutput refuses it", api, variant)
				}
			}
			if api == APIOpenAIResponses && len(responsesEfforts(id)) == 0 {
				t.Errorf("%s is declared with no efforts", id)
			}
		}
	}
	if got := DeclaredModels("made-up"); len(got) != 0 {
		t.Errorf("unknown API declares %v", got)
	}
	// The Responses-only families are no Chat Completions model.
	completions := DeclaredModels(APIOpenAICompletions)
	for _, id := range []string{"gpt-5-pro", "gpt-5.3-codex"} {
		if slices.Contains(completions, id) || !slices.Contains(DeclaredModels(APIOpenAIResponses), id) {
			t.Errorf("%s: want Responses only", id)
		}
	}
}
