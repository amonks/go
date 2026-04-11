package table

import (
	"os"
	"strings"
	"testing"
)

func TestTruncateCellCountsRunes(t *testing.T) {
	value := strings.Repeat("a", cellMaxWidth-1) + "\u00e9"

	got := TruncateCell(value)

	if got != value {
		t.Fatalf("expected value to remain untruncated, got %q", got)
	}
}

func TestTruncateCellNormalizesLineBreaks(t *testing.T) {
	value := "Hello\nWorld\r\nAgain\tTab"

	got := TruncateCell(value)

	if got != "Hello World Again Tab" {
		t.Fatalf("expected line breaks to normalize, got %q", got)
	}
}

func TestTruncateCellIgnoresANSICodes(t *testing.T) {
	value := "\x1b[1m\x1b[36m" + strings.Repeat("a", cellMaxWidth) + "\x1b[0m"

	got := TruncateCell(value)

	if got != value {
		t.Fatalf("expected value to remain untruncated, got %q", got)
	}
}

func TestFormatNormalizesLineBreaks(t *testing.T) {
	headers := []string{"COL"}
	rows := [][]string{{"Hello\nWorld\r\nAgain\tTab"}}

	got := Format(headers, rows)

	expected := "COL                  \nHello World Again Tab\n"
	if got != expected {
		t.Fatalf("expected normalized table output, got %q", got)
	}
}

func TestFormatUsesViewportWidth(t *testing.T) {
	originalWidth := viewportWidth
	viewportWidth = func() int {
		return 10
	}
	t.Cleanup(func() {
		viewportWidth = originalWidth
	})

	headers := []string{"COL1", "COL2"}
	rows := [][]string{{"A", "B"}}

	got := Format(headers, rows)

	lines := strings.SplitSeq(strings.TrimSuffix(got, "\n"), "\n")
	for line := range lines {
		if width := displayWidth(line); width != 10 {
			t.Fatalf("expected table width 10, got %d in %q", width, line)
		}
	}
}

func TestFormatDoesNotWrapRows(t *testing.T) {
	originalWidth := viewportWidth
	viewportWidth = func() int {
		return 10
	}
	t.Cleanup(func() {
		viewportWidth = originalWidth
	})

	headers := []string{"COL"}
	rows := [][]string{{"ABCDEFGHIJK"}}

	got := Format(headers, rows)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected header and row only, got %d lines in %q", len(lines), got)
	}
	if width := displayWidth(lines[1]); width != 10 {
		t.Fatalf("expected row width 10, got %d in %q", width, lines[1])
	}
}

func TestDetectViewportWidthUsesStdout(t *testing.T) {
	originalIsTerminal := isTerminal
	originalGetSize := getSize
	t.Cleanup(func() {
		isTerminal = originalIsTerminal
		getSize = originalGetSize
	})

	stdoutFD := int(os.Stdout.Fd())
	stderrFD := int(os.Stderr.Fd())
	stdoutChecked := false
	stderrChecked := false

	isTerminal = func(fd int) bool {
		switch fd {
		case stdoutFD:
			stdoutChecked = true
			return true
		case stderrFD:
			stderrChecked = true
			return true
		default:
			return false
		}
	}

	getSize = func(fd int) (int, int, error) {
		if fd != stdoutFD {
			t.Fatalf("expected stdout fd, got %d", fd)
		}
		return 90, 0, nil
	}

	width := detectViewportWidth()
	if !stdoutChecked {
		t.Fatalf("expected stdout to be checked")
	}
	if stderrChecked {
		t.Fatalf("expected stderr not to be checked")
	}
	if width != 90 {
		t.Fatalf("expected width 90, got %d", width)
	}
}

func TestDetectViewportWidthFallsBackToStderr(t *testing.T) {
	originalIsTerminal := isTerminal
	originalGetSize := getSize
	t.Cleanup(func() {
		isTerminal = originalIsTerminal
		getSize = originalGetSize
	})

	stdoutFD := int(os.Stdout.Fd())
	stderrFD := int(os.Stderr.Fd())
	stdoutChecked := false
	stderrChecked := false

	isTerminal = func(fd int) bool {
		switch fd {
		case stdoutFD:
			stdoutChecked = true
			return false
		case stderrFD:
			stderrChecked = true
			return true
		default:
			return false
		}
	}

	getSize = func(fd int) (int, int, error) {
		if fd != stderrFD {
			t.Fatalf("expected stderr fd, got %d", fd)
		}
		return 78, 0, nil
	}

	width := detectViewportWidth()
	if !stdoutChecked || !stderrChecked {
		t.Fatalf("expected stdout and stderr checks")
	}
	if width != 78 {
		t.Fatalf("expected width 78, got %d", width)
	}
}
