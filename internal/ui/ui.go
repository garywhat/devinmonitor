// Package ui provides shared terminal rendering utilities — tables with
// CJK-correct alignment, styled panels, and progress bars.
//
// We implement our own table renderer instead of using lipgloss/table because
// lipgloss/table v1.1.0 has a CJK width bug that truncates double-width
// characters. Our renderer uses lipgloss.Width() (which delegates to
// go-runewidth) for all column width calculations.
package ui

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// termWidth detects the terminal width via ioctl. Returns 0 if not a TTY.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 0
	}
	return w
}

// terminalWidth returns the current terminal width in columns.
// Tries ioctl first, then COLUMNS env var, then falls back to 200.
func terminalWidth() int {
	if w := termWidth(); w > 0 {
		return w
	}
	if cols := os.Getenv("COLUMNS"); cols != "" {
		var w int
		if _, err := fmt.Sscanf(cols, "%d", &w); err == nil && w > 0 {
			return w
		}
	}
	return 200
}

// Color palette (dark theme).
var (
	ColorPrimary = lipgloss.Color("39")
	ColorAccent  = lipgloss.Color("41")
	ColorWarn    = lipgloss.Color("220")
	ColorError   = lipgloss.Color("203")
	ColorDim     = lipgloss.Color("240")
	ColorHeader  = lipgloss.Color("99")
	ColorValue   = lipgloss.Color("252")
	ColorLabel   = lipgloss.Color("245")
	ColorFree    = lipgloss.Color("42")
	ColorCost    = lipgloss.Color("220")
	ColorBorder  = lipgloss.Color("238")

	StyleHeader = lipgloss.NewStyle().Bold(true).Foreground(ColorHeader)
	StyleValue  = lipgloss.NewStyle().Foreground(ColorValue)
	StyleLabel  = lipgloss.NewStyle().Foreground(ColorLabel)
	StyleDim    = lipgloss.NewStyle().Foreground(ColorDim)
	StyleFree   = lipgloss.NewStyle().Foreground(ColorFree)
	StyleCost   = lipgloss.NewStyle().Foreground(ColorCost)
	StyleWarn   = lipgloss.NewStyle().Foreground(ColorWarn)
	StyleError  = lipgloss.NewStyle().Foreground(ColorError)
	StyleAccent = lipgloss.NewStyle().Foreground(ColorAccent)
)

// ansiRe matches ANSI escape sequences for width calculations.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07`)

// init configures go-runewidth to treat East Asian Ambiguous-width characters
// (e.g. … U+2026) as 1 cell wide, matching how most Western/CJK terminals
// render them. Without this, … would be 2 cells wide, causing misalignment.
func init() {
	runewidth.DefaultCondition.EastAsianWidth = false
}

// strWidth returns the display width of s, stripping ANSI escape codes first.
// This is CJK-correct (uses runewidth) and ANSI-safe.
func strWidth(s string) int {
	clean := ansiRe.ReplaceAllString(s, "")
	return runewidth.StringWidth(clean)
}

// padRight pads s with spaces to target display width, CJK-correct.
func padRight(s string, targetWidth int) string {
	w := strWidth(s)
	if w >= targetWidth {
		return s
	}
	return s + strings.Repeat(" ", targetWidth-w)
}

// padLeft right-aligns s by padding on the left.
func padLeft(s string, targetWidth int) string {
	w := strWidth(s)
	if w >= targetWidth {
		return s
	}
	return strings.Repeat(" ", targetWidth-w) + s
}

// truncateCJK truncates s to maxDisplayWidth using runewidth, appending "…".
// ANSI escape sequences are preserved (not counted toward width).
func truncateCJK(s string, maxDisplayWidth int) string {
	if strWidth(s) <= maxDisplayWidth {
		return s
	}
	// Strip ANSI, truncate the clean text, then we lose styling (acceptable
	// for truncation — the truncated portion is at the end).
	clean := ansiRe.ReplaceAllString(s, "")
	out := ""
	for _, r := range clean {
		if strWidth(out)+runewidth.RuneWidth(r)+1 > maxDisplayWidth {
			break
		}
		out += string(r)
	}
	return out + "…"
}

