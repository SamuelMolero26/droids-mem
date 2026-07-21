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

	// Graph tab replaces the normal three-pane layout entirely.
	if m.mode == modeGraph {
		return m.graphView()
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

// graphView renders the graph tab: header, query input, results viewport.
func (m Model) graphView() string {
	h := max(1, m.height-6)
	outH := max(1, h-2)
	m.graphOut.Height = outH
	out := m.graphOut.View()
	pad := lipgloss.NewStyle().Padding(0, 2)
	rows := []string{
		m.headerView(),
		pad.Render(m.graphIn.View()),
		hrule(m.width),
		pad.Render(out),
		hrule(m.width),
		footerStyle.Render("  enter=query  esc=back  ↑↓=scroll"),
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
	pill := pillStyle.Render("kind:" + kindLabel(m.query.kind))
	if m.query.scope != "" { // active scope filter shows as a cyan chip left of kind
		pill = sharedChip.Render("scope:"+m.query.scope+" ×") + " " + pill
	}
	pill += " " + hintStyle.Render("⇥ cycle")
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
	if m.mode == modeShare { // amber confirm dialog, centered over a dimmed list
		return lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.shareDialog())
	}
	return row
}

// shareDialog is the share-confirm modal (share-registry mockup, registry chrome
// dropped): what will be shared, the SHARED/STRIPPED split, and the public-pool
// warning. It flips the targeted memories into the git-tracked shared pool.
func (m Model) shareDialog() string {
	ids := m.shareTargets()
	n := len(ids)
	var b strings.Builder
	b.WriteString(shareWarn.Render("⚠ ") + shareTitle.Render(fmt.Sprintf("Share %d %s?", n, plural(n, "memory", "memories"))))
	b.WriteString("\n")
	b.WriteString(metaStyle.Render("into the shared pool · public"))
	b.WriteString("\n\n")

	// Rows: title + kind chip for each target (cap the list so the box stays sane).
	for i, id := range ids {
		if i >= 6 {
			b.WriteString(metaStyle.Render(fmt.Sprintf("  …and %d more", n-i)))
			b.WriteByte('\n')
			break
		}
		title := id
		if it, ok := m.itemByID(id); ok {
			title = it.title
			b.WriteString(selectDot.Render("● ") + bodyStyle.Render(truncate(title, 40)) + " " + pillStyle.Render(it.kind))
		} else {
			b.WriteString(selectDot.Render("● ") + bodyStyle.Render(truncate(title, 40)))
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString(shareKept.Render("✓ SHARED  ") + metaStyle.Render("content · tags · kind"))
	b.WriteString("\n")
	b.WriteString(shareStrip.Render("✗ STRIPPED ") + metaStyle.Render("local paths · session ids · timestamps"))
	b.WriteString("\n\n")
	b.WriteString(shareWarn.Render("⚠ Shared copies enter the git-tracked pool and can't be fully\n  retracted — anyone who pulled keeps their copy."))
	b.WriteString("\n\n")
	b.WriteString(metaStyle.Render("Memory repo:") + " " + m.repoInput.View())
	b.WriteString("\n\n")
	b.WriteString(footerKey.Render("esc") + footerStyle.Render(" cancel") + "   " + shareBtn.Render(fmt.Sprintf("↵ Push %d", n)))
	return shareBox.Render(b.String())
}

// itemByID finds a loaded list row by id (for the share dialog's title lookup).
func (m Model) itemByID(id string) (listItem, bool) {
	for _, raw := range m.list.Items() {
		if it, ok := raw.(listItem); ok && it.id == id {
			return it, true
		}
	}
	return listItem{}, false
}

// sidebarView renders the KINDS census (arrow-navigable) and the SCOPE section
// (cycled by `s`, not the cursor) — the scope-filter mockup.
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
	b.WriteString(sectionLabel.Render("SCOPE"))
	b.WriteString("\n\n")
	personal := max(0, m.total-m.shared)
	for _, sc := range scopeFilters {
		n := m.total
		switch sc {
		case "personal":
			n = personal
		case "shared":
			n = m.shared
		}
		label := fmt.Sprintf("%-17s", scopeLabel(sc))
		count := countStyle.Render(fmt.Sprintf("%d", n))
		if sc == m.query.scope { // active filter — highlighted, not cursor-marked
			b.WriteString(scopeActive.Render("▸ " + label))
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
		footerKey.Render("^s") + footerStyle.Render(" share   ") +
		footerKey.Render("^p") + footerStyle.Render(" pull   ") +
		footerKey.Render("s") + footerStyle.Render(" scope   ") +
		footerKey.Render("^d") + footerStyle.Render(" delete")
	if it, ok := m.list.SelectedItem().(listItem); ok && it.shared {
		left += "   " + footerKey.Render("^x") + footerStyle.Render(" unshare")
	}
	if n := len(m.selected); n > 0 {
		left = footerStyle.Render(fmt.Sprintf("%d selected", n)) + "   " + left
	}
	mid := ""
	if m.graphQ != nil {
		mid = footerStyle.Render("   ") + footerKey.Render("^g") + footerStyle.Render(" graph")
	}
	if m.status != "" {
		left = footerStyle.Render(m.status) + "   " + left
	}
	right := footerKey.Render("q") + footerStyle.Render(" quit")
	if m.query.scope != "" {
		right = footerKey.Render("esc") + footerStyle.Render(" clear filter")
	}
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(mid)-lipgloss.Width(right))
	return chromeRow(m.width).Render(left + mid + strings.Repeat(" ", gap) + right)
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
	if mem.Scope == "shared" { // shared memories carry a cyan ◇ chip (scope-filter mockup)
		chips = append(chips, sharedChip.Render("◇ shared"))
	}
	b.WriteString(strings.Join(chips, " "))
	b.WriteString("\n\n")

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
