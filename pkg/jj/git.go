package jj

import (
	"os/exec"
	"regexp"
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
// It adapts to jj's bookmark-tracking behavior across versions.
// Pushing a bookmark new to the remote needs --allow-new on older jj,
// while newer jj has removed the flag and allows new bookmarks by
// default. Newer jj also no longer auto-tracks remote bookmarks, so
// pushing an untracked local bookmark over an existing remote one is
// refused; GitPush tracks it and retries.
func (c *Client) GitPush(workspacePath, bookmark string) error {
	err := c.gitPushOnce(workspacePath, bookmark)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "--allow-new") {
		cmd := exec.Command("jj", "git", "push", "--bookmark", bookmark, "--allow-new")
		cmd.Dir = workspacePath
		return runCombinedOutput(cmd, "jj git push")
	}
	if m := nonTrackingPattern(bookmark).FindStringSubmatch(err.Error()); m != nil {
		// Tracking merges the local and remote positions with no
		// common base, which conflicts the bookmark whenever they
		// differ. The local position is the caller's intent, so
		// capture it first and re-set after tracking.
		pos, err := c.CommitIDAt(workspacePath, bookmark)
		if err != nil {
			return err
		}
		if err := c.BookmarkTrack(workspacePath, bookmark, m[1]); err != nil {
			return err
		}
		if err := c.BookmarkSet(workspacePath, bookmark, pos); err != nil {
			return err
		}
		return c.gitPushOnce(workspacePath, bookmark)
	}
	return err
}

func (c *Client) gitPushOnce(workspacePath, bookmark string) error {
	cmd := exec.Command("jj", "git", "push", "--bookmark", bookmark)
	cmd.Dir = workspacePath
	return runCombinedOutput(cmd, "jj git push")
}

// nonTrackingPattern matches jj's refusal to push an untracked local
// bookmark over an existing remote one, capturing the remote name.
func nonTrackingPattern(bookmark string) *regexp.Regexp {
	return regexp.MustCompile(`Non-tracking remote bookmark ` + regexp.QuoteMeta(bookmark) + `@(\S+) exists`)
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
