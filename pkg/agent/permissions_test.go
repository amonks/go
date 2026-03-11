package agent

import "testing"

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		str     string
		want    bool
	}{
		// Empty pattern
		{"", "", true},
		{"", "abc", false},

		// Exact match
		{"abc", "abc", true},
		{"abc", "abcd", false},
		{"abc", "ab", false},

		// Single wildcard
		{"*", "", true},
		{"*", "abc", true},
		{"*", "anything at all", true},

		// Wildcard at end
		{"abc*", "abc", true},
		{"abc*", "abcd", true},
		{"abc*", "abcdef", true},
		{"abc*", "ab", false},
		{"abc*", "xabc", false},

		// Wildcard at start
		{"*abc", "abc", true},
		{"*abc", "xabc", true},
		{"*abc", "xxxabc", true},
		{"*abc", "abcd", false},

		// Wildcard in middle
		{"a*c", "ac", true},
		{"a*c", "abc", true},
		{"a*c", "aXXXc", true},
		{"a*c", "acd", false},

		// Multiple wildcards
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "ac", false},

		// Question mark
		{"a?c", "abc", true},
		{"a?c", "aXc", true},
		{"a?c", "ac", false},
		{"a?c", "abbc", false},

		// Command-like patterns
		{"jj diff", "jj diff", true},
		{"jj diff", "jj diffx", false},
		{"jj diff *", "jj diff foo", true},
		{"jj diff *", "jj diff foo bar", true},
		{"jj diff *", "jj diff", false},
		{"jj *", "jj anything", true},
		{"jj *", "jj", false},

		// Edge cases
		{"**", "anything", true},
		{"a**b", "ab", true},
		{"a**b", "aXb", true},
		{"a**b", "aXXb", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.str, func(t *testing.T) {
			got := globMatch(tt.pattern, tt.str)
			if got != tt.want {
				t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.str, got, tt.want)
			}
		})
	}
}

func TestBashPermissions_IsAllowed(t *testing.T) {
	// Permissions matching the spec example and default rules in agent/store.go
	perms := BashPermissions{
		Rules: []BashRule{
			{Pattern: "jj diff", Allow: true},
			{Pattern: "jj diff *", Allow: true},
			{Pattern: "jj file", Allow: true},
			{Pattern: "jj file *", Allow: true},
			{Pattern: "jj log", Allow: true},
			{Pattern: "jj log *", Allow: true},
			{Pattern: "jj show", Allow: true},
			{Pattern: "jj show *", Allow: true},
			{Pattern: "jj status", Allow: true},
			{Pattern: "jj status *", Allow: true},
			{Pattern: "jj *", Allow: false},
			{Pattern: "git *", Allow: false},
			{Pattern: "ii todo create *", Allow: true},
			{Pattern: "ii todo show *", Allow: true},
			{Pattern: "ii *", Allow: false},
			{Pattern: "*", Allow: true},
		},
	}

	tests := []struct {
		command string
		want    bool
	}{
		// Allowed jj commands
		{"jj diff", true},
		{"jj diff foo.txt", true},
		{"jj diff --stat", true},
		{"jj file", true},
		{"jj file list", true},
		{"jj log", true},
		{"jj log --limit 10", true},
		{"jj show", true},
		{"jj show @", true},
		{"jj status", true},
		{"jj status --no-pager", true},

		// Denied jj commands
		{"jj commit", false},
		{"jj commit -m message", false},
		{"jj new", false},
		{"jj squash", false},
		{"jj abandon", false},

		// Denied git commands
		{"git status", false},
		{"git diff", false},
		{"git commit -m message", false},

		// Allowed ii commands
		{"ii todo create --title=foo", true},
		{"ii todo create --title=foo --description=bar", true},
		{"ii todo show abc123", true},
		{"ii todo show abc123 --json", true},

		// Denied ii commands
		{"ii todo", false},
		{"ii todo list", false},
		{"ii todo create", false}, // missing args, no trailing *
		{"ii todo show", false},   // missing args, no trailing *
		{"ii agent run", false},
		{"ii agent list", false},
		{"ii job run", false},
		{"ii workspace list", false},

		// Other commands allowed
		{"ls", true},
		{"cat foo.txt", true},
		{"grep pattern file.txt", true},
		{"echo hello", true},
		{"cd /tmp && ls", true},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := perms.IsAllowed(tt.command)
			if got != tt.want {
				t.Errorf("IsAllowed(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestBashPermissions_DefaultDeny(t *testing.T) {
	// Empty permissions should deny everything
	perms := BashPermissions{}

	tests := []string{
		"ls",
		"cat file",
		"echo hello",
	}

	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			if perms.IsAllowed(cmd) {
				t.Errorf("empty permissions should deny %q", cmd)
			}
		})
	}
}

func TestBashPermissions_FirstMatchWins(t *testing.T) {
	// Test that first match wins
	perms := BashPermissions{
		Rules: []BashRule{
			{Pattern: "rm *", Allow: false},
			{Pattern: "rm -rf *", Allow: true}, // This should never match
			{Pattern: "*", Allow: true},
		},
	}

	// rm -rf should be denied because "rm *" matches first
	if perms.IsAllowed("rm -rf /") {
		t.Error("expected 'rm -rf /' to be denied")
	}

	// ls should be allowed by catch-all
	if !perms.IsAllowed("ls") {
		t.Error("expected 'ls' to be allowed")
	}
}

