package llm

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// captureLogOutput redirects the package logger to a buffer for the duration of
// the test and restores the prior settings afterward.
func captureLogOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	prevPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	})
	return &buf
}

func TestLogInvocationStart_DescribesRequest(t *testing.T) {
	buf := captureLogOutput(t)

	logInvocationStart(
		Model{ID: "claude-sonnet-4-6", API: APIAnthropicMessages},
		Request{
			Tools:      []Tool{{Name: "recommend"}},
			ToolChoice: "recommend",
		},
		StreamOptions{MaxTokens: ptrInt(4000)},
	)

	out := buf.String()
	for _, want := range []string{"llm", "claude-sonnet-4-6", "anthropic-messages", "recommend", "4000"} {
		if !strings.Contains(out, want) {
			t.Errorf("start log missing %q\n got: %s", want, out)
		}
	}
}

func TestLogInvocationDone_ReportsUsage(t *testing.T) {
	buf := captureLogOutput(t)

	h := &StreamHandle{model: "claude-sonnet-4-6", api: APIAnthropicMessages, start: time.Now().Add(-2 * time.Second)}
	logInvocationDone(h, AssistantMessage{
		StopReason: StopReasonToolUse,
		Usage:      Usage{Input: 1200, Output: 210, CacheRead: 50},
	})

	out := buf.String()
	for _, want := range []string{"claude-sonnet-4-6", "tool_use", "1200", "210"} {
		if !strings.Contains(out, want) {
			t.Errorf("done log missing %q\n got: %s", want, out)
		}
	}
}

func TestLogInvocationError_ReportsError(t *testing.T) {
	buf := captureLogOutput(t)

	logInvocationError("claude-sonnet-4-6", APIAnthropicMessages, errString("boom"), time.Second)

	out := buf.String()
	for _, want := range []string{"claude-sonnet-4-6", "boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("error log missing %q\n got: %s", want, out)
		}
	}
}

func ptrInt(i int) *int { return &i }

type errString string

func (e errString) Error() string { return string(e) }
