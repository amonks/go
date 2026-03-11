// Package llm provides the core LLM abstraction for streaming completions.
//
// This package implements unified message types, streaming via channels,
// tool schema generation, and provider implementations for Anthropic and OpenAI.
package llm

import "time"

// API represents the API style used by a provider.
type API string

const (
	// APIAnthropicMessages is the Anthropic Messages API.
	APIAnthropicMessages API = "anthropic-messages"
	// APIOpenAICompletions is the OpenAI Chat Completions API.
	APIOpenAICompletions API = "openai-completions"
	// APIOpenAIResponses is the OpenAI Responses API.
	APIOpenAIResponses API = "openai-responses"
)

// StopReason indicates why generation stopped.
type StopReason string

const (
	// StopReasonEnd indicates natural completion.
	StopReasonEnd StopReason = "end"
	// StopReasonToolUse indicates the model wants to call tools.
	StopReasonToolUse StopReason = "tool_use"
	// StopReasonMaxTokens indicates the output limit was reached.
	StopReasonMaxTokens StopReason = "max_tokens"
	// StopReasonError indicates an unrecoverable error occurred.
	StopReasonError StopReason = "error"
	// StopReasonAborted indicates generation was cancelled via context.
	StopReasonAborted StopReason = "aborted"
)

// Message is an interface implemented by all message types.
type Message interface {
	message()
}

// ContentBlock is an interface implemented by all content block types.
type ContentBlock interface {
	contentBlock()
}

// UserMessage represents user input to the model.
type UserMessage struct {
	Role      string         // "user"
	Content   []ContentBlock // Text and/or images
	Timestamp time.Time
}

func (UserMessage) message() {}

// AssistantMessage represents model output.
type AssistantMessage struct {
	Role         string         // "assistant"
	Content      []ContentBlock // Text, thinking, and/or tool calls
	Model        string         // Model ID used
	API          API            // Which API was used
	Provider     string         // Provider name
	Usage        Usage          // Token counts and costs
	StopReason   StopReason     // Why generation stopped
	ErrorMessage string         // Set when StopReason is "error"
	Timestamp    time.Time
}

func (AssistantMessage) message() {}

// ToolResultMessage represents tool execution results returned to the model.
type ToolResultMessage struct {
	Role       string         // "toolResult"
	ToolCallID string         // Matches the ToolCall.ID
	ToolName   string         // Tool that was called
	Content    []ContentBlock // Text and/or images
	IsError    bool           // Whether the tool execution failed
	Timestamp  time.Time
}

func (ToolResultMessage) message() {}

// TextContent represents text content in a message.
type TextContent struct {
	Type string // "text"
	Text string
}

func (TextContent) contentBlock() {}

// ThinkingContent represents thinking/reasoning content from the model.
type ThinkingContent struct {
	Type     string // "thinking"
	Thinking string
}

func (ThinkingContent) contentBlock() {}

// ImageContent represents an image in a message.
type ImageContent struct {
	Type     string // "image"
	Data     string // base64 encoded
	MimeType string
}

func (ImageContent) contentBlock() {}

// ToolCall represents a tool call from the model.
type ToolCall struct {
	Type      string         // "toolCall"
	ID        string         // Unique identifier
	Name      string         // Tool name
	Arguments map[string]any // Parsed arguments
}

func (ToolCall) contentBlock() {}

// Usage contains token counts and costs for a completion.
type Usage struct {
	Input      int // Input tokens
	Output     int // Output tokens
	CacheRead  int // Tokens read from cache
	CacheWrite int // Tokens written to cache
	Total      int // Total tokens
	Cost       UsageCost
}

// UsageCost contains the monetary cost breakdown.
type UsageCost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

// Cost contains pricing per million tokens.
type Cost struct {
	Input      float64 // $/million input tokens
	Output     float64 // $/million output tokens
	CacheRead  float64 // $/million cached input tokens
	CacheWrite float64 // $/million cache write tokens
}

// Model contains configuration for an LLM model.
// Note: APIKey is included here (not in the llm.md spec's Model type) because
// internal/llm needs the resolved key to make API calls. The public llm package
// handles key resolution from api-key-command before passing Model to Stream.
type Model struct {
	ID                     string   // e.g., "claude-sonnet-4-20250514"
	Name                   string   // Human-readable name
	API                    API      // Which API style to use
	Provider               string   // Provider name from config
	BaseURL                string   // API endpoint
	APIKey                 string   // API key (resolved from api-key-command by caller)
	ContextWindow          int      // Max context tokens
	MaxTokens              int      // Max output tokens
	Reasoning              bool     // Supports thinking mode
	UseMaxCompletionTokens bool     // Use max_completion_tokens instead of max_tokens (for o1/o3/o4 models)
	InputTypes             []string // "text", "image"
	Cost                   Cost     // Pricing per million tokens
}

// Request contains the input for a completion.
type Request struct {
	System   []SystemBlock
	Messages []Message // UserMessage, AssistantMessage, or ToolResultMessage
	Tools    []Tool
}

// SystemBlock is a system prompt segment with optional cache breakpoint metadata.
type SystemBlock struct {
	Text            string
	CacheBreakpoint bool
}

// Tool defines a tool that the model can call.
type Tool struct {
	Name        string
	Description string
	Parameters  any // Struct type for JSON Schema generation
}

// CacheRetention controls prompt caching behavior.
//
// Anthropic uses cache_control markers plus the prompt-caching-2024-07-31 beta
// header. OpenAI caching is automatic; we only parse cached token counts from
// response usage details.
type CacheRetention string

const (
	// CacheNone disables caching.
	CacheNone CacheRetention = "none"
	// CacheShort caches for ~5 minutes.
	CacheShort CacheRetention = "short"
	// CacheLong caches for ~1 hour.
	CacheLong CacheRetention = "long"
)

// ThinkingLevel controls the amount of reasoning the model performs.
type ThinkingLevel string

const (
	// ThinkingOff disables thinking.
	ThinkingOff ThinkingLevel = "off"
	// ThinkingMinimal enables minimal thinking.
	ThinkingMinimal ThinkingLevel = "minimal"
	// ThinkingLow enables low thinking.
	ThinkingLow ThinkingLevel = "low"
	// ThinkingMedium enables medium thinking.
	ThinkingMedium ThinkingLevel = "medium"
	// ThinkingHigh enables high thinking.
	ThinkingHigh ThinkingLevel = "high"
	// ThinkingXHigh enables extra-high thinking.
	ThinkingXHigh ThinkingLevel = "xhigh"
)

// StreamOptions contains options for streaming completions.
type StreamOptions struct {
	Temperature    *float64
	MaxTokens      *int
	CacheRetention CacheRetention
	ThinkingLevel  ThinkingLevel
	SessionID      string
	UserAgent      string
}
