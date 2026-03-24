// Package jj provides a wrapper around the jj CLI tool.
package jj

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Client wraps the jj CLI.
type Client struct{}

// ErrFileNotFound indicates a file or path is missing at a revision.
var ErrFileNotFound = errors.New("jj file not found")

// New creates a new jj client.
func New() *Client {
	return &Client{}
}

func commandOutput(cmd *exec.Cmd, context string) ([]byte, error) {
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s: %w: %s", context, err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("%s: %w", context, err)
	}
	return output, nil
}

func commandOutputString(cmd *exec.Cmd, context string) (string, error) {
	output, err := commandOutput(cmd, context)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func splitTrimmedLines(output []byte) []string {
	lines := strings.Split(string(output), "\n")
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		trimmed = append(trimmed, line)
	}
	return trimmed
}

func commandCombinedOutput(cmd *exec.Cmd, context string) ([]byte, error) {
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s: %w: %s", context, err, output)
	}
	return output, nil
}

func runCombinedOutput(cmd *exec.Cmd, context string) error {
	_, err := commandCombinedOutput(cmd, context)
	return err
}

func logFieldAt(workspacePath, rev, field string) (string, error) {
	cmd := exec.Command("jj", "log", "-r", rev, "-T", field, "--no-graph")
	cmd.Dir = workspacePath
	return commandOutputString(cmd, "jj log")
}

// Init initializes a new jj repository at the given path.
func (c *Client) Init(path string) error {
	cmd := exec.Command("jj", "git", "init")
	cmd.Dir = path
	return runCombinedOutput(cmd, "jj git init")
}

// WorkspaceRoot returns the root directory of the workspace containing the given path.
func (c *Client) WorkspaceRoot(path string) (string, error) {
	cmd := exec.Command("jj", "workspace", "root")
	cmd.Dir = path
	return commandOutputString(cmd, "jj workspace root")
}

// WorkspaceAdd adds a new workspace to the repository.
func (c *Client) WorkspaceAdd(repoPath, name, workspacePath string) error {
	cmd := exec.Command("jj", "workspace", "add", "--name", name, workspacePath)
	cmd.Dir = repoPath
	return runCombinedOutput(cmd, "jj workspace add")
}

// WorkspaceList returns the list of workspace names in the repository.
func (c *Client) WorkspaceList(repoPath string) ([]string, error) {
	cmd := exec.Command("jj", "workspace", "list")
	cmd.Dir = repoPath
	output, err := commandOutput(cmd, "jj workspace list")
	if err != nil {
		return nil, err
	}

	lines := splitTrimmedLines(output)
	workspaces := make([]string, 0, len(lines))
	for _, line := range lines {
		// Output format is "name: <change_id>" - extract just the name
		parts := strings.SplitN(line, ":", 2)
		workspaces = append(workspaces, strings.TrimSpace(parts[0]))
	}
	return workspaces, nil
}

// Edit checks out the specified revision in the workspace.
func (c *Client) Edit(workspacePath, rev string) error {
	cmd := exec.Command("jj", "edit", rev)
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj edit")
}

// CurrentChangeID returns the change ID of the current working copy commit.
func (c *Client) CurrentChangeID(workspacePath string) (string, error) {
	return logFieldAt(workspacePath, "@", "change_id")
}

// CurrentCommitID returns the commit ID of the current working copy commit.
func (c *Client) CurrentCommitID(workspacePath string) (string, error) {
	return logFieldAt(workspacePath, "@", "commit_id")
}

// CurrentChangeEmpty reports whether the current change has no diff.
func (c *Client) CurrentChangeEmpty(workspacePath string) (bool, error) {
	output, err := logFieldAt(workspacePath, "@", "empty")
	if err != nil {
		return false, err
	}
	switch output {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected jj log empty output %q", output)
	}
}

