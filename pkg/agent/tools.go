package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"monks.co/pkg/llm"
)

// formatValidationError creates a detailed error message for missing/invalid tool arguments.
func formatValidationError(toolName string, paramName string, expectedType string, args map[string]any) string {
	argsJSON, _ := json.Marshal(args)
	return fmt.Sprintf("Validation error for tool %q:\n  - %s (%s): parameter is required\n\nReceived arguments:\n%s",
		toolName, paramName, expectedType, string(argsJSON))
}

// formatInvalidValueError creates a detailed error message for invalid parameter values (e.g., invalid enum).
func formatInvalidValueError(toolName string, paramName string, receivedValue string, validValues []string, args map[string]any) string {
	argsJSON, _ := json.Marshal(args)
	return fmt.Sprintf("Validation error for tool %q:\n  - %s: invalid value %q, valid values are %v\n\nReceived arguments:\n%s",
		toolName, paramName, receivedValue, validValues, string(argsJSON))
}

// Tool parameter types for JSON schema generation

// BashParams contains parameters for the bash tool.
type BashParams struct {
	// Command is the command to execute.
	Command string `json:"command" jsonschema:"description=The command to execute"`

	// Timeout is the timeout in seconds (default 120).
	Timeout *int `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds (default 120),optional"`
}

// ReadParams contains parameters for the read tool.
type ReadParams struct {
	// Path is the absolute path to the file to read.
	Path string `json:"path" jsonschema:"description=Absolute path to the file to read"`

	// Offset is the line offset (0-based).
	Offset *int `json:"offset,omitempty" jsonschema:"description=Line offset (0-based),optional"`

	// Limit is the number of lines to read (default 2000).
	Limit *int `json:"limit,omitempty" jsonschema:"description=Number of lines to read (default 2000),optional"`
}

// WriteParams contains parameters for the write tool.
type WriteParams struct {
	// Path is the absolute path to the file to write.
	Path string `json:"path" jsonschema:"description=Absolute path to the file to write"`

	// Content is the content to write to the file.
	Content string `json:"content" jsonschema:"description=Content to write to the file"`
}

// EditParams contains parameters for the edit tool.
type EditParams struct {
	// Path is the absolute path to the file to edit.
	Path string `json:"path" jsonschema:"description=Absolute path to the file to edit"`

	// OldString is the text to find and replace.
	OldString string `json:"old_string" jsonschema:"description=Text to find and replace"`

	// NewString is the replacement text.
	NewString string `json:"new_string" jsonschema:"description=Replacement text"`

	// ReplaceAll replaces all occurrences if true (default false).
	ReplaceAll *bool `json:"replace_all,omitempty" jsonschema:"description=Replace all occurrences (default false),optional"`
}

// TaskParams contains parameters for the task tool that spawns subagents.
type TaskParams struct {
	// Description is a short (3-5 word) description of the task.
	Description string `json:"description" jsonschema:"description=A short (3-5 word) description of the task"`

	// Prompt is the task for the agent to perform.
	Prompt string `json:"prompt" jsonschema:"description=The task for the agent to perform"`

	// SubagentType is the type of specialized agent to use.
	SubagentType string `json:"subagent_type" jsonschema:"description=The type of specialized agent to use for this task"`
}

// TaskWithContextParams contains parameters for the task_with_context tool.
type TaskWithContextParams struct {
	// Description is a short (3-5 word) description of the task.
	Description string `json:"description" jsonschema:"description=A short (3-5 word) description of the task"`

	// Prompt is the task for the agent to perform.
	Prompt string `json:"prompt" jsonschema:"description=The task for the agent to perform"`

	// SubagentType is the type of specialized agent to use.
	SubagentType string `json:"subagent_type" jsonschema:"description=The type of specialized agent to use for this task"`

	// ProjectContext is workflow context and review instructions.
	ProjectContext []string `json:"project_context" jsonschema:"description=Project workflow context, review instructions, and other system guidance"`

	// ContextFiles are paths to additional context files.
	ContextFiles []string `json:"context_files" jsonschema:"description=Paths to additional context files"`

	// TestCommands are the commands used for testing.
	TestCommands []string `json:"test_commands" jsonschema:"description=Commands used to run tests"`

	// PhaseContent describes the current workflow phase content.
	PhaseContent string `json:"phase_content" jsonschema:"description=Workflow phase content"`
}

