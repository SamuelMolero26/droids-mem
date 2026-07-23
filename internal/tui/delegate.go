package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// memDelegate renders one memory as two lines — a bright title and a dim
// `kind · task_type · date` meta — matching the Paper mockup: no borders, a
// full-width subtle fill on the selected row with a magenta→indigo accent bar
// down its left edge (ADR-0021). Shared rows carry a trailing ◇ SHARED chip and
// multi-selected rows a leading amber dot (share-registry mockups); the delegate
// reads the live selection set by reference from the model.
type memDelegate struct {
	selected map[string]bool
}

func (memDelegate) Height() int                         { return 2 }
func (memDelegate) Spacing() int                        { return 1 }
func (memDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d memDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(listItem)
	if !ok {
		return
	}
	width := m.Width()
	cursor := index == m.Index()

	// 2-cell gutter: accent bar (cursor) or amber select-dot, else blank.
	gutter := "  "
	switch {
	case cursor:
		gutter = lipgloss.NewStyle().Foreground(colAccent).Render("▌") + " "
	case d.selected[it.id]:
		gutter = selectDot.Render("●") + " "
	}

	chip := ""
	if it.shared {
		chip = " " + sharedChip.Render("◇ SHARED")
	}
	textW := max(1, width-2-lipgloss.Width(chip))

	title := truncate(it.Title(), textW)
	meta := truncate(it.Description(), max(1, width-2))

	titleStyleN, metaStyleN := rowTitle, rowMeta
	if cursor {
		titleStyleN, metaStyleN = rowTitleSel, rowMeta
	}

	line1 := gutter + titleStyleN.Render(title) + chip
	line2 := "  " + metaStyleN.Render(meta)

	// No background fill — selection is the magenta bar + bright bold title only
	// (the row-fill read as a drop shadow on real terminals).
	fmt.Fprint(w, line1+"\n"+line2)
}

// truncate clips a raw (unstyled) string to n cells with an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}
