package llm

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
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

	// Set by Stream so Wait can log the completion with model and duration.
	// Zero when the handle was built directly via NewStreamHandle (e.g. by a
	// wrapper package), in which case Wait skips completion logging.
	model string
	api   API
	start time.Time
}

// Wait blocks until the stream completes and returns the final message.
func (h *StreamHandle) Wait() (AssistantMessage, error) {
	// Drain events first
	for range h.Events {
	}

	select {
	case msg := <-h.done:
		if !h.start.IsZero() {
			// Only handles built by Stream log; a wrapper handle
			// re-delivering an inner stream's message must not log
			// the completion twice.
			logInvocationDone(h, msg)
		}
		return msg, nil
	case err := <-h.errCh:
		if !h.start.IsZero() {
			logInvocationError(h.model, h.api, err, time.Since(h.start))
		}
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

	start := time.Now()
	logInvocationStart(model, req, opts)

	var (
		handle *StreamHandle
		err    error
	)
	switch model.API {
	case APIAnthropicMessages:
		handle, err = streamAnthropic(ctx, model, req, opts)
	case APIOpenAICompletions:
		handle, err = streamOpenAICompletions(ctx, model, req, opts)
	case APIOpenAIResponses:
		handle, err = streamOpenAIResponses(ctx, model, req, opts)
	default:
		// A permanent configuration error: retrying will pick the same branch.
		err = &retryableError{err: fmt.Errorf("unsupported API: %s", model.API)}
	}
	if err != nil {
		// Request never got off the ground (bad config, non-2xx, network).
		logInvocationError(model.ID, model.API, err, time.Since(start))
		return nil, err
	}

	// Stamp the handle so Wait can log the completion with duration.
	handle.model = model.ID
	handle.api = model.API
	handle.start = start
	return handle, nil
}

// Invocation logging. One line when a call starts and one when it finishes,
// to stderr via the standard logger, so operators can confirm LLM calls are
// happening and see their token usage and latency without a debugger.

func logInvocationStart(model Model, req Request, opts StreamOptions) {
	maxTokens := 0
	if opts.MaxTokens != nil {
		maxTokens = *opts.MaxTokens
	}
	toolChoice := opts.toolChoiceLabel(req)
	log.Printf("llm: → %s (%s) maxTokens=%d tools=%s%s",
		model.ID, model.API, maxTokens, toolNames(req.Tools), toolChoice)
}

func logInvocationDone(h *StreamHandle, msg AssistantMessage) {
	u := msg.Usage
	log.Printf("llm: ← %s (%s) stop=%s in=%d out=%d cacheR=%d cacheW=%d dur=%s",
		h.model, h.api, msg.StopReason, u.Input, u.Output, u.CacheRead, u.CacheWrite,
		time.Since(h.start).Round(time.Millisecond))
}

func logInvocationError(modelID string, api API, err error, dur time.Duration) {
	log.Printf("llm: ✗ %s (%s) error: %v dur=%s", modelID, api, err, dur.Round(time.Millisecond))
}

// toolChoiceLabel renders the forced-tool suffix, if any. It hangs off
// StreamOptions only for symmetry with logInvocationStart's signature.
func (StreamOptions) toolChoiceLabel(req Request) string {
	if req.ToolChoice == "" {
		return ""
	}
	return " toolChoice=" + req.ToolChoice
}

func toolNames(tools []Tool) string {
	if len(tools) == 0 {
		return "[]"
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return "[" + strings.Join(names, ",") + "]"
}
