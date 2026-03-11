package llm

import (
	"context"
	"fmt"
)

// StreamEvent is an interface implemented by all stream event types.
type StreamEvent interface {
	streamEvent()
}

// StartEvent indicates the stream has started.
type StartEvent struct {
	Partial AssistantMessage
}

func (StartEvent) streamEvent() {}

// TextDeltaEvent contains a text content delta.
type TextDeltaEvent struct {
	ContentIndex int
	Delta        string
	Partial      AssistantMessage
}

func (TextDeltaEvent) streamEvent() {}

// ThinkingDeltaEvent contains a thinking content delta.
type ThinkingDeltaEvent struct {
	ContentIndex int
	Delta        string
	Partial      AssistantMessage
}

func (ThinkingDeltaEvent) streamEvent() {}

// ToolCallDeltaEvent contains a tool call JSON fragment.
type ToolCallDeltaEvent struct {
	ContentIndex int
	Delta        string // JSON fragment
	Partial      AssistantMessage
}

func (ToolCallDeltaEvent) streamEvent() {}

// ToolCallEndEvent indicates a tool call is complete.
type ToolCallEndEvent struct {
	ContentIndex int
	ToolCall     ToolCall
	Partial      AssistantMessage
}

func (ToolCallEndEvent) streamEvent() {}

// DoneEvent indicates the stream completed successfully.
type DoneEvent struct {
	Reason  StopReason
	Message AssistantMessage
}

func (DoneEvent) streamEvent() {}

// ErrorEvent indicates an error occurred.
type ErrorEvent struct {
	Reason  StopReason
	Message AssistantMessage
}

func (ErrorEvent) streamEvent() {}

// StreamHandle provides access to a streaming completion.
type StreamHandle struct {
	Events <-chan StreamEvent
	done   chan AssistantMessage
	errCh  chan error
}

// Wait blocks until the stream completes and returns the final message.
func (h *StreamHandle) Wait() (AssistantMessage, error) {
	// Drain events first
	for range h.Events {
	}

	select {
	case msg := <-h.done:
		return msg, nil
	case err := <-h.errCh:
		return AssistantMessage{}, err
	}
}

// newStreamHandle creates a new stream handle with the given channels.
func newStreamHandle(events <-chan StreamEvent, done chan AssistantMessage, errCh chan error) *StreamHandle {
	return &StreamHandle{
		Events: events,
		done:   done,
		errCh:  errCh,
	}
}

// NewStreamHandle creates a new stream handle with the given channels.
// This is exported for use by wrapper packages like llm.
func NewStreamHandle(events <-chan StreamEvent, done chan AssistantMessage, errCh chan error) *StreamHandle {
	return newStreamHandle(events, done, errCh)
}

// Stream starts a streaming completion with the given model and request.
// The returned StreamHandle provides access to events via the Events channel.
// Call Wait() to block until completion and get the final AssistantMessage.
func Stream(ctx context.Context, model Model, req Request, opts StreamOptions) (*StreamHandle, error) {
	// Default User-Agent for direct internal/llm usage.
	// Wrapper packages (like the public llm/ store) can override this.
	if opts.UserAgent == "" {
		opts.UserAgent = "incrementum [unknown] unknown"
	}
	if opts.CacheRetention == "" {
		opts.CacheRetention = CacheShort
	}
	switch model.API {
	case APIAnthropicMessages:
		return streamAnthropic(ctx, model, req, opts)
	case APIOpenAICompletions:
		return streamOpenAICompletions(ctx, model, req, opts)
	case APIOpenAIResponses:
		return streamOpenAIResponses(ctx, model, req, opts)
	default:
		return nil, fmt.Errorf("unsupported API: %s", model.API)
	}
}
