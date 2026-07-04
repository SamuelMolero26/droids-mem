package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

// tab-bar geometry, reused by the header and its gradient underline.
const (
	logoGap = 4 // spaces between logo and the tab bar
	tabGap  = 3 // spaces between tabs
)

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	bodyH := max(1, m.height-6)
	rows := []string{
		m.headerView(),
		m.underlineView(),
		m.searchView(),
		hrule(m.width),
		m.bodyView(bodyH),
		hrule(m.width),
		m.footerView(),
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m Model) logoText() string {
	return logoGlyph.Render("◉") + " " + logoStyle.Render("droids") + logoDim.Render("-mem")
}

// tabBar renders the Memories/Graph tabs and returns the rendered string.
func (m Model) tabBar() string {
	mem, graph := tabInactive, tabInactive
	if m.tab == tabMemories {
		mem = tabActive
	} else {
		graph = tabActive
	}
	return mem.Render("Memories") + strings.Repeat(" ", tabGap) + graph.Render("Graph")
}

func (m Model) headerView() string {
	left := m.logoText() + strings.Repeat(" ", logoGap) + m.tabBar()
	right := headerCount.Render(fmt.Sprintf("%d memories", m.total)) + " " + kbdBadge.Render("⌘K")
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	return chromeRow(m.width).Render(left + strings.Repeat(" ", gap) + right)
}

// underlineView draws the magenta→indigo gradient bar beneath the active tab.
func (m Model) underlineView() string {
	offset := lipgloss.Width(m.logoText()) + logoGap
	label := "Memories"
	if m.tab == tabGraph {
		offset += lipgloss.Width("Memories") + tabGap
		label = "Graph"
	}
	bar := gradientBar(lipgloss.Width(label), colGradA, colGradB)
	return chromeRow(m.width).Render(strings.Repeat(" ", offset) + bar)
}

func (m Model) searchView() string {
	pill := pillStyle.Render("kind:"+kindLabel(m.query.kind)) + " " + hintStyle.Render("⇥ cycle")
	left := m.search.View()
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(pill))
	return chromeRow(m.width).Render(left + strings.Repeat(" ", gap) + pill)
}

// bodyView composes the three borderless columns separated by vertical rules,
// or the Graph stub.
func (m Model) bodyView(bodyH int) string {
	if m.tab == tabGraph {
		return lipgloss.NewStyle().Foreground(colMeta).
			Width(m.width).Height(bodyH).Align(lipgloss.Center, lipgloss.Center).
			Render("codebase graph — coming soon\n\ncode graph + memory overlay (ADR-0021 Phase 3)")
	}
	inner := max(20, m.width-sidebarWidth-2)
	detailW := inner * 34 / 100
	listW := inner - detailW

	sidebar := lipgloss.NewStyle().Width(sidebarWidth).Height(bodyH).Render(m.sidebarView())
	list := lipgloss.NewStyle().Width(listW).Height(bodyH).Render(m.list.View())
	detail := lipgloss.NewStyle().Width(detailW).Height(bodyH).Render(m.detail.View())

	row := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, vrule(bodyH), list, vrule(bodyH), detail)
	if m.mode == modeConfirm {
		return row + "\n" + dangerStyle.Render(fmt.Sprintf("Delete %q?  [y/N]", m.confirmTarget.title))
	}
	return row
}

// sidebarView renders the KINDS census and the VIEWS card.
func (m Model) sidebarView() string {
	var b strings.Builder
	b.WriteString(sectionLabel.Render("KINDS"))
	b.WriteString("\n\n")
	for i, k := range sidebarKinds {
		n := m.total
		if k != "" {
			n = m.counts[k]
		}
		label := fmt.Sprintf("%-17s", kindLabel(k))
		count := countStyle.Render(fmt.Sprintf("%d", n))
		if i == m.kindIdx {
			b.WriteString(sidebarSel.Render("▸ " + label))
		} else {
			b.WriteString("  ")
			b.WriteString(sidebarUnsel.Render(label))
		}
		b.WriteString(count)
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString(sectionLabel.Render("VIEWS"))
	b.WriteString("\n\n")
	card := logoGlyph.Render("◈") + " " + "codebase graph\n" + cardHint.Render("^g to open")
	b.WriteString(cardStyle.Width(sidebarWidth - 4).Render(card))
	return b.String()
}

func (m Model) footerView() string {
	left := footerKey.Render("↵") + footerStyle.Render(" open   ") +
		footerKey.Render("^d") + footerStyle.Render(" delete   ") +
		footerKey.Render("⇥") + footerStyle.Render(" kind   ") +
		footerKey.Render("^g") + footerStyle.Render(" graph")
	right := footerKey.Render("esc") + footerStyle.Render(" quit")
	if m.status != "" {
		left = footerStyle.Render(m.status) + "   " + left
	}
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	return chromeRow(m.width).Render(left + strings.Repeat(" ", gap) + right)
}

// renderDetail is the detail-pane body for one Memory: MEMORY label, title,
// outlined tag chips, the prose, and a CONNECTIONS list of the most related
// memories (BM25-neighbor fast-follow, ADR-0021). Content is wrapped to w so the
// non-wrapping viewport doesn't clip long titles and bodies.
func renderDetail(mem *store.Memory, neighbors []store.Neighbor, w int) string {
	if mem == nil {
		return metaStyle.Render("no memory selected")
	}
	wrap := max(10, w)
	var b strings.Builder
	b.WriteString(sectionLabel.Render("MEMORY"))
	b.WriteString("\n\n")
	b.WriteString(titleStyle.Width(wrap).Render(mem.Title))
	b.WriteString("\n\n")

	chips := []string{pillStyle.Render(mem.Kind)}
	if mem.TaskType != "" {
		chips = append(chips, pillStyle.Render(mem.TaskType))
	}
	if len(chips) > 0 {
		b.WriteString(strings.Join(chips, " "))
		b.WriteString("\n\n")
	}

	body := mem.Learned
	if body == "" {
		body = mem.What
	}
	b.WriteString(bodyStyle.Width(wrap).Render(body))
	b.WriteString("\n\n")
	b.WriteString(sectionLabel.Render("CONNECTIONS"))
	b.WriteString("\n")
	b.WriteString(renderConnections(neighbors, wrap))
	return b.String()
}

// renderConnections lists related memories as one line each: a similarity dot,
// the title (truncated to fit), and a dim kind tag. Empty → a dim placeholder.
func renderConnections(neighbors []store.Neighbor, wrap int) string {
	if len(neighbors) == 0 {
		return metaStyle.Render("no related memories")
	}
	var b strings.Builder
	for i, n := range neighbors {
		if i > 0 {
			b.WriteByte('\n')
		}
		tag := metaStyle.Render(n.Kind)
		room := max(10, wrap-lipgloss.Width(tag)-4) // "• " + space before tag
		b.WriteString(connDot.Render("• "))
		b.WriteString(connTitle.Render(truncate(n.Title, room)))
		b.WriteByte(' ')
		b.WriteString(tag)
	}
	return b.String()
}