// ---- Table ----

// TableBuilder builds a styled table with rounded borders and CJK-correct
// column width calculation. Automatically fits to terminal width by
// truncating the widest text columns first — only when the terminal is
// too narrow. No truncation happens when there is enough space.
type TableBuilder struct {
	headers    []string
	rows       [][]string
	totalRow   []string
	rightAlign []bool
	termWidth  int // 0 = auto-detect
}

// NewTable creates a new TableBuilder with the given headers.
func NewTable(headers ...string) *TableBuilder {
	return &TableBuilder{
		headers:    headers,
		rightAlign: make([]bool, len(headers)),
		termWidth:  terminalWidth(),
	}
}

// RightAlign sets columns (0-indexed) to right-aligned.
func (t *TableBuilder) RightAlign(cols ...int) *TableBuilder {
	for _, c := range cols {
		if c >= 0 && c < len(t.rightAlign) {
			t.rightAlign[c] = true
		}
	}
	return t
}

// Row adds a data row.
func (t *TableBuilder) Row(values ...string) *TableBuilder {
	t.rows = append(t.rows, values)
	return t
}

// TotalRow adds a totals row that is rendered with a separator line above it.
// Use this for the summary/aggregate row at the bottom of a table.
func (t *TableBuilder) TotalRow(values ...string) *TableBuilder {
	t.totalRow = values
	return t
}

