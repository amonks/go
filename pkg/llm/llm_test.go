package llm

import (
	"os"
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

func TestGenerateMovieQuery(t *testing.T) {
	// Skip test if LLM_SKIP_TESTS environment variable is set
	if os.Getenv("LLM_SKIP_TESTS") != "" {
		t.Skip("Skipping LLM test due to LLM_SKIP_TESTS environment variable")
	}

	// Skip test if llm command is not available
	if _, err := exec.LookPath("llm"); err != nil {
		t.Skip("llm command not available, skipping test")
	}

	client := New("4o-mini")

	testCases := []struct {
		name     string
		filepath string
	}{
		{
			name:     "Standard movie filename",
			filepath: "The.Descendants.2011.720p.BluRay.DD5.1.x264-EbP/The.Descendants.2011.720p.BluRay.DD5.1.x264-EbP.mkv",
		},
		{
			name:     "Movie with dots",
			filepath: "No.Country.For.Old.Men.2007.1080p.BluRay.x264-HD/No.Country.For.Old.Men.2007.1080p.BluRay.x264-HD.mkv",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			title, year, err := client.GenerateMovieQuery(tc.filepath)
			if err != nil {
				t.Fatalf("GenerateMovieQuery returned error: %v", err)
			}

			if title == "" {
				t.Error("Expected non-empty title")
			}

			if year <= 1900 || year > 2030 {
				t.Errorf("Expected reasonable year, got %d", year)
			}
		})
	}
}

func TestGenerateTVQuery(t *testing.T) {
	// Skip test if LLM_SKIP_TESTS environment variable is set
	if os.Getenv("LLM_SKIP_TESTS") != "" {
		t.Skip("Skipping LLM test due to LLM_SKIP_TESTS environment variable")
	}

	// Skip test if llm command is not available
	if _, err := exec.LookPath("llm"); err != nil {
		t.Skip("llm command not available, skipping test")
	}

	client := New("4o-mini")

	testCases := []struct {
		name     string
		filepath string
	}{
		{
			name:     "Standard TV folder",
			filepath: "Breaking.Bad.S01.1080p.BluRay.x264-HD",
		},
		{
			name:     "TV with dots",
			filepath: "The.Office.US.S03.720p.BluRay.x264-ER",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			title, year, err := client.GenerateTVQuery(tc.filepath)
			if err != nil {
				t.Fatalf("GenerateTVQuery returned error: %v", err)
			}

			if title == "" {
				t.Error("Expected non-empty title")
			}

			// Year might be 0 for TV shows as it's sometimes not available
			if year != 0 && (year <= 1900 || year > 2030) {
				t.Errorf("Expected reasonable year or 0, got %d", year)
			}
		})
	}
}
