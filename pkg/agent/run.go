package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"monks.co/pkg/llm"
)

type streamWithRetryFunc func(ctx context.Context, model llm.Model, req llm.Request, opts llm.StreamOptions, config llm.RetryConfig) (*llm.StreamHandle, error)

var streamWithRetry streamWithRetryFunc = llm.StreamWithRetry

func isRetryableStreamError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "unexpected eof")
}

// Run starts an agent run with the given prompt and configuration.
// It returns a RunHandle that provides access to events and the final result.
func Run(ctx context.Context, prompt PromptContent, config AgentConfig) (*RunHandle, error) {
	if config.CacheRetention == "" {
		config.CacheRetention = llm.CacheShort
	}
	workDir := config.WorkDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
	}

	// Create result channel
	result := make(chan RunResult, 1)

	handle := &RunHandle{
		result: result,
	}

	content, err := promptContextFromRepo(workDir, config.GlobalConfigDir, prompt.TestCommands)
	if err != nil {
		return nil, err
	}
	if len(prompt.ProjectContext) == 0 {
		prompt.ProjectContext = content.ProjectContext
	}
	if len(prompt.ContextFiles) == 0 {
		prompt.ContextFiles = content.ContextFiles
	}
	if len(prompt.TestCommands) == 0 {
		prompt.TestCommands = content.TestCommands
	}
	if prompt.PhaseContent == "" {
		prompt.PhaseContent = content.PhaseContent
	}

	// Start the agent loop in a goroutine
	go runAgent(ctx, prompt, config, workDir, result)

	return handle, nil
}

func runAgent(ctx context.Context, prompt PromptContent, config AgentConfig, workDir string, result chan<- RunResult) {
	defer close(result)

	// Initialize conversation with user message
	userContent := prompt.UserContent
	messages := []llm.Message{}
	if strings.TrimSpace(userContent) != "" {
		messages = append(messages, llm.UserMessage{
			Role: "user",
			Content: []llm.ContentBlock{
				llm.TextContent{Type: "text", Text: userContent},
			},
			Timestamp: time.Now(),
		})
	}

	// Create tool executor with config (enables task tool for spawning subagents)
	executor := &toolExecutor{
		workDir:     workDir,
		permissions: config.Permissions,
		env:         config.Env,
		config:      &config,
	}

	// Track aggregate usage
	var totalUsage llm.Usage

	turnIndex := 0
	for {
		// Check for context cancellation
		if ctx.Err() != nil {
			result <- RunResult{
				Messages: messages,
				Usage:    totalUsage,
				Error:    ctx.Err(),
			}
			return
		}

		// Build request (parent agents have the task tool)
		req := llm.Request{
			System:   BuildSystemBlocks(workDir, prompt),
			Messages: messages,
			Tools:    builtInToolsWithTask(true),
		}

		streamErrRetries := 0
		var assistantMsg llm.AssistantMessage
		for {
			streamHandle, err := streamWithRetry(ctx, config.Model, req, llm.StreamOptions{
				CacheRetention: config.CacheRetention,
				SessionID:      config.SessionID,
				UserAgent:      UserAgent(workDir, config.Version),
			}, llm.DefaultRetryConfig())
			if err != nil {
				if isRetryableStreamError(err) && streamErrRetries < 2 {
					streamErrRetries++
					continue
				}
				result <- RunResult{
					Messages: messages,
					Usage:    totalUsage,
					Error:    fmt.Errorf("start stream: %w", err),
				}
				return
			}

			// Drain stream events
			for range streamHandle.Events {
			}

			// Wait for stream completion and get final message
			assistantMsg, err = streamHandle.Wait()
			if err != nil {
				if isRetryableStreamError(err) && streamErrRetries < 2 {
					streamErrRetries++
					continue
				}
				result <- RunResult{
					Messages: messages,
					Usage:    totalUsage,
					Error:    fmt.Errorf("stream error: %w", err),
				}
				return
			}

			break
		}

		// Update total usage
		totalUsage = addUsage(totalUsage, assistantMsg.Usage)

		// Add assistant message to conversation
		messages = append(messages, assistantMsg)

		// Check stop reason
		switch assistantMsg.StopReason {
		case llm.StopReasonMaxTokens:
			result <- RunResult{
				Messages: messages,
				Usage:    totalUsage,
				Error:    fmt.Errorf("context overflow: max tokens reached"),
			}
			return

		case llm.StopReasonError:
			result <- RunResult{
				Messages: messages,
				Usage:    totalUsage,
				Error:    fmt.Errorf("LLM error: %s", assistantMsg.ErrorMessage),
			}
			return

		case llm.StopReasonAborted:
			result <- RunResult{
				Messages: messages,
				Usage:    totalUsage,
				Error:    ctx.Err(),
			}
			return

		case llm.StopReasonEnd:
			// Natural completion - no tool calls
			if config.InputCh != nil {
				for {
					select {
					case input, ok := <-config.InputCh:
						if !ok {
							goto finishRun
						}
						raw := input
						if strings.TrimSpace(raw) == "" {
							continue
						}
						messages = append(messages, llm.UserMessage{
							Role: "user",
							Content: []llm.ContentBlock{
								llm.TextContent{Type: "text", Text: raw},
							},
							Timestamp: time.Now(),
						})
						turnIndex++
						goto continueRun
					case <-ctx.Done():
						result <- RunResult{
							Messages: messages,
							Usage:    totalUsage,
							Error:    ctx.Err(),
						}
						return
					}
				}
			}
		finishRun:
			result <- RunResult{
				Messages: messages,
				Usage:    totalUsage,
			}
			return
		continueRun:
			continue

		case llm.StopReasonToolUse:
			// Execute tool calls
			toolCalls := extractToolCalls(assistantMsg)

			for _, tc := range toolCalls {
				toolResult := executor.executeTool(ctx, tc)
				messages = append(messages, toolResult)
			}

			// Continue to next turn
			turnIndex++
		}
	}
}