// String renders the table as a string with rounded borders.
// If the table exceeds terminal width, text columns are truncated to fit.
func (t *TableBuilder) String() string {
	nCols := len(t.headers)
	if nCols == 0 {
		return ""
	}

	// Calculate column widths from headers, rows, and total row.
	colWidths := make([]int, nCols)
	for i, h := range t.headers {
		colWidths[i] = strWidth(h)
	}
	for _, row := range t.rows {
		for i := 0; i < nCols && i < len(row); i++ {
			if w := strWidth(row[i]); w > colWidths[i] {
				colWidths[i] = w
			}
		}
	}
	if t.totalRow != nil {
		for i := 0; i < nCols && i < len(t.totalRow); i++ {
			if w := strWidth(t.totalRow[i]); w > colWidths[i] {
				colWidths[i] = w
			}
		}
	}

	// Auto-fit to terminal width.
	// Total width = sum(colWidths) + nCols*2 (padding) + nCols+1 (borders).
	totalWidth := 0
	for _, w := range colWidths {
		totalWidth += w
	}
	totalWidth += nCols * 2 // " content " padding
	totalWidth += nCols + 1 // borders

	termW := t.termWidth
	if termW <= 0 {
		termW = 200
	}

	// If too wide, truncate columns to fit. Text columns are truncated first
	// (they can go down to 10 chars), then numeric columns (down to 6 chars).
	if totalWidth > termW {
		overflow := totalWidth - termW
		type colInfo struct {
			idx  int
			w    int
			text bool
		}
		var cols []colInfo
		for i, w := range colWidths {
			text := !t.rightAlign[i]
			cols = append(cols, colInfo{i, w, text})
		}
		// Sort: text columns first (by width desc), then numeric (by width desc).
		for i := 0; i < len(cols); i++ {
			for j := i + 1; j < len(cols); j++ {
				a, b := cols[i], cols[j]
				swap := false
				if a.text && !b.text {
					swap = true
				} else if a.text == b.text && a.w > b.w {
					swap = true
				}
				if swap {
					cols[i], cols[j] = cols[j], cols[i]
				}
			}
		}
		// Pass 1: truncate text columns down to 10 chars.
		for _, c := range cols {
			if overflow <= 0 {
				break
			}
			if !c.text {
				continue
			}
			curW := colWidths[c.idx]
			if curW <= 10 {
				continue
			}
			shrink := overflow
			if shrink > curW-10 {
				shrink = curW - 10
			}
			colWidths[c.idx] -= shrink
			overflow -= shrink
			for _, row := range t.rows {
				if c.idx < len(row) {
					row[c.idx] = truncateCJK(row[c.idx], colWidths[c.idx])
				}
			}
			if c.idx < len(t.totalRow) {
				t.totalRow[c.idx] = truncateCJK(t.totalRow[c.idx], colWidths[c.idx])
			}
		}
		// Pass 2: truncate numeric columns down to 6 chars.
		for _, c := range cols {
			if overflow <= 0 {
				break
			}
			curW := colWidths[c.idx]
			if curW <= 6 {
				continue
			}
			shrink := overflow
			if shrink > curW-6 {
				shrink = curW - 6
			}
			colWidths[c.idx] -= shrink
			overflow -= shrink
			for _, row := range t.rows {
				if c.idx < len(row) {
					row[c.idx] = truncateCJK(row[c.idx], colWidths[c.idx])
				}
			}
			if c.idx < len(t.totalRow) {
				t.totalRow[c.idx] = truncateCJK(t.totalRow[c.idx], colWidths[c.idx])
			}
		}
		// Pass 3: if still overflowing, aggressively truncate text to 4, numeric to 3.
		for _, c := range cols {
			if overflow <= 0 {
				break
			}
			minW := 4
			if !c.text {
				minW = 3
			}
			curW := colWidths[c.idx]
			if curW <= minW {
				continue
			}
			shrink := overflow
			if shrink > curW-minW {
				shrink = curW - minW
			}
			colWidths[c.idx] -= shrink
			overflow -= shrink
			for _, row := range t.rows {
				if c.idx < len(row) {
					row[c.idx] = truncateCJK(row[c.idx], colWidths[c.idx])
				}
			}
			if c.idx < len(t.totalRow) {
				t.totalRow[c.idx] = truncateCJK(t.totalRow[c.idx], colWidths[c.idx])
			}
		}
	}

	// Clamp all column widths to >= 1 to prevent negative Repeat in borders.
	for i := range colWidths {
		if colWidths[i] < 1 {
			colWidths[i] = 1
		}
	}

	// Border characters (rounded).
	const (
		topLeft  = "╭"
		topRight = "╮"
		botLeft  = "╰"
		botRight = "╯"
		horiz    = "─"
		vert     = "│"
		teeDown  = "┬"
		teeUp    = "┴"
		teeRight = "├"
		teeLeft  = "┤"
		cross    = "┼"
	)

	// Build border lines.
	buildHoriz := func(left, mid, right string) string {
		var b strings.Builder
		b.WriteString(left)
		for i, w := range colWidths {
			b.WriteString(strings.Repeat(horiz, w+2)) // +2 for padding
			if i < nCols-1 {
				b.WriteString(mid)
			}
		}
		b.WriteString(right)
		return b.String()
	}

	topBorder := buildHoriz(topLeft, teeDown, topRight)
	midBorder := buildHoriz(teeRight, cross, teeLeft)
	botBorder := buildHoriz(botLeft, teeUp, botRight)

	// Build a row line.
	borderColor := "\x1b[38;5;238m"
	borderReset := "\x1b[0m"
	borderVert := borderColor + vert + borderReset
	buildRow := func(cells []string, style string) string {
		var b strings.Builder
		b.WriteString(borderVert)
		for i := 0; i < nCols; i++ {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			// Truncate cell to colWidth if needed.
			if strWidth(cell) > colWidths[i] {
				cell = truncateCJK(cell, colWidths[i])
			}
			if t.rightAlign[i] {
				cell = padLeft(cell, colWidths[i])
			} else {
				cell = padRight(cell, colWidths[i])
			}
			// Apply styling with raw ANSI codes.
			if style != "" {
				cell = style + cell + "\x1b[0m"
			}
			b.WriteString(" " + cell + " ")
			b.WriteString(borderVert)
		}
		return b.String()
	}

	var out strings.Builder

	out.WriteString(borderColor + topBorder + borderReset)
	out.WriteString("\n")
	out.WriteString(buildRow(t.headers, "\x1b[1;38;5;99m")) // header: bold purple
	out.WriteString("\n")
	out.WriteString(borderColor + midBorder + borderReset)
	out.WriteString("\n")
	for _, row := range t.rows {
		out.WriteString(buildRow(row, ""))
		out.WriteString("\n")
	}
	// Render totals row with a separator above it (bold).
	if t.totalRow != nil {
		out.WriteString(borderColor + midBorder + borderReset)
		out.WriteString("\n")
		out.WriteString(buildRow(t.totalRow, "\x1b[1m")) // bold
		out.WriteString("\n")
	}
	out.WriteString(borderColor + botBorder + borderReset)

	return out.String()
}

