package tui

import (
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// TestView_RendersThreePanes drives the full render path at a real terminal size
// and asserts the chrome + sidebar + detail all appear — a no-TTY guard against
// panics and missing panes (ADR-0021).
func TestView_RendersThreePanes(t *testing.T) {
	m := New(&fakeStore{})
	m, _ = upd(t, m, sizeMsg(120, 40))
	m, _ = upd(t, m, countsMsg{counts: map[string]int{"user_rule": 4}, total: 9})
	m, _ = upd(t, m, itemsMsg{gen: m.gen, items: listOf("mem_a")})
	m, _ = upd(t, m, detailMsg{gen: m.detailGen, mem: &store.Memory{
		ID: "mem_a", Title: "Hello", Kind: "user_rule", TaskType: "droids-mem",
		Tags: "tui redesign", What: "what body", Learned: "learned body",
	}, neighbors: []store.Neighbor{{ID: "mem_b", Kind: "task_pattern", Title: "Related lesson", Score: 0.4}}})

	out := m.View()
	for _, want := range []string{"droids", "Memories", "KINDS", "all", "9 memories", "Hello", "MEMORY", "CONNECTIONS", "Related lesson"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

func TestView_GraphTabShowsStub(t *testing.T) {
	m := New(&fakeStore{})
	m, _ = upd(t, m, sizeMsg(120, 40))
	m, _ = upd(t, m, key("ctrl+g"))
	if !strings.Contains(m.View(), "coming soon") {
		t.Error("graph tab did not render the stub")
	}
}
