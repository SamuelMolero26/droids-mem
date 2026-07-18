package tui

import (
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
	colChip = lipgloss.Color("#17171C") // chip fill (kind / ⌘K / tag pills)
	colDiv  = lipgloss.Color("#1E1E24") // divider rules

	// Accents — magenta brand color; cyan for the live-search caret.
	colAccent   = lipgloss.Color("#C026D3") // magenta: logo, selected-row bar
	colSelect   = lipgloss.Color("#00DFD8") // cyan: search caret
	colDanger   = lipgloss.Color("#D9484D") // delete confirmation; error_resolution dot + anchor ring
	colConnBlue = lipgloss.Color("#0E6CDF") // session_summary dot (Connection Layout mockup)

	// Sharing (share-confirm mockup — amber dialog mirrors the delete-confirm
	// danger accent; green marks the kept ✓ SHARED column).
	colShareGreen = lipgloss.Color("#446D57") // ✓ SHARED label
	colAmber      = lipgloss.Color("#BE986B") // share-confirm accent + button
	colAmberDim   = lipgloss.Color("#665C50") // share-confirm border
	colStripped   = colDanger                 // ✗ STRIPPED column label

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

	titleStyle = lipgloss.NewStyle().Foreground(colBright).Bold(true)
	metaStyle  = lipgloss.NewStyle().Foreground(colMeta)
	bodyStyle  = lipgloss.NewStyle().Foreground(colText)

	// CONNECTIONS spine (detail-pane BM25 neighbors, Connection Layout mockup).
	connRing  = lipgloss.NewStyle().Foreground(colDanger)                 // hollow anchor ring
	connSpine = lipgloss.NewStyle().Foreground(lipgloss.Color("#3A3E49")) // vertical line + dash
	connMeta  = lipgloss.NewStyle().Foreground(lipgloss.Color("#8D8D8D")) // dim kind label
	connTitle = lipgloss.NewStyle().Foreground(colBright).Bold(true)

	footerStyle = lipgloss.NewStyle().Foreground(colDim)
	footerKey   = lipgloss.NewStyle().Foreground(colMeta)
	dangerStyle = lipgloss.NewStyle().Bold(true).Foreground(colDanger)

	// Scope filter + sharing (share-registry mockups).
	sharedChip  = lipgloss.NewStyle().Foreground(colSelect)            // ◇ SHARED row/detail chip (cyan)
	selectDot   = lipgloss.NewStyle().Foreground(colAmber)             // ● multi-select marker
	scopeActive = lipgloss.NewStyle().Foreground(colSelect).Bold(true) // selected SCOPE row

	// Share-confirm dialog — amber bordered box, SHARED/STRIPPED columns, warning.
	shareBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(colAmberDim).Padding(0, 2)
	shareTitle = lipgloss.NewStyle().Bold(true).Foreground(colBright)
	shareKept  = lipgloss.NewStyle().Foreground(colShareGreen) // ✓ SHARED label
	shareStrip = lipgloss.NewStyle().Foreground(colStripped)   // ✗ STRIPPED label
	shareWarn  = lipgloss.NewStyle().Foreground(colAmber)
	shareBtn   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0A0A0A")).Background(colAmber).Padding(0, 1)
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
