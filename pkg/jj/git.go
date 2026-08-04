package jj

import (
	"os/exec"
	"strings"
)

// GitRemoteAdd adds a named git remote to the repository.
func (c *Client) GitRemoteAdd(workspacePath, name, url string) error {
	cmd := exec.Command("jj", "git", "remote", "add", name, url)
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj git remote add")
}

// GitClone clones a git repository into dest as a jj repo.
func (c *Client) GitClone(url, dest string) error {
	cmd := exec.Command("jj", "git", "clone", url, dest)
	return runCombinedOutput(cmd, "jj git clone")
}

// BookmarkTrack makes the local bookmark track the remote bookmark of
// the same name, so BookmarkSet + GitPush move the remote bookmark.
func (c *Client) BookmarkTrack(workspacePath, name, remote string) error {
	cmd := exec.Command("jj", "bookmark", "track", name, "--remote", remote)
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj bookmark track")
}

// GitFetch fetches from the default remote.
func (c *Client) GitFetch(workspacePath string) error {
	cmd := exec.Command("jj", "git", "fetch")
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj git fetch")
}

// GitPush pushes the named bookmark to the remote. It fails if the
// remote bookmark has moved since the last fetch (jj refuses to push
// over unexpected remote changes), which callers use to detect races.
//
// Pushing a bookmark that doesn't exist on the remote yet needs
// --allow-new on older jj, while newer jj has removed the flag and
// allows new bookmarks by default. Try the plain push first and retry
// with the flag only when the error asks for it, so both versions work.
func (c *Client) GitPush(workspacePath, bookmark string) error {
	cmd := exec.Command("jj", "git", "push", "--bookmark", bookmark)
	cmd.Dir = workspacePath
	err := runCombinedOutput(cmd, "jj git push")
	if err != nil && strings.Contains(err.Error(), "--allow-new") {
		cmd := exec.Command("jj", "git", "push", "--bookmark", bookmark, "--allow-new")
		cmd.Dir = workspacePath
		return runCombinedOutput(cmd, "jj git push")
	}
	return err
}

// LogTemplate returns the output of jj log over the given revset with
// the given template, without the graph.
func (c *Client) LogTemplate(workspacePath, revset, template string) (string, error) {
	cmd := exec.Command("jj", "log", "--no-graph", "-r", revset, "-T", template)
	cmd.Dir = workspacePath
	output, err := commandOutput(cmd, "jj log")
	if err != nil {
		return "", err
	}
	return string(output), nil
}
