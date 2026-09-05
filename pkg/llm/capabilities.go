package llm

// SupportsToolChoice reports whether the adapter permits forcing a named
// tool on this model. It describes known adapter restrictions, not provider
// availability. Anthropic requests also need ThinkingOff when forcing tools.
func (m Model) SupportsToolChoice() bool {
	switch m.API {
	case APIAnthropicMessages:
		return !modelAlwaysThinks(m.ID)
	case APIOpenAICompletions, APIOpenAIResponses:
		return true
	default:
		return false
	}
}
