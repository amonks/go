package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const anthropicAPIVersion = "2023-06-01"

// anthropicRequest is the request body for the Anthropic Messages API.
type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Messages    []anthropicMessage `json:"messages"`
	System      []anthropicContent `json:"system,omitempty"`
	Stream      bool               `json:"stream"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	Thinking     string                 `json:"thinking,omitempty"`
	Source       *anthropicSource       `json:"source,omitempty"`
	ID           string                 `json:"id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        map[string]any         `json:"input,omitempty"`
	ToolUseID    string                 `json:"tool_use_id,omitempty"`
	Content      string                 `json:"content,omitempty"`
	IsError      bool                   `json:"is_error,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  *Schema                `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// Anthropic SSE event types
type anthropicEvent struct {
	Type         string            `json:"type"`
	Message      *anthropicAPIMsg  `json:"message,omitempty"`
	Index        int               `json:"index,omitempty"`
	ContentBlock *anthropicContent `json:"content_block,omitempty"`
	Delta        *anthropicDelta   `json:"delta,omitempty"`
	Usage        *anthropicUsage   `json:"usage,omitempty"`
}

type anthropicAPIMsg struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []anthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"`
	StopSequence string             `json:"stop_sequence"`
	Usage        anthropicUsage     `json:"usage"`
}

type anthropicDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

func streamAnthropic(ctx context.Context, model Model, req Request, opts StreamOptions) (*StreamHandle, error) {
	// Convert request to Anthropic format
	anthropicReq := convertToAnthropicRequest(model, req, opts)

	body, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	baseURL := model.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	url := baseURL + "/v1/messages"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", opts.UserAgent)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
	if model.APIKey != "" {
		httpReq.Header.Set("x-api-key", model.APIKey)
	}
	if opts.SessionID != "" {
		httpReq.Header.Set("session_id", opts.SessionID)
	}
	if shouldEnableAnthropicCaching(opts.CacheRetention) {
		httpReq.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	}

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		// Network errors (connection refused, timeout, DNS failure, etc.) are retryable
		return nil, &retryableError{
			err:       fmt.Errorf("send request: %w", err),
			retryable: true,
		}
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		return nil, &retryableError{
			err:        err,
			retryable:  isRetryable(resp.StatusCode),
			statusCode: resp.StatusCode,
		}
	}

	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)

	go processAnthropicStream(ctx, resp.Body, model, events, done, errCh)

	return newStreamHandle(events, done, errCh), nil
}

func convertToAnthropicRequest(model Model, req Request, opts StreamOptions) anthropicRequest {
	anthropicReq := anthropicRequest{
		Model:  model.ID,
		Stream: true,
		System: convertSystemBlocksToAnthropic(req.System),
		Tools: make([]anthropicTool, 0, len(req.Tools)),
	}

	if len(req.System) == 0 {
		anthropicReq.System = nil
	}

	// Set max tokens
	if opts.MaxTokens != nil {
		anthropicReq.MaxTokens = *opts.MaxTokens
	} else if model.MaxTokens > 0 {
		anthropicReq.MaxTokens = model.MaxTokens
	} else {
		anthropicReq.MaxTokens = 8192 // Default
	}

	// Set temperature
	if opts.Temperature != nil {
		anthropicReq.Temperature = opts.Temperature
	}

	// Set thinking if enabled
	if opts.ThinkingLevel != "" && opts.ThinkingLevel != ThinkingOff {
		budget := thinkingBudget(opts.ThinkingLevel)
		anthropicReq.Thinking = &anthropicThinking{
			Type:         "enabled",
			BudgetTokens: budget,
		}
		// Temperature must be 1 for thinking mode
		temp := 1.0
		anthropicReq.Temperature = &temp
	}

	// Convert messages
	anthropicReq.Messages = convertMessagesToAnthropic(req.Messages)

	// Convert tools
	for _, tool := range req.Tools {
		anthropicReq.Tools = append(anthropicReq.Tools, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: GenerateSchema(tool.Parameters),
		})
	}

	applyAnthropicCaching(&anthropicReq, req.System, opts.CacheRetention)

	return anthropicReq
}

