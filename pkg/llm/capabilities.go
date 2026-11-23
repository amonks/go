package llm

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
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
		// Chat Completions serves compatible endpoints whose models this
		// package doesn't know, but no Responses-only family, and no
		// tools to a GPT-6 model that always reasons.
		family, _ := openAIFamily(m.ID)
		return !family.responsesOnly && !family.completionsNoTools
	case APIGoogleGenerateContent:
		return googleModelKnown(m.ID)
	default:
		return false
	}
}

var anthropicSnapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// anthropicModels are the Anthropic families the package declares,
// without dated snapshots; openAIModels and googleModels hold the others'.
// Each takes native JSON-schema output, which is also what the package
// declares a model by. A family leaves its table, snapshots and all, when
// it shuts down or its provider withdraws it from new keys, and a model
// open only to verified organizations or invited programs stays out.
// Sources: the providers' structured-output guides linked in specs/pkg-llm.md,
// and https://platform.claude.com/docs/en/about-claude/model-deprecations:
// Sonnet 4.5 retires on 2026-11-30. Mythos 5, 5.1 and 5.2 are open only
// to organizations Anthropic verifies, and Mythos Preview, deprecated on
// 2026-06-09, only to Project Glasswing; the Fable models share their
// cards and request shapes.
var anthropicModels = []string{
	"claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-opus-5-5",
	"claude-sonnet-4-5", "claude-sonnet-4-6", "claude-sonnet-5", "claude-sonnet-5-5", "claude-haiku-4-5", "claude-haiku-5-5",
	"claude-fable-5", "claude-fable-5-1", "claude-fable-5-2",
}

// DeclaredModels lists, sorted, the model families the package declares
// for api: the ids SupportsStructuredOutput accepts, without the dated
// snapshots it also accepts. Provider listings name models this package
// cannot drive, so a host that offers "any declared model" — and has to
// price, or otherwise serve, each one — walks this rather than the
// provider's list.
func DeclaredModels(api API) []string {
	var ids []string
	switch api {
	case APIAnthropicMessages:
		ids = slices.Clone(anthropicModels)
	case APIOpenAIResponses:
		ids = slices.Collect(maps.Keys(openAIModels))
	case APIOpenAICompletions:
		for id, m := range openAIModels {
			if !m.responsesOnly {
				ids = append(ids, id)
			}
		}
	case APIGoogleGenerateContent:
		ids = slices.Collect(maps.Keys(googleModels))
	}
	slices.Sort(ids)
	return ids
}

// SupportsStructuredOutput reports documented native JSON-schema output
// support, independently of forced tool execution and thinking settings.
func (m Model) SupportsStructuredOutput() bool {
	switch m.API {
	case APIGoogleGenerateContent:
		return googleModelKnown(m.ID)
	case APIAnthropicMessages:
		return slices.Contains(anthropicModels, anthropicSnapshotSuffix.ReplaceAllString(m.ID, ""))
	case APIOpenAIResponses, APIOpenAICompletions:
		family, ok := openAIFamily(m.ID)
		return ok && !(m.API == APIOpenAICompletions && family.responsesOnly)
	}
	return false
}

func structuredRequestSchema(model Model, schema *Schema) (jsonObject, error) {
	if !model.SupportsStructuredOutput() {
		return nil, fmt.Errorf("llm: model %s has no documented native structured-output capability", model.ID)
	}
	return outputSchema(schema, model.API)
}
