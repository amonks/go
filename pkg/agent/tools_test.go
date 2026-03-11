package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"monks.co/pkg/llm"
)

func TestToolExecutor_Bash(t *testing.T) {
	tmpDir := t.TempDir()
	executor := &toolExecutor{
		workDir: tmpDir,
		permissions: BashPermissions{
			Rules: []BashRule{
				{Pattern: "*", Allow: true},
			},
		},
	}

	t.Run("echo command", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:        "tc1",
			Name:      "bash",
			Arguments: map[string]any{"command": "echo hello"},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "hello") {
			t.Errorf("expected output to contain 'hello', got %q", text)
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		restrictedExecutor := &toolExecutor{
			workDir: tmpDir,
			permissions: BashPermissions{
				Rules: []BashRule{
					{Pattern: "rm *", Allow: false},
					{Pattern: "*", Allow: true},
				},
			},
		}
		tc := llm.ToolCall{
			ID:        "tc2",
			Name:      "bash",
			Arguments: map[string]any{"command": "rm -rf /"},
		}
		result := restrictedExecutor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected permission denied error")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "Permission denied") {
			t.Errorf("expected 'Permission denied' in error, got %q", text)
		}
	})

	t.Run("missing command parameter", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:        "tc3",
			Name:      "bash",
			Arguments: map[string]any{},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for missing command")
		}
		text := getToolResultText(result)
		// Verify the error message contains helpful information
		if !strings.Contains(text, "Validation error") {
			t.Errorf("expected 'Validation error' in message, got %q", text)
		}
		if !strings.Contains(text, "command") {
			t.Errorf("expected 'command' in error message, got %q", text)
		}
		if !strings.Contains(text, "Received arguments") {
			t.Errorf("expected 'Received arguments' in error message, got %q", text)
		}
	})

	t.Run("command failure", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:        "tc4",
			Name:      "bash",
			Arguments: map[string]any{"command": "exit 1"},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for failed command")
		}
	})

	t.Run("working directory", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:        "tc5",
			Name:      "bash",
			Arguments: map[string]any{"command": "pwd"},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}
		text := getToolResultText(result)
		if !strings.Contains(text, tmpDir) {
			t.Errorf("expected pwd to contain %q, got %q", tmpDir, text)
		}
	})

	t.Run("environment variables", func(t *testing.T) {
		envExecutor := &toolExecutor{
			workDir: tmpDir,
			permissions: BashPermissions{
				Rules: []BashRule{
					{Pattern: "*", Allow: true},
				},
			},
			env: []string{"INCREMENTUM_TODO_PROPOSER=true"},
		}
		tc := llm.ToolCall{
			ID:        "tc6",
			Name:      "bash",
			Arguments: map[string]any{"command": "echo $INCREMENTUM_TODO_PROPOSER"},
		}
		result := envExecutor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}
		text := strings.TrimSpace(getToolResultText(result))
		if text != "true" {
			t.Errorf("expected env var to be 'true', got %q", text)
		}
	})
}

func TestToolExecutor_Read(t *testing.T) {
	tmpDir := t.TempDir()
	executor := &toolExecutor{
		workDir: tmpDir,
		permissions: BashPermissions{
			Rules: []BashRule{{Pattern: "*", Allow: true}},
		},
	}

	// Create test file
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("read whole file", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:        "tc1",
			Name:      "read",
			Arguments: map[string]any{"path": testFile},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "line 1") {
			t.Errorf("expected 'line 1' in output, got %q", text)
		}
		if !strings.Contains(text, "line 5") {
			t.Errorf("expected 'line 5' in output, got %q", text)
		}
		// Check line numbers are present
		if !strings.Contains(text, "1\t") {
			t.Errorf("expected line numbers in output, got %q", text)
		}
	})

	t.Run("read with offset", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:        "tc2",
			Name:      "read",
			Arguments: map[string]any{"path": testFile, "offset": 2.0},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}
		text := getToolResultText(result)
		// Offset is 0-based, so offset=2 means starting from line 3
		if !strings.Contains(text, "line 3") {
			t.Errorf("expected 'line 3' in output with offset, got %q", text)
		}
	})

	t.Run("read with limit", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:        "tc3",
			Name:      "read",
			Arguments: map[string]any{"path": testFile, "limit": 2.0},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "line 1") {
			t.Errorf("expected 'line 1' in output, got %q", text)
		}
		if !strings.Contains(text, "line 2") {
			t.Errorf("expected 'line 2' in output, got %q", text)
		}
		// Should not contain line 3 with limit of 2
		lines := strings.Split(strings.TrimSpace(text), "\n")
		if len(lines) > 2 {
			t.Errorf("expected at most 2 lines, got %d", len(lines))
		}
	})

	t.Run("file not found", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:        "tc4",
			Name:      "read",
			Arguments: map[string]any{"path": "/nonexistent/file.txt"},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for missing file")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "not found") {
			t.Errorf("expected 'not found' in error, got %q", text)
		}
	})

	t.Run("relative path", func(t *testing.T) {
		// Create file in workdir
		relFile := filepath.Join(tmpDir, "relative.txt")
		if err := os.WriteFile(relFile, []byte("relative content"), 0644); err != nil {
			t.Fatal(err)
		}

		tc := llm.ToolCall{
			ID:        "tc5",
			Name:      "read",
			Arguments: map[string]any{"path": "relative.txt"},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "relative content") {
			t.Errorf("expected 'relative content' in output, got %q", text)
		}
	})

	t.Run("binary file detection", func(t *testing.T) {
		binFile := filepath.Join(tmpDir, "binary.dat")
		// Create file with null bytes (binary indicator)
		if err := os.WriteFile(binFile, []byte{0x00, 0x01, 0x02}, 0644); err != nil {
			t.Fatal(err)
		}

		tc := llm.ToolCall{
			ID:        "tc6",
			Name:      "read",
			Arguments: map[string]any{"path": binFile},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for binary file")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "binary") {
			t.Errorf("expected 'binary' in error, got %q", text)
		}
	})
}

