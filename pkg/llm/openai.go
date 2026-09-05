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

// OpenAI Chat Completions API types
type openAICompletionsRequest struct {
	Model               string               `json:"model"`
	Messages            []openAIMessage      `json:"messages"`
	MaxTokens           int                  `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                  `json:"max_completion_tokens,omitempty"` // For o1/o3/o4 reasoning models
	Temperature         *float64             `json:"temperature,omitempty"`
	Stream              bool                 `json:"stream"`
	Tools               []openAITool         `json:"tools,omitempty"`
	ToolChoice          any                  `json:"tool_choice,omitempty"`
	StreamOptions       *openAIStreamOptions `json:"stream_options,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"` // string or []openAIContentPart
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type openAIToolCall struct {
	Index    int                `json:"index"` // Required for streaming: identifies which tool call is being updated
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAITool struct {
	Type     string            `json:"type"`
	Function openAIFunctionDef `json:"function"`
}

type openAIFunctionDef struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Parameters  *Schema `json:"parameters"`
}

// OpenAI SSE response types
type openAIStreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
}

type openAIChoice struct {
	Index        int          `json:"index"`
	Delta        *openAIDelta `json:"delta,omitempty"`
	FinishReason string       `json:"finish_reason,omitempty"`
}

type openAIDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type openAIUsage struct {
	PromptTokens        int                        `json:"prompt_tokens"`
	CompletionTokens    int                        `json:"completion_tokens"`
	TotalTokens         int                        `json:"total_tokens"`
	PromptTokensDetails *openAIPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

func streamOpenAICompletions(ctx context.Context, model Model, req Request, opts StreamOptions) (*StreamHandle, error) {
	openAIReq, err := convertToOpenAIRequest(model, req, opts)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	baseURL := model.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	url := baseURL + "/v1/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", opts.UserAgent)
	if model.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+model.APIKey)
	}
	if opts.SessionID != "" {
		httpReq.Header.Set("session_id", opts.SessionID)
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
		err := fmt.Errorf("openai API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		return nil, &retryableError{
			err:        err,
			retryable:  isRetryable(resp.StatusCode),
			statusCode: resp.StatusCode,
		}
	}

	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)

	go processOpenAIStream(ctx, resp.Body, model, events, done, errCh)

	return newStreamHandle(events, done, errCh), nil
}

func convertToOpenAIRequest(model Model, req Request, opts StreamOptions) (openAICompletionsRequest, error) {
	openAIReq := openAICompletionsRequest{
		Model:         model.ID,
		Stream:        true,
		StreamOptions: &openAIStreamOptions{IncludeUsage: true},
	}

	// The field is optional here — omitted (0), the provider applies
	// its own default. Reasoning models (o1, o3, o4) take
	// max_completion_tokens instead of max_tokens.
	limit, err := opts.outputLimit(model)
	if err != nil {
		return openAICompletionsRequest{}, err
	}
	if model.UseMaxCompletionTokens {
		openAIReq.MaxCompletionTokens = limit
	} else {
		openAIReq.MaxTokens = limit
	}

	// Set temperature
	if opts.Temperature != nil {
		openAIReq.Temperature = opts.Temperature
	}

	// Convert messages
	openAIReq.Messages = convertMessagesToOpenAI(req.System, req.Messages)

	// Convert tools
	for _, tool := range req.Tools {
		openAIReq.Tools = append(openAIReq.Tools, openAITool{
			Type: "function",
			Function: openAIFunctionDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  GenerateSchema(tool.Parameters),
			},
		})
	}

	// Force a specific tool when requested, for schema-constrained output.
	if req.ToolChoice != "" {
		openAIReq.ToolChoice = map[string]any{
			"type":     "function",
			"function": map[string]any{"name": req.ToolChoice},
		}
	}

	return openAIReq, nil
}

func convertMessagesToOpenAI(systemBlocks []SystemBlock, messages []Message) []openAIMessage {
	var result []openAIMessage

	systemText := joinSystemBlocks(systemBlocks)
	if systemText != "" {
		result = append(result, openAIMessage{
			Role:    "system",
			Content: systemText,
		})
	}

	// The API takes no images in tool messages, and every tool result for an
	// assistant turn's calls must directly follow it — so images ride one
	// synthetic user message after the run of tool results, each preceded by
	// a text part naming the call it belongs to.
	var pendingToolImages []openAIContentPart
	flushToolImages := func() {
		if len(pendingToolImages) == 0 {
			return
		}
		result = append(result, openAIMessage{Role: "user", Content: pendingToolImages})
		pendingToolImages = nil
	}

	for _, msg := range messages {
		switch m := msg.(type) {
		case UserMessage:
			flushToolImages()
			content := convertContentBlocksToOpenAI(m.Content)
			result = append(result, openAIMessage{
				Role:    "user",
				Content: content,
			})
		case AssistantMessage:
			flushToolImages()
			assistantMsg := openAIMessage{
				Role: "assistant",
			}
			// Extract text and tool calls
			var textParts []string
			for _, block := range m.Content {
				switch b := block.(type) {
				case TextContent:
					textParts = append(textParts, b.Text)
				case ThinkingContent:
					// Convert thinking to text for history
					textParts = append(textParts, b.Thinking)
				case ToolCall:
					argsJSON, _ := json.Marshal(b.Arguments)
					assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, openAIToolCall{
						ID:   b.ID,
						Type: "function",
						Function: openAIToolFunction{
							Name:      b.Name,
							Arguments: string(argsJSON),
						},
					})
				}
			}
			if len(textParts) > 0 {
				assistantMsg.Content = strings.Join(textParts, "\n")
			}
			result = append(result, assistantMsg)
		case ToolResultMessage:
			result = append(result, openAIMessage{
				Role:       "tool",
				Content:    extractTextFromContent(m.Content),
				ToolCallID: m.ToolCallID,
			})
			for i, img := range imageBlocks(m.Content) {
				if i == 0 {
					pendingToolImages = append(pendingToolImages, openAIContentPart{
						Type: "text",
						Text: "Image output of tool call " + m.ToolCallID + ":",
					})
				}
				pendingToolImages = append(pendingToolImages, openAIContentPart{
					Type:     "image_url",
					ImageURL: &openAIImageURL{URL: "data:" + img.MimeType + ";base64," + img.Data},
				})
			}
		}
	}
	flushToolImages()

	return result
}

func convertContentBlocksToOpenAI(blocks []ContentBlock) any {
	// If only text, return as string
	if len(blocks) == 1 {
		if tc, ok := blocks[0].(TextContent); ok {
			return tc.Text
		}
	}

	// Otherwise return as array of content parts
	var parts []openAIContentPart
	for _, block := range blocks {
		switch b := block.(type) {
		case TextContent:
			parts = append(parts, openAIContentPart{
				Type: "text",
				Text: b.Text,
			})
		case ImageContent:
			parts = append(parts, openAIContentPart{
				Type: "image_url",
				ImageURL: &openAIImageURL{
					URL: "data:" + b.MimeType + ";base64," + b.Data,
				},
			})
		}
	}
	return parts
}

func joinSystemBlocks(blocks []SystemBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) == "" {
			continue
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n\n")
}

func processOpenAIStream(ctx context.Context, body io.ReadCloser, model Model, events chan<- StreamEvent, done chan<- AssistantMessage, errCh chan<- error) {
	defer body.Close()
	defer close(events)

	reader := bufio.NewReader(body)

	var partial AssistantMessage
	partial.Role = "assistant"
	partial.Model = model.ID
	partial.API = model.API
	partial.Provider = model.Provider
	partial.Timestamp = time.Now()

	// Track tool calls being built. OpenAI streams tool calls with an integer index
	// to identify which tool call is being updated (ID only appears on first delta).
	type toolCallBuilder struct {
		id         string
		name       string
		arguments  string
		contentIdx int // Index in partial.Content where this tool call is stored
	}
	toolCalls := make(map[int]*toolCallBuilder)
	hasStarted := false
	textContentIdx := -1

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

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Handle usage in final chunk
		if chunk.Usage != nil {
			partial.Usage.Input = chunk.Usage.PromptTokens
			partial.Usage.Output = chunk.Usage.CompletionTokens
			partial.Usage.Total = chunk.Usage.TotalTokens
			if chunk.Usage.PromptTokensDetails != nil {
				partial.Usage.CacheRead = chunk.Usage.PromptTokensDetails.CachedTokens
				partial.Usage.Input -= partial.Usage.CacheRead
			}
		}

		for _, choice := range chunk.Choices {
			if !hasStarted {
				hasStarted = true
				events <- StartEvent{Partial: partial}
			}

			if choice.Delta != nil {
				// Handle text content
				if choice.Delta.Content != "" {
					if textContentIdx == -1 {
						textContentIdx = len(partial.Content)
						partial.Content = append(partial.Content, TextContent{Type: "text", Text: ""})
					}
					if tc, ok := partial.Content[textContentIdx].(TextContent); ok {
						tc.Text += choice.Delta.Content
						partial.Content[textContentIdx] = tc
						events <- TextDeltaEvent{ContentIndex: textContentIdx, Delta: choice.Delta.Content, Partial: partial}
					}
				}

				// Handle tool calls
				for _, tc := range choice.Delta.ToolCalls {
					// OpenAI uses integer indexes to track which tool call is being updated.
					// The ID only appears on the first delta for a tool call, subsequent
					// deltas only have the index.
					tcIdx := tc.Index

					builder := toolCalls[tcIdx]
					if builder == nil {
						// New tool call starting
						builder = &toolCallBuilder{}
						toolCalls[tcIdx] = builder
					}

					if tc.ID != "" {
						builder.id = tc.ID
					}
					if tc.Function.Name != "" {
						builder.name = tc.Function.Name
						// Add tool call to content and track mapping from tcIdx to content index
						contentIdx := len(partial.Content)
						builder.contentIdx = contentIdx
						partial.Content = append(partial.Content, ToolCall{
							Type: "toolCall",
							ID:   builder.id,
							Name: builder.name,
						})
						events <- ToolCallDeltaEvent{ContentIndex: contentIdx, Delta: "", Partial: partial}
					}
					if tc.Function.Arguments != "" {
						builder.arguments += tc.Function.Arguments
						events <- ToolCallDeltaEvent{ContentIndex: builder.contentIdx, Delta: tc.Function.Arguments, Partial: partial}
					}
				}
			}

			// Handle finish reason
			if choice.FinishReason != "" {
				partial.StopReason = mapOpenAIFinishReason(choice.FinishReason)

				// Finalize tool calls - parse accumulated arguments JSON
				for _, builder := range toolCalls {
					if builder.arguments != "" {
						var args map[string]any
						json.Unmarshal([]byte(builder.arguments), &args)
						// Update the tool call in content using tracked content index
						if builder.contentIdx >= 0 && builder.contentIdx < len(partial.Content) {
							if toolCall, ok := partial.Content[builder.contentIdx].(ToolCall); ok {
								toolCall.Arguments = args
								partial.Content[builder.contentIdx] = toolCall
								events <- ToolCallEndEvent{ContentIndex: builder.contentIdx, ToolCall: toolCall, Partial: partial}
							}
						}
					}
				}
			}
		}
	}

	// Calculate costs
	partial.Usage.Cost = calculateCost(partial.Usage, model.Cost)
	if partial.StopReason == "" {
		partial.StopReason = StopReasonEnd
	}
	events <- DoneEvent{Reason: partial.StopReason, Message: partial}
	done <- partial
}

func mapOpenAIFinishReason(reason string) StopReason {
	switch reason {
	case "stop":
		return StopReasonEnd
	case "tool_calls":
		return StopReasonToolUse
	case "length":
		return StopReasonMaxTokens
	default:
		return StopReasonEnd
	}
}

// OpenAI Responses API types

type responsesAPIRequest struct {
	Store           bool                `json:"store"`
	Include         []string            `json:"include"`
	Reasoning       *responsesReasoning `json:"reasoning,omitempty"`
	Model           string              `json:"model"`
	Input           any                 `json:"input"` // string or []map[string]any
	Instructions    string              `json:"instructions,omitempty"`
	MaxOutputTokens int                 `json:"max_output_tokens,omitempty"`
	Temperature     *float64            `json:"temperature,omitempty"`
	Stream          bool                `json:"stream"`
	Tools           []responsesAPITool  `json:"tools,omitempty"`
	ToolChoice      any                 `json:"tool_choice,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesContentPart struct {
	Type     string `json:"type"` // "input_text", "input_image"
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesAPITool struct {
	Type        string  `json:"type"` // "function"
	Name        string  `json:"name,omitempty"`
	Description string  `json:"description,omitempty"`
	Parameters  *Schema `json:"parameters,omitempty"`
}

// Responses API stream event types
type responsesStreamEvent struct {
	Refusal string `json:"refusal,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Type    string `json:"type"`

	// Common routing fields present on many streaming events
	ResponseID  string `json:"response_id,omitempty"`
	OutputIndex int    `json:"output_index,omitempty"`
	ItemID      string `json:"item_id,omitempty"`

	// Payload fields (vary by event type)
	Response  *responsesObj  `json:"response,omitempty"`
	Item      *responsesItem `json:"item,omitempty"`
	Part      *responsesPart `json:"part,omitempty"`
	Delta     string         `json:"delta,omitempty"`
	Text      string         `json:"text,omitempty"`
	Arguments string         `json:"arguments,omitempty"`
}

type responsesObj struct {
	Error             *responsesError `json:"error,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Model  string          `json:"model"`
	Output []responsesItem `json:"output"`
	Usage  *responsesUsage `json:"usage,omitempty"`
}

type responsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type responsesItem struct {
	Phase            string          `json:"phase,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Summary          []responsesPart `json:"summary,omitempty"`
	ID               string          `json:"id"`
	Type             string          `json:"type"` // "message", "function_call"
	Status           string          `json:"status"`
	Role             string          `json:"role,omitempty"`
	Content          []responsesPart `json:"content,omitempty"`
	Name             string          `json:"name,omitempty"`      // for function_call
	CallID           string          `json:"call_id,omitempty"`   // for function_call
	Arguments        string          `json:"arguments,omitempty"` // for function_call
}

type responsesPart struct {
	Refusal string `json:"refusal,omitempty"`
	Type    string `json:"type"` // "output_text"
	Text    string `json:"text"`
}

type responsesInputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type responsesUsage struct {
	InputTokens        int                          `json:"input_tokens"`
	OutputTokens       int                          `json:"output_tokens"`
	TotalTokens        int                          `json:"total_tokens"`
	InputTokensDetails *responsesInputTokensDetails `json:"input_tokens_details,omitempty"`
}

// streamOpenAIResponses implements the OpenAI Responses API streaming.
func streamOpenAIResponses(ctx context.Context, model Model, req Request, opts StreamOptions) (*StreamHandle, error) {
	responsesReq, err := convertToResponsesRequest(model, req, opts)
	if err != nil {
		return nil, &retryableError{err: err, retryable: false}
	}

	body, err := json.Marshal(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	baseURL := model.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	url := baseURL + "/v1/responses"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", opts.UserAgent)
	if model.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+model.APIKey)
	}
	if opts.SessionID != "" {
		httpReq.Header.Set("session_id", opts.SessionID)
	}

	client := newHTTPClient(0)
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, &retryableError{
			err:       fmt.Errorf("send request: %w", err),
			retryable: true,
		}
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("openai responses API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		return nil, &retryableError{
			err:        err,
			retryable:  isRetryable(resp.StatusCode),
			statusCode: resp.StatusCode,
		}
	}

	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)

	go processResponsesStream(ctx, resp.Body, model, events, done, errCh)

	return newStreamHandle(events, done, errCh), nil
}

func convertToResponsesRequest(model Model, req Request, opts StreamOptions) (responsesAPIRequest, error) {
	responsesReq := responsesAPIRequest{
		Model:   model.ID,
		Stream:  true,
		Include: []string{"reasoning.encrypted_content"},
	}

	// Optional here; omitted, the provider applies its own default.
	limit, err := opts.outputLimit(model)
	if err != nil {
		return responsesAPIRequest{}, err
	}
	responsesReq.MaxOutputTokens = limit

	effort, err := responsesReasoningEffort(model.ID, opts.ThinkingLevel)
	if err != nil {
		return responsesAPIRequest{}, err
	}
	if effort != "" {
		responsesReq.Reasoning = &responsesReasoning{Effort: effort}
	}
	if opts.Temperature != nil {
		if !responsesSupportsTemperature(model.ID, effort) {
			return responsesAPIRequest{}, fmt.Errorf("OpenAI model %s does not support temperature with reasoning effort %q", model.ID, effort)
		}
		responsesReq.Temperature = opts.Temperature
	}

	// Set instructions (system prompt)
	if systemText := joinSystemBlocks(req.System); systemText != "" {
		responsesReq.Instructions = systemText
	}

	// Convert messages to input
	responsesReq.Input = convertMessagesToResponsesInputForModel(model, req.Messages)

	// Convert tools
	for _, tool := range req.Tools {
		responsesReq.Tools = append(responsesReq.Tools, responsesAPITool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  GenerateSchema(tool.Parameters),
		})
	}

	// Force a specific tool when requested, for schema-constrained output.
	if req.ToolChoice != "" {
		if len(responsesEfforts(model.ID)) == 0 {
			return responsesAPIRequest{}, fmt.Errorf("OpenAI model %s has no documented forced-tool capability", model.ID)
		}
		responsesReq.ToolChoice = map[string]any{"type": "function", "name": req.ToolChoice}
	}

	return responsesReq, nil
}

func convertMessagesToResponsesInput(messages []Message) any {
	return convertMessagesToResponsesInputForModel(Model{}, messages)
}

func convertMessagesToResponsesInputForModel(model Model, messages []Message) any {
	// If just a single user message with text, we can use simple string input.
	// As soon as there is any multi-turn context (assistant messages, tool calls,
	// tool results, images), we must use the structured array form.
	if len(messages) == 1 {
		if um, ok := messages[0].(UserMessage); ok && len(um.Content) == 1 {
			if tc, ok := um.Content[0].(TextContent); ok {
				return tc.Text
			}
		}
	}

	// Responses API supports a mixed list of message objects and tool call/output
	// items (e.g., function_call, function_call_output).
	items := make([]any, 0, len(messages))

	appendMessage := func(role, content string) {
		if strings.TrimSpace(content) == "" {
			return
		}
		items = append(items, map[string]any{
			"role":    role,
			"content": content,
		})
	}

	appendUser := func(m UserMessage) {
		// Prefer simple string content for pure-text user messages.
		if len(m.Content) == 1 {
			if tc, ok := m.Content[0].(TextContent); ok {
				items = append(items, map[string]any{
					"role":    "user",
					"content": tc.Text,
				})
				return
			}
		}

		// Otherwise, use typed content parts.
		var parts []responsesContentPart
		for _, block := range m.Content {
			switch b := block.(type) {
			case TextContent:
				parts = append(parts, responsesContentPart{Type: "input_text", Text: b.Text})
			case ImageContent:
				parts = append(parts, responsesContentPart{Type: "input_image", ImageURL: "data:" + b.MimeType + ";base64," + b.Data})
			}
		}
		items = append(items, map[string]any{
			"role":    "user",
			"content": parts,
		})
	}

	// Same constraint as the chat path: function_call_output is text-only and
	// the outputs of one assistant turn must stay adjacent, so tool-result
	// images ride one synthetic user item after the run of outputs.
	var pendingToolImages []responsesContentPart
	flushToolImages := func() {
		if len(pendingToolImages) == 0 {
			return
		}
		items = append(items, map[string]any{
			"role":    "user",
			"content": pendingToolImages,
		})
		pendingToolImages = nil
	}

	for _, msg := range messages {
		switch m := msg.(type) {
		case UserMessage:
			flushToolImages()
			appendUser(m)

		case AssistantMessage:
			flushToolImages()
			// Responses API models return function calls as separate output items.
			// We recreate that structure from our internal message representation by
			// emitting:
			// - a normal assistant message item for any text/thinking
			// - one function_call item per ToolCall block
			var textParts []string
			flushText := func() {
				if len(textParts) == 0 {
					return
				}
				appendMessage("assistant", strings.Join(textParts, "\n"))
				textParts = nil
			}

			for _, block := range m.Content {
				switch b := block.(type) {
				case TextContent:
					if b.Message != nil && b.Message.API == APIOpenAIResponses && b.Message.Model == model.ID && b.Message.Provider == model.Provider && b.Message.BaseURL == model.BaseURL {
						flushText()
						item := map[string]any{"role": "assistant", "content": b.Text}
						if b.Message.Phase != "" {
							item["phase"] = b.Message.Phase
						}
						items = append(items, item)
						continue
					}
					if strings.TrimSpace(b.Text) != "" {
						textParts = append(textParts, b.Text)
					}
				case ThinkingContent:
					// Preserve thinking in the conversation as plain text. This keeps the
					// history coherent across APIs.
					if strings.TrimSpace(b.Thinking) != "" {
						textParts = append(textParts, b.Thinking)
					}
				case OpaqueContent:
					if b.API == APIOpenAIResponses && b.Model == model.ID && b.Provider == model.Provider && b.BaseURL == model.BaseURL {
						var item responsesItem
						if json.Unmarshal(b.Data, &item) == nil && item.Type == "reasoning" && item.EncryptedContent != "" {
							flushText()
							items = append(items, json.RawMessage(b.Data))
						}
					}
				case ToolCall:
					flushText()
					argsJSON, _ := json.Marshal(b.Arguments)
					items = append(items, map[string]any{
						"type":      "function_call",
						"call_id":   b.ID,
						"name":      b.Name,
						"arguments": string(argsJSON),
					})
				}
			}
			flushText()

		case ToolResultMessage:
			// Tool outputs must be passed back in subsequent requests.
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  extractTextFromContent(m.Content),
			})
			for i, img := range imageBlocks(m.Content) {
				if i == 0 {
					pendingToolImages = append(pendingToolImages, responsesContentPart{
						Type: "input_text",
						Text: "Image output of tool call " + m.ToolCallID + ":",
					})
				}
				pendingToolImages = append(pendingToolImages, responsesContentPart{
					Type:     "input_image",
					ImageURL: "data:" + img.MimeType + ";base64," + img.Data,
				})
			}
		}
	}
	flushToolImages()

	return items
}

func processResponsesStream(ctx context.Context, body io.ReadCloser, model Model, events chan<- StreamEvent, done chan<- AssistantMessage, errCh chan<- error) {
	defer body.Close()
	defer close(events)

	reader := bufio.NewReader(body)

	var partial AssistantMessage
	partial.Role = "assistant"
	partial.Model = model.ID
	partial.API = model.API
	partial.Provider = model.Provider
	partial.Timestamp = time.Now()

	fail := func(err error) {
		partial.StopReason = StopReasonError
		partial.ErrorMessage = err.Error()
		events <- ErrorEvent{Reason: StopReasonError, Message: partial}
		errCh <- err
	}
	finish := func() {
		partial.Usage.Cost = calculateCost(partial.Usage, model.Cost)
		events <- DoneEvent{Reason: partial.StopReason, Message: partial}
		done <- partial
	}
	hasStarted := false
	reasoningIndices := make(map[string]int)
	textIndices := make(map[string]int)
	ensureText := func(id string) int {
		if i, ok := textIndices[id]; ok {
			return i
		}
		i := len(partial.Content)
		partial.Content = append(partial.Content, TextContent{Type: "text"})
		textIndices[id] = i
		return i
	}

	// Track tool calls being built
	type toolCallBuilder struct {
		id         string
		name       string
		arguments  string
		contentIdx int
	}
	toolCalls := make(map[string]*toolCallBuilder) // keyed by item ID

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
		if err == io.EOF && len(line) == 0 {
			fail(fmt.Errorf("OpenAI Responses stream ended before a terminal event"))
			return
		}
		if err != nil && err != io.EOF {
			partial.StopReason = StopReasonError
			partial.ErrorMessage = err.Error()
			events <- ErrorEvent{Reason: StopReasonError, Message: partial}
			errCh <- err
			return
		}

		line = strings.TrimSpace(line)

		// Skip empty lines and event type lines
		if line == "" || strings.HasPrefix(line, "event:") {
			continue
		}

		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		var event responsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			fail(fmt.Errorf("decode OpenAI Responses event: %w", err))
			return
		}
		if event.Type == "" {
			fail(fmt.Errorf("decode OpenAI Responses event: missing type"))
			return
		}

		switch event.Type {
		case "response.created", "response.in_progress":
			if !hasStarted {
				hasStarted = true
				events <- StartEvent{Partial: partial}
			}

		case "response.output_text.delta", "response.refusal.delta":
			i := ensureText(event.ItemID)
			block := partial.Content[i].(TextContent)
			block.Text += event.Delta
			partial.Content[i] = block
			events <- TextDeltaEvent{ContentIndex: i, Delta: event.Delta, Partial: partial}
		case "response.refusal.done":
			i := ensureText(event.ItemID)
			block := partial.Content[i].(TextContent)
			previous := block.Text
			block.Text = event.Refusal
			partial.Content[i] = block
			if strings.HasPrefix(block.Text, previous) && len(block.Text) > len(previous) {
				events <- TextDeltaEvent{ContentIndex: i, Delta: block.Text[len(previous):], Partial: partial}
			}
		case "response.output_text.done":
			// The message's completed output item supplies its final text and phase.

		case "response.function_call_arguments.delta":
			// Function call arguments stream separately and reference the tool item by item_id.
			if event.ItemID != "" {
				builder := toolCalls[event.ItemID]
				if builder == nil {
					// Be defensive: in practice response.output_item.added should arrive first.
					builder = &toolCallBuilder{contentIdx: -1}
					toolCalls[event.ItemID] = builder
				}
				builder.arguments += event.Delta
				if builder.contentIdx >= 0 {
					events <- ToolCallDeltaEvent{ContentIndex: builder.contentIdx, Delta: event.Delta, Partial: partial}
				}
			}

		case "response.function_call_arguments.done":
			// Some streams provide the full arguments in one shot.
			if event.ItemID != "" && event.Arguments != "" {
				builder := toolCalls[event.ItemID]
				if builder == nil {
					builder = &toolCallBuilder{contentIdx: -1}
					toolCalls[event.ItemID] = builder
				}
				builder.arguments = event.Arguments
			}

		case "response.output_item.added":
			if event.Item != nil && event.Item.Type == "message" {
				i := ensureText(event.Item.ID)
				partial.Content[i] = responsesMessageText(model, *event.Item)
			}
			if event.Item != nil && event.Item.Type == "reasoning" {
				reasoningIndices[event.Item.ID] = len(partial.Content)
				partial.Content = append(partial.Content, OpaqueContent{Type: "opaque", API: APIOpenAIResponses, Provider: model.Provider, Model: model.ID, BaseURL: model.BaseURL})
			}
			if event.Item != nil && event.Item.Type == "function_call" {
				contentIdx := len(partial.Content)
				builder := &toolCallBuilder{
					id:         event.Item.CallID,
					name:       event.Item.Name,
					contentIdx: contentIdx,
				}
				toolCalls[event.Item.ID] = builder
				partial.Content = append(partial.Content, ToolCall{
					Type: "toolCall",
					ID:   event.Item.CallID,
					Name: event.Item.Name,
				})
				events <- ToolCallDeltaEvent{ContentIndex: contentIdx, Delta: "", Partial: partial}
			}

		case "response.output_item.done":
			if event.Item != nil && event.Item.Type == "message" {
				i := ensureText(event.Item.ID)
				partial.Content[i] = responsesMessageText(model, *event.Item)
			}
			if event.Item != nil && event.Item.Type == "reasoning" {
				var raw struct {
					Item json.RawMessage `json:"item"`
				}
				json.Unmarshal([]byte(data), &raw)
				block := OpaqueContent{Type: "opaque", API: APIOpenAIResponses, Provider: model.Provider, Model: model.ID, BaseURL: model.BaseURL, Data: raw.Item}
				if i, ok := reasoningIndices[event.Item.ID]; ok {
					partial.Content[i] = block
				} else {
					reasoningIndices[event.Item.ID] = len(partial.Content)
					partial.Content = append(partial.Content, block)
				}
			}
			if event.Item != nil && event.Item.Type == "function_call" {
				builder := toolCalls[event.Item.ID]
				if builder != nil {
					var args map[string]any
					if event.Item.Arguments != "" {
						builder.arguments = event.Item.Arguments
					}
					// An exhausted output budget may end a call in the middle of
					// its JSON. Keep it incomplete until the terminal status is known.
					if err := json.Unmarshal([]byte(builder.arguments), &args); err != nil {
						args = nil
					}
					if builder.contentIdx >= 0 && builder.contentIdx < len(partial.Content) {
						if toolCall, ok := partial.Content[builder.contentIdx].(ToolCall); ok {
							toolCall.Arguments = args
							partial.Content[builder.contentIdx] = toolCall
							events <- ToolCallEndEvent{ContentIndex: builder.contentIdx, ToolCall: toolCall, Partial: partial}
						}
					}
				}
			}

		case "error":
			fail(fmt.Errorf("OpenAI Responses error %s: %s", event.Code, event.Message))
			return
		case "response.failed", "response.completed", "response.incomplete":
			response := event.Response
			if response == nil {
				fail(fmt.Errorf("OpenAI Responses terminal event %s has no response", event.Type))
				return
			}
			if response.Usage != nil {
				partial.Usage.Input = response.Usage.InputTokens
				partial.Usage.Output = response.Usage.OutputTokens
				partial.Usage.Total = response.Usage.TotalTokens
				if response.Usage.InputTokensDetails != nil {
					partial.Usage.CacheRead = response.Usage.InputTokensDetails.CachedTokens
					partial.Usage.Input -= partial.Usage.CacheRead
				}
			}
			if event.Type == "response.failed" || response.Error != nil {
				detail := "response failed"
				if response.Error != nil {
					detail = response.Error.Code + ": " + response.Error.Message
				}
				fail(fmt.Errorf("OpenAI Responses %s", detail))
				return
			}
			if event.Type == "response.incomplete" {
				if response.Status != "incomplete" {
					fail(fmt.Errorf("OpenAI Responses inconsistent terminal status %q", response.Status))
					return
				}
				reason := "unknown"
				if response.IncompleteDetails != nil {
					reason = response.IncompleteDetails.Reason
				}
				if reason != "max_output_tokens" {
					fail(fmt.Errorf("OpenAI Responses incomplete: %s", reason))
					return
				}
				partial.StopReason = StopReasonMaxTokens
			} else {
				if response.Status != "completed" {
					fail(fmt.Errorf("OpenAI Responses inconsistent terminal status %q", response.Status))
					return
				}
				partial.StopReason = StopReasonEnd
				if len(toolCalls) > 0 {
					partial.StopReason = StopReasonToolUse
				}
			}
			// The terminal output is authoritative, including reasoning item fields
			// unavailable in deltas. Keep original JSON for opaque provider items.
			if response.Output != nil {
				var raw struct {
					Response struct {
						Output []json.RawMessage `json:"output"`
					} `json:"response"`
				}
				json.Unmarshal([]byte(data), &raw)
				content := make([]ContentBlock, 0, len(response.Output))
				for i, item := range response.Output {
					switch item.Type {
					case "reasoning":
						content = append(content, OpaqueContent{Type: "opaque", API: APIOpenAIResponses, Provider: model.Provider, Model: model.ID, BaseURL: model.BaseURL, Data: raw.Response.Output[i]})
					case "message":
						content = append(content, responsesMessageText(model, item))
					case "function_call":
						var args map[string]any
						err := json.Unmarshal([]byte(item.Arguments), &args)
						if err != nil || args == nil {
							if partial.StopReason != StopReasonMaxTokens {
								fail(fmt.Errorf("invalid OpenAI function arguments for %s", item.Name))
								return
							}
							args = nil
						}
						content = append(content, ToolCall{Type: "toolCall", ID: item.CallID, Name: item.Name, Arguments: args})
						if partial.StopReason == StopReasonEnd {
							partial.StopReason = StopReasonToolUse
						}
					}
				}
				partial.Content = content
			}
			if partial.StopReason != StopReasonMaxTokens {
				for _, block := range partial.Content {
					if call, ok := block.(ToolCall); ok && call.Arguments == nil {
						fail(fmt.Errorf("invalid OpenAI function arguments for %s", call.Name))
						return
					}
				}
			}
			finish()
			return
		}
	}
}

// One TextContent is one Responses assistant message, including its phase.
// Refusals are visible explanations, not empty successful answers.
func responsesMessageText(model Model, item responsesItem) TextContent {
	var texts []string
	for _, part := range item.Content {
		switch part.Type {
		case "output_text":
			texts = append(texts, part.Text)
		case "refusal":
			texts = append(texts, part.Refusal)
		}
	}
	return TextContent{Type: "text", Text: strings.Join(texts, "\n"), Message: &MessageMetadata{API: APIOpenAIResponses, Provider: model.Provider, Model: model.ID, BaseURL: model.BaseURL, Phase: item.Phase}}
}
