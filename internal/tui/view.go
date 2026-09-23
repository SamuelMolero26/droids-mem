package tui

import (
	"fmt"
	"strings"
	"time"

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
	if m.newVersion != "" {
		right = shareBtn.Render(fmt.Sprintf("↑ v%s — run `droids-mem upgrade`", m.newVersion)) + " " + right
	}
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

// bodyView composes the three panes as bordered boxes. The focused pane gets a
// cyan border; the others get a dim border. Borders replace the old vrule
// dividers between panes (ADR-0021 update).
func (m Model) bodyView(bodyH int) string {
	if m.mode == modeStats {
		return m.statsView(bodyH)
	}
	inner := max(20, m.width-sidebarWidth)
	detailW := inner * 34 / 100
	listW := inner - detailW

	sidebar := m.paneStyle(focusSidebar, sidebarWidth-2, bodyH-2).Render(m.sidebarView())
	list := m.paneStyle(focusList, listW-2, bodyH-2).Render(m.list.View())
	detail := m.paneStyle(focusDetail, detailW-2, bodyH-2).Render(m.detail.View())

	row := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, list, detail)
	if m.mode == modeConfirm {
		return row + "\n" + dangerStyle.Render(fmt.Sprintf("Delete %q?  [y/N]", m.confirmTarget.title))
	}
	if m.mode == modeShare { // amber confirm dialog, centered over a dimmed list
		return lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.shareDialog())
	}
	return row
}

