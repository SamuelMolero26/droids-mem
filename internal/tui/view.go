package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

var (
	statusStyle = lipgloss.NewStyle().Faint(true)
	labelStyle  = lipgloss.NewStyle().Bold(true)
	confirmBox  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
)

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	switch m.state {
	case stateDetail:
		return m.detail.View() + "\n" + statusStyle.Render("↑/↓ scroll · esc back · ctrl+c quit")
	case stateConfirm:
		t := m.confirmTarget.title
		return m.list.View() + "\n" + confirmBox.Render(fmt.Sprintf("Delete %q? [y/N]", t))
	default:
		kind := m.query.kind
		if kind == "" {
			kind = "all"
		}
		header := m.search.View() + "   " + statusStyle.Render("kind:"+kind+" (tab)")
		footer := statusStyle.Render("enter open · ctrl+d delete · tab kind · esc clear/quit")
		if m.status != "" {
			footer = statusStyle.Render(m.status) + "  " + footer
		}
		return header + "\n" + m.list.View() + "\n" + footer
	}
}

// renderDetail is the full-screen body for one Memory.
func renderDetail(mem *store.Memory) string {
	var b strings.Builder
	b.WriteString(labelStyle.Render(mem.Title) + "\n")
	b.WriteString(statusStyle.Render(fmt.Sprintf("%s · %s · id %s", mem.Kind, mem.TaskType, mem.ID)) + "\n\n")
	b.WriteString(labelStyle.Render("What") + "\n" + mem.What + "\n\n")
	b.WriteString(labelStyle.Render("Learned") + "\n" + mem.Learned + "\n")
	if strings.TrimSpace(mem.Tags) != "" {
		b.WriteString("\n" + labelStyle.Render("Tags") + " " + mem.Tags + "\n")
	}
	return b.String()
}
