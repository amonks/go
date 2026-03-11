package llm

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// UserAgent returns the HTTP User-Agent header value to use for LLM API requests.
//
// Installed builds:
//
//	incrementum [$changeID:$commitID] $dirname
//
// Dev builds (bin/ii):
//
//	incrementum [dev] $dirname
//
// During `go test` it is overridden to:
//
//	incrementum TEST
func UserAgent(repoPath string, version string) string {
	if testing.Testing() {
		return "incrementum TEST"
	}
	if version == "" {
		version = "unknown"
	}
	repoPath = strings.TrimSpace(repoPath)
	dir := filepath.Base(repoPath)
	if dir == "." || dir == string(filepath.Separator) || dir == "" {
		dir = "unknown"
	}
	return fmt.Sprintf("incrementum [%s] %s", version, dir)
}