// builtInTools returns the list of built-in agent tools.
// The task tool is not included until subagent spawning is implemented.
func builtInTools() []llm.Tool {
	return builtInToolsWithTask(false)
}

// builtInToolsWithTask returns the list of built-in agent tools,
// optionally including the task tool. Subagents should not have the
// task tool to prevent recursive spawning.
func builtInToolsWithTask(includeTask bool) []llm.Tool {
	tools := []llm.Tool{
		{
			Name:        "bash",
			Description: "Execute a shell command in the working directory. Commands are checked against permission rules before execution.",
			Parameters:  BashParams{},
		},
		{
			Name:        "read",
			Description: "Read the contents of a file. Returns the file content with line numbers. Handles binary files, missing files, and permission errors gracefully.",
			Parameters:  ReadParams{},
		},
		{
			Name:        "write",
			Description: "Write content to a file. Creates parent directories as needed.",
			Parameters:  WriteParams{},
		},
		{
			Name:        "edit",
			Description: "Perform text replacement in a file. Returns an error if old_string is not found or found multiple times (when replace_all is false).",
			Parameters:  EditParams{},
		},
	}

	if includeTask {
		tools = append(tools, llm.Tool{
			Name:        "task",
			Description: "Launch a subagent to handle a task. Use this for complex multi-step operations that benefit from focused context. The subagent runs synchronously and returns its result.",
			Parameters:  TaskParams{},
		})
		tools = append(tools, llm.Tool{
			Name:        "task_with_context",
			Description: "Launch a subagent to handle a task using a full PromptContent payload (including project context, context files, test commands, and workflow phase content). The subagent runs synchronously and returns its result.",
			Parameters:  TaskWithContextParams{},
		})
	}

	return tools
}

// toolExecutor executes tools and returns results.
type toolExecutor struct {
	workDir     string
	permissions BashPermissions
	env         []string

	// config is the full agent configuration, needed for spawning subagents.
	// Only set for parent agents; nil for subagents (which can't spawn).
	config *AgentConfig
}

// executeTool executes a tool call and returns the result.
func (e *toolExecutor) executeTool(ctx context.Context, toolCall llm.ToolCall) llm.ToolResultMessage {
	var content string
	var isError bool

	switch toolCall.Name {
	case "bash":
		content, isError = e.executeBash(ctx, toolCall.Arguments)
	case "read":
		content, isError = e.executeRead(toolCall.Arguments)
	case "write":
		content, isError = e.executeWrite(toolCall.Arguments)
	case "edit":
		content, isError = e.executeEdit(toolCall.Arguments)
	case "task":
		content, isError = e.executeTask(ctx, toolCall.Arguments)
	case "task_with_context":
		content, isError = e.executeTaskWithContext(ctx, toolCall.Arguments)
	default:
		content = fmt.Sprintf("Unknown tool: %s", toolCall.Name)
		isError = true
	}

	return llm.ToolResultMessage{
		Role:       "toolResult",
		ToolCallID: toolCall.ID,
		ToolName:   toolCall.Name,
		Content: []llm.ContentBlock{
			llm.TextContent{
				Type: "text",
				Text: content,
			},
		},
		IsError:   isError,
		Timestamp: time.Now(),
	}
}

func (e *toolExecutor) executeBash(ctx context.Context, args map[string]any) (string, bool) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return formatValidationError("bash", "command", "string", args), true
	}

	// Check permissions
	if !e.permissions.IsAllowed(command) {
		return fmt.Sprintf("Permission denied: command %q is not allowed by bash permissions", command), true
	}

	timeout := 120
	if t, ok := args["timeout"]; ok {
		switch v := t.(type) {
		case float64:
			timeout = int(v)
		case int:
			timeout = v
		}
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Execute command
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = e.workDir
	if len(e.env) > 0 {
		cmd.Env = append(os.Environ(), e.env...)
	}

	// Capture both stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Build output
	var result strings.Builder
	if stdout.Len() > 0 {
		result.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString(stderr.String())
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("Command timed out after %d seconds\n%s", timeout, result.String()), true
		}
		if result.Len() > 0 {
			return result.String(), true
		}
		return fmt.Sprintf("Command failed: %v", err), true
	}

	output := result.String()
	if output == "" {
		output = "(command completed successfully with no output)"
	}
	return output, false
}