// paneStyle builds a rounded-border box for one of the three panes. The border
// is highlighted (cyan) when the pane owns keyboard focus, dim otherwise.
func (m Model) paneStyle(pane focus, w, h int) lipgloss.Style {
	s := lipgloss.NewStyle().Width(w).Height(h).Border(lipgloss.RoundedBorder()).BorderForeground(paneBorderColor)
	if m.focus == pane {
		s = s.BorderForeground(paneBorderHighlight)
	}
	return s
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

// statsView renders the full-body usage pane (modeStats). It replaces the
// 3-pane layout when the user hits ^u. Mirrors sidebarView's selection style
// (▸ + sidebarSel) and reuses sectionLabel/metaStyle/countStyle so the pane
// feels like the rest of the inspector. Header shows total payload + optional
// db-file bytes; bars and percentages are shares of that total, so a full bar
// means "most of the corpus" rather than merely "the biggest row". The project
// list is windowed to bodyH — without that the footer hint and the bottom rule
// fall off the screen as soon as the corpus outgrows the terminal. esc handling
// and drill are driven by handleStatsKey — this is view only.
func (m Model) statsView(bodyH int) string {
	var b strings.Builder
	b.WriteString(sectionLabel.Render("USAGE"))
	b.WriteString("\n\n")
	if m.statsErr != nil {
		b.WriteString(dangerStyle.Render("load failed: " + m.statsErr.Error()))
		b.WriteString("\n\n")
		b.WriteString(metaStyle.Render("esc back"))
		return b.String()
	}
	var totalBytes int64
	var totalCount int
	for _, p := range m.stats {
		totalBytes += p.Bytes
		totalCount += p.Count
	}
	payload := formatBytes(totalBytes)
	hdr := metaStyle.Render("payload ") + bodyStyle.Render(payload)
	if m.statsFile != -1 {
		hdr += metaStyle.Render("  ·  file ") + bodyStyle.Render(formatBytes(m.statsFile))
	}
	hdr += countStyle.Render(fmt.Sprintf("  ·  %d %s", totalCount, plural(totalCount, "memory", "memories")))
	b.WriteString(hdr)
	b.WriteString("\n\n")

	if len(m.stats) == 0 {
		b.WriteString(metaStyle.Render("no projects"))
		b.WriteString("\n\n")
		b.WriteString(metaStyle.Render("esc back"))
		return b.String()
	}

	// Drill-down: per-kind split for the selected project.
	if m.statsDrill != "" {
		var proj *store.ProjectSize
		for i := range m.stats {
			if m.stats[i].TaskType == m.statsDrill {
				proj = &m.stats[i]
				break
			}
		}
		if proj == nil {
			b.WriteString(metaStyle.Render("no project " + m.statsDrill))
			b.WriteString("\n\n")
			b.WriteString(metaStyle.Render("esc back"))
			return b.String()
		}
		b.WriteString(sectionLabel.Render(proj.TaskType))
		b.WriteString(" ")
		b.WriteString(countStyle.Render(fmt.Sprintf("%d %s", proj.Count, plural(proj.Count, "memory", "memories"))))
		b.WriteString(metaStyle.Render(" · " + formatBytes(proj.Bytes)))
		b.WriteString("\n\n")
		if len(proj.ByKind) == 0 {
			b.WriteString(metaStyle.Render("no kinds"))
			b.WriteString("\n\n")
			b.WriteString(metaStyle.Render("esc back"))
			return b.String()
		}
		// Kinds are measured against the project, not the corpus: in here the
		// question is "what shape is this project", not "how big is it".
		for _, k := range proj.ByKind {
			b.WriteString(usageRow(k.Kind, 17, k.Count, k.Bytes, proj.Bytes, 10, false))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(metaStyle.Render("esc back"))
		return b.String()
	}

	// Project list, bytes-desc, windowed around the cursor so it always stays
	// on screen. 6 lines of chrome: label, blank, header, blank … blank, hint.
	avail := max(1, bodyH-6)
	start := 0
	if len(m.stats) > avail {
		start = min(max(0, m.statsIdx-avail/2), len(m.stats)-avail)
	}
	end := min(len(m.stats), start+avail)
	for i := start; i < end; i++ {
		p := m.stats[i]
		b.WriteString(usageRow(p.TaskType, 20, p.Count, p.Bytes, totalBytes, 12, i == m.statsIdx))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	hint := "↑/↓ navigate  enter drill  esc back"
	if end-start < len(m.stats) {
		hint += fmt.Sprintf("  ·  %d–%d of %d", start+1, end, len(m.stats))
	}
	b.WriteString(metaStyle.Render(hint))
	return b.String()
}

// usageRow renders one label/count/bytes/share line for the usage pane. share
// is the denominator the percentage and bar are measured against — the corpus
// total in the project list, the project's own payload in the kind drill — so
// the bar answers "how much of the whole is this" either way.
func usageRow(label string, width, count int, bytes, share int64, barW int, sel bool) string {
	var b strings.Builder
	name := fmt.Sprintf("%-*s", width, truncate(label, width))
	if sel {
		b.WriteString(sidebarSel.Render("▸ " + name))
	} else {
		b.WriteString("  ")
		b.WriteString(sidebarUnsel.Render(name))
	}
	var pct float64
	if share > 0 {
		pct = float64(bytes) / float64(share) * 100
	}
	b.WriteString(countStyle.Render(fmt.Sprintf("%3d", count)))
	b.WriteString("  ")
	b.WriteString(metaStyle.Render(fmt.Sprintf("%8s", formatBytes(bytes))))
	b.WriteString("  ")
	b.WriteString(metaStyle.Render(fmt.Sprintf("%5.1f%%", pct)))
	// Eighths, not whole cells: one project usually dominates the corpus, so
	// at whole-cell resolution everything below 1/barW collapses to the same
	// single block and the bar stops distinguishing 11% from 1%.
	eighths := int(pct / 100 * float64(barW) * 8)
	if eighths == 0 && bytes > 0 { // a non-empty project never renders as nothing
		eighths = 1
	}
	if eighths > 0 {
		bar := strings.Repeat("█", eighths/8)
		if rem := eighths % 8; rem > 0 {
			bar += string(barEighths[rem])
		}
		style := barUnsel
		if sel {
			style = barSel
		}
		b.WriteString(" " + style.Render(bar))
	}
	return b.String()
}

// barEighths indexes partial block glyphs by eighth, so a bar can end mid-cell.
var barEighths = []rune(" ▏▎▍▌▋▊▉█")

func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
	}
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
		footerKey.Render("^u") + footerStyle.Render(" usage   ") +
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
	if m.status != "" {
		left = footerStyle.Render(m.status) + "   " + left
	}
	right := footerKey.Render("q") + footerStyle.Render(" quit")
	if m.query.scope != "" {
		right = footerKey.Render("esc") + footerStyle.Render(" clear filter")
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
	if mem.Scope == "shared" { // shared memories carry a cyan ◇ chip (scope-filter mockup)
		chips = append(chips, sharedChip.Render("◇ shared"))
	}
	b.WriteString(strings.Join(chips, " "))
	b.WriteString("\n\n")

	// authored_at is provenance, not recency: it only earns a line when it
	// diverges from created_at, which happens on an imported (scope='shared')
	// row — ImportShared re-stamps created_at to the local import time but
	// carries the peer's original authoring date forward.
	if mem.AuthoredAt != 0 && mem.AuthoredAt != mem.CreatedAt {
		authored := time.Unix(mem.AuthoredAt, 0).UTC().Format("2006-01-02")
		b.WriteString(metaStyle.Render("authored " + authored))
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
