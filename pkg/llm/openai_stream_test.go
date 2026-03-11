package llm

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestProcessOpenAIStream_ReportsCachedTokens(t *testing.T) {
	stream := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\",\"index\":0}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15,\"prompt_tokens_details\":{\"cached_tokens\":7}}}",
		"data: [DONE]",
		"",
	}, "\n")

	body := io.NopCloser(strings.NewReader(stream))
	model := Model{ID: "gpt-5.2", API: APIOpenAICompletions, Provider: "test"}

	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)

	processOpenAIStream(context.Background(), body, model, events, done, errCh)

	for range events {
	}

	select {
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	default:
	}

	msg := <-done
	if msg.Usage.CacheRead != 7 {
		t.Fatalf("Usage.CacheRead=%d, want %d", msg.Usage.CacheRead, 7)
	}
}