func (e *toolExecutor) executeRead(args map[string]any) (string, bool) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return formatValidationError("read", "path", "string", args), true
	}

	// Resolve path
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workDir, path)
	}

	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("File not found: %s", path), true
		}
		if os.IsPermission(err) {
			return fmt.Sprintf("Permission denied: %s", path), true
		}
		return fmt.Sprintf("Error reading file: %v", err), true
	}

	// Check for binary content
	if isBinary(data) {
		return fmt.Sprintf("File appears to be binary: %s", path), true
	}

	// Parse offset and limit
	offset := 0
	if o, ok := args["offset"]; ok {
		switch v := o.(type) {
		case float64:
			offset = int(v)
		case int:
			offset = v
		}
	}

	limit := 2000
	if l, ok := args["limit"]; ok {
		switch v := l.(type) {
		case float64:
			limit = int(v)
		case int:
			limit = v
		}
	}

	// Format with line numbers
	lines := strings.Split(string(data), "\n")
	if offset >= len(lines) {
		return fmt.Sprintf("Offset %d is past end of file (%d lines)", offset, len(lines)), true
	}

	end := min(offset+limit, len(lines))

	var result strings.Builder
	for i := offset; i < end; i++ {
		line := lines[i]
		// Truncate lines longer than 2000 characters
		if len(line) > 2000 {
			line = line[:2000] + "..."
		}
		fmt.Fprintf(&result, "%6d\t%s\n", i+1, line)
	}

	return result.String(), false
}

func (e *toolExecutor) executeWrite(args map[string]any) (string, bool) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return formatValidationError("write", "path", "string", args), true
	}

	content, ok := args["content"].(string)
	if !ok {
		return formatValidationError("write", "content", "string", args), true
	}

	// Resolve path
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workDir, path)
	}

	// Create parent directories
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Sprintf("Error creating directory: %v", err), true
	}

	// Write file
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		if os.IsPermission(err) {
			return fmt.Sprintf("Permission denied: %s", path), true
		}
		return fmt.Sprintf("Error writing file: %v", err), true
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), false
}

func (e *toolExecutor) executeEdit(args map[string]any) (string, bool) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return formatValidationError("edit", "path", "string", args), true
	}

	oldString, ok := args["old_string"].(string)
	if !ok {
		return formatValidationError("edit", "old_string", "string", args), true
	}

	newString, ok := args["new_string"].(string)
	if !ok {
		return formatValidationError("edit", "new_string", "string", args), true
	}

	replaceAll := false
	if r, ok := args["replace_all"]; ok {
		switch v := r.(type) {
		case bool:
			replaceAll = v
		}
	}

	// Resolve path
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workDir, path)
	}

	// Read existing content
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("File not found: %s", path), true
		}
		if os.IsPermission(err) {
			return fmt.Sprintf("Permission denied: %s", path), true
		}
		return fmt.Sprintf("Error reading file: %v", err), true
	}

	content := string(data)

	// Count occurrences
	count := strings.Count(content, oldString)
	if count == 0 {
		return fmt.Sprintf("old_string not found in %s", path), true
	}

	if count > 1 && !replaceAll {
		return fmt.Sprintf("old_string found %d times in %s; use replace_all=true to replace all occurrences or provide more context to match uniquely", count, path), true
	}

	// Perform replacement
	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(content, oldString, newString)
	} else {
		newContent = strings.Replace(content, oldString, newString, 1)
	}

	// Write back
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		if os.IsPermission(err) {
			return fmt.Sprintf("Permission denied: %s", path), true
		}
		return fmt.Sprintf("Error writing file: %v", err), true
	}

	if replaceAll {
		return fmt.Sprintf("Replaced %d occurrences in %s", count, path), false
	}
	return fmt.Sprintf("Successfully edited %s", path), false
}

// executeTask spawns a subagent to handle a task.
// Subagents run synchronously and return their final text response.
// They do not have access to the task tool to prevent recursive spawning.
func (e *toolExecutor) executeTask(ctx context.Context, args map[string]any) (string, bool) {
	description, ok := args["description"].(string)
	if !ok || description == "" {
		return formatValidationError("task", "description", "string", args), true
	}

	prompt, ok := args["prompt"].(string)
	if !ok || prompt == "" {
		return formatValidationError("task", "prompt", "string", args), true
	}

	subagentType, ok := args["subagent_type"].(string)
	if !ok || subagentType == "" {
		return formatValidationError("task", "subagent_type", "string", args), true
	}

	_ = description

	subPrompt := PromptContent{UserContent: prompt}

	return e.runSubagent(ctx, "task", subagentType, subPrompt)
}

