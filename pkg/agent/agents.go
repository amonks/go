package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// ContextFile represents a discovered AGENTS.md or CLAUDE.md file.
type ContextFile struct {
	Path    string
	Content string
}

// loadContextFileFromDir looks for AGENTS.md or CLAUDE.md in dir, returning
// the first one found (preferring AGENTS.md).
func loadContextFileFromDir(dir string) (*ContextFile, error) {
	candidates := []string{"AGENTS.md", "CLAUDE.md"}
	for _, filename := range candidates {
		path := filepath.Join(dir, filename)
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		return &ContextFile{
			Path:    path,
			Content: string(b),
		}, nil
	}
	return nil, nil
}

// LoadContextFilesOptions configures context file discovery.
type LoadContextFilesOptions struct {
	// WorkDir is the starting directory for ancestor traversal.
	WorkDir string
	// GlobalConfigDir is the global config directory (e.g., ~/.config/incrementum).
	// If empty, no global context is loaded.
	GlobalConfigDir string
}

// LoadContextFiles discovers AGENTS.md or CLAUDE.md files following the
// pi-mono reference implementation:
//
//  1. Load from globalConfigDir (if provided)
//  2. Walk from workDir up to filesystem root, collecting context files
//  3. Return files in order: global first, then ancestors from root to workDir
//
// Files are deduplicated by canonical (absolute, cleaned) path. The returned
// ContextFile.Path values are also canonicalized (absolute and cleaned).
func LoadContextFiles(opts LoadContextFilesOptions) ([]ContextFile, error) {
	var files []ContextFile
	seenPaths := make(map[string]bool)

	// Helper to canonicalize a path for deduplication and returned paths
	canonicalize := func(path string) string {
		abs, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		return filepath.Clean(abs)
	}

	// 1. Load from global config directory
	if opts.GlobalConfigDir != "" {
		cf, err := loadContextFileFromDir(opts.GlobalConfigDir)
		if err != nil {
			return nil, fmt.Errorf("load global context: %w", err)
		}
		if cf != nil {
			canonicalPath := canonicalize(cf.Path)
			cf.Path = canonicalPath
			files = append(files, *cf)
			seenPaths[canonicalPath] = true
		}
	}

	// 2. Resolve workDir to an absolute path for proper ancestor traversal
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = "."
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve workDir: %w", err)
	}
	absWorkDir = filepath.Clean(absWorkDir)

	// 3. Walk from workDir up to root, collecting files in workDir→root order
	// We loop until filepath.Dir returns the same path (indicating we've reached the root).
	// This approach is cross-platform and avoids hand-rolling root detection logic.
	var ancestorFiles []ContextFile
	currentDir := absWorkDir

	for {
		cf, err := loadContextFileFromDir(currentDir)
		if err != nil {
			return nil, fmt.Errorf("load context from %s: %w", currentDir, err)
		}
		if cf != nil {
			canonicalPath := canonicalize(cf.Path)
			if !seenPaths[canonicalPath] {
				cf.Path = canonicalPath
				ancestorFiles = append(ancestorFiles, *cf)
				seenPaths[canonicalPath] = true
			}
		}

		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			// Reached filesystem root
			break
		}
		currentDir = parentDir
	}

	// 4. Reverse ancestorFiles to get root→workDir order (O(n) instead of O(n²) prepends)
	for i, j := 0, len(ancestorFiles)-1; i < j; i, j = i+1, j-1 {
		ancestorFiles[i], ancestorFiles[j] = ancestorFiles[j], ancestorFiles[i]
	}

	// 5. Combine: global first, then ancestors from root to workDir
	files = append(files, ancestorFiles...)
	return files, nil
}
