package jj_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"monks.co/pkg/jj"
)

// initBareOrigin creates a bare git repository to act as a remote.
func initBareOrigin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	cmd := exec.Command("git", "init", "--bare", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	return dir
}

// initRepoWithOrigin creates a jj repo with the given bare repo as origin.
func initRepoWithOrigin(t *testing.T, origin string) (string, *jj.Client) {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	client := jj.New()
	if err := client.Init(dir); err != nil {
		t.Fatalf("failed to init jj repo: %v", err)
	}
	if err := client.GitRemoteAdd(dir, "origin", origin); err != nil {
		t.Fatalf("failed to add remote: %v", err)
	}
	return dir, client
}

func TestGitPushAndFetch(t *testing.T) {
	origin := initBareOrigin(t)

	// Repo A commits and pushes main.
	repoA, client := initRepoWithOrigin(t, origin)
	if err := client.Commit(repoA, "first commit"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	if err := client.BookmarkCreate(repoA, "main", "@-"); err != nil {
		t.Fatalf("failed to create bookmark: %v", err)
	}
	if err := client.GitPush(repoA, "main"); err != nil {
		t.Fatalf("failed to push: %v", err)
	}

	// Repo B fetches and sees main.
	repoB, _ := initRepoWithOrigin(t, origin)
	if err := client.GitFetch(repoB); err != nil {
		t.Fatalf("failed to fetch: %v", err)
	}
	desc, err := client.DescriptionAt(repoB, "main@origin")
	if err != nil {
		t.Fatalf("failed to read description at main@origin: %v", err)
	}
	if !strings.Contains(desc, "first commit") {
		t.Errorf("expected fetched main@origin to have description %q, got %q", "first commit", desc)
	}
}

func TestGitPush_RejectsStaleBookmark(t *testing.T) {
	origin := initBareOrigin(t)

	// Repo A pushes main.
	repoA, client := initRepoWithOrigin(t, origin)
	if err := client.Commit(repoA, "base"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	if err := client.BookmarkCreate(repoA, "main", "@-"); err != nil {
		t.Fatalf("failed to create bookmark: %v", err)
	}
	if err := client.GitPush(repoA, "main"); err != nil {
		t.Fatalf("failed to push: %v", err)
	}

	// Repo B fetches, advances main, and pushes.
	repoB, _ := initRepoWithOrigin(t, origin)
	if err := client.GitFetch(repoB); err != nil {
		t.Fatalf("failed to fetch: %v", err)
	}
	if err := client.BookmarkTrack(repoB, "main", "origin"); err != nil {
		t.Fatalf("failed to track bookmark: %v", err)
	}
	if _, err := client.NewChange(repoB, "main@origin"); err != nil {
		t.Fatalf("failed to create change: %v", err)
	}
	if err := client.Commit(repoB, "concurrent work"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	if err := client.BookmarkSet(repoB, "main", "@-"); err != nil {
		t.Fatalf("failed to set bookmark: %v", err)
	}
	if err := client.GitPush(repoB, "main"); err != nil {
		t.Fatalf("failed to push from repo B: %v", err)
	}

	// Repo A, unaware of B's push, advances its stale main and pushes.
	// This must fail: the remote bookmark moved.
	if _, err := client.NewChange(repoA, "main"); err != nil {
		t.Fatalf("failed to create change: %v", err)
	}
	if err := client.Commit(repoA, "stale work"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	if err := client.BookmarkSet(repoA, "main", "@-"); err != nil {
		t.Fatalf("failed to set bookmark: %v", err)
	}
	if err := client.GitPush(repoA, "main"); err == nil {
		t.Error("expected push of stale bookmark to fail")
	}
}

func TestGitPush_HealsNonTracking(t *testing.T) {
	origin := initBareOrigin(t)

	// Repo A creates main on the remote.
	repoA, client := initRepoWithOrigin(t, origin)
	if err := client.Commit(repoA, "base"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	if err := client.BookmarkCreate(repoA, "main", "@-"); err != nil {
		t.Fatalf("failed to create bookmark: %v", err)
	}
	if err := client.GitPush(repoA, "main"); err != nil {
		t.Fatalf("failed to push: %v", err)
	}

	// Repo B fetches but does NOT track main, sets a local main, and
	// pushes. GitPush must track and retry rather than fail.
	repoB, _ := initRepoWithOrigin(t, origin)
	if err := client.GitFetch(repoB); err != nil {
		t.Fatalf("failed to fetch: %v", err)
	}
	if _, err := client.NewChange(repoB, "main@origin"); err != nil {
		t.Fatalf("failed to create change: %v", err)
	}
	if err := client.Commit(repoB, "untracked work"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	if err := client.BookmarkSet(repoB, "main", "@-"); err != nil {
		t.Fatalf("failed to set bookmark: %v", err)
	}
	if err := client.GitPush(repoB, "main"); err != nil {
		t.Fatalf("expected push to heal non-tracking bookmark, got %v", err)
	}

	// The push landed on the remote.
	check := t.TempDir()
	check, _ = filepath.EvalSymlinks(check)
	if err := jj.New().GitClone(origin, check); err != nil {
		t.Fatal(err)
	}
	desc, err := client.DescriptionAt(check, "trunk()")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(desc, "untracked work") {
		t.Errorf("expected origin main at %q, got %q", "untracked work", desc)
	}
}

func TestGitClone(t *testing.T) {
	origin := initBareOrigin(t)

	repoA, client := initRepoWithOrigin(t, origin)
	if err := client.Commit(repoA, "seed"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	if err := client.BookmarkCreate(repoA, "main", "@-"); err != nil {
		t.Fatalf("failed to create bookmark: %v", err)
	}
	if err := client.GitPush(repoA, "main"); err != nil {
		t.Fatalf("failed to push: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "clone")
	if err := client.GitClone(origin, dest); err != nil {
		t.Fatalf("failed to clone: %v", err)
	}
	desc, err := client.DescriptionAt(dest, "trunk()")
	if err != nil {
		t.Fatalf("failed to read trunk description: %v", err)
	}
	if !strings.Contains(desc, "seed") {
		t.Errorf("expected trunk() description to contain %q, got %q", "seed", desc)
	}
}

func TestLogTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	client := jj.New()
	if err := client.Init(tmpDir); err != nil {
		t.Fatalf("failed to init jj repo: %v", err)
	}
	if err := client.Commit(tmpDir, "alpha"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	if err := client.Commit(tmpDir, "beta"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	out, err := client.LogTemplate(tmpDir, "::@- & ~root()", `description.first_line() ++ "\n"`)
	if err != nil {
		t.Fatalf("failed to log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != "beta" || lines[1] != "alpha" {
		t.Errorf("expected [beta alpha], got %v", lines)
	}
}
