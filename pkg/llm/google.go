package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const googleBaseURL = "https://generativelanguage.googleapis.com"

type googleContent struct {
	Role  string `json:"role,omitempty"`
	Parts []any  `json:"parts"`
}

type googleBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type googlePart struct {
	Text             *string                 `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	InlineData       *googleBlob             `json:"inlineData,omitempty"`
	FunctionCall     *googleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *googleFunctionResponse `json:"functionResponse,omitempty"`
}

type googleFunctionCall struct {
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name"`
	Args         map[string]any  `json:"args"`
	PartialArgs  json.RawMessage `json:"partialArgs,omitempty"`
	WillContinue bool            `json:"willContinue,omitempty"`
}

type googleFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	Parts    []googlePart   `json:"parts,omitempty"`
}

type googleTool struct {
	FunctionDeclarations []googleFunctionDeclaration `json:"functionDeclarations"`
}

type googleFunctionDeclaration struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Parameters  *Schema `json:"parametersJsonSchema"`
}

type googleToolConfig struct {
	FunctionCallingConfig googleFunctionCallingConfig `json:"functionCallingConfig"`
}

type googleFunctionCallingConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type googleGenerationConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	ThinkingConfig  *googleThinkingConfig `json:"thinkingConfig,omitempty"`
	ResponseFormat  any                   `json:"responseFormat,omitempty"`
}

type googleRequest struct {
	Contents          []googleContent        `json:"contents"`
	SystemInstruction *googleContent         `json:"systemInstruction,omitempty"`
	Tools             []googleTool           `json:"tools,omitempty"`
	ToolConfig        *googleToolConfig      `json:"toolConfig,omitempty"`
	GenerationConfig  googleGenerationConfig `json:"generationConfig"`
}

// googleReplay preserves complete provider parts, including signatures on empty
// text and function calls. Its visible text and tools are represented separately
// in AssistantMessage for other adapters and the UI. A matching replay block
// replaces that whole assistant turn, so parts are never duplicated or reordered.
type googleReplay struct {
	Kind  string            `json:"kind"`
	Parts []json.RawMessage `json:"parts"`
}

func googleModelURL(model Model) string {
	base := model.BaseURL
	if base == "" {
		base = googleBaseURL
	}
	return strings.TrimRight(base, "/") + "/v1beta/models/" + url.PathEscape(model.ID)
}

