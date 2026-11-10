package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Rejected tells a request the provider rejects as sent — invalid, or
// too large — from failures that pass on their own (rate limits,
// outages) or are never about the request (credentials, an unknown
// model).
func TestRejected(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusBadRequest:            true,
		http.StatusRequestEntityTooLarge: true,
		http.StatusUnauthorized:          false,
		http.StatusForbidden:             false,
		http.StatusNotFound:              false,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`, status)
			}))
			defer srv.Close()
			model := Model{ID: "claude-opus-5-5", API: APIAnthropicMessages, BaseURL: srv.URL, MaxTokens: 1024}
			_, err := StreamWithRetry(context.Background(), model, Request{}, StreamOptions{}, DefaultRetryConfig())
			if err == nil {
				t.Fatal("no error")
			}
			if Rejected(err) != want || Rejected(fmt.Errorf("call: %w", err)) != want {
				t.Errorf("Rejected = %v, want %v (%v)", Rejected(err), want, err)
			}
		})
	}
	if Rejected(errors.New("connection refused")) || Rejected(nil) {
		t.Error("an error with no provider answer is not a rejection")
	}
}

// Google answers a bad API key with 400 INVALID_ARGUMENT, reason
// API_KEY_INVALID: a credential failure, which every other provider
// sends as 401 and an operator fixes once, so it is no rejection.
func TestGoogleInvalidKeyIsNotRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID","domain":"googleapis.com"}]}}`))
	}))
	defer srv.Close()
	model := Model{ID: "gemini-3.8-flash", API: APIGoogleGenerateContent, BaseURL: srv.URL, APIKey: "revoked", MaxTokens: 1024}
	_, err := Stream(context.Background(), model, Request{}, StreamOptions{})
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("err = %v, want the provider's 400", err)
	}
	if Rejected(err) {
		t.Errorf("a bad key reads as a rejected request: %v", err)
	}
}

// A provider can report an error inside an HTTP 200 stream. Anthropic
// names it by type; it carries the status the type stands for, so a
// streamed rejection is a rejection like the HTTP one, with its reason,
// and an overload is not.
func TestStreamedAnthropicErrorKeepsItsStatus(t *testing.T) {
	for errType, want := range map[string]bool{
		"invalid_request_error": true,
		"request_too_large":     true,
		"overloaded_error":      false,
		"api_error":             false,
	} {
		t.Run(errType, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"))
				_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"` + errType + `","message":"prompt is too long"}}` + "\n\n"))
			}))
			defer srv.Close()
			model := Model{ID: "claude-fable-5-2", API: APIAnthropicMessages, BaseURL: srv.URL, MaxTokens: 1024}
			h, err := Stream(context.Background(), model, Request{}, StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.Wait()
			if err == nil || !strings.Contains(err.Error(), "prompt is too long") {
				t.Fatalf("err = %v, want the stream's reason", err)
			}
			if Rejected(err) != want {
				t.Errorf("Rejected = %v, want %v: %v", !want, want, err)
			}
		})
	}
}

// Google's stream error carries its HTTP-style code, which classifies it.
func TestStreamedGoogleErrorKeepsItsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"error":{"code":400,"message":"input too long","status":"INVALID_ARGUMENT"}}` + "\n\n"))
	}))
	defer srv.Close()
	model := Model{ID: "gemini-3.8-flash", API: APIGoogleGenerateContent, BaseURL: srv.URL, APIKey: "k", MaxTokens: 1024}
	h, err := Stream(context.Background(), model, Request{}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Wait()
	if err == nil || !strings.Contains(err.Error(), "input too long") {
		t.Fatalf("err = %v, want the stream's reason", err)
	}
	if !Rejected(err) {
		t.Errorf("a streamed 400 is not Rejected: %v", err)
	}
}
