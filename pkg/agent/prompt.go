package agent

import (
	"fmt"
	"strings"
	"time"

	"monks.co/pkg/llm"
)

// BuildSystemBlocks generates the system prompt blocks for the agent with context-specific information.
// workDir is the working directory where commands will be executed.
func BuildSystemBlocks(workDir string, content PromptContent) []llm.SystemBlock {
	now := time.Now().Format("Monday, January 2, 2006")

	var blocks []llm.SystemBlock

	tier1 := strings.Join([]string{
		"You are an AI assistant that helps users with software engineering tasks. You have access to tools that let you interact with the filesystem and execute commands.",
		"",
		"## Task tool guidance",
		"",
		"When to use the task tool:",
		"- **Use for codebase exploration**: When exploring the codebase to gather context or answer questions that aren't simple file lookups, use explore subagent.",
		"- **Use for complex multi-step operations**: When a task requires multiple tool calls with focused context.",
		"- **Use to reduce context usage**: Subagents help keep the main conversation focused.",
		"",
		"When NOT to use the task tool:",
		"- If you want to read a specific file path, use the read tool directly.",
		"- If you are searching for a specific class or function definition, use bash with grep directly.",
		"- If you are searching within a specific file or 2-3 files, use the read tool directly.",
		"",
		"Supported subagent types:",
		"- general: General-purpose agent with full tool access (bash, read, write, edit). Use for multi-step tasks requiring file edits or when you need to research complex questions and execute multi-step operations.",
		"- explore: Fast, read-only agent for exploring codebases (bash, read only). Use when you need to quickly find files, search code for keywords, or answer questions about the codebase. Cannot modify files.",
		"- bash: Command execution specialist for running bash commands only.",
		"",
		"Usage notes:",
		"- Provide clear, detailed prompts so the subagent can work autonomously.",
		"- Tell the subagent whether you expect it to write code or just research (search, read).",
		"- The subagent's response is returned as the tool result.",
		"- Subagents cannot spawn further subagents (single level of nesting).",
		"",
		"## Guidelines",
		"",
		"1. **Read before editing**: Always read a file before editing it to understand its content and structure.",
		"",
		"2. **Use precise edits**: When using the edit tool, provide enough context in old_string to uniquely identify the location. If old_string matches multiple locations, you'll get an error.",
		"",
		"3. **Prefer edit over write**: Use the edit tool for modifications. Only use write for creating new files or when the entire file content needs to be replaced.",
		"",
		"4. **Handle errors gracefully**: Tool calls may fail. Check the result and adjust your approach if needed.",
		"",
		"5. **Be concise but thorough**: Provide clear, focused responses without unnecessary verbosity, but include sufficient detail to be useful.",
		"",
		"6. **When uncertain, investigate**: If you're unsure about something, use available tools to investigate rather than guess.",
		"",
		"7. **Format output as markdown**: Structure your responses using markdown formatting.",
		"",
		"8. **Execute independent tool calls in parallel**: When you need to perform multiple independent operations (like reading several files), make those tool calls in parallel rather than sequentially.",
		"",
		"9. **Verify changes**: After making edits, consider reading the file again to verify the changes were applied correctly.",
	}, "\n")
	blocks = append(blocks, llm.SystemBlock{Text: tier1, CacheBreakpoint: true})

	var tier2Parts []string
	for _, part := range content.ProjectContext {
		if strings.TrimSpace(part) == "" {
			continue
		}
		tier2Parts = append(tier2Parts, part)
	}
	for _, part := range content.ContextFiles {
		if strings.TrimSpace(part) == "" {
			continue
		}
		tier2Parts = append(tier2Parts, part)
	}
	if len(content.TestCommands) > 0 {
		var items []string
		for _, command := range content.TestCommands {
			command = strings.TrimSpace(command)
			if command == "" {
				continue
			}
			items = append(items, fmt.Sprintf("- %s", command))
		}
		if len(items) > 0 {
			lines := []string{"Test commands", ""}
			lines = append(lines, items...)
			tier2Parts = append(tier2Parts, strings.Join(lines, "\n"))
		}
	}
	if len(tier2Parts) > 0 {
		blocks = append(blocks, llm.SystemBlock{Text: strings.Join(tier2Parts, "\n\n"), CacheBreakpoint: true})
	}

	var tier3Parts []string
	if strings.TrimSpace(workDir) != "" {
		tier3Parts = append(tier3Parts, fmt.Sprintf("Current working directory: %s", workDir))
	}
	tier3Parts = append(tier3Parts, fmt.Sprintf("Current date and time: %s", now))
	if strings.TrimSpace(content.PhaseContent) != "" {
		tier3Parts = append(tier3Parts, content.PhaseContent)
	}
	if len(tier3Parts) > 0 {
		blocks = append(blocks, llm.SystemBlock{Text: strings.Join(tier3Parts, "\n\n"), CacheBreakpoint: false})
	}

	return blocks
}
