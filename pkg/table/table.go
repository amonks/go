// Package table renders aligned, borderless tables for CLI output.
//
// It wraps charmbracelet/lipgloss/table with sensible defaults:
// no borders, automatic terminal width detection, ANSI-aware
// column sizing, and cell normalization (tabs/newlines → spaces).
package table

import (
	"os"
	"strings"
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
	return truncateVisible(value, maxVisible) + cellEllipsis
}

func displayWidth(value string) int {
	return lipgloss.Width(stripANSICodes(value))
}

func normalizeCell(value string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(value)
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