func TestBashPermissions_AllowAll(t *testing.T) {
	perms := BashPermissions{
		Rules: []BashRule{
			{Pattern: "*", Allow: true},
		},
	}

	tests := []string{
		"ls",
		"rm -rf /",
		"cat /etc/passwd",
		"any command at all",
	}

	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			if !perms.IsAllowed(cmd) {
				t.Errorf("allow-all should allow %q", cmd)
			}
		})
	}
}

func TestBashPermissions_DenyAll(t *testing.T) {
	perms := BashPermissions{
		Rules: []BashRule{
			{Pattern: "*", Allow: false},
		},
	}

	tests := []string{
		"ls",
		"echo hello",
	}

	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			if perms.IsAllowed(cmd) {
				t.Errorf("deny-all should deny %q", cmd)
			}
		})
	}
}

func TestBashPermissions_ComplexPatterns(t *testing.T) {
	perms := BashPermissions{
		Rules: []BashRule{
			// Allow read-only git commands
			{Pattern: "git status", Allow: true},
			{Pattern: "git status *", Allow: true},
			{Pattern: "git diff", Allow: true},
			{Pattern: "git diff *", Allow: true},
			{Pattern: "git log", Allow: true},
			{Pattern: "git log *", Allow: true},
			{Pattern: "git show", Allow: true},
			{Pattern: "git show *", Allow: true},
			// Deny all other git commands
			{Pattern: "git *", Allow: false},
			// Allow everything else
			{Pattern: "*", Allow: true},
		},
	}

	tests := []struct {
		command string
		want    bool
	}{
		// Allowed git commands
		{"git status", true},
		{"git status --short", true},
		{"git diff", true},
		{"git diff HEAD~1", true},
		{"git log", true},
		{"git log --oneline -10", true},
		{"git show", true},
		{"git show HEAD", true},

		// Denied git commands
		{"git commit", false},
		{"git commit -m message", false},
		{"git push", false},
		{"git push origin main", false},
		{"git rebase", false},
		{"git reset --hard", false},

		// Other commands allowed
		{"make build", true},
		{"go test ./...", true},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := perms.IsAllowed(tt.command)
			if got != tt.want {
				t.Errorf("IsAllowed(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestSplitShellCommand(t *testing.T) {
	tests := []struct {
		command string
		want    []string
	}{
		// Simple command
		{"ls", []string{"ls"}},
		{"echo hello", []string{"echo hello"}},

		// && operator
		{"cd /tmp && ls", []string{"cd /tmp", "ls"}},
		{"a && b && c", []string{"a", "b", "c"}},

		// || operator
		{"test -f x || echo missing", []string{"test -f x", "echo missing"}},

		// ; operator
		{"cd /tmp; ls", []string{"cd /tmp", "ls"}},
		{"a; b; c", []string{"a", "b", "c"}},

		// | operator (pipe)
		{"ls | grep foo", []string{"ls", "grep foo"}},
		{"cat file | head -10 | tail -5", []string{"cat file", "head -10", "tail -5"}},

		// Mixed operators
		{"cd /tmp && ls | grep foo", []string{"cd /tmp", "ls", "grep foo"}},
		{"a && b || c; d | e", []string{"a", "b", "c", "d", "e"}},

		// Extra whitespace
		{"  cd /tmp  &&  ls  ", []string{"cd /tmp", "ls"}},

		// Empty segments are skipped
		{"ls &&", []string{"ls"}},
		{"&& ls", []string{"ls"}},

		// Edge cases
		{"", []string{}},
		{"   ", []string{}},
		{"&&", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := splitShellCommand(tt.command)
			if len(got) != len(tt.want) {
				t.Errorf("splitShellCommand(%q) = %v, want %v", tt.command, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitShellCommand(%q)[%d] = %q, want %q", tt.command, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestBashPermissions_CompoundCommands(t *testing.T) {
	perms := BashPermissions{
		Rules: []BashRule{
			{Pattern: "rm *", Allow: false},
			{Pattern: "*", Allow: true},
		},
	}

	tests := []struct {
		command string
		want    bool
	}{
		// Simple allowed commands
		{"ls", true},
		{"echo hello", true},
		{"cd /tmp", true},

		// Simple denied commands
		{"rm foo", false},
		{"rm -rf /", false},

		// Compound commands with all allowed
		{"cd /tmp && ls", true},
		{"echo hello; cat file", true},
		{"ls | grep foo", true},
		{"test -f x || echo missing", true},

		// Compound commands with one denied
		{"cd /tmp && rm foo", false},
		{"rm foo && ls", false},
		{"ls; rm bar; echo done", false},
		{"echo start && rm x || echo fallback", false},

		// Pipes with denied command
		{"cat file | rm -", false},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := perms.IsAllowed(tt.command)
			if got != tt.want {
				t.Errorf("IsAllowed(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestBashPermissions_CompoundCommands_FromTodo(t *testing.T) {
	// Test case from the todo description:
	// the rule 'ban:"rm *"' should match 'cd somewhere && rm something'
	// (but not, eg, 'echo worm juice')
	perms := BashPermissions{
		Rules: []BashRule{
			{Pattern: "rm *", Allow: false},
			{Pattern: "*", Allow: true},
		},
	}

	// This should be denied because it contains "rm something"
	if perms.IsAllowed("cd somewhere && rm something") {
		t.Error("expected 'cd somewhere && rm something' to be denied")
	}

	// This should be allowed because it doesn't contain rm
	if !perms.IsAllowed("echo worm juice") {
		t.Error("expected 'echo worm juice' to be allowed")
	}
}

