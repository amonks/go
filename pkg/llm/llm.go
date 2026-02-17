package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Client is used to access LLM functionality
type Client struct {
	model string
}

// New creates a new LLM client
func New(model string) *Client {
	if model == "" {
		model = "4o-mini" // Default to 4o-mini if not specified
	}

	return &Client{
		model: model,
	}
}

// GenerateWithSchema runs an LLM query and returns JSON-structured output based on the schema
func (c *Client) GenerateWithSchema(prompt string, schema string) (map[string]any, error) {
	cmd := exec.Command("llm", "-m", c.model, "--schema", schema, prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run LLM command: %w, stderr: %s", err, stderr.String())
	}

	// Trim any leading/trailing whitespace
	output := strings.TrimSpace(stdout.String())

	// Parse the JSON output
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM output as JSON: %w, output: %s", err, output)
	}

	return result, nil
}