// executeTaskWithContext spawns a subagent with explicit prompt context.
func (e *toolExecutor) executeTaskWithContext(ctx context.Context, args map[string]any) (string, bool) {
	description, ok := args["description"].(string)
	if !ok || description == "" {
		return formatValidationError("task_with_context", "description", "string", args), true
	}

	prompt, ok := args["prompt"].(string)
	if !ok || prompt == "" {
		return formatValidationError("task_with_context", "prompt", "string", args), true
	}

	subagentType, ok := args["subagent_type"].(string)
	if !ok || subagentType == "" {
		return formatValidationError("task_with_context", "subagent_type", "string", args), true
	}

	_ = description

	projectContext, ok := stringSliceArg(args, "project_context")
	if !ok {
		return formatValidationError("task_with_context", "project_context", "[]string", args), true
	}
	contextFiles, ok := stringSliceArg(args, "context_files")
	if !ok {
		return formatValidationError("task_with_context", "context_files", "[]string", args), true
	}
	testCommands, ok := stringSliceArg(args, "test_commands")
	if !ok {
		return formatValidationError("task_with_context", "test_commands", "[]string", args), true
	}
	phaseContent, ok := args["phase_content"].(string)
	if !ok {
		return formatValidationError("task_with_context", "phase_content", "string", args), true
	}

	subPrompt := PromptContent{
		ProjectContext: projectContext,
		ContextFiles:   contextFiles,
		TestCommands:   testCommands,
		PhaseContent:   phaseContent,
		UserContent:    prompt,
	}

	return e.runSubagent(ctx, "task_with_context", subagentType, subPrompt)
}

