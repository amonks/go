package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadContextFiles_WhenMissing_ReturnsEmpty(t *testing.T) {
	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty files, got %d", len(files))
	}
}

func TestLoadContextFiles_WhenPresent_ReturnsContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("  hello\nworld\n\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Content != "  hello\nworld\n\n" {
		t.Fatalf("unexpected content: %q", files[0].Content)
	}
}

func TestLoadContextFiles_PrefersAGENTSoverCLAUDE(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("from agents"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("from claude"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Content != "from agents" {
		t.Fatalf("expected AGENTS.md to win, got %q", files[0].Content)
	}
}

func TestLoadContextFiles_FallsBackToCLAUDE(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("from claude"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Content != "from claude" {
		t.Fatalf("expected CLAUDE.md content, got %q", files[0].Content)
	}
}

func TestLoadContextFiles_AncestorDirectories(t *testing.T) {
	// Create a directory structure:
	// root/
	//   AGENTS.md (content: "root instructions")
	//   sub/
	//     AGENTS.md (content: "sub instructions")
	//     deep/
	//       (no AGENTS.md)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	deep := filepath.Join(sub, "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatalf("write root AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub instructions"), 0o644); err != nil {
		t.Fatalf("write sub AGENTS.md: %v", err)
	}

	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: deep})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should contain both, root first then sub
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Content != "root instructions" {
		t.Fatalf("unexpected first content: %q", files[0].Content)
	}
	if files[1].Content != "sub instructions" {
		t.Fatalf("unexpected second content: %q", files[1].Content)
	}
}

func TestLoadContextFiles_GlobalConfigDir(t *testing.T) {
	// Create:
	// globalDir/
	//   AGENTS.md (content: "global instructions")
	// workDir/
	//   AGENTS.md (content: "project instructions")
	globalDir := t.TempDir()
	workDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(globalDir, "AGENTS.md"), []byte("global instructions"), 0o644); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte("project instructions"), 0o644); err != nil {
		t.Fatalf("write project AGENTS.md: %v", err)
	}

	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: workDir, GlobalConfigDir: globalDir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Global first, then project
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Content != "global instructions" {
		t.Fatalf("unexpected global content: %q", files[0].Content)
	}
	if files[1].Content != "project instructions" {
		t.Fatalf("unexpected project content: %q", files[1].Content)
	}
}

func TestLoadContextFiles_MixedFilenames(t *testing.T) {
	// Create:
	// root/
	//   CLAUDE.md (content: "root via claude")
	//   sub/
	//     AGENTS.md (content: "sub via agents")
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("root via claude"), 0o644); err != nil {
		t.Fatalf("write root CLAUDE.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub via agents"), 0o644); err != nil {
		t.Fatalf("write sub AGENTS.md: %v", err)
	}

	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: sub})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Root (via CLAUDE.md) first, then sub (via AGENTS.md)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Content != "root via claude" {
		t.Fatalf("unexpected root content: %q", files[0].Content)
	}
	if files[1].Content != "sub via agents" {
		t.Fatalf("unexpected sub content: %q", files[1].Content)
	}
}

func TestLoadContextFiles_EmptyFilesIncluded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("   \n\n  "), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	trimmed := strings.TrimRight(files[0].Content, "\r\n")
	if strings.TrimSpace(trimmed) == "" {
		// Expected: whitespace-only file is considered blank
	} else {
		t.Fatalf("expected blank content, got %q", files[0].Content)
	}
}

func TestLoadContextFiles_RelativeWorkDir(t *testing.T) {
	// Create a directory structure within temp:
	// root/
	//   AGENTS.md (content: "root instructions")
	//   sub/
	//     deep/
	//       (working directory)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	deep := filepath.Join(sub, "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatalf("write root AGENTS.md: %v", err)
	}

	// Save current directory to restore later
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer os.Chdir(origDir)

	// Change into deep directory and call with relative path "."
	if err := os.Chdir(deep); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: "."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still find root AGENTS.md via ancestor traversal
	// even though we passed a relative path
	if len(files) == 0 {
		t.Fatal("expected to find ancestor AGENTS.md with relative workDir, got empty files")
	}
	if files[0].Content != "root instructions" {
		t.Fatalf("unexpected content: %q", files[0].Content)
	}
}

func TestLoadContextFiles_DeduplicatesWithDifferentPathForms(t *testing.T) {
	// Test that paths are canonicalized for deduplication
	// Create root/AGENTS.md and use root as globalConfigDir while root is also an ancestor
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	// Use root with a trailing slash and ".." in the path to create a different textual representation
	globalConfigDir := filepath.Join(root, "sub", "..")

	files, err := LoadContextFiles(LoadContextFilesOptions{
		WorkDir:         sub,
		GlobalConfigDir: globalConfigDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have one entry (deduplicated despite different path forms)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (deduplicated), got %d: %+v", len(files), files)
	}
	if files[0].Content != "root instructions" {
		t.Fatalf("unexpected content: %q", files[0].Content)
	}
}

func TestLoadContextFiles_ReturnsCanonicalizedPaths(t *testing.T) {
	// Test that returned paths are absolute and cleaned
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub instructions"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	// Use a non-canonical path for workDir
	nonCanonicalWorkDir := filepath.Join(root, "sub", "..", "sub")

	files, err := LoadContextFiles(LoadContextFilesOptions{
		WorkDir: nonCanonicalWorkDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both files should be found
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	// Both paths should be absolute and cleaned (no ".." components)
	for _, f := range files {
		if !filepath.IsAbs(f.Path) {
			t.Errorf("expected absolute path, got %q", f.Path)
		}
		if filepath.Clean(f.Path) != f.Path {
			t.Errorf("expected cleaned path, got %q (cleaned: %q)", f.Path, filepath.Clean(f.Path))
		}
	}

	// Verify exact paths
	wantRoot := filepath.Join(root, "AGENTS.md")
	wantSub := filepath.Join(sub, "AGENTS.md")
	if files[0].Path != wantRoot {
		t.Errorf("expected first file path %q, got %q", wantRoot, files[0].Path)
	}
	if files[1].Path != wantSub {
		t.Errorf("expected second file path %q, got %q", wantSub, files[1].Path)
	}
}