// Print renders and prints the table to stdout.
func (t *TableBuilder) Print() {
	fmt.Println(t.String())
}

// Truncate truncates s to maxDisplayWidth using runewidth for CJK correctness.
func Truncate(s string, maxDisplayWidth int) string {
	return truncateCJK(s, maxDisplayWidth)
}

// Panel renders a titled bordered panel with the given content.
// Borders are colored manually because lipgloss strips ANSI from pure-symbol
// strings when output is not a TTY.
func Panel(title, content string, width int) string {
	t := lipgloss.NewStyle().Bold(true).Foreground(ColorHeader).Render(title)
	innerW := width - 4 // width - 2 borders - 2 padding
	body := content
	if innerW > 0 {
		// Truncate each line to inner width.
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			lines[i] = truncateCJK(line, innerW)
		}
		body = strings.Join(lines, "\n")
	}
	// Build panel with manual border coloring.
	bc := "\x1b[38;5;238m"
	rst := "\x1b[0m"
	horiz := strings.Repeat("─", innerW+2) // +2 for padding
	top := bc + "╭" + horiz + "╮" + rst
	bot := bc + "╰" + horiz + "╯" + rst
	v := bc + "│" + rst
	var b strings.Builder
	b.WriteString(top)
	b.WriteString("\n")
	// Title line.
	b.WriteString(v)
	titleCell := " " + t
	titleCell = padRight(titleCell, innerW+1)
	b.WriteString(titleCell + " ")
	b.WriteString(v)
	b.WriteString("\n")
	// Body lines.
	for _, line := range strings.Split(body, "\n") {
		b.WriteString(v)
		cell := " " + padRight(line, innerW) + " "
		b.WriteString(cell)
		b.WriteString(v)
		b.WriteString("\n")
	}
	b.WriteString(bot)
	return b.String()
}

// ProgressBar renders a unicode progress bar with correct width.
func ProgressBar(pct, width float64) string {
	if width < 4 {
		width = 4
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(width * pct / 100)
	if filled > int(width) {
		filled = int(width)
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", int(width)-filled)
}

// PctColor returns a lipgloss style based on percentage threshold.
func PctColor(pct float64) lipgloss.Style {
	switch {
	case pct >= 90:
		return StyleError
	case pct >= 70:
		return StyleWarn
	default:
		return StyleFree
	}
}

// Sparkline renders a unicode sparkline from float values.
func Sparkline(pts []float64, width int) string {
	if len(pts) == 0 || width < 4 {
		return ""
	}
	sampled := downsample(pts, width)
	min, max := minMax(sampled)
	rng := max - min
	if rng == 0 {
		rng = 1
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range sampled {
		idx := int((v - min) / rng * float64(len(blocks)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		b.WriteRune(blocks[idx])
	}
	return b.String()
}

func downsample(pts []float64, width int) []float64 {
	if len(pts) <= width {
		return pts
	}
	out := make([]float64, 0, width)
	step := float64(len(pts)) / float64(width)
	for i := 0.0; i < float64(len(pts)); i += step {
		idx := int(i)
		if idx >= len(pts) {
			idx = len(pts) - 1
		}
		out = append(out, pts[idx])
	}
	return out
}

func minMax(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	min, max := xs[0], xs[0]
	for _, v := range xs[1:] {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max
}