func TestToolExecutor_Write(t *testing.T) {
	tmpDir := t.TempDir()
	executor := &toolExecutor{
		workDir: tmpDir,
		permissions: BashPermissions{
			Rules: []BashRule{{Pattern: "*", Allow: true}},
		},
	}

	t.Run("write new file", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "newfile.txt")
		tc := llm.ToolCall{
			ID:        "tc1",
			Name:      "write",
			Arguments: map[string]any{"path": testFile, "content": "new content"},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}

		// Verify file was created
		content, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "new content" {
			t.Errorf("expected 'new content', got %q", string(content))
		}
	})

	t.Run("create parent directories", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "nested", "dir", "file.txt")
		tc := llm.ToolCall{
			ID:        "tc2",
			Name:      "write",
			Arguments: map[string]any{"path": testFile, "content": "nested content"},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}

		// Verify file was created
		content, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "nested content" {
			t.Errorf("expected 'nested content', got %q", string(content))
		}
	})

	t.Run("overwrite existing file", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "existing.txt")
		if err := os.WriteFile(testFile, []byte("old content"), 0644); err != nil {
			t.Fatal(err)
		}

		tc := llm.ToolCall{
			ID:        "tc3",
			Name:      "write",
			Arguments: map[string]any{"path": testFile, "content": "updated content"},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}

		content, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(content) != "updated content" {
			t.Errorf("expected 'updated content', got %q", string(content))
		}
	})

	t.Run("missing parameters", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:        "tc4",
			Name:      "write",
			Arguments: map[string]any{"path": "/some/path"},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for missing content")
		}
	})
}

func TestToolExecutor_Edit(t *testing.T) {
	tmpDir := t.TempDir()
	executor := &toolExecutor{
		workDir: tmpDir,
		permissions: BashPermissions{
			Rules: []BashRule{{Pattern: "*", Allow: true}},
		},
	}

	t.Run("single replacement", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "edit1.txt")
		if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
			t.Fatal(err)
		}

		tc := llm.ToolCall{
			ID:   "tc1",
			Name: "edit",
			Arguments: map[string]any{
				"path":       testFile,
				"old_string": "world",
				"new_string": "universe",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}

		content, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "hello universe" {
			t.Errorf("expected 'hello universe', got %q", string(content))
		}
	})

	t.Run("replace all", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "edit2.txt")
		if err := os.WriteFile(testFile, []byte("foo bar foo baz foo"), 0644); err != nil {
			t.Fatal(err)
		}

		tc := llm.ToolCall{
			ID:   "tc2",
			Name: "edit",
			Arguments: map[string]any{
				"path":        testFile,
				"old_string":  "foo",
				"new_string":  "qux",
				"replace_all": true,
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if result.IsError {
			t.Errorf("unexpected error: %v", getToolResultText(result))
		}

		content, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "qux bar qux baz qux" {
			t.Errorf("expected 'qux bar qux baz qux', got %q", string(content))
		}
	})

	t.Run("old string not found", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "edit3.txt")
		if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
			t.Fatal(err)
		}

		tc := llm.ToolCall{
			ID:   "tc3",
			Name: "edit",
			Arguments: map[string]any{
				"path":       testFile,
				"old_string": "nonexistent",
				"new_string": "replacement",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for string not found")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "not found") {
			t.Errorf("expected 'not found' in error, got %q", text)
		}
	})

	t.Run("multiple matches without replace_all", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "edit4.txt")
		if err := os.WriteFile(testFile, []byte("foo bar foo"), 0644); err != nil {
			t.Fatal(err)
		}

		tc := llm.ToolCall{
			ID:   "tc4",
			Name: "edit",
			Arguments: map[string]any{
				"path":       testFile,
				"old_string": "foo",
				"new_string": "qux",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for multiple matches")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "found 2 times") {
			t.Errorf("expected 'found 2 times' in error, got %q", text)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:   "tc5",
			Name: "edit",
			Arguments: map[string]any{
				"path":       "/nonexistent/file.txt",
				"old_string": "foo",
				"new_string": "bar",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for missing file")
		}
	})
}