// extractToolCalls extracts tool calls from an assistant message.
func extractToolCalls(msg llm.AssistantMessage) []llm.ToolCall {
	var toolCalls []llm.ToolCall
	for _, block := range msg.Content {
		if tc, ok := block.(llm.ToolCall); ok {
			toolCalls = append(toolCalls, tc)
		}
	}
	return toolCalls
}

// runSubagent runs an agent synchronously with a custom set of tools.
// Unlike runAgent, this blocks until completion and returns the result directly.
// It does not emit events (subagent activity is internal to the parent).
func runSubagent(ctx context.Context, prompt PromptContent, config AgentConfig, tools []llm.Tool) (RunResult, error) {
	if config.CacheRetention == "" {
		config.CacheRetention = llm.CacheShort
	}
	workDir := config.WorkDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return RunResult{}, fmt.Errorf("get working directory: %w", err)
		}
	}

	content, err := promptContextFromRepo(workDir, config.GlobalConfigDir, prompt.TestCommands)
	if err != nil {
		return RunResult{}, err
	}
	if len(prompt.ProjectContext) == 0 {
		prompt.ProjectContext = content.ProjectContext
	}
	if len(prompt.ContextFiles) == 0 {
		prompt.ContextFiles = content.ContextFiles
	}
	if len(prompt.TestCommands) == 0 {
		prompt.TestCommands = content.TestCommands
	}
	if prompt.PhaseContent == "" {
		prompt.PhaseContent = content.PhaseContent
	}

	// Initialize conversation with user message
	userContent := prompt.UserContent
	messages := []llm.Message{}
	if strings.TrimSpace(userContent) != "" {
		messages = append(messages, llm.UserMessage{
			Role: "user",
			Content: []llm.ContentBlock{
				llm.TextContent{Type: "text", Text: userContent},
			},
			Timestamp: time.Now(),
		})
	}

	executor := &toolExecutor{
		workDir:     workDir,
		permissions: config.Permissions,
		env:         config.Env,
		config:      nil, // Prevents task tool from working
	}

	// Track aggregate usage
	var totalUsage llm.Usage

	for {
		// Check for context cancellation
		if ctx.Err() != nil {
			return RunResult{
				Messages: messages,
				Usage:    totalUsage,
				Error:    ctx.Err(),
			}, ctx.Err()
		}

		// Build request with custom tools
		req := llm.Request{
			System:   BuildSystemBlocks(workDir, prompt),
			Messages: messages,
			Tools:    tools,
		}

		streamErrRetries := 0
		var assistantMsg llm.AssistantMessage
		for {
			streamHandle, err := streamWithRetry(ctx, config.Model, req, llm.StreamOptions{
				CacheRetention: config.CacheRetention,
				SessionID:      config.SessionID,
				UserAgent:      UserAgent(workDir, config.Version),
			}, llm.DefaultRetryConfig())
			if err != nil {
				if isRetryableStreamError(err) && streamErrRetries < 2 {
					streamErrRetries++
					continue
				}
				return RunResult{
					Messages: messages,
					Usage:    totalUsage,
					Error:    fmt.Errorf("start stream: %w", err),
				}, err
			}

			// Drain stream events (we don't emit them for subagents)
			for range streamHandle.Events {
			}

			// Wait for stream completion and get final message
			assistantMsg, err = streamHandle.Wait()
			if err != nil {
				if isRetryableStreamError(err) && streamErrRetries < 2 {
					streamErrRetries++
					continue
				}
				return RunResult{
					Messages: messages,
					Usage:    totalUsage,
					Error:    fmt.Errorf("stream error: %w", err),
				}, err
			}
			break
		}

		// Update total usage
		totalUsage = addUsage(totalUsage, assistantMsg.Usage)

		// Add assistant message to conversation
		messages = append(messages, assistantMsg)

		// Check stop reason
		switch assistantMsg.StopReason {
		case llm.StopReasonMaxTokens:
			err := fmt.Errorf("context overflow: max tokens reached")
			return RunResult{
				Messages: messages,
				Usage:    totalUsage,
				Error:    err,
			}, err

		case llm.StopReasonError:
			err := fmt.Errorf("LLM error: %s", assistantMsg.ErrorMessage)
			return RunResult{
				Messages: messages,
				Usage:    totalUsage,
				Error:    err,
			}, err

		case llm.StopReasonAborted:
			return RunResult{
				Messages: messages,
				Usage:    totalUsage,
				Error:    ctx.Err(),
			}, ctx.Err()

		case llm.StopReasonEnd:
			// Natural completion - no tool calls
			return RunResult{
				Messages: messages,
				Usage:    totalUsage,
			}, nil

		case llm.StopReasonToolUse:
			// Execute tool calls
			toolCalls := extractToolCalls(assistantMsg)
			for _, tc := range toolCalls {
				toolResult := executor.executeTool(ctx, tc)
				messages = append(messages, toolResult)
			}
			// Continue to next turn
		}
	}
}

// addUsage adds two usage values together.
func addUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		Input:      a.Input + b.Input,
		Output:     a.Output + b.Output,
		CacheRead:  a.CacheRead + b.CacheRead,
		CacheWrite: a.CacheWrite + b.CacheWrite,
		Total:      a.Total + b.Total,
		Cost: llm.UsageCost{
			Input:      a.Cost.Input + b.Cost.Input,
			Output:     a.Cost.Output + b.Cost.Output,
			CacheRead:  a.Cost.CacheRead + b.Cost.CacheRead,
			CacheWrite: a.Cost.CacheWrite + b.Cost.CacheWrite,
			Total:      a.Cost.Total + b.Cost.Total,
		},
	}
}

