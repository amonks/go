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
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	Messages     []anthropicMessage     `json:"messages"`
	System       []anthropicContent     `json:"system,omitempty"`
	Stream       bool                   `json:"stream"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	ToolChoice   *anthropicToolChoice   `json:"tool_choice,omitempty"`
	Temperature  *float64               `json:"temperature,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
}

// anthropicOutputConfig carries the effort level for models that use
// adaptive thinking (Claude 4.6 and later).
type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
	Format any    `json:"format,omitempty"`
}

// anthropicToolChoice forces the model to call a specific tool. Type is
// "tool" with Name set to the tool to force.
type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	Display      string `json:"display,omitempty"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string           `json:"type"`
	Text      string           `json:"text,omitempty"`
	Thinking  *string          `json:"thinking,omitempty"`
	Signature string           `json:"signature,omitempty"`
	Data      string           `json:"data,omitempty"`
	Source    *anthropicSource `json:"source,omitempty"`
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Input     map[string]any   `json:"input,omitempty"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	// Content is a tool_result's payload: the API takes a plain string or an
	// array of text/image blocks, so this holds a string or
	// []anthropicContent (nil omits the key, the shape an empty result has
	// always been sent as).
	Content      any                    `json:"content,omitempty"`
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
	Signature   string `json:"signature,omitempty"`
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
	anthropicReq, err := convertToAnthropicRequest(model, req, opts)
	if err != nil {
		return nil, err
	}

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

	client := newHTTPClient(0)
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

