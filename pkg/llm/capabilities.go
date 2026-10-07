package llm

import (
	"fmt"
	"regexp"
)

// SupportsToolChoice reports whether the adapter permits forcing a named
// tool on this model. It describes known adapter restrictions, not provider
// availability. Except on Haiku 5.5, Anthropic requests also need ThinkingOff
// when forcing tools.
func (m Model) SupportsToolChoice() bool {
	switch m.API {
	case APIAnthropicMessages:
		return !modelRejectsForcedTools(m.ID)
	case APIOpenAIResponses:
		return len(responsesEfforts(m.ID)) > 0
	case APIOpenAICompletions:
		return true
	default:
		return false
	}
}

var anthropicSnapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// SupportsStructuredOutput reports documented native JSON-schema output
// support, independently of forced tool execution and thinking settings.
// Sources: the providers' structured-output guides linked in specs/pkg-llm.md.
func (m Model) SupportsStructuredOutput() bool {
	switch m.API {
	case APIAnthropicMessages:
		id := anthropicSnapshotSuffix.ReplaceAllString(m.ID, "")
		switch id {
		case "claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-opus-5-5",
			"claude-sonnet-4-5", "claude-sonnet-4-6", "claude-sonnet-5", "claude-sonnet-5-5", "claude-haiku-4-5", "claude-haiku-5-5",
			"claude-fable-5", "claude-fable-5-1", "claude-mythos-5", "claude-mythos-5-1", "claude-mythos-preview":
			return true
		}
	case APIOpenAIResponses, APIOpenAICompletions:
		id := openAISnapshotSuffix.ReplaceAllString(m.ID, "")
		// The first general-purpose snapshots precede native structured output.
		// o1-preview and o1-mini are distinct families and unsupported below.
		if id != m.ID {
			snapshot := m.ID[len(id)+1:]
			switch id {
			case "gpt-4o":
				if snapshot < "2024-08-06" {
					return false
				}
			case "gpt-4o-mini":
				if snapshot < "2024-07-18" {
					return false
				}
			case "o1":
				if snapshot < "2024-12-17" {
					return false
				}
			}
		}
		// Native output is an independent capability, not inferred from reasoning
		// efforts or tool-choice support.
		switch id {
		case "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-mini",
			"o1", "o3", "o3-mini", "o4-mini", "gpt-5", "gpt-5-mini", "gpt-5-nano",
			"gpt-5.1", "gpt-5.2", "gpt-5.4", "gpt-5.5", "gpt-5.6", "gpt-5.6-sol",
			"gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-sol", "gpt-6-luna", "gpt-6-astra":
			return true
		}
	}
	return false
}

func structuredRequestSchema(model Model, schema *Schema) (map[string]any, error) {
	if !model.SupportsStructuredOutput() {
		return nil, fmt.Errorf("llm: model %s has no documented native structured-output capability", model.ID)
	}
	return outputSchema(schema, model.API != APIAnthropicMessages)
}
