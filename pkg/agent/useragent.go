package agent

import (
	"monks.co/pkg/llm"
)

// UserAgent builds the User-Agent value used for LLM HTTP requests.
func UserAgent(repoPath string, version string) string {
	return llm.UserAgent(repoPath, version)
}
