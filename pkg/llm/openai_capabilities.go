package llm

import (
	"fmt"
	"regexp"
	"slices"
)

var openAISnapshotSuffix = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// responsesEfforts records documented request capabilities, not account access.
// Model retrieval exposes no capability metadata. Unknown names therefore cannot
// promise any explicit effort setting; the provider may still take its default.
// Sources: https://developers.openai.com/api/docs/models/{model}
func responsesEfforts(id string) []string {
	id = openAISnapshotSuffix.ReplaceAllString(id, "")
	switch id {
	case "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-mini":
		return []string{"off"} // Non-reasoning models take no reasoning parameter.
	case "gpt-5", "gpt-5-mini", "gpt-5-nano":
		return []string{"minimal", "low", "medium", "high"}
	case "o1", "o3", "o3-mini", "o4-mini":
		return []string{"low", "medium", "high"}
	case "gpt-5.1":
		return []string{"none", "low", "medium", "high"}
	case "gpt-5.2", "gpt-5.4", "gpt-5.5":
		return []string{"none", "low", "medium", "high", "xhigh"}
	case "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return []string{"none", "low", "medium", "high", "xhigh", "max"}
	case "gpt-6-astra":
		return []string{"low", "medium", "high", "xhigh", "max"}
	default:
		return nil
	}
}

func responsesReasoningEffort(id string, level ThinkingLevel) (string, error) {
	if level == "" {
		return "", nil
	}
	supported := responsesEfforts(id)
	if level == ThinkingOff && slices.Contains(supported, "off") {
		return "", nil
	}
	effort := string(level)
	if level == ThinkingOff {
		effort = "none"
	}
	if !slices.Contains(supported, effort) {
		return "", fmt.Errorf("OpenAI model %s does not support thinking level %q", id, level)
	}
	return effort, nil
}

// SupportsThinkingLevel reports whether an adapter can honor the requested
// setting. An empty level always leaves the provider's default in charge.
// Unknown OpenAI model names cannot promise explicit reasoning settings.
// Manual Anthropic budgets must fit MaxTokens when that ceiling is known.
func (m Model) SupportsThinkingLevel(level ThinkingLevel) bool {
	if level == "" {
		return true
	}
	switch m.API {
	case APIOpenAIResponses:
		_, err := responsesReasoningEffort(m.ID, level)
		return err == nil
	case APIAnthropicMessages:
		switch level {
		case ThinkingOff:
			return !modelAlwaysThinks(m.ID)
		case ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh:
			return modelUsesAdaptiveThinking(m.ID) || m.MaxTokens == 0 || thinkingBudget(level) < m.MaxTokens
		default:
			return false
		}
	default:
		return false
	}
}

func responsesSupportsTemperature(id, effort string) bool {
	supported := responsesEfforts(id)
	if slices.Contains(supported, "off") {
		return true
	}
	return effort == "none" && slices.Contains(supported, "none")
}

// ThinkingLevels lists the settings the adapter can honor, including the
// empty setting that leaves the provider's default in charge.
func (m Model) ThinkingLevels() []ThinkingLevel {
	levels := []ThinkingLevel{""}
	for _, level := range []ThinkingLevel{ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingMax} {
		if m.SupportsThinkingLevel(level) {
			levels = append(levels, level)
		}
	}
	return levels
}