func convertToAnthropicRequest(model Model, req Request, opts StreamOptions) (anthropicRequest, error) {
	anthropicReq := anthropicRequest{
		Model:  model.ID,
		Stream: true,
		System: convertSystemBlocksToAnthropic(req.System),
		Tools:  make([]anthropicTool, 0, len(req.Tools)),
	}

	if len(req.System) == 0 {
		anthropicReq.System = nil
	}

	// max_tokens is required here, and the package invents no value
	// for it: the caller names one, or the model's ceiling stands in.
	limit, err := opts.outputLimit(model)
	if err != nil {
		return anthropicRequest{}, err
	}
	if limit == 0 {
		return anthropicRequest{}, fmt.Errorf("anthropic requires max_tokens: %s has no known output ceiling and the call set none", model.ID)
	}
	anthropicReq.MaxTokens = limit

	// Set temperature
	if opts.Temperature != nil {
		anthropicReq.Temperature = opts.Temperature
	}

	// Set thinking if enabled. Claude 4.6-era and later models reject the
	// old {type: "enabled", budget_tokens: N} shape with a 400 — they take
	// {type: "adaptive"} with an output_config effort level instead, and
	// also reject explicit sampling parameters like temperature.
	switch {
	case opts.ThinkingLevel == ThinkingOff:
		// On the adaptive families thinking is on unless the request says
		// otherwise, and its tokens come out of max_tokens — so a caller
		// asking for no thinking has to be sent as an explicit "disabled",
		// not as an omitted parameter. (Sending no effort leaves the
		// default, which is the only level Opus 5 accepts alongside
		// disabled thinking.) Before 4.6, omitting thinking already meant
		// off, and Fable and Mythos think unconditionally and 400 on
		// "disabled": both take the omission. Opus 5.5 also cannot disable
		// thinking; Sonnet 5.5 uses between_tools for no up-front thinking.
		if modelUsesBetweenTools(model.ID) {
			anthropicReq.Thinking = &anthropicThinking{Type: "between_tools"}
		} else if modelUsesAdaptiveThinking(model.ID) && !modelAlwaysThinks(model.ID) {
			anthropicReq.Thinking = &anthropicThinking{Type: "disabled"}
		}
	case opts.ThinkingLevel != "":
		if modelUsesAdaptiveThinking(model.ID) {
			anthropicReq.Thinking = &anthropicThinking{Type: "adaptive"}
			anthropicReq.OutputConfig = &anthropicOutputConfig{
				Effort: effortLevel(opts.ThinkingLevel),
			}
		} else {
			budget := thinkingBudget(opts.ThinkingLevel)
			if budget >= limit {
				return anthropicRequest{}, fmt.Errorf("anthropic thinking level %q requires max_tokens greater than its %d-token budget (got %d)", opts.ThinkingLevel, budget, limit)
			}
			anthropicReq.Thinking = &anthropicThinking{
				Type:         "enabled",
				BudgetTokens: budget,
			}
			// Temperature must be 1 for thinking mode
			temp := 1.0
			anthropicReq.Temperature = &temp
		}
	}

	// Claude 5.5 omits thinking text by default, including progress between
	// tool calls. Ask for summaries so streaming agent UIs keep showing it.
	// between_tools already returns progress and accepts no display field.
	if modelIsClaude55(model.ID) && (anthropicReq.Thinking == nil || anthropicReq.Thinking.Type == "adaptive") {
		anthropicReq.Thinking = &anthropicThinking{Type: "adaptive", Display: "summarized"}
	}

	// Convert messages
	anthropicReq.Messages = convertMessagesToAnthropic(model, req.Messages)

	// Convert tools
	for _, tool := range req.Tools {
		anthropicReq.Tools = append(anthropicReq.Tools, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: GenerateSchema(tool.Parameters),
		})
	}

	// Force execution of a specific tool when requested.
	// A forced tool and extended thinking are mutually exclusive at the
	// API, which refuses the pair with a 400; refusing here names the
	// reason. A caller forcing a tool says ThinkingOff — on the adaptive
	// families thinking is on unless the request disables it, so an
	// unset level is thinking too — and a model that thinks
	// unconditionally cannot take a forced tool at all. Sonnet 5.5 also
	// rejects forced tools, even with between_tools.
	if req.ToolChoice != "" {
		switch {
		case modelRejectsForcedTools(model.ID):
			return anthropicRequest{}, fmt.Errorf("llm: %s cannot take a forced tool (%s)", model.ID, req.ToolChoice)
		case opts.ThinkingLevel != ThinkingOff && (opts.ThinkingLevel != "" || modelUsesAdaptiveThinking(model.ID)):
			return anthropicRequest{}, fmt.Errorf("llm: a forced tool (%s) needs ThinkingOff: the API refuses tool_choice beside extended thinking", req.ToolChoice)
		}
		anthropicReq.ToolChoice = &anthropicToolChoice{Type: "tool", Name: req.ToolChoice}
	}

	if req.OutputSchema != nil {
		schema, err := structuredRequestSchema(model, req.OutputSchema)
		if err != nil {
			return anthropicRequest{}, err
		}
		if anthropicReq.OutputConfig == nil {
			anthropicReq.OutputConfig = &anthropicOutputConfig{}
		}
		anthropicReq.OutputConfig.Format = map[string]any{"type": "json_schema", "schema": schema}
	}
	applyAnthropicCaching(&anthropicReq, req.System, opts.CacheRetention)

	return anthropicReq, nil
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

// adaptiveThinkingModelPrefixes lists the Anthropic model families that use
// adaptive thinking. On the 4.6 family the old enabled/budget_tokens shape is
// deprecated; on everything newer (4.7+, Sonnet 5, Opus 5, Fable/Mythos 5) it
// is rejected with a 400, as are explicit sampling parameters.
var adaptiveThinkingModelPrefixes = []string{
	"claude-fable-5",
	"claude-mythos-5",
	"claude-opus-5",
	"claude-sonnet-5",
	"claude-opus-4-6",
	"claude-opus-4-7",
	"claude-opus-4-8",
	"claude-sonnet-4-6",
}

func modelUsesAdaptiveThinking(modelID string) bool {
	for _, prefix := range adaptiveThinkingModelPrefixes {
		if strings.HasPrefix(modelID, prefix) {
			return true
		}
	}
	return false
}

