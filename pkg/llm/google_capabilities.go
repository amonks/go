package llm

import (
	"fmt"
	"slices"
	"strings"
)

// googleModel is one documented Gemini text-generation model: the
// thinking levels it accepts and, on a model that decodes long outputs
// in hops, the most one streaming hop generates. Both facts sit in one
// declaration so a long-decode model cannot be declared without the
// guard that keeps a larger cap from opting into the continuation
// protocol, which the adapter lacks. A zero longDecodeHop means no long
// decode.
type googleModel struct {
	levels        []ThinkingLevel
	longDecodeHop int
}

var (
	geminiMinimalToHigh = []ThinkingLevel{ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}
	geminiLowToHigh     = []ThinkingLevel{ThinkingLow, ThinkingMedium, ThinkingHigh}
	geminiLowToXHigh    = []ThinkingLevel{ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh}
)

// gemini4Hop is every Gemini 4 model's streaming hop:
// https://ai.google.dev/gemini-api/docs/generate-content/long-decode
const gemini4Hop = 65536

// googleModels lists documented text-generation families, not a guess
// based on the "gemini-" prefix or Models API visibility. Sources:
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
var googleModels = map[string]googleModel{
	"gemini-3-flash-preview":             {levels: geminiMinimalToHigh},
	"gemini-3.1-flash-lite":              {levels: geminiMinimalToHigh},
	"gemini-3.5-flash":                   {levels: geminiMinimalToHigh},
	"gemini-3.5-flash-lite":              {levels: geminiMinimalToHigh},
	"gemini-3.6-flash":                   {levels: geminiMinimalToHigh},
	"gemini-3.1-pro-preview":             {levels: geminiLowToHigh},
	"gemini-3.1-pro-preview-customtools": {levels: geminiLowToHigh},
	"gemini-3.7-flash":                   {levels: geminiLowToHigh},
	"gemini-3.8-flash":                   {levels: geminiLowToHigh},
	"gemini-4-argon":                     {levels: geminiLowToHigh, longDecodeHop: gemini4Hop},
	"gemini-4-carbon":                    {levels: geminiLowToHigh, longDecodeHop: gemini4Hop},
	// The Geminis that add xhigh:
	// https://ai.google.dev/gemini-api/docs/whats-new-gemini-4-hydrogen
	// https://ai.google.dev/gemini-api/docs/whats-new-gemini-4-neon
	"gemini-4-hydrogen": {levels: geminiLowToXHigh, longDecodeHop: gemini4Hop},
	"gemini-4-neon":     {levels: geminiLowToXHigh, longDecodeHop: gemini4Hop},
}

func googleThinkingLevels(id string) []ThinkingLevel { return googleModels[id].levels }

func googleModelKnown(id string) bool { return len(googleThinkingLevels(id)) > 0 }

// googleLongDecodeHop reports the most one streaming hop of a
// long-decode model generates; a larger output cap is long decode.
func googleLongDecodeHop(id string) (int, bool) {
	hop := googleModels[id].longDecodeHop
	return hop, hop > 0
}

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
