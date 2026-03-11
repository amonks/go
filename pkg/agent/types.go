// Package agent provides the core agent loop without persistence.
//
// This package implements the agent loop (prompt -> LLM -> tool execution -> repeat),
// built-in tools (bash, read, write, edit), and bash command permission filtering.
package agent

import (
	"monks.co/pkg/llm"
)

// PromptContent bundles the structured pieces used to assemble prompts.
type PromptContent struct {
	// ProjectContext contains rendered templates shared across phases.
	ProjectContext []string
	// ContextFiles contains AGENTS.md/CLAUDE.md contents.
	ContextFiles []string
	// TestCommands lists configured test commands.
	TestCommands []string
	// PhaseContent is the phase-specific instructions.
	PhaseContent string
	// UserContent is the todo/series/feedback content for the current iteration.
	UserContent string
}

// AgentConfig configures an agent run.
type AgentConfig struct {
	// Model is the LLM model to use for completions.
	Model llm.Model

	// Permissions controls which bash commands are allowed.
	Permissions BashPermissions

	// WorkDir is the working directory for tools.
	// If empty, the current working directory is used.
	WorkDir string

	// GlobalConfigDir is the global config directory (e.g., ~/.config/incrementum)
	// for loading global AGENTS.md or CLAUDE.md files.
	// If empty, no global context is loaded.
	GlobalConfigDir string

	// Env contains additional environment variables for tool execution.
	Env []string

	// SessionID is an optional identifier for grouping related API requests
	// into sessions for observability purposes.
	SessionID string

	// Version is the version string (commit ID) to include in the User-Agent header.
	Version string

	// InputCh receives additional user input for interactive sessions.
	// If nil, the agent runs in single-shot mode.
	InputCh <-chan string

	// CacheRetention controls prompt caching for providers that support it.
	// Defaults to CacheShort when unset.
	CacheRetention llm.CacheRetention
}

// BashPermissions controls which bash commands are allowed.
// Rules are evaluated in order; first match wins. Default is deny.
type BashPermissions struct {
	Rules []BashRule
}

// BashRule defines a single permission rule for bash commands.
type BashRule struct {
	// Pattern is a glob pattern matching the command.
	Pattern string

	// Allow indicates whether matching commands are allowed (true) or denied (false).
	Allow bool
}

// RunResult contains the result of an agent run.
type RunResult struct {
	// Messages contains the full conversation history.
	Messages []llm.Message

	// Usage contains aggregate token usage and costs across all LLM calls.
	Usage llm.Usage

	// Error is non-nil if the agent failed.
	Error error
}

// RunHandle provides access to a running agent.
type RunHandle struct {
	result chan RunResult
}

// Wait blocks until the agent completes and returns the result.
func (h *RunHandle) Wait() (RunResult, error) {
	result := <-h.result
	return result, result.Error
}