func applyAnthropicCaching(req *anthropicRequest, blocks []SystemBlock, retention CacheRetention) {
	if !shouldEnableAnthropicCaching(retention) {
		return
	}

	cacheControl := &anthropicCacheControl{Type: "ephemeral"}
	for i, block := range blocks {
		if !block.CacheBreakpoint {
			continue
		}
		if i < len(req.System) {
			req.System[i].CacheControl = cacheControl
		}
	}
	if len(req.Tools) > 0 {
		req.Tools[len(req.Tools)-1].CacheControl = cacheControl
	}

	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := &req.Messages[i]
		if msg.Role != "user" || len(msg.Content) == 0 {
			continue
		}
		msg.Content[len(msg.Content)-1].CacheControl = cacheControl
		break
	}
}

func convertSystemBlocksToAnthropic(blocks []SystemBlock) []anthropicContent {
	if len(blocks) == 0 {
		return nil
	}
	result := make([]anthropicContent, 0, len(blocks))
	for _, block := range blocks {
		content := anthropicContent{
			Type: "text",
			Text: block.Text,
		}
		result = append(result, content)
	}
	return result
}

func shouldEnableAnthropicCaching(retention CacheRetention) bool {
	switch retention {
	case CacheShort, CacheLong:
		return true
	default:
		return false
	}
}

func thinkingBudget(level ThinkingLevel) int {
	switch level {
	case ThinkingMinimal:
		return 1024
	case ThinkingLow:
		return 4096
	case ThinkingMedium:
		return 10000
	case ThinkingHigh:
		return 32000
	case ThinkingXHigh:
		return 100000
	default:
		return 10000
	}
}

func convertMessagesToAnthropic(messages []Message) []anthropicMessage {
	var result []anthropicMessage
	var pendingToolResults []anthropicContent

	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			result = append(result, anthropicMessage{
				Role:    "user",
				Content: pendingToolResults,
			})
			pendingToolResults = nil
		}
	}

	for _, msg := range messages {
		switch m := msg.(type) {
		case UserMessage:
			flushToolResults()
			result = append(result, anthropicMessage{
				Role:    "user",
				Content: convertContentBlocksToAnthropic(m.Content),
			})
		case AssistantMessage:
			flushToolResults()
			content := convertContentBlocksToAnthropic(m.Content)
			// Only add assistant message if it has content
			if len(content) > 0 {
				result = append(result, anthropicMessage{
					Role:    "assistant",
					Content: content,
				})
			}
		case ToolResultMessage:
			// Accumulate tool results to merge into a single user message
			pendingToolResults = append(pendingToolResults, anthropicContent{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   extractTextFromContent(m.Content),
				IsError:   m.IsError,
			})
		}
	}

	// Flush any remaining tool results at the end
	flushToolResults()

	return result
}

func convertContentBlocksToAnthropic(blocks []ContentBlock) []anthropicContent {
	var result []anthropicContent

	for _, block := range blocks {
		switch b := block.(type) {
		case TextContent:
			result = append(result, anthropicContent{
				Type: "text",
				Text: b.Text,
			})
		case ThinkingContent:
			// Exclude thinking blocks from replayed messages.
			// Thinking is internal reasoning that doesn't need to be sent back
			// to the API. Including it without the original signature would
			// cause validation errors.
			continue
		case ImageContent:
			result = append(result, anthropicContent{
				Type: "image",
				Source: &anthropicSource{
					Type:      "base64",
					MediaType: b.MimeType,
					Data:      b.Data,
				},
			})
		case ToolCall:
			result = append(result, anthropicContent{
				Type:  "tool_use",
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Arguments,
			})
		}
	}

	return result
}

