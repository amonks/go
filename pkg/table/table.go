// Package table renders aligned, borderless tables for CLI output.
//
// It wraps charmbracelet/lipgloss/table with sensible defaults:
// no borders, automatic terminal width detection, ANSI-aware
// column sizing, and cell normalization (tabs/newlines → spaces,
// control characters other than SGR styling made visible). Printable
// is the same terminal-safety for text a CLI prints outside a table,
// and PrintableStyled the same again for text whose SGR styling is kept.
package table

import (
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"golang.org/x/term"
)

const cellMaxWidth = 50
const cellEllipsis = "..."
const columnPadding = 2

var viewportWidth = detectViewportWidth
var isTerminal = term.IsTerminal
var getSize = term.GetSize

// ViewportWidth reports the configured viewport width.
func ViewportWidth() int {
	return viewportWidth()
}

// OverrideViewportWidth replaces the viewport width provider.
func OverrideViewportWidth(fn func() int) func() {
	original := viewportWidth
	viewportWidth = fn
	return func() {
		viewportWidth = original
	}
}

// CellWidth reports the display width of a table cell.
func CellWidth(value string) int {
	return displayWidth(normalizeCell(value))
}

// CellMaxWidth reports the default maximum width for table cells.
func CellMaxWidth() int {
	return cellMaxWidth
}

// ColumnPaddingWidth reports the padding between columns.
func ColumnPaddingWidth() int {
	return columnPadding
}

// Builder collects rows and renders a formatted table.
type Builder struct {
	headers []string
	rows    [][]string
}

// NewBuilder returns a builder with preallocated rows.
func NewBuilder(headers []string, capacity int) *Builder {
	return &Builder{headers: headers, rows: make([][]string, 0, capacity)}
}

// AddRow appends a row to the table.
func (b *Builder) AddRow(row []string) {
	b.rows = append(b.rows, row)
}

// String renders the table output.
func (b *Builder) String() string {
	return Format(b.headers, b.rows)
}

// Format renders headers and rows as an aligned table.
func Format(headers []string, rows [][]string) string {
	normalizedHeaders := make([]string, len(headers))
	for i, header := range headers {
		normalizedHeaders[i] = normalizeCell(header)
	}

	normalizedRows := make([][]string, 0, len(rows))
	columnCount := len(normalizedHeaders)
	for _, row := range rows {
		normalizedRow := make([]string, len(row))
		for i, cell := range row {
			normalizedRow[i] = normalizeCell(cell)
		}
		normalizedRows = append(normalizedRows, normalizedRow)
		if len(normalizedRow) > columnCount {
			columnCount = len(normalizedRow)
		}
	}

	builder := table.New().
		Headers(normalizedHeaders...).
		Rows(normalizedRows...).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderHeader(false).
		BorderColumn(false).
		BorderRow(false).
		Wrap(false).
		StyleFunc(func(_, col int) lipgloss.Style {
			if columnCount > 1 && col < columnCount-1 {
				return lipgloss.NewStyle().PaddingRight(columnPadding)
			}
			return lipgloss.NewStyle()
		})

	if width := viewportWidth(); width > 0 {
		builder.Width(width)
	}

	output := builder.String()
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	return output
}

// TruncateCell limits cell width while preserving visible characters.
func TruncateCell(value string) string {
	return TruncateCellToWidth(value, cellMaxWidth)
}

// TruncateCellToWidth limits cell width while preserving visible characters.
func TruncateCellToWidth(value string, max int) string {
	value = normalizeCell(value)
	if max <= 0 {
		return ""
	}
	if displayWidth(value) <= max {
		return value
	}

	ellipsisWidth := displayWidth(cellEllipsis)
	if max <= ellipsisWidth {
		return truncateVisible(cellEllipsis, max)
	}

	maxVisible := max - ellipsisWidth
	// Normalized again so a style the cut dropped the reset of is
	// closed.
	return normalizeCell(truncateVisible(value, maxVisible) + cellEllipsis)
}

func displayWidth(value string) int {
	return lipgloss.Width(stripANSICodes(value))
}

