package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	bodyH := max(1, m.height-6)
	rows := []string{
		m.headerView(),
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

func (m Model) headerView() string {
	left := m.logoText()
	right := headerCount.Render(fmt.Sprintf("%d memories", m.total)) + " " + kbdBadge.Render("⌘K")
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	return chromeRow(m.width).Render(left + strings.Repeat(" ", gap) + right)
}

func (m Model) searchView() string {
	pill := pillStyle.Render("kind:"+kindLabel(m.query.kind)) + " " + hintStyle.Render("⇥ cycle")
	left := m.search.View()
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(pill))
	return chromeRow(m.width).Render(left + strings.Repeat(" ", gap) + pill)
}

// bodyView composes the three borderless columns separated by vertical rules.
func (m Model) bodyView(bodyH int) string {
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

// sidebarView renders the KINDS census.
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
	return b.String()
}

func (m Model) footerView() string {
	left := footerKey.Render("↵") + footerStyle.Render(" open   ") +
		footerKey.Render("^d") + footerStyle.Render(" delete   ") +
		footerKey.Render("⇥") + footerStyle.Render(" kind")
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

// renderConnections draws the vertical connection spine (Connection Layout
// mockup): a hollow anchor ring labeled "current memory", then each neighbor
// as a kind-colored dot + dash + two-line entry (title, dim kind label),
// linked by a spine line down the left column.
func renderConnections(neighbors []store.Neighbor, wrap int) string {
	var b strings.Builder
	b.WriteString(connRing.Render("○"))
	b.WriteByte(' ')
	b.WriteString(connTitle.Render("current memory"))
	if len(neighbors) == 0 {
		b.WriteByte('\n')
		b.WriteString(connSpine.Render("│"))
		b.WriteByte('\n')
		b.WriteString(metaStyle.Render("no related memories"))
		return b.String()
	}
	room := max(10, wrap-2) // "●─" prefix
	for _, n := range neighbors {
		b.WriteByte('\n')
		b.WriteString(connSpine.Render("│"))
		b.WriteByte('\n')
		b.WriteString(connDotStyle(n.Kind).Render("●"))
		b.WriteString(connSpine.Render("─"))
		b.WriteByte(' ')
		b.WriteString(connTitle.Render(truncate(n.Title, room)))
		b.WriteByte('\n')
		b.WriteString(connSpine.Render("│"))
		b.WriteString("  ")
		b.WriteString(connMeta.Render(n.Kind))
	}
	return b.String()
}

// connDotStyle maps a Memory Kind to its spine dot color (Connection Layout
// mockup showed session_summary/error_resolution; task_pattern/user_rule reuse
// existing theme accents rather than inventing new hexes).
func connDotStyle(kind string) lipgloss.Style {
	switch kind {
	case "error_resolution":
		return lipgloss.NewStyle().Foreground(colDanger)
	case "session_summary":
		return lipgloss.NewStyle().Foreground(colConnBlue)
	case "task_pattern":
		return lipgloss.NewStyle().Foreground(colSelect)
	case "user_rule":
		return lipgloss.NewStyle().Foreground(colAccent)
	default:
		return lipgloss.NewStyle().Foreground(colMeta)
	}
}
