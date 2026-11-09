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
//
// Shut-down and withdrawn ids leave the list rather than linger, per
// https://ai.google.dev/gemini-api/docs/deprecations: 3.1 Flash-Lite
// Preview shut down on 2026-05-25, and every 2.5 model is closed to
// keys that never used one — ours answers 404, "no longer available to
// new users" — while 2.5 Pro and Flash route to Gemini 3 models, which
// take thinking levels in place of the 2.5 budgets, from 2026-12-01.
// An id Google routes to a model taking the same parameters (3.5
// Flash, 3.7 Flash) stays, declared with its route's levels.
func googleThinkingLevels(id string) []ThinkingLevel {
	switch id {
	case "gemini-3-flash-preview", "gemini-3.1-flash-lite",
		"gemini-3.5-flash", "gemini-3.5-flash-lite", "gemini-3.6-flash":
		return []ThinkingLevel{ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}
	case "gemini-3.1-pro-preview", "gemini-3.1-pro-preview-customtools", "gemini-3.7-flash", "gemini-3.8-flash", "gemini-4-argon":
		return []ThinkingLevel{ThinkingLow, ThinkingMedium, ThinkingHigh}
	default:
		return nil
	}
}

func googleModelKnown(id string) bool { return len(googleThinkingLevels(id)) > 0 }

type googleThinkingConfig struct {
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitzero"`
}

func googleThinking(id string, level ThinkingLevel) (*googleThinkingConfig, error) {
	if level == "" {
		return nil, nil
	}
	if !slices.Contains(googleThinkingLevels(id), level) {
		return nil, fmt.Errorf("google model %s does not support thinking level %q", id, level)
	}
	return &googleThinkingConfig{ThinkingLevel: strings.ToUpper(string(level)), IncludeThoughts: true}, nil
}
