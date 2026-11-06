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

// A cell is data, often somebody else's: every control in it but SGR
// styling is shown as a replacement character, never sent to the
// terminal, and counts as one column; a style the cell sets ends with
// the cell.
func TestCellsCarryNoControlSequences(t *testing.T) {
	value := "\x1b]52;c;cHduZWQ=\x07\x1b[1m" + strings.Repeat("a", cellMaxWidth-6) + "\u009b2J\x7f"

	got := TruncateCell(value)

	if strings.ContainsAny(got, "\x07\x7f\u009b") || strings.Contains(got, "\x1b]") {
		t.Fatalf("control characters reached the cell: %q", got)
	}
	if !strings.HasSuffix(got, cellEllipsis+"\x1b[0m") || CellWidth(got) != cellMaxWidth {
		t.Fatalf("cell = %q (width %d), want it truncated to %d and its style closed", got, CellWidth(got), cellMaxWidth)
	}
	out := Format([]string{"COL", "NEXT"}, [][]string{{"\x1b[8mhidden\x1b[2J", "shown"}})
	if strings.Contains(out, "\x1b[2J") {
		t.Fatalf("Format passed an erase through: %q", out)
	}
	if !strings.Contains(out, "\x1b[8mhidden\ufffd[2J\x1b[0m") {
		t.Fatalf("Format should keep the cell's style and close it within the cell: %q", out)
	}
}

// Printable keeps what multi-line text needs and nothing a terminal
// would act on: C0 controls but newline and tab, DEL, the C1 controls,
// and bytes that aren't UTF-8 (a lone 0x9b is CSI to an 8-bit
// terminal) become U+FFFD; a CRLF is a newline.
func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":               "plain text",
		"two\nlines\tand a tab":    "two\nlines\tand a tab",
		"windows\r\nline":          "windows\nline",
		"over\rwrite":              "over\ufffdwrite",
		"\x1b]52;c;cHduZWQ=\x07":   "\ufffd]52;c;cHduZWQ=\ufffd",
		"\x1b[2J\x00\x7f":          "\ufffd[2J\ufffd\ufffd",
		"c1 \u009b31m and \u0085":  "c1 \ufffd31m and \ufffd",
		"raw \x9b byte":            "raw \ufffd byte",
		"unicode stays: café ✓ 漢字": "unicode stays: café ✓ 漢字",
	} {
		if got := Printable(in); got != want {
			t.Errorf("Printable(%q) = %q, want %q", in, got, want)
		}
	}
}

// PrintableStyled is Printable for text whose styling is the point, a
// build log a task runner colored: a well-formed SGR sequence survives,
// every other control is replaced as Printable replaces it, and a line
// that leaves a style set is closed with a reset, so nothing one line
// says about style outlasts it.
func TestPrintableStyled(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":            "plain text",
		"two\nlines\tand a tab": "two\nlines\tand a tab",
		"windows\r\nline":       "windows\nline",
		"\x1b[3;38;2;253;246;227mstarting\x1b[m\nexit ok": "\x1b[3;38;2;253;246;227mstarting\x1b[m\nexit ok",
		"\x1b[31mred\nplain":                              "\x1b[31mred\x1b[0m\nplain",
		"\x1b[8mhidden":                                   "\x1b[8mhidden\x1b[0m",
		"\x1b[1mbold\x1b[0m":                              "\x1b[1mbold\x1b[0m",
		"\x1b]52;c;cHduZWQ=\x07":                          "\ufffd]52;c;cHduZWQ=\ufffd",
		"\x1b[2J\x00\x7f":                                 "\ufffd[2J\ufffd\ufffd",
		"c1 \u009b31m and \u0085":                         "c1 \ufffd31m and \ufffd",
		"raw \x9b byte":                                   "raw \ufffd byte",
		"over\rwrite":                                     "over\ufffdwrite",
		"unicode stays: café ✓ 漢字":                        "unicode stays: café ✓ 漢字",
	} {
		if got := PrintableStyled(in); got != want {
			t.Errorf("PrintableStyled(%q) = %q, want %q", in, got, want)
		}
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