func googleHTTPClient(timeout time.Duration) *http.Client {
	client := newHTTPClient(timeout)
	// net/http protects Authorization on redirects, but not x-goog-api-key.
	// Neither Models nor GenerateContent needs to redirect; never send the key
	// to another location, even when the configured endpoint asks us to.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

func googleModelLimits(ctx context.Context, model Model) (Limits, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleModelURL(model), nil)
	if err != nil {
		return Limits{}, fmt.Errorf("google model limits: create request: %w", err)
	}
	req.Header.Set("x-goog-api-key", model.APIKey)
	resp, err := googleHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return Limits{}, fmt.Errorf("google model limits: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Limits{}, fmt.Errorf("google model limits: %s: %w", model.ID, ErrModelNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return Limits{}, fmt.Errorf("google model limits: API error (status %d)", resp.StatusCode)
	}
	var metadata struct {
		Name                       string   `json:"name"`
		InputTokenLimit            int      `json:"inputTokenLimit"`
		OutputTokenLimit           int      `json:"outputTokenLimit"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&metadata); err != nil {
		return Limits{}, fmt.Errorf("google model limits: invalid model metadata")
	}
	if metadata.Name != "models/"+model.ID || metadata.SupportedGenerationMethods == nil {
		return Limits{}, fmt.Errorf("google model limits: invalid model metadata")
	}
	if !slices.Contains(metadata.SupportedGenerationMethods, "generateContent") {
		return Limits{}, fmt.Errorf("google model limits: %s: %w", model.ID, ErrModelNotFound)
	}
	if metadata.InputTokenLimit <= 0 || metadata.OutputTokenLimit <= 0 {
		return Limits{}, fmt.Errorf("google model limits: %s publishes no valid token limits", model.ID)
	}
	return Limits{ContextWindow: metadata.InputTokenLimit, MaxTokens: metadata.OutputTokenLimit}, nil
}

func convertToGoogleRequest(model Model, req Request, opts StreamOptions) (googleRequest, error) {
	var out googleRequest
	limit, err := opts.outputLimit(model)
	if err != nil {
		return out, err
	}
	if opts.MaxTokens != nil && limit <= 0 {
		return out, fmt.Errorf("google maxOutputTokens must be positive")
	}
	// Argon budgets above one streaming hop opt into a different protocol:
	// CONTINUATION plus a continuation token. Until we support that protocol,
	// refuse it before generation rather than fail after consuming the budget.
	// This boundary is not a replacement for the Models API's default limit.
	// https://ai.google.dev/gemini-api/docs/generate-content/long-decode
	if model.ID == "gemini-4-argon" && limit > 65536 {
		return out, fmt.Errorf("google gemini-4-argon output caps above 65536 require unsupported continuation")
	}
	thinking, err := googleThinking(model.ID, opts.ThinkingLevel)
	if err != nil {
		return out, err
	}
	out.GenerationConfig = googleGenerationConfig{MaxOutputTokens: limit, Temperature: opts.Temperature, ThinkingConfig: thinking}
	for _, system := range req.System {
		if out.SystemInstruction == nil {
			out.SystemInstruction = &googleContent{}
		}
		out.SystemInstruction.Parts = append(out.SystemInstruction.Parts, googlePart{Text: new(system.Text)})
	}
	if req.OutputSchema != nil {
		if googleBudgetModel(model.ID) && len(req.Tools) > 0 {
			return out, fmt.Errorf("google model %s cannot combine tools and structured output", model.ID)
		}
		schema, err := structuredRequestSchema(model, req.OutputSchema)
		if err != nil {
			return out, err
		}
		// The REST discovery schema calls this an enum, despite examples in
		// the guide showing a MIME string: TextResponseFormat.MimeType.
		out.GenerationConfig.ResponseFormat = map[string]any{"text": map[string]any{"mimeType": "APPLICATION_JSON", "schema": schema}}
	}
	if len(req.Tools) > 0 {
		var declarations []googleFunctionDeclaration
		for _, tool := range req.Tools {
			declarations = append(declarations, googleFunctionDeclaration{Name: tool.Name, Description: tool.Description, Parameters: GenerateSchema(tool.Parameters)})
		}
		out.Tools = []googleTool{{FunctionDeclarations: declarations}}
	}
	if req.ToolChoice != "" {
		if !model.SupportsToolChoice() {
			return out, fmt.Errorf("google model %s has no documented forced-tool capability", model.ID)
		}
		found := false
		for _, tool := range req.Tools {
			if tool.Name == req.ToolChoice {
				found = true
			}
		}
		if !found {
			return out, fmt.Errorf("google forced tool %q is not declared", req.ToolChoice)
		}
		out.ToolConfig = &googleToolConfig{FunctionCallingConfig: googleFunctionCallingConfig{Mode: "ANY", AllowedFunctionNames: []string{req.ToolChoice}}}
	}
	// Google's function ids are optional. Keep native ids distinct from the
	// client-side ids assigned to id-less calls. Calls belong to one assistant
	// turn; an old turn's same-named calls must never bind a later result batch.
	var calls map[string]googleFunctionCall
	var callOrder []string
	for i := 0; i < len(req.Messages); i++ {
		switch msg := req.Messages[i].(type) {
		case UserMessage:
			parts := googleVisibleParts(msg.Content)
			if len(parts) > 0 {
				out.Contents = append(out.Contents, googleContent{Role: "user", Parts: parts})
				calls, callOrder = nil, nil
			}
		case AssistantMessage:
			calls = make(map[string]googleFunctionCall)
			callOrder = nil
			parts, err := googleAssistantParts(model, msg, calls)
			if err != nil {
				return out, err
			}
			if len(parts) > 0 {
				out.Contents = append(out.Contents, googleContent{Role: "model", Parts: parts})
			}
			for _, block := range msg.Content {
				if call, ok := block.(ToolCall); ok {
					callOrder = append(callOrder, call.ID)
				}
			}
		case ToolResultMessage:
			batch := []ToolResultMessage{msg}
			for i+1 < len(req.Messages) {
				next, ok := req.Messages[i+1].(ToolResultMessage)
				if !ok {
					break
				}
				i++
				batch = append(batch, next)
			}
			batch, err := googleOrderToolResults(batch, calls, callOrder)
			if err != nil {
				return out, err
			}
			var parts []any
			var images []any
			for _, msg := range batch {
				call := calls[msg.ToolCallID]
				response := googleFunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{}}
				var texts []string
				for _, block := range msg.Content {
					switch block := block.(type) {
					case TextContent:
						texts = append(texts, block.Text)
					case ImageContent:
						part := googlePart{InlineData: &googleBlob{MimeType: block.MimeType, Data: block.Data}}
						if googleBudgetModel(model.ID) {
							images = append(images, googlePart{Text: new(fmt.Sprintf("Image from tool %s (call %s):", call.Name, msg.ToolCallID))}, part)
						} else {
							response.Parts = append(response.Parts, part)
						}
					}
				}
				key := "output"
				if msg.IsError {
					key = "error"
				}
				response.Response[key] = strings.Join(texts, "\n")
				parts = append(parts, googlePart{FunctionResponse: &response})
			}
			// Gemini 2.5 predates multimodal functionResponse.parts. Its image
			// inputs follow the complete run of function results in the same turn.
			parts = append(parts, images...)
			out.Contents = append(out.Contents, googleContent{Role: "user", Parts: parts})
			calls, callOrder = nil, nil
		}
	}
	return out, nil
}

// Id-less function responses are positional, even when parallel calls share a
// name. A complete batch can be put back in the original call order using the
// client ids. A partial, duplicate, or unknown batch cannot be expressed safely.
// Native-id-only batches retain their arrival order and match by their ids.
func googleOrderToolResults(batch []ToolResultMessage, calls map[string]googleFunctionCall, callOrder []string) ([]ToolResultMessage, error) {
	byID := make(map[string]ToolResultMessage, len(batch))
	for _, result := range batch {
		call, ok := calls[result.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("google tool result %q does not match the preceding assistant turn", result.ToolCallID)
		}
		if _, duplicate := byID[result.ToolCallID]; duplicate {
			return nil, fmt.Errorf("google tool result batch duplicates call %q", result.ToolCallID)
		}
		if call.Name == "" || result.ToolName != "" && result.ToolName != call.Name {
			return nil, fmt.Errorf("google tool result %q does not match its function name", result.ToolCallID)
		}
		byID[result.ToolCallID] = result
	}
	positional := false
	for _, id := range callOrder {
		if calls[id].ID == "" {
			positional = true
			break
		}
	}
	if !positional {
		return batch, nil
	}
	if len(batch) != len(callOrder) {
		return nil, fmt.Errorf("google positional tool results require the complete assistant call batch")
	}
	ordered := make([]ToolResultMessage, 0, len(batch))
	for _, id := range callOrder {
		ordered = append(ordered, byID[id])
	}
	return ordered, nil
}

func googleVisibleParts(content []ContentBlock) []any {
	var parts []any
	for _, block := range content {
		switch block := block.(type) {
		case TextContent:
			parts = append(parts, googlePart{Text: new(block.Text)})
		case ImageContent:
			parts = append(parts, googlePart{InlineData: &googleBlob{MimeType: block.MimeType, Data: block.Data}})
		}
	}
	return parts
}

func googleAssistantParts(model Model, msg AssistantMessage, calls map[string]googleFunctionCall) ([]any, error) {
	var visibleCalls []ToolCall
	visibleIDs := make(map[string]bool)
	var replay *googleReplay
	for _, block := range msg.Content {
		switch block := block.(type) {
		case ToolCall:
			if block.ID == "" || visibleIDs[block.ID] {
				return nil, fmt.Errorf("google assistant history requires distinct nonempty tool-call ids")
			}
			visibleIDs[block.ID] = true
			visibleCalls = append(visibleCalls, block)
		case OpaqueContent:
			if block.API != APIGoogleGenerateContent || block.Provider != model.Provider || block.BaseURL != model.BaseURL || block.Model != model.ID {
				continue
			}
			if replay != nil {
				return nil, fmt.Errorf("google assistant history has duplicate replay state")
			}
			replay = new(googleReplay)
			if err := json.Unmarshal(block.Data, replay); err != nil || replay.Kind != "google-content" || len(replay.Parts) == 0 {
				return nil, fmt.Errorf("google assistant history has invalid replay state")
			}
		}
	}
	var parts []any
	if replay != nil {
		callIndex := 0
		for _, raw := range replay.Parts {
			var part googlePart
			if err := json.Unmarshal(raw, &part); err != nil {
				return nil, fmt.Errorf("google replay part: %w", err)
			}
			if part.FunctionCall != nil {
				if callIndex >= len(visibleCalls) {
					return nil, fmt.Errorf("google replay tools do not match visible history")
				}
				visible := visibleCalls[callIndex]
				// Replaying an edited tool silently would make the model see
				// different arguments from those the caller actually executed.
				visibleArguments := visible.Arguments
				if visibleArguments == nil {
					visibleArguments = map[string]any{}
				}
				rawArguments := part.FunctionCall.Args
				if rawArguments == nil {
					rawArguments = map[string]any{}
				}
				visibleArgs, err := json.Marshal(visibleArguments)
				rawArgs, rawErr := json.Marshal(rawArguments)
				if visible.Name != part.FunctionCall.Name || err != nil || rawErr != nil || !bytes.Equal(visibleArgs, rawArgs) {
					return nil, fmt.Errorf("google replay tools do not match visible history")
				}
				calls[visible.ID] = *part.FunctionCall
				callIndex++
			}
			parts = append(parts, raw)
		}
		if callIndex != len(visibleCalls) {
			return nil, fmt.Errorf("google replay tools do not match visible history")
		}
		return parts, nil
	}
	for _, block := range msg.Content {
		switch block := block.(type) {
		case TextContent:
			parts = append(parts, googlePart{Text: new(block.Text)})
		case ToolCall:
			args := block.Arguments
			if args == nil {
				args = map[string]any{}
			}
			call := googleFunctionCall{ID: block.ID, Name: block.Name, Args: args}
			part := googlePart{FunctionCall: &call}
			// Google's documented escape hatch for transferring another model's
			// tool history. Never substitute this for matching signed state.
			if !googleBudgetModel(model.ID) {
				part.ThoughtSignature = "skip_thought_signature_validator"
			}
			parts = append(parts, part)
			calls[block.ID] = call
		}
	}
	return parts, nil
}

func streamGoogle(ctx context.Context, model Model, req Request, opts StreamOptions) (*StreamHandle, error) {
	if model.APIKey == "" {
		return nil, &retryableError{err: fmt.Errorf("google API key is required")}
	}
	converted, err := convertToGoogleRequest(model, req, opts)
	if err != nil {
		return nil, &retryableError{err: err}
	}
	body, err := json.Marshal(converted)
	if err != nil {
		return nil, &retryableError{err: fmt.Errorf("marshal Google request: %w", err)}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, googleModelURL(model)+":streamGenerateContent?alt=sse", bytes.NewReader(body))
	if err != nil {
		return nil, &retryableError{err: fmt.Errorf("create Google request: %w", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-goog-api-key", model.APIKey)
	httpReq.Header.Set("User-Agent", opts.UserAgent)
	if opts.SessionID != "" {
		httpReq.Header.Set("session_id", opts.SessionID)
	}
	resp, err := googleHTTPClient(0).Do(httpReq)
	if err != nil {
		return nil, &retryableError{err: fmt.Errorf("send Google request: %w", err), retryable: true}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, &retryableError{err: fmt.Errorf("google API error (status %d): %s", resp.StatusCode, body), retryable: isRetryable(resp.StatusCode), statusCode: resp.StatusCode}
	}
	events := make(chan StreamEvent, 100)
	done := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)
	go processGoogleStream(ctx, resp.Body, model, events, done, errCh)
	return newStreamHandle(events, done, errCh), nil
}

type googleStreamResponse struct {
	Candidates []struct {
		Index   int `json:"index"`
		Content struct {
			Role  string            `json:"role"`
			Parts []json.RawMessage `json:"parts"`
		} `json:"content"`
		FinishReason  string `json:"finishReason"`
		FinishMessage string `json:"finishMessage"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		ToolUsePromptTokenCount int `json:"toolUsePromptTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	PromptFeedback *struct {
		BlockReason        string `json:"blockReason"`
		BlockReasonMessage string `json:"blockReasonMessage"`
	} `json:"promptFeedback"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func processGoogleStream(ctx context.Context, body io.ReadCloser, model Model, events chan<- StreamEvent, done chan<- AssistantMessage, errCh chan<- error) {
	defer body.Close()
	defer close(events)
	partial := AssistantMessage{Role: "assistant", Model: model.ID, API: model.API, Provider: model.Provider, Timestamp: time.Now()}
	var replayParts []json.RawMessage
	terminal := false
	toolCount := 0
	toolIDs := make(map[string]bool)
	// Partial messages are snapshots: consumers can keep an event while the
	// stream advances, so never append into a previous event's backing array.
	snapshot := func() AssistantMessage {
		msg := partial
		msg.Content = append([]ContentBlock(nil), partial.Content...)
		msg.Usage.Cost = calculateCost(msg.Usage, model.Cost)
		return msg
	}
	emit := func(event StreamEvent) bool {
		select {
		case events <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	replayFinished := false
	finishReplay := func() {
		if replayFinished || len(replayParts) == 0 {
			return
		}
		replayFinished = true
		data, _ := json.Marshal(googleReplay{Kind: "google-content", Parts: replayParts})
		partial.Content = append(partial.Content, OpaqueContent{Type: "opaque", API: model.API, Provider: model.Provider, Model: model.ID, BaseURL: model.BaseURL, Data: data})
	}
	fail := func(err error) {
		partial.StopReason = StopReasonError
		if ctx.Err() != nil {
			partial.StopReason = StopReasonAborted
			err = ctx.Err()
		}
		partial.ErrorMessage = err.Error()
		finishReplay()
		msg := snapshot()
		emit(ErrorEvent{Reason: msg.StopReason, Message: msg})
		errCh <- newStreamFailure(model, msg, err)
	}
	appendText := func(text string, thought bool) bool {
		if text == "" {
			return true
		}
		index := len(partial.Content) - 1
		if thought {
			if index >= 0 {
				if block, ok := partial.Content[index].(ThinkingContent); ok {
					block.Thinking += text
					partial.Content[index] = block
				} else {
					index = -1
				}
			}
			if index < 0 {
				partial.Content = append(partial.Content, ThinkingContent{Type: "thinking", Thinking: text})
				index = len(partial.Content) - 1
			}
			return emit(ThinkingDeltaEvent{ContentIndex: index, Delta: text, Partial: snapshot()})
		}
		if index >= 0 {
			if block, ok := partial.Content[index].(TextContent); ok {
				block.Text += text
				partial.Content[index] = block
			} else {
				index = -1
			}
		}
		if index < 0 {
			partial.Content = append(partial.Content, TextContent{Type: "text", Text: text})
			index = len(partial.Content) - 1
		}
		return emit(TextDeltaEvent{ContentIndex: index, Delta: text, Partial: snapshot()})
	}
	if !emit(StartEvent{Partial: snapshot()}) {
		fail(ctx.Err())
		return
	}
	consume := func(data string) error {
		var response googleStreamResponse
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			return fmt.Errorf("decode Google stream: %w", err)
		}
		if usage := response.UsageMetadata; usage != nil {
			partial.Usage = Usage{Input: max(0, usage.PromptTokenCount-usage.CachedContentTokenCount) + usage.ToolUsePromptTokenCount, Output: usage.CandidatesTokenCount + usage.ThoughtsTokenCount, CacheRead: usage.CachedContentTokenCount, Total: usage.TotalTokenCount}
		}
		if response.Error != nil {
			return fmt.Errorf("google stream error (%s, %d): %s", response.Error.Status, response.Error.Code, response.Error.Message)
		}
		if feedback := response.PromptFeedback; feedback != nil && feedback.BlockReason != "" && feedback.BlockReason != "BLOCK_REASON_UNSPECIFIED" {
			terminal = true
			partial.StopReason = StopReasonRefusal
			text := feedback.BlockReasonMessage
			if text == "" {
				text = "Google refused the prompt: " + feedback.BlockReason
			}
			if !appendText(text, false) {
				return ctx.Err()
			}
		}
		if len(response.Candidates) > 1 {
			return fmt.Errorf("google returned multiple candidates for a single-candidate request")
		}
		for _, candidate := range response.Candidates {
			if candidate.Index != 0 {
				return fmt.Errorf("google returned unexpected candidate index %d", candidate.Index)
			}
			if candidate.Content.Role != "" && candidate.Content.Role != "model" {
				return fmt.Errorf("google returned unexpected candidate role %q", candidate.Content.Role)
			}
			if terminal && (len(candidate.Content.Parts) > 0 || candidate.FinishReason != "") {
				return fmt.Errorf("google returned candidate content after its terminal event")
			}
			for _, raw := range candidate.Content.Parts {
				var part googlePart
				if err := json.Unmarshal(raw, &part); err != nil {
					return fmt.Errorf("decode Google part: %w", err)
				}
				replayParts = append(replayParts, raw)
				if part.Text != nil {
					if !appendText(*part.Text, part.Thought) {
						return ctx.Err()
					}
				}
				if part.FunctionCall != nil {
					call := part.FunctionCall
					if call.WillContinue || len(call.PartialArgs) > 0 {
						return fmt.Errorf("google returned unexpected partial function-call arguments")
					}
					if call.Name == "" {
						return fmt.Errorf("google function call has no name")
					}
					id := call.ID
					if id == "" {
						id = "google_" + rand.Text()
					}
					if toolIDs[id] {
						return fmt.Errorf("google returned duplicate function-call id %q", id)
					}
					toolIDs[id] = true
					args := call.Args
					if args == nil {
						args = map[string]any{}
					}
					tool := ToolCall{Type: "toolCall", ID: id, Name: call.Name, Arguments: args}
					partial.Content = append(partial.Content, tool)
					toolCount++
					if !emit(ToolCallEndEvent{ContentIndex: len(partial.Content) - 1, ToolCall: tool, Partial: snapshot()}) {
						return ctx.Err()
					}
				}
				if part.InlineData != nil {
					return fmt.Errorf("google returned unsupported generated media")
				}
			}
			if candidate.FinishReason == "" {
				continue
			}
			terminal = true
			switch candidate.FinishReason {
			case "STOP":
				partial.StopReason = StopReasonEnd
				if toolCount > 0 {
					partial.StopReason = StopReasonToolUse
				}
			case "MAX_TOKENS":
				partial.StopReason = StopReasonMaxTokens
			case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "MODEL_ARMOR", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
				partial.StopReason = StopReasonRefusal
				text := candidate.FinishMessage
				if text == "" {
					text = "Google refused the response: " + candidate.FinishReason
				}
				if !appendText(text, false) {
					return ctx.Err()
				}
			default:
				return fmt.Errorf("google finish reason %s: %s", candidate.FinishReason, candidate.FinishMessage)
			}
		}
		return nil
	}
	// SSE data may span lines, and the last event may end at EOF without a
	// trailing blank line. Parse complete events, never mistake EOF for success.
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		return consume(payload)
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			fail(ctx.Err())
			return
		}
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				fail(err)
				return
			}
			continue
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(after, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		fail(fmt.Errorf("read Google stream: %w", err))
		return
	}
	if err := flush(); err != nil {
		fail(err)
		return
	}
	if ctx.Err() != nil {
		fail(ctx.Err())
		return
	}
	if !terminal {
		fail(fmt.Errorf("google stream ended before a terminal response"))
		return
	}
	finishReplay()
	msg := snapshot()
	if !emit(DoneEvent{Reason: msg.StopReason, Message: msg}) {
		fail(ctx.Err())
		return
	}
	done <- msg
}