// alwaysThinkingModelPrefixes lists the families whose thinking cannot be
// turned off: an explicit {type: "disabled"} is a 400, so a request that
// wants no thinking still gets it (omitted, or adaptive with display).
var alwaysThinkingModelPrefixes = []string{
	"claude-opus-5-5",
	"claude-fable-5",
	"claude-mythos-5",
}

func modelAlwaysThinks(modelID string) bool {
	for _, prefix := range alwaysThinkingModelPrefixes {
		if strings.HasPrefix(modelID, prefix) {
			return true
		}
	}
	return false
}

// Claude 5.5 request differences are documented in each model's migration guide:
// https://platform.claude.com/docs/en/models/opus-5-5/migration-guide
// https://platform.claude.com/docs/en/models/sonnet-5-5/migration-guide
func modelIsClaude55(id string) bool {
	return strings.HasPrefix(id, "claude-opus-5-5") || modelUsesBetweenTools(id)
}

func modelUsesBetweenTools(id string) bool {
	return strings.HasPrefix(id, "claude-sonnet-5-5")
}

func modelRejectsForcedTools(id string) bool {
	return modelAlwaysThinks(id) || modelUsesBetweenTools(id)
}

// effortLevel maps a ThinkingLevel onto the adaptive-thinking effort scale.
func effortLevel(level ThinkingLevel) string {
	switch level {
	case ThinkingMinimal, ThinkingLow:
		return "low"
	case ThinkingMedium:
		return "medium"
	case ThinkingHigh:
		return "high"
	case ThinkingXHigh:
		return "xhigh"
	case ThinkingMax:
		return "max"
	default:
		return "medium"
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

func convertMessagesToAnthropic(model Model, messages []Message) []anthropicMessage {
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
				Content: convertContentBlocksToAnthropic(model, m.Content),
			})
		case AssistantMessage:
			flushToolResults()
			content := convertContentBlocksToAnthropic(model, m.Content)
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
				Content:   toolResultContentToAnthropic(m.Content),
				IsError:   m.IsError,
			})
		}
	}

	// Flush any remaining tool results at the end
	flushToolResults()

	return result
}

