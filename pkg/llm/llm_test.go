package llm

import (
	"os/exec"
	"testing"
)

func TestGenerateWithSchema(t *testing.T) {
	// Skip test if llm command is not available
	if _, err := exec.LookPath("llm"); err != nil {
		t.Skip("llm command not available, skipping test")
	}

	client := New("4o-mini")

	// Test with a simple prompt
	result, err := client.GenerateWithSchema("Test prompt", "test_field str")
	if err != nil {
		t.Fatalf("GenerateWithSchema returned error: %v", err)
	}

	if result == nil {
		t.Fatal("GenerateWithSchema returned nil result")
	}

	if _, ok := result["test_field"]; !ok {
		t.Errorf("Expected result to contain 'test_field', got %v", result)
	}
}
