package llm

import (
	"fmt"
	"slices"
	"strings"
)

// These are documented text-generation families, not a guess based on the
// "gemini-" prefix or Models API visibility. Sources:
// https://ai.google.dev/gemini-api/docs/generate-content/thinking
// https://ai.google.dev/gemini-api/docs/generate-content/structured-output
// https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/thinking
func googleThinkingLevels(id string) []ThinkingLevel {
	switch id {
	case "gemini-2.5-flash", "gemini-2.5-flash-lite":
		return []ThinkingLevel{ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}
	case "gemini-2.5-pro", "gemini-3-flash-preview", "gemini-3.1-flash-lite", "gemini-3.1-flash-lite-preview",
		"gemini-3.5-flash", "gemini-3.5-flash-lite", "gemini-3.6-flash":
		return []ThinkingLevel{ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}
	case "gemini-3.1-pro-preview", "gemini-3.1-pro-preview-customtools", "gemini-3.7-flash", "gemini-3.8-flash", "gemini-4-argon":
		return []ThinkingLevel{ThinkingLow, ThinkingMedium, ThinkingHigh}
	default:
		return nil
	}
}

func googleModelKnown(id string) bool { return len(googleThinkingLevels(id)) > 0 }

func googleBudgetModel(id string) bool {
	return id == "gemini-2.5-pro" || id == "gemini-2.5-flash" || id == "gemini-2.5-flash-lite"
}

type googleThinkingConfig struct {
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
}

func googleThinking(id string, level ThinkingLevel) (*googleThinkingConfig, error) {
	if level == "" {
		return nil, nil
	}
	if !slices.Contains(googleThinkingLevels(id), level) {
		return nil, fmt.Errorf("google model %s does not support thinking level %q", id, level)
	}
	if !googleBudgetModel(id) {
		return &googleThinkingConfig{ThinkingLevel: strings.ToUpper(string(level)), IncludeThoughts: true}, nil
	}
	// The 2.5 API accepts a token budget, not an effort enum. Keep the mapping
	// within each model's documented range; off is distinct from provider default.
	budget := map[ThinkingLevel]int{ThinkingOff: 0, ThinkingMinimal: 512, ThinkingLow: 1024, ThinkingMedium: 8192, ThinkingHigh: 24576}[level]
	if id == "gemini-2.5-pro" && level == ThinkingHigh {
		budget = 32768
	}
	return &googleThinkingConfig{ThinkingBudget: &budget, IncludeThoughts: level != ThinkingOff}, nil
}