func convertContentBlocksToAnthropic(model Model, blocks []ContentBlock) []anthropicContent {
	var result []anthropicContent

	for _, block := range blocks {
		switch b := block.(type) {
		case TextContent:
			result = append(result, anthropicContent{
				Type: "text",
				Text: b.Text,
			})
		case ThinkingContent:
			// Display text is not replay state. The signed block follows as
			// OpaqueContent, preserved through the proxy and agent store.
			continue
		case OpaqueContent:
			if b.API == APIAnthropicMessages && b.Model == model.ID && b.Provider == model.Provider && b.BaseURL == model.BaseURL {
				var item anthropicContent
				if json.Unmarshal(b.Data, &item) == nil && ((item.Type == "thinking" && item.Thinking != nil && item.Signature != "") || (item.Type == "redacted_thinking" && item.Data != "")) {
					result = append(result, item)
				}
			}
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

// toolResultContentToAnthropic picks the tool_result content's wire form: a
// text-only result stays the plain string it has always been (nil when
// empty, omitting the key), and a result carrying images becomes the block
// array the API takes.
func toolResultContentToAnthropic(blocks []ContentBlock) any {
	if len(imageBlocks(blocks)) == 0 {
		if text := extractTextFromContent(blocks); text != "" {
			return text
		}
		return nil
	}
	var result []anthropicContent
	for _, block := range blocks {
		switch b := block.(type) {
		case TextContent:
			// The API rejects empty text blocks, so an image-only result
			// carries no text half.
			if b.Text == "" {
				continue
			}
			result = append(result, anthropicContent{Type: "text", Text: b.Text})
		case ImageContent:
			result = append(result, anthropicContent{
				Type: "image",
				Source: &anthropicSource{
					Type:      "base64",
					MediaType: b.MimeType,
					Data:      b.Data,
				},
			})
		}
	}
	return result
}

// imageBlocks collects the image blocks out of mixed content.
func imageBlocks(blocks []ContentBlock) []ImageContent {
	var out []ImageContent
	for _, block := range blocks {
		if ic, ok := block.(ImageContent); ok {
			out = append(out, ic)
		}
	}
	return out
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
	var thinkingBlocks = make(map[int]anthropicContent)

	// The API's content indices count every block it streams, including
	// types we don't recognize. Map the server's index to our position in partial.Content so an unrecognized block
	// can't desync every block after it — before this map, a leading
	// redacted_thinking block silently shifted the tool_use index and the
	// tool call's arguments were dropped.
	var blockPositions = make(map[int]int)

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
			errCh <- newStreamFailure(model, partial, err)
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
					blockPositions[event.Index] = len(partial.Content) - 1
				case "thinking":
					text := ""
					if event.ContentBlock.Thinking != nil {
						text = *event.ContentBlock.Thinking
					}
					block := *event.ContentBlock
					block.Thinking = &text
					thinkingBlocks[event.Index] = block
					partial.Content = append(partial.Content, ThinkingContent{Type: "thinking", Thinking: text})
					blockPositions[event.Index] = len(partial.Content) - 1
				case "redacted_thinking":
					thinkingBlocks[event.Index] = *event.ContentBlock
				case "tool_use":
					partial.Content = append(partial.Content, ToolCall{
						Type: "toolCall",
						ID:   event.ContentBlock.ID,
						Name: event.ContentBlock.Name,
					})
					blockPositions[event.Index] = len(partial.Content) - 1
					toolCallJSONs[event.Index] = ""
				}
			}

		case "content_block_delta":
			if event.Delta != nil {
				if block, ok := thinkingBlocks[event.Index]; ok {
					switch event.Delta.Type {
					case "thinking_delta":
						text := event.Delta.Thinking
						if block.Thinking != nil {
							text = *block.Thinking + text
						}
						block.Thinking = &text
					case "signature_delta":
						block.Signature += event.Delta.Signature
					}
					thinkingBlocks[event.Index] = block
				}
				if idx, ok := blockPositions[event.Index]; ok {
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
						toolCallJSONs[event.Index] += event.Delta.PartialJSON
						events <- ToolCallDeltaEvent{ContentIndex: idx, Delta: event.Delta.PartialJSON, Partial: partial}
					}
				}
			}

		case "content_block_stop":
			if block, ok := thinkingBlocks[event.Index]; ok {
				if block.Signature != "" || block.Data != "" {
					data, _ := json.Marshal(block)
					partial.Content = append(partial.Content, OpaqueContent{Type: "opaque", API: APIAnthropicMessages, Provider: model.Provider, Model: model.ID, BaseURL: model.BaseURL, Data: data})
				}
				delete(thinkingBlocks, event.Index)
			}
			if idx, ok := blockPositions[event.Index]; ok {
				if tc, ok := partial.Content[idx].(ToolCall); ok {
					// Parse accumulated JSON
					jsonStr := toolCallJSONs[event.Index]
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
				if partial.StopReason == StopReasonError {
					partial.ErrorMessage = fmt.Sprintf("anthropic stop reason %q", event.Delta.StopReason)
				}
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

	// A disconnected stream is not a completed answer, even if its final
	// text delta happens to be valid JSON.
	err := fmt.Errorf("anthropic stream ended before message_stop")
	partial.StopReason = StopReasonError
	partial.ErrorMessage = err.Error()
	events <- ErrorEvent{Reason: StopReasonError, Message: partial}
	errCh <- newStreamFailure(model, partial, err)
}

func mapAnthropicStopReason(reason string) StopReason {
	switch reason {
	case "end_turn", "stop_sequence":
		return StopReasonEnd
	case "tool_use":
		return StopReasonToolUse
	case "max_tokens":
		return StopReasonMaxTokens
	case "refusal":
		return StopReasonRefusal
	default:
		return StopReasonError
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