func stringSliceArg(args map[string]any, key string) ([]string, bool) {
	value, ok := args[key]
	if !ok {
		return nil, false
	}

	switch raw := value.(type) {
	case []string:
		return raw, true
	case []any:
		out := make([]string, 0, len(raw))
		for _, entry := range raw {
			str, ok := entry.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	default:
		return nil, false
	}
}

func (e *toolExecutor) runSubagent(ctx context.Context, toolName string, subagentType string, subPrompt PromptContent) (string, bool) {
	// Validate subagent type
	validTypes := []string{"general", "explore", "bash"}
	if !slices.Contains(validTypes, subagentType) {
		return formatInvalidValueError(toolName, "subagent_type", subagentType, validTypes, map[string]any{"subagent_type": subagentType}), true
	}

	// Check that we have config to spawn from (subagents don't have config, preventing recursion)
	if e.config == nil {
		return "Cannot spawn subagent: task tool not available in this context", true
	}

	// Get tools for this subagent type
	tools := subagentTools(subagentType)

	// Create subagent config (inherits from parent)
	subConfig := AgentConfig{
		Model:           e.config.Model,
		Permissions:     e.config.Permissions,
		WorkDir:         e.workDir,
		GlobalConfigDir: e.config.GlobalConfigDir,
		Env:             e.env,
		CacheRetention:  e.config.CacheRetention,
		SessionID:       e.config.SessionID, // Share session ID for observability
		Version:         e.config.Version,
	}

	// Run the subagent synchronously
	result, err := runSubagent(ctx, subPrompt, subConfig, tools)
	if err != nil {
		return fmt.Sprintf("Subagent error: %v", err), true
	}

	// Extract the final text response from the subagent
	response := extractFinalResponse(result.Messages)
	if response == "" {
		return "Subagent completed but produced no text response", true
	}

	return response, false
}

// subagentTools returns the tools available for a given subagent type.
// None of the subagent types have access to the task tool to prevent recursion.
func subagentTools(subagentType string) []llm.Tool {
	// Get base tools without task
	allTools := builtInToolsWithTask(false)

	switch subagentType {
	case "bash":
		// Bash agent only gets the bash tool
		for _, t := range allTools {
			if t.Name == "bash" {
				return []llm.Tool{t}
			}
		}
		return allTools // fallback

	case "explore":
		// Explore agent gets read-only tools (no write/edit)
		var tools []llm.Tool
		for _, t := range allTools {
			if t.Name == "bash" || t.Name == "read" {
				tools = append(tools, t)
			}
		}
		return tools

	case "general":
		// General agent gets all tools except task
		return allTools

	default:
		return allTools
	}
}

// extractFinalResponse extracts the final text response from messages.
// It looks at the last assistant message and concatenates all text content.
func extractFinalResponse(messages []llm.Message) string {
	// Find the last assistant message
	for i := len(messages) - 1; i >= 0; i-- {
		if msg, ok := messages[i].(llm.AssistantMessage); ok {
			var textParts []string
			for _, block := range msg.Content {
				if tc, ok := block.(llm.TextContent); ok {
					textParts = append(textParts, tc.Text)
				}
			}
			if len(textParts) > 0 {
				return strings.Join(textParts, "\n")
			}
		}
	}
	return ""
}

// isBinary checks if data appears to be binary content.
func isBinary(data []byte) bool {
	// Check for null bytes in the first 8000 bytes
	checkLen := min(len(data), 8000)

	for i := range checkLen {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// IsAllowed checks if a command is allowed by the permission rules.
// Rules are evaluated in order; first match wins. Default is deny.
//
// For compound commands (containing &&, ||, ;, or |), each sub-command
// is checked independently. All sub-commands must be allowed for the
// entire command to be allowed.
func (p BashPermissions) IsAllowed(command string) bool {
	// Split on shell operators and check each sub-command
	subCommands := splitShellCommand(command)
	for _, subCmd := range subCommands {
		if !p.isSubCommandAllowed(subCmd) {
			return false
		}
	}
	return true
}

// isSubCommandAllowed checks if a single (non-compound) command is allowed.
func (p BashPermissions) isSubCommandAllowed(command string) bool {
	for _, rule := range p.Rules {
		if matchPattern(rule.Pattern, command) {
			return rule.Allow
		}
	}
	// Default deny
	return false
}

// splitShellCommand splits a command string on shell operators (&&, ||, ;, |).
// Each resulting sub-command is trimmed of leading/trailing whitespace.
// Empty sub-commands are skipped.
func splitShellCommand(command string) []string {
	var result []string
	var current strings.Builder
	i := 0

	for i < len(command) {
		// Check for two-character operators first
		if i+1 < len(command) {
			twoChar := command[i : i+2]
			if twoChar == "&&" || twoChar == "||" {
				// Found a two-character operator
				if sub := strings.TrimSpace(current.String()); sub != "" {
					result = append(result, sub)
				}
				current.Reset()
				i += 2
				continue
			}
		}

		// Check for single-character operators
		if command[i] == ';' || command[i] == '|' {
			if sub := strings.TrimSpace(current.String()); sub != "" {
				result = append(result, sub)
			}
			current.Reset()
			i++
			continue
		}

		// Regular character
		current.WriteByte(command[i])
		i++
	}

	// Don't forget the last segment
	if sub := strings.TrimSpace(current.String()); sub != "" {
		result = append(result, sub)
	}

	return result
}

// matchPattern matches a command against a glob pattern.
// The pattern supports:
//   - * matches any sequence of characters
//   - ? matches any single character
func matchPattern(pattern, command string) bool {
	// Use filepath.Match for glob matching
	// However, filepath.Match doesn't handle arbitrary command strings well
	// because it treats / specially. Use a custom implementation.
	return globMatch(pattern, command)
}

// globMatch performs glob-style pattern matching.
func globMatch(pattern, str string) bool {
	// Handle empty pattern
	if pattern == "" {
		return str == ""
	}

	// Handle wildcard-only pattern
	if pattern == "*" {
		return true
	}

	pi := 0 // pattern index
	si := 0 // string index
	lastWildcard := -1
	lastMatch := -1

	for si < len(str) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == str[si]) {
			// Match single character or ?
			pi++
			si++
		} else if pi < len(pattern) && pattern[pi] == '*' {
			// Record position for backtracking
			lastWildcard = pi
			lastMatch = si
			pi++
		} else if lastWildcard >= 0 {
			// Backtrack: try matching one more character with *
			pi = lastWildcard + 1
			lastMatch++
			si = lastMatch
		} else {
			return false
		}
	}

	// Check remaining pattern characters (should all be *)
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}

	return pi == len(pattern)
}
