package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

// TestView_RendersThreePanes drives the full render path at a real terminal size
// and asserts the chrome + sidebar + detail all appear — a no-TTY guard against
// panics and missing panes (ADR-0021).
func TestView_RendersThreePanes(t *testing.T) {
	m := New(&fakeStore{}, nil, "")
	m, _ = upd(t, m, sizeMsg(120, 40))
	m, _ = upd(t, m, countsMsg{counts: map[string]int{"user_rule": 4}, total: 9})
	m, _ = upd(t, m, itemsMsg{gen: m.gen, items: listOf("mem_a")})
	m, _ = upd(t, m, detailMsg{gen: m.detailGen, mem: &store.Memory{
		ID: "mem_a", Title: "Hello", Kind: "user_rule", TaskType: "droids-mem",
		Tags: "tui redesign", What: "what body", Learned: "learned body",
	}, neighbors: []store.Neighbor{{ID: "mem_b", Kind: "task_pattern", Title: "Related lesson", Score: 0.4}}})

	out := m.View()
	for _, want := range []string{"droids", "KINDS", "all", "9 memories", "Hello", "MEMORY", "CONNECTIONS", "current memory", "Related lesson", "SCOPE", "shared", "share"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

// TestView_ShareDialogAndToast drives the two share-flow overlays and asserts
// their key copy renders without panicking (share-registry mockups).
func TestView_ShareDialogAndToast(t *testing.T) {
	m := New(&fakeStore{}, nil, "")
	m, _ = upd(t, m, sizeMsg(120, 40))
	m, _ = upd(t, m, itemsMsg{gen: m.gen, items: []list.Item{
		listItem{id: "a", title: "CI-red but local-green", kind: "error_resolution"},
	}})

	// Share dialog.
	m, _ = upd(t, m, key("ctrl+s"))
	dlg := m.View()
	for _, want := range []string{"Share 1 memory?", "SHARED", "STRIPPED", "can't be fully", "Share 1"} {
		if !strings.Contains(dlg, want) {
			t.Errorf("share dialog missing %q", want)
		}
	}

	// Share success surfaces in the footer status line.
	m.mode = modeNormal
	m, _ = upd(t, m, sharedMsg{n: 3})
	if !strings.Contains(m.View(), "pushed 3 memories") {
		t.Error("footer status missing after sharedMsg")
	}
}
