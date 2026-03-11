package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUserAgent_TestingOverride(t *testing.T) {
	ua := UserAgent("/tmp/incrementum", "abc123")
	if ua != "incrementum TEST" {
		t.Fatalf("UserAgent() = %q, want %q", ua, "incrementum TEST")
	}
}

func TestStream_SetsUserAgentHeader(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":\"end\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()

	model := Model{ID: "m", API: APIAnthropicMessages, BaseURL: srv.URL}
	_, err := Stream(context.Background(), model, Request{}, StreamOptions{UserAgent: "incrementum [v] repo"})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	if gotUA != "incrementum [v] repo" {
		t.Fatalf("User-Agent header = %q, want %q", gotUA, "incrementum [v] repo")
	}
}