func TestToolExecutor_UnknownTool(t *testing.T) {
	executor := &toolExecutor{
		workDir: t.TempDir(),
		permissions: BashPermissions{
			Rules: []BashRule{{Pattern: "*", Allow: true}},
		},
	}

	tc := llm.ToolCall{
		ID:        "tc1",
		Name:      "unknown_tool",
		Arguments: map[string]any{},
	}
	result := executor.executeTool(context.Background(), tc)
	if !result.IsError {
		t.Error("expected error for unknown tool")
	}
	text := getToolResultText(result)
	if !strings.Contains(text, "Unknown tool") {
		t.Errorf("expected 'Unknown tool' in error, got %q", text)
	}
}

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", []byte{}, false},
		{"text", []byte("hello world"), false},
		{"text with newlines", []byte("line1\nline2\n"), false},
		{"null byte at start", []byte{0x00, 'a', 'b', 'c'}, true},
		{"null byte in middle", []byte{'a', 'b', 0x00, 'c'}, true},
		{"all null bytes", []byte{0x00, 0x00, 0x00}, true},
		{"utf8 text", []byte("hello \xe4\xb8\x96\xe7\x95\x8c"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBinary(tt.data)
			if got != tt.want {
				t.Errorf("isBinary(%v) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestBuiltInTools(t *testing.T) {
	tools := builtInTools()

	// builtInTools() no longer includes the task tool until subagent spawning is implemented
	if len(tools) != 4 {
		t.Errorf("expected 4 built-in tools, got %d", len(tools))
	}

	expectedTools := map[string]bool{
		"bash":  false,
		"read":  false,
		"write": false,
		"edit":  false,
	}

	for _, tool := range tools {
		if _, ok := expectedTools[tool.Name]; !ok {
			t.Errorf("unexpected tool: %s", tool.Name)
		}
		expectedTools[tool.Name] = true

		if tool.Description == "" {
			t.Errorf("tool %s has empty description", tool.Name)
		}
		if tool.Parameters == nil {
			t.Errorf("tool %s has nil parameters", tool.Name)
		}
	}

	for name, found := range expectedTools {
		if !found {
			t.Errorf("missing expected tool: %s", name)
		}
	}
}

func TestBuiltInToolsWithTask(t *testing.T) {
	t.Run("with task tool", func(t *testing.T) {
		tools := builtInToolsWithTask(true)
		hasTask := false
		hasTaskWithContext := false
		for _, tool := range tools {
			if tool.Name == "task" {
				hasTask = true
			}
			if tool.Name == "task_with_context" {
				hasTaskWithContext = true
			}
		}
		if !hasTask {
			t.Error("expected task tool when includeTask=true")
		}
		if !hasTaskWithContext {
			t.Error("expected task_with_context tool when includeTask=true")
		}
	})

	t.Run("without task tool", func(t *testing.T) {
		tools := builtInToolsWithTask(false)
		for _, tool := range tools {
			if tool.Name == "task" {
				t.Error("did not expect task tool when includeTask=false")
			}
			if tool.Name == "task_with_context" {
				t.Error("did not expect task_with_context tool when includeTask=false")
			}
		}
		if len(tools) != 4 {
			t.Errorf("expected 4 tools without task, got %d", len(tools))
		}
	})
}

func TestToolExecutor_Task(t *testing.T) {
	tmpDir := t.TempDir()
	executor := &toolExecutor{
		workDir: tmpDir,
		permissions: BashPermissions{
			Rules: []BashRule{{Pattern: "*", Allow: true}},
		},
	}

	t.Run("missing description", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:   "tc1",
			Name: "task",
			Arguments: map[string]any{
				"prompt":        "do something",
				"subagent_type": "general",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for missing description")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "Validation error") {
			t.Errorf("expected 'Validation error' in message, got %q", text)
		}
	})

	t.Run("missing prompt", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:   "tc2",
			Name: "task",
			Arguments: map[string]any{
				"description":   "test task",
				"subagent_type": "general",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for missing prompt")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "Validation error") {
			t.Errorf("expected 'Validation error' in message, got %q", text)
		}
	})

	t.Run("missing subagent_type", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:   "tc3",
			Name: "task",
			Arguments: map[string]any{
				"description": "test task",
				"prompt":      "do something",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for missing subagent_type")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "Validation error") {
			t.Errorf("expected 'Validation error' in message, got %q", text)
		}
	})

	t.Run("invalid subagent_type", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:   "tc4",
			Name: "task",
			Arguments: map[string]any{
				"description":   "test task",
				"prompt":        "do something",
				"subagent_type": "invalid-type",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for invalid subagent_type")
		}
		text := getToolResultText(result)
		// Should use consistent validation error format
		if !strings.Contains(text, "Validation error") {
			t.Errorf("expected 'Validation error' in message, got %q", text)
		}
		if !strings.Contains(text, "invalid value") {
			t.Errorf("expected 'invalid value' in message, got %q", text)
		}
	})

	t.Run("task tool without config returns error", func(t *testing.T) {
		// Executor without config (as in subagents) should not be able to spawn subagents
		tc := llm.ToolCall{
			ID:   "tc5",
			Name: "task",
			Arguments: map[string]any{
				"description":   "test task",
				"prompt":        "do something",
				"subagent_type": "general",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		// Should return an error because executor has no config
		if !result.IsError {
			t.Error("expected error when config is nil")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "task tool not available") {
			t.Errorf("expected 'task tool not available' in message, got %q", text)
		}
	})
}

func TestToolExecutor_TaskWithContext(t *testing.T) {
	tmpDir := t.TempDir()
	executor := &toolExecutor{
		workDir: tmpDir,
		permissions: BashPermissions{
			Rules: []BashRule{{Pattern: "*", Allow: true}},
		},
	}

	t.Run("missing project_context", func(t *testing.T) {
		tc := llm.ToolCall{
			ID:   "tc1",
			Name: "task_with_context",
			Arguments: map[string]any{
				"description":   "test task",
				"prompt":        "do something",
				"subagent_type": "general",
			},
		}
		result := executor.executeTool(context.Background(), tc)
		if !result.IsError {
			t.Error("expected error for missing project_context")
		}
		text := getToolResultText(result)
		if !strings.Contains(text, "Validation error") {
			t.Errorf("expected 'Validation error' in message, got %q", text)
		}
	})

	t.Run("general subagent gets all tools except task", func(t *testing.T) {
		tools := subagentTools("general")
		toolNames := make(map[string]bool)
		for _, tool := range tools {
			toolNames[tool.Name] = true
		}

		// Should have bash, read, write, edit
		expected := []string{"bash", "read", "write", "edit"}
		for _, name := range expected {
			if !toolNames[name] {
				t.Errorf("general subagent missing tool: %s", name)
			}
		}
		// Should NOT have task tools
		if toolNames["task"] {
			t.Error("general subagent should not have task tool")
		}
		if toolNames["task_with_context"] {
			t.Error("general subagent should not have task_with_context tool")
		}
	})

	t.Run("bash subagent only gets bash tool", func(t *testing.T) {
		tools := subagentTools("bash")
		if len(tools) != 1 {
			t.Errorf("bash subagent should have 1 tool, got %d", len(tools))
		}
		if len(tools) > 0 && tools[0].Name != "bash" {
			t.Errorf("bash subagent's tool should be 'bash', got %q", tools[0].Name)
		}
	})

	t.Run("explore subagent gets read-only tools", func(t *testing.T) {
		tools := subagentTools("explore")
		toolNames := make(map[string]bool)
		for _, tool := range tools {
			toolNames[tool.Name] = true
		}

		// Should have bash and read
		if !toolNames["bash"] {
			t.Error("explore subagent should have bash tool")
		}
		if !toolNames["read"] {
			t.Error("explore subagent should have read tool")
		}
		// Should NOT have write or edit
		if toolNames["write"] {
			t.Error("explore subagent should not have write tool")
		}
		if toolNames["edit"] {
			t.Error("explore subagent should not have edit tool")
		}
		// Should NOT have task tools
		if toolNames["task"] {
			t.Error("explore subagent should not have task tool")
		}
		if toolNames["task_with_context"] {
			t.Error("explore subagent should not have task_with_context tool")
		}
	})
}

// Helper to extract text from tool result
func getToolResultText(result llm.ToolResultMessage) string {
	for _, block := range result.Content {
		if tc, ok := block.(llm.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
