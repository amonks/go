package llm

import (
	"fmt"
	"regexp"
	"slices"
)

var openAISnapshotSuffix = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// openAIModel is one declared OpenAI family: the reasoning efforts its
// model page documents, whether that page lists the Responses API alone
// among the generation endpoints, whether Chat Completions refuses it
// tools, and, for a family whose first dated snapshots precede native
// structured output, the earliest snapshot that has it. The facts sit in one declaration, as googleModel's do, so
// a family can't be declared in one table and missing from another.
type openAIModel struct {
	efforts       []string
	responsesOnly bool
	// Every GPT-6 model's Chat Completions endpoint takes function calls
	// only with reasoning off, and the Completions adapter sends no
	// reasoning setting, so a GPT-6 model that always reasons takes no
	// tools there: https://developers.openai.com/api/docs/guides/latest-model#migration-quickstart
	completionsNoTools bool
	firstSnapshot      string
}

var (
	openAIOff        = []string{"off"} // Non-reasoning models take no reasoning parameter.
	openAIMinToHigh  = []string{"minimal", "low", "medium", "high"}
	openAINoneToHigh = []string{"none", "low", "medium", "high"}
	openAINoneToXH   = []string{"none", "low", "medium", "high", "xhigh"}
	openAINoneToMax  = []string{"none", "low", "medium", "high", "xhigh", "max"}
	openAILowToMax   = []string{"low", "medium", "high", "xhigh", "max"}
)

// openAIModels records documented request capabilities, not account access.
// Model retrieval exposes no capability metadata, so unknown names cannot
// promise any explicit effort setting; the provider may still take its default.
// Sources: https://developers.openai.com/api/docs/models/{model}
// A family OpenAI shuts down leaves the table, snapshots and all:
// gpt-4.1-nano, o1, o3-mini and o4-mini went on 2026-10-23
// (https://developers.openai.com/api/docs/deprecations). GPT-5 Pro
// "defaults to (and only supports) reasoning.effort: high"; GPT-5 Pro and
// GPT-5.3-Codex are the Responses-only families. The gpt-5.6 alias routes
// to gpt-5.6-sol and takes its parameters.
var openAIModels = map[string]openAIModel{
	"gpt-4.1":       {efforts: openAIOff},
	"gpt-4.1-mini":  {efforts: openAIOff},
	"gpt-4o":        {efforts: openAIOff, firstSnapshot: "2024-08-06"},
	"gpt-4o-mini":   {efforts: openAIOff, firstSnapshot: "2024-07-18"},
	"o3":            {efforts: []string{"low", "medium", "high"}},
	"gpt-5":         {efforts: openAIMinToHigh},
	"gpt-5-mini":    {efforts: openAIMinToHigh},
	"gpt-5-nano":    {efforts: openAIMinToHigh},
	"gpt-5-pro":     {efforts: []string{"high"}, responsesOnly: true},
	"gpt-5.1":       {efforts: openAINoneToHigh},
	"gpt-5.2":       {efforts: openAINoneToXH},
	"gpt-5.3-codex": {efforts: []string{"low", "medium", "high", "xhigh"}, responsesOnly: true},
	"gpt-5.4":       {efforts: openAINoneToXH},
	"gpt-5.4-mini":  {efforts: openAINoneToXH},
	"gpt-5.4-nano":  {efforts: openAINoneToXH},
	"gpt-5.5":       {efforts: openAINoneToXH},
	"gpt-5.6":       {efforts: openAINoneToMax},
	"gpt-5.6-sol":   {efforts: openAINoneToMax},
	"gpt-5.6-terra": {efforts: openAINoneToMax},
	"gpt-5.6-luna":  {efforts: openAINoneToMax},
	"gpt-6-sol":     {efforts: openAINoneToMax},
	"gpt-6-luna":    {efforts: openAINoneToMax},
	"gpt-6.1-terra": {efforts: openAINoneToMax},
	"gpt-6-astra":   {efforts: openAILowToMax, completionsNoTools: true},
	"gpt-6.1-sol":   {efforts: openAILowToMax, completionsNoTools: true},
	"gpt-6.2-astra": {efforts: openAILowToMax, completionsNoTools: true},
	"gpt-6.2-sol":   {efforts: openAILowToMax, completionsNoTools: true},
}

// openAIFamily resolves a model id, dated snapshot or not, to its declared
// family. A snapshot older than its family's first structured-output
// snapshot is no declared model.
func openAIFamily(id string) (openAIModel, bool) {
	family := openAISnapshotSuffix.ReplaceAllString(id, "")
	m, ok := openAIModels[family]
	if ok && family != id && id[len(family)+1:] < m.firstSnapshot {
		return openAIModel{}, false
	}
	return m, ok
}

func responsesEfforts(id string) []string {
	m, _ := openAIFamily(id)
	return m.efforts
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
	case APIGoogleGenerateContent:
		_, err := googleThinking(m.ID, level)
		return err == nil
	case APIOpenAIResponses:
		_, err := responsesReasoningEffort(m.ID, level)
		return err == nil
	case APIAnthropicMessages:
		switch level {
		case ThinkingOff:
			return !modelAlwaysThinks(m.ID)
		case ThinkingMax:
			return modelTakesMaxEffort(m.ID)
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
