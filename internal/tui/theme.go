package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// theme centralizes every color the inspector uses (ADR-0021). Values are lifted
// from the Paper mockup; lipgloss + termenv auto-degrade truecolor to 256/16 on
// limited terminals. The inspector is borderless and dark-only — panes are set
// off by faint dividers and a selected-row fill, not boxes. Roles, not raw
// hexes, are what the views reference; retheming is this file.
var (
	// Grounds. No app-wide fill — the terminal's own background shows through
	// (full-width painted bands read as shadow strips on real terminals). Only
	// small chips carry a subtle fill.
	colChip     = lipgloss.Color("#17171C") // chip fill (kind / ⌘K / tag pills)
	colChipEdge = lipgloss.Color("#3A3A44") // card border
	colDiv      = lipgloss.Color("#1E1E24") // divider rules

	// Accents — magenta→indigo brand pair; cyan for the live-search caret.
	colAccent = lipgloss.Color("#C026D3") // magenta: logo, selected-row bar
	colGradA  = "#C026D3"                 // gradient start (magenta)
	colGradB  = "#4F46E5"                 // gradient end (indigo)
	colSelect = lipgloss.Color("#00DFD8") // cyan: search caret
	colDanger = lipgloss.Color("#D9484D") // delete confirmation

	// Text ramp.
	colBright = lipgloss.Color("#E8E8E8") // selected title
	colText   = lipgloss.Color("#C6C6C6") // titles, body
	colMeta   = lipgloss.Color("#6E6E6E") // meta lines, unselected labels
	colDim    = lipgloss.Color("#4E4E56") // section labels, footer, counts
)

// Header / chrome.
var (
	logoStyle   = lipgloss.NewStyle().Bold(true).Foreground(colBright)
	logoDim     = lipgloss.NewStyle().Foreground(colMeta)
	logoGlyph   = lipgloss.NewStyle().Foreground(colAccent)
	tabActive   = lipgloss.NewStyle().Bold(true).Foreground(colBright)
	tabInactive = lipgloss.NewStyle().Foreground(colMeta)
	headerCount = lipgloss.NewStyle().Foreground(colMeta)
	// 1-row fill chips — a terminal can't do a single-line rounded pill, so the
	// mockup's outlined pills become subtle-fill chips.
	kbdBadge  = lipgloss.NewStyle().Foreground(colMeta).Background(colChip).Padding(0, 1)
	pillStyle = lipgloss.NewStyle().Foreground(colText).Background(colChip).Padding(0, 1)
	hintStyle = lipgloss.NewStyle().Foreground(colDim)
)

// List rows (see delegate.go).
var (
	rowTitle    = lipgloss.NewStyle().Foreground(colText)
	rowTitleSel = lipgloss.NewStyle().Foreground(colBright).Bold(true)
	rowMeta     = lipgloss.NewStyle().Foreground(colMeta)
)

// Sidebar + detail.
var (
	sectionLabel = lipgloss.NewStyle().Foreground(colDim).Bold(true)
	sidebarSel   = lipgloss.NewStyle().Foreground(colBright).Bold(true)
	sidebarUnsel = lipgloss.NewStyle().Foreground(colMeta)
	countStyle   = lipgloss.NewStyle().Foreground(colDim)
	cardStyle    = lipgloss.NewStyle().Foreground(colText).
			Border(lipgloss.RoundedBorder()).BorderForeground(colChipEdge).Padding(0, 1)
	cardHint = lipgloss.NewStyle().Foreground(colDim)

	titleStyle = lipgloss.NewStyle().Foreground(colBright).Bold(true)
	metaStyle  = lipgloss.NewStyle().Foreground(colMeta)
	bodyStyle  = lipgloss.NewStyle().Foreground(colText)

	// CONNECTIONS rows (detail-pane BM25 neighbors, ADR-0021).
	connDot   = lipgloss.NewStyle().Foreground(colAccent)
	connTitle = lipgloss.NewStyle().Foreground(colText)

	footerStyle = lipgloss.NewStyle().Foreground(colDim)
	footerKey   = lipgloss.NewStyle().Foreground(colMeta)
	dangerStyle = lipgloss.NewStyle().Bold(true).Foreground(colDanger)
)

// chromeRow lays out a full-width header/search/footer row. No background — the
// terminal ground shows through, so there are no painted bands.
func chromeRow(width int) lipgloss.Style {
	return lipgloss.NewStyle().Width(width)
}

// hrule / vrule are the faint dividers that replace pane borders.
func hrule(width int) string {
	return lipgloss.NewStyle().Foreground(colDiv).Render(strings.Repeat("─", max(0, width)))
}

func vrule(height int) string {
	col := lipgloss.NewStyle().Foreground(colDiv).Render("│")
	return strings.Repeat(col+"\n", max(1, height)-1) + col
}

// gradientBar renders width cells of "─" interpolated from→to — the magenta→
// indigo underline beneath the active tab.
func gradientBar(width int, from, to string) string {
	if width < 1 {
		return ""
	}
	var b strings.Builder
	for i := range width {
		t := 0.0
		if width > 1 {
			t = float64(i) / float64(width-1)
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(lerpHex(from, to, t))).Render("─"))
	}
	return b.String()
}

func hexRGB(h string) (r, g, b int) {
	_, _ = fmt.Sscanf(h, "#%02x%02x%02x", &r, &g, &b)
	return
}

func lerpHex(a, b string, t float64) string {
	ar, ag, ab := hexRGB(a)
	br, bg, bb := hexRGB(b)
	lerp := func(x, y int) int { return x + int(float64(y-x)*t) }
	return fmt.Sprintf("#%02X%02X%02X", lerp(ar, br), lerp(ag, bg), lerp(ab, bb))
}