// BookmarkList returns all bookmark names in the repository.
func (c *Client) BookmarkList(workspacePath string) ([]string, error) {
	cmd := exec.Command("jj", "bookmark", "list", "-T", "name ++ \"\\n\"")
	cmd.Dir = workspacePath
	output, err := commandOutput(cmd, "jj bookmark list")
	if err != nil {
		return nil, err
	}

	lines := splitTrimmedLines(output)
	return lines, nil
}

// BookmarkCreate creates a bookmark at the specified revision.
func (c *Client) BookmarkCreate(workspacePath, name, rev string) error {
	cmd := exec.Command("jj", "bookmark", "create", name, "-r", rev)
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj bookmark create")
}

// BookmarkSet moves (or creates) a bookmark to the specified revision.
func (c *Client) BookmarkSet(workspacePath, name, rev string) error {
	cmd := exec.Command("jj", "bookmark", "set", name, "-r", rev, "--allow-backwards")
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj bookmark set")
}

// Rebase rebases the branch rooted at rev onto dest.
func (c *Client) Rebase(workspacePath, rev, dest string) error {
	cmd := exec.Command("jj", "rebase", "-b", rev, "-d", dest)
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj rebase")
}

// NewChange creates a new change with the given parent revision.
// Returns the change ID of the newly created change.
// Note: This moves the working copy to the new change.
func (c *Client) NewChange(workspacePath, parentRev string) (string, error) {
	cmd := exec.Command("jj", "new", parentRev)
	cmd.Dir = workspacePath
	if err := runCombinedOutput(cmd, "jj new"); err != nil {
		return "", err
	}

	// Get the change ID of the newly created change (now at @)
	return c.CurrentChangeID(workspacePath)
}

// NewChangeWithMessage creates a new change with the given parent revision and description.
// Returns the change ID of the newly created change.
// Note: This moves the working copy to the new change.
func (c *Client) NewChangeWithMessage(workspacePath, parentRev, message string) (string, error) {
	changeID, err := c.NewChange(workspacePath, parentRev)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(message) == "" {
		return changeID, nil
	}
	if err := c.Describe(workspacePath, message); err != nil {
		return "", err
	}
	return changeID, nil
}

// ChangeIDAt returns the change ID at the given revision.
func (c *Client) ChangeIDAt(workspacePath, rev string) (string, error) {
	return logFieldAt(workspacePath, rev, "change_id")
}

// ChangeIDsForRevset returns change IDs for the given revset.
func (c *Client) ChangeIDsForRevset(workspacePath, revset string) ([]string, error) {
	if strings.TrimSpace(revset) == "" {
		return nil, fmt.Errorf("revset is required")
	}
	cmd := exec.Command("jj", "log", "--no-graph", "-r", revset, "-T", "change_id")
	cmd.Dir = workspacePath
	output, err := commandOutput(cmd, "jj log change ids")
	if err != nil {
		return nil, err
	}
	return splitTrimmedLines(output), nil
}

// CommitIDAt returns the commit ID at the given revision.
func (c *Client) CommitIDAt(workspacePath, rev string) (string, error) {
	return logFieldAt(workspacePath, rev, "commit_id")
}

