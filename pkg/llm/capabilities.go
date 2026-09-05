package llm

// SupportsToolChoice reports whether the adapter permits forcing a named
// tool on this model. It describes known adapter restrictions, not provider
// availability. Anthropic requests also need ThinkingOff when forcing tools.
func (m Model) SupportsToolChoice() bool {
	switch m.API {
	case APIAnthropicMessages:
		return !modelAlwaysThinks(m.ID)
	case APIOpenAIResponses:
		return len(responsesEfforts(m.ID)) > 0
	case APIOpenAICompletions:
		return true
	default:
		return false
	}
}
