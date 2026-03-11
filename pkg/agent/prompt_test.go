package agent

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSystemPrompt_UsesDateOnly(t *testing.T) {
	prompt := BuildSystemBlocks("/workdir", PromptContent{PhaseContent: "Phase content"})

	var dateText string
	for _, block := range prompt {
		for line := range strings.SplitSeq(block.Text, "\n") {
			if after, ok := strings.CutPrefix(line, "Current date and time: "); ok {
				dateText = after
				break
			}
		}
		if dateText != "" {
			break
		}
	}

	if dateText == "" {
		t.Fatalf("expected date line in prompt")
	}

	if strings.Contains(dateText, ":") {
		t.Fatalf("expected date-only timestamp without time-of-day, got %q", dateText)
	}

	if _, err := time.Parse("Monday, January 2, 2006", dateText); err != nil {
		t.Fatalf("expected date-only timestamp, got %q: %v", dateText, err)
	}
}
