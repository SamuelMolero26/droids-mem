package tui

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

type fakeStore struct {
	listResp   *store.ListResponse
	searchResp *store.SearchResponse
	getResp    *store.Memory

	listCalls, searchCalls, getCalls, pruneCalls int
	lastList                                     store.ListRequest
	lastSearch                                   store.SearchRequest
	lastGetID                                    string
	lastPrune                                    store.PruneRequest
}

func (f *fakeStore) List(_ context.Context, r store.ListRequest) (*store.ListResponse, error) {
	f.listCalls++
	f.lastList = r
	if f.listResp == nil {
		return &store.ListResponse{}, nil
	}
	return f.listResp, nil
}
func (f *fakeStore) Search(_ context.Context, r store.SearchRequest) (*store.SearchResponse, error) {
	f.searchCalls++
	f.lastSearch = r
	if f.searchResp == nil {
		return &store.SearchResponse{}, nil
	}
	return f.searchResp, nil
}
func (f *fakeStore) GetRow(_ context.Context, id string) (*store.Memory, error) {
	f.getCalls++
	f.lastGetID = id
	return f.getResp, nil
}
func (f *fakeStore) Prune(_ context.Context, r store.PruneRequest) (*store.PruneResponse, error) {
	f.pruneCalls++
	f.lastPrune = r
	return &store.PruneResponse{Status: "pruned", Count: 1}, nil
}

func upd(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	tm, cmd := m.Update(msg)
	mm, ok := tm.(Model)
	if !ok {
		t.Fatalf("Update returned non-Model")
	}
	return mm, cmd
}

func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