// normalizeCell makes a cell one line that is safe to print. A cell is
// often text the caller did not write, so its controls are made visible
// as Printable's are — with one exception, the styling the package's
// ANSI-aware sizing exists for: a well-formed SGR sequence (ESC [
// params m), which a caller uses to color part of a cell, survives.
// SGR only styles text, and a cell that sets any is closed with a reset,
// so whatever the cell's text says about its style ends with the cell.
func normalizeCell(value string) string {
	value = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(value)
	var b strings.Builder
	styled := false
	for i := 0; i < len(value); {
		if n := sgrLen(value[i:]); n > 0 {
			b.WriteString(value[i : i+n])
			styled = true
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if (r == utf8.RuneError && size == 1) || unicode.IsControl(r) {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(value[i : i+size])
		}
		i += size
	}
	out := b.String()
	if styled && !strings.HasSuffix(out, sgrReset) {
		out += sgrReset
	}
	return out
}

// sgrReset ends every style a cell set.
const sgrReset = "\x1b[0m"

// sgrLen is the length of the SGR sequence s begins with — ESC [, then
// digits and separators, then m — or 0 when s begins with anything else.
func sgrLen(s string) int {
	if len(s) < 3 || s[0] != '\x1b' || s[1] != '[' {
		return 0
	}
	for i := 2; i < len(s); i++ {
		switch c := s[i]; {
		case c == 'm':
			return i + 1
		case c >= '0' && c <= '9', c == ';', c == ':':
		default:
			return 0
		}
	}
	return 0
}

// Printable makes text safe to write to a terminal. A CLI's output is
// mostly text it did not write — a prompt, a title, an email's subject,
// an error a server returned — and a terminal acts on the controls in
// it: an escape sequence can set the clipboard (OSC 52), retitle the
// window, or repaint the screen into something it isn't. Every C0
// control but newline and tab, DEL, every C1 control, and every byte
// that isn't UTF-8 (a lone 0x9b is CSI to a terminal reading 8-bit
// controls) becomes U+FFFD, which shows that something was there
// without doing it. A CRLF is a newline; a lone CR, which returns to the
// start of the line to write over it, is replaced like the rest.
func Printable(s string) string {
	clean := true
	for _, r := range s {
		if r == utf8.RuneError || (r != '\n' && r != '\t' && unicode.IsControl(r)) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\r\n", "\n"), "\ufffd")
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return '\ufffd'
		}
		return r
	}, s)
}

// PrintableStyled is Printable for text whose styling is the point — a
// build log a task runner colored — and which is still text the CLI did
// not write. A well-formed SGR sequence survives, since SGR only styles
// text; every other control is replaced as Printable replaces it. A line
// that leaves a style set is closed with a reset before its newline, as
// a cell is at its end, so nothing one line says about style (concealed
// text, say) outlasts it. Text that sets nothing comes back unchanged.
func PrintableStyled(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	styled := false
	for i := 0; i < len(s); {
		if n := sgrLen(s[i:]); n > 0 {
			seq := s[i : i+n]
			b.WriteString(seq)
			styled = seq != "\x1b[m" && seq != sgrReset
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\n':
			if styled {
				b.WriteString(sgrReset)
				styled = false
			}
			b.WriteByte('\n')
		case r == '\t':
			b.WriteByte('\t')
		case (r == utf8.RuneError && size == 1) || unicode.IsControl(r):
			b.WriteRune(utf8.RuneError)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	if styled {
		b.WriteString(sgrReset)
	}
	return b.String()
}

func truncateVisible(value string, max int) string {
	if max <= 0 {
		return ""
	}

	var builder strings.Builder
	visible := 0
	for i := 0; i < len(value); {
		if value[i] == '\x1b' {
			end := i + 1
			if end < len(value) && value[end] == '[' {
				end++
				for end < len(value) && value[end] != 'm' {
					end++
				}
				if end < len(value) {
					end++
				}
				builder.WriteString(value[i:end])
				i = end
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 1 {
			if visible >= max {
				break
			}
			builder.WriteByte(value[i])
			visible++
			i++
			continue
		}
		width := lipgloss.Width(string(r))
		if visible+width > max {
			break
		}
		builder.WriteRune(r)
		visible += width
		i += size
	}
	return builder.String()
}

func stripANSICodes(input string) string {
	var builder strings.Builder
	inEscape := false
	for i := 0; i < len(input); i++ {
		char := input[i]
		if inEscape {
			if char == 'm' {
				inEscape = false
			}
			continue
		}
		if char == '\x1b' {
			inEscape = true
			continue
		}
		builder.WriteByte(char)
	}
	return builder.String()
}

func detectViewportWidth() int {
	if width := detectTerminalWidth(os.Stdout.Fd()); width > 0 {
		return width
	}
	return detectTerminalWidth(os.Stderr.Fd())
}

func detectTerminalWidth(fd uintptr) int {
	if !isTerminal(int(fd)) {
		return 0
	}
	width, _, err := getSize(int(fd))
	if err != nil || width <= 0 {
		return 0
	}
	return width
}