func extractTextFromContent(blocks []ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		if tc, ok := block.(TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func processAnthropicStream(ctx context.Context, body io.ReadCloser, model Model, events chan<- StreamEvent, done chan<- AssistantMessage, errCh chan<- error) {
	defer body.Close()
	defer close(events)

	reader := bufio.NewReader(body)

	var partial AssistantMessage
	partial.Role = "assistant"
	partial.Model = model.ID
	partial.API = model.API
	partial.Provider = model.Provider
	partial.Timestamp = time.Now()

	// Track content blocks and their JSON accumulation for tool calls
	var toolCallJSONs = make(map[int]string)

	for {
		select {
		case <-ctx.Done():
			partial.StopReason = StopReasonAborted
			events <- ErrorEvent{Reason: StopReasonAborted, Message: partial}
			done <- partial
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			partial.StopReason = StopReasonError
			partial.ErrorMessage = err.Error()
			events <- ErrorEvent{Reason: StopReasonError, Message: partial}
			errCh <- err
			return
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event anthropicEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue // Skip malformed events
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				partial.Usage.Input = event.Message.Usage.InputTokens
			partial.Usage.Output = event.Message.Usage.OutputTokens
				partial.Usage.CacheRead = event.Message.Usage.CacheReadInputTokens
				partial.Usage.CacheWrite = event.Message.Usage.CacheCreationInputTokens
			}
			events <- StartEvent{Partial: partial}

		case "content_block_start":
			if event.ContentBlock != nil {
				switch event.ContentBlock.Type {
				case "text":
					partial.Content = append(partial.Content, TextContent{Type: "text", Text: ""})
				case "thinking":
					partial.Content = append(partial.Content, ThinkingContent{Type: "thinking", Thinking: ""})
				case "tool_use":
					partial.Content = append(partial.Content, ToolCall{
						Type: "toolCall",
						ID:   event.ContentBlock.ID,
						Name: event.ContentBlock.Name,
					})
					toolCallJSONs[event.Index] = ""
				}
			}

		case "content_block_delta":
			if event.Delta != nil {
				idx := event.Index
				if idx < len(partial.Content) {
					switch event.Delta.Type {
					case "text_delta":
						if tc, ok := partial.Content[idx].(TextContent); ok {
							tc.Text += event.Delta.Text
							partial.Content[idx] = tc
							events <- TextDeltaEvent{ContentIndex: idx, Delta: event.Delta.Text, Partial: partial}
						}
					case "thinking_delta":
						if tc, ok := partial.Content[idx].(ThinkingContent); ok {
							tc.Thinking += event.Delta.Thinking
							partial.Content[idx] = tc
							events <- ThinkingDeltaEvent{ContentIndex: idx, Delta: event.Delta.Thinking, Partial: partial}
						}
					case "input_json_delta":
						toolCallJSONs[idx] += event.Delta.PartialJSON
						events <- ToolCallDeltaEvent{ContentIndex: idx, Delta: event.Delta.PartialJSON, Partial: partial}
					}
				}
			}

		case "content_block_stop":
			idx := event.Index
			if idx < len(partial.Content) {
				if tc, ok := partial.Content[idx].(ToolCall); ok {
					// Parse accumulated JSON
					jsonStr := toolCallJSONs[idx]
					if jsonStr != "" {
						var args map[string]any
						if err := json.Unmarshal([]byte(jsonStr), &args); err == nil {
							tc.Arguments = args
							partial.Content[idx] = tc
						}
					}
					events <- ToolCallEndEvent{ContentIndex: idx, ToolCall: tc, Partial: partial}
				}
			}

		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != "" {
				partial.StopReason = mapAnthropicStopReason(event.Delta.StopReason)
			}
			if event.Usage != nil {
				partial.Usage.Output = event.Usage.OutputTokens
			}

		case "message_stop":
			// Final message - compute costs
			partial.Usage.Total = partial.Usage.Input + partial.Usage.Output
			partial.Usage.Cost = calculateCost(partial.Usage, model.Cost)
			events <- DoneEvent{Reason: partial.StopReason, Message: partial}
			done <- partial
			return
		}
	}

	// If we get here without a message_stop, send what we have
	partial.Usage.Total = partial.Usage.Input + partial.Usage.Output
	partial.Usage.Cost = calculateCost(partial.Usage, model.Cost)
	if partial.StopReason == "" {
		partial.StopReason = StopReasonEnd
	}
	events <- DoneEvent{Reason: partial.StopReason, Message: partial}
	done <- partial
}

func mapAnthropicStopReason(reason string) StopReason {
	switch reason {
	case "end_turn":
		return StopReasonEnd
	case "tool_use":
		return StopReasonToolUse
	case "max_tokens":
		return StopReasonMaxTokens
	default:
		return StopReasonEnd
	}
}

func calculateCost(usage Usage, cost Cost) UsageCost {
	result := UsageCost{
		Input:      float64(usage.Input) * cost.Input / 1_000_000,
		Output:     float64(usage.Output) * cost.Output / 1_000_000,
		CacheRead:  float64(usage.CacheRead) * cost.CacheRead / 1_000_000,
		CacheWrite: float64(usage.CacheWrite) * cost.CacheWrite / 1_000_000,
	}
	result.Total = result.Input + result.Output + result.CacheRead + result.CacheWrite
	return result
}