// DiffStat returns the diff stat between two revisions.
func (c *Client) DiffStat(workspacePath, from, to string) (string, error) {
	cmd := exec.Command("jj", "diff", "--from", from, "--to", to, "--stat")
	cmd.Dir = workspacePath
	output, err := commandCombinedOutput(cmd, "jj diff --stat")
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// DescriptionAt returns the description at the given revision.
func (c *Client) DescriptionAt(workspacePath, rev string) (string, error) {
	return logFieldAt(workspacePath, rev, "description")
}

// Snapshot runs jj debug snapshot to record working copy changes to the current change.
func (c *Client) Snapshot(workspacePath string) error {
	cmd := exec.Command("jj", "debug", "snapshot")
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj debug snapshot")
}

// Describe sets the description for the current change.
func (c *Client) Describe(workspacePath, message string) error {
	cmd := exec.Command("jj", "describe", "--stdin")
	cmd.Dir = workspacePath
	cmd.Stdin = strings.NewReader(message)
	return runCombinedOutput(cmd, "jj describe")
}

// Commit commits the current change and leaves a new empty change.
func (c *Client) Commit(workspacePath, message string) error {
	if err := c.Describe(workspacePath, message); err != nil {
		return fmt.Errorf("jj commit: %w", err)
	}
	if _, err := c.NewChange(workspacePath, "@"); err != nil {
		return fmt.Errorf("jj commit: %w", err)
	}
	return nil
}

// WorkspaceUpdateStale updates a stale working copy.
func (c *Client) WorkspaceUpdateStale(workspacePath string) error {
	cmd := exec.Command("jj", "workspace", "update-stale")
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj workspace update-stale")
}

// WorkspaceForget removes a workspace from the repository without deleting it from disk.
func (c *Client) WorkspaceForget(repoPath, workspaceName string) error {
	cmd := exec.Command("jj", "workspace", "forget", workspaceName)
	cmd.Dir = repoPath
	return runCombinedOutput(cmd, "jj workspace forget")
}

// FileShow returns the contents of a file at the given revision.
func (c *Client) FileShow(repoPath, rev, path string) ([]byte, error) {
	cmd := exec.Command("jj", "file", "show", "-r", rev, path)
	cmd.Dir = repoPath
	output, err := commandCombinedOutput(cmd, "jj file show")
	if err != nil {
		if isFileNotFoundOutput(output) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}
	return output, nil
}

func isFileNotFoundOutput(output []byte) bool {
	lower := strings.ToLower(string(output))
	return strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "no such path") ||
		strings.Contains(lower, "path does not exist") ||
		strings.Contains(lower, "path doesn't exist")
}

// ConflictedInRange returns change IDs with conflicts in the given revset.
func (c *Client) ConflictedInRange(workspacePath, revset string) ([]string, error) {
	if strings.TrimSpace(revset) == "" {
		return nil, fmt.Errorf("revset is required")
	}
	cmd := exec.Command("jj", "log", "--no-graph", "-r", revset, "-T", "if(conflict, change_id)")
	cmd.Dir = workspacePath
	output, err := commandOutput(cmd, "jj log conflicts")
	if err != nil {
		return nil, err
	}
	return splitTrimmedLines(output), nil
}

// HasConflicts reports whether the revision has any conflicts.
func (c *Client) HasConflicts(workspacePath, rev string) (bool, error) {
	cmd := exec.Command("jj", "log", "--no-graph", "-r", rev, "-T", "conflict")
	cmd.Dir = workspacePath
	output, err := commandOutputString(cmd, "jj log conflict")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) == "true", nil
}

// Squash squashes the current change into its parent.
func (c *Client) Squash(workspacePath string) error {
	cmd := exec.Command("jj", "squash", "-m", "squash")
	cmd.Dir = workspacePath
	cmd.Env = append(os.Environ(),
		"JJ_EDITOR=true",
		"EDITOR=true",
		"VISUAL=true",
	)
	return runCombinedOutput(cmd, "jj squash")
}

// SeriesLog returns the jj log for the patch series from fork_point(@|main) to @-.
// The output includes commit descriptions and diff stats.
// Returns an empty string if there are no commits in the series (including when
// jj outputs "No revisions to show").
func (c *Client) SeriesLog(workspacePath string) (string, error) {
	cmd := exec.Command("jj", "log", "-r", "fork_point(@|main)..@-", "--no-graph", "--stat")
	cmd.Dir = workspacePath
	output, err := commandOutputString(cmd, "jj log series")
	if err != nil {
		return "", err
	}
	// jj outputs "No revisions to show" when the revset is empty; treat this as
	// an empty series rather than returning the message.
	if output == "" || output == "No revisions to show" || output == "No revisions to show." {
		return "", nil
	}
	return output, nil
}