func listOf(ids ...string) []list.Item {
	out := make([]list.Item, 0, len(ids))
	for _, id := range ids {
		out = append(out, listItem{id: id, title: id})
	}
	return out
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestInit_LoadsViaList(t *testing.T) {
	fs := &fakeStore{listResp: &store.ListResponse{Memories: []store.Memory{{ID: "mem_a"}, {ID: "mem_b"}}}}
	m := New(fs)
	msg, ok := runCmd(m.Init()).(itemsMsg)
	if !ok {
		t.Fatal("Init did not produce itemsMsg")
	}
	if msg.gen != 0 || len(msg.items) != 2 {
		t.Errorf("init load = gen %d, %d items; want gen 0, 2 items", msg.gen, len(msg.items))
	}
	if fs.listCalls != 1 || fs.searchCalls != 0 {
		t.Errorf("init used list=%d search=%d, want list=1 search=0", fs.listCalls, fs.searchCalls)
	}
}

func TestItemsMsg_StaleGenDropped(t *testing.T) {
	m := New(&fakeStore{})
	m.gen = 5
	m, _ = upd(t, m, itemsMsg{gen: 5, items: listOf("a", "b")})
	if len(m.list.Items()) != 2 {
		t.Fatalf("current-gen items not applied: %d", len(m.list.Items()))
	}
	m, _ = upd(t, m, itemsMsg{gen: 4, items: listOf("x")})
	if len(m.list.Items()) != 2 {
		t.Errorf("stale items applied: list now %d, want still 2", len(m.list.Items()))
	}
}

func TestLoadCmd_BelowMinUsesList(t *testing.T) {
	fs := &fakeStore{}
	m := New(fs)
	runCmd(m.loadCmd(0, queryDesc{search: "cr"})) // 2 runes
	if fs.listCalls != 1 || fs.searchCalls != 0 {
		t.Errorf("2-rune query used search; want list. list=%d search=%d", fs.listCalls, fs.searchCalls)
	}
}

func TestLoadCmd_AtMinUsesSearch(t *testing.T) {
	fs := &fakeStore{}
	m := New(fs)
	runCmd(m.loadCmd(0, queryDesc{search: "crm"})) // 3 runes
	if fs.searchCalls != 1 || fs.lastSearch.Query != "crm" {
		t.Errorf("3-rune query did not search: search=%d q=%q", fs.searchCalls, fs.lastSearch.Query)
	}
}

func TestTick_FiresOnlyForCurrentGen(t *testing.T) {
	m := New(&fakeStore{})
	m.gen = 3
	if _, cmd := upd(t, m, tickMsg{gen: 2}); cmd != nil {
		t.Error("stale tick fired a load")
	}
	_, cmd := upd(t, m, tickMsg{gen: 3})
	if cmd == nil {
		t.Fatal("current tick did not fire a load")
	}
	if msg, ok := runCmd(cmd).(itemsMsg); !ok || msg.gen != 3 {
		t.Errorf("current tick load = %v, want itemsMsg gen 3", msg)
	}
}

func TestTyping_BumpsGenAndDebounces(t *testing.T) {
	m := New(&fakeStore{})
	startGen := m.gen
	m, _ = upd(t, m, key("c"))
	m, _ = upd(t, m, key("r"))
	m, _ = upd(t, m, key("m"))
	if m.gen != startGen+3 {
		t.Errorf("gen after 3 keystrokes = %d, want %d", m.gen, startGen+3)
	}
	if m.query.search != "crm" {
		t.Errorf("descriptor search = %q, want crm", m.query.search)
	}
}

func TestEnter_OpensDetailViaGetRow(t *testing.T) {
	fs := &fakeStore{getResp: &store.Memory{ID: "mem_x", Title: "T", What: "w", Learned: "l"}}
	m := New(fs)
	m.list.SetItems(listOf("mem_x"))
	m, cmd := upd(t, m, key("enter"))
	if m.state != stateDetail {
		t.Fatalf("state = %v, want detail", m.state)
	}
	if _, ok := runCmd(cmd).(detailMsg); !ok {
		t.Fatal("enter did not produce detailMsg")
	}
	if fs.getCalls != 1 || fs.lastGetID != "mem_x" {
		t.Errorf("GetRow calls=%d id=%q, want 1/mem_x", fs.getCalls, fs.lastGetID)
	}
}

func TestDelete_ConfirmPruneReload(t *testing.T) {
	fs := &fakeStore{}
	m := New(fs)
	m.list.SetItems(listOf("mem_x"))

	m, _ = upd(t, m, key("ctrl+d"))
	if m.state != stateConfirm || m.confirmTarget.id != "mem_x" {
		t.Fatalf("ctrl+d → state=%v target=%q, want confirm/mem_x", m.state, m.confirmTarget.id)
	}

	m, cmd := upd(t, m, key("y"))
	if m.state != stateList {
		t.Errorf("post-confirm state = %v, want list", m.state)
	}
	if _, ok := runCmd(cmd).(deletedMsg); !ok {
		t.Fatal("confirm-y did not produce deletedMsg")
	}
	if fs.pruneCalls != 1 || fs.lastPrune.ID != "mem_x" || !fs.lastPrune.Apply {
		t.Errorf("prune = %d %+v, want 1 call ID=mem_x Apply=true", fs.pruneCalls, fs.lastPrune)
	}

	// deletedMsg triggers a reload of the current descriptor.
	genBefore := m.gen
	m, cmd = upd(t, m, deletedMsg{})
	if m.gen != genBefore+1 {
		t.Errorf("deletedMsg did not bump gen: %d → %d", genBefore, m.gen)
	}
	if _, ok := runCmd(cmd).(itemsMsg); !ok {
		t.Error("deletedMsg did not reload")
	}
}

func TestConfirm_CancelDoesNotPrune(t *testing.T) {
	fs := &fakeStore{}
	m := New(fs)
	m.list.SetItems(listOf("mem_x"))
	m, _ = upd(t, m, key("ctrl+d"))
	m, _ = upd(t, m, key("n"))
	if m.state != stateList {
		t.Errorf("cancel → state %v, want list", m.state)
	}
	if fs.pruneCalls != 0 {
		t.Errorf("cancel pruned anyway: %d calls", fs.pruneCalls)
	}
}

func TestItemsMsg_ClampsCursor(t *testing.T) {
	m := New(&fakeStore{})
	m.list.SetItems(listOf("a", "b", "c"))
	m.list.Select(2)
	m, _ = upd(t, m, itemsMsg{gen: m.gen, items: listOf("a")})
	if idx := m.list.Index(); idx != 0 {
		t.Errorf("cursor after shrink = %d, want 0 (clamped)", idx)
	}
}

func TestTab_CyclesKindAndReloads(t *testing.T) {
	fs := &fakeStore{}
	m := New(fs)
	m, cmd := upd(t, m, key("tab"))
	if m.query.kind != "error_resolution" {
		t.Errorf("kind after one tab = %q, want error_resolution", m.query.kind)
	}
	runCmd(cmd)
	if fs.lastList.Kind != "error_resolution" {
		t.Errorf("reload kind filter = %q, want error_resolution", fs.lastList.Kind)
	}
}
