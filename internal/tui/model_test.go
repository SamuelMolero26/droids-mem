package tui

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

type fakeStore struct {
	listResp      *store.ListResponse
	searchResp    *store.SearchResponse
	getResp       *store.Memory
	countsResp    *store.CountsResponse
	neighborsResp []store.Neighbor

	listCalls, searchCalls, getCalls, pruneCalls, countsCalls, neighborsCalls int
	lastList                                                                  store.ListRequest
	lastSearch                                                                store.SearchRequest
	lastGetID                                                                 string
	lastPrune                                                                 store.PruneRequest
	lastNeighborsID                                                           string
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
func (f *fakeStore) Counts(_ context.Context) (*store.CountsResponse, error) {
	f.countsCalls++
	if f.countsResp == nil {
		return &store.CountsResponse{ByKind: map[string]int{}}, nil
	}
	return f.countsResp, nil
}
func (f *fakeStore) Neighbors(_ context.Context, id string, _ int) ([]store.Neighbor, error) {
	f.neighborsCalls++
	f.lastNeighborsID = id
	return f.neighborsResp, nil
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

// collect runs a cmd and flattens tea.Batch into the individual messages, so a
// test can find the message it cares about among a batched result.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch v := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range v {
			out = append(out, collect(c)...)
		}
		return out
	case nil:
		return nil
	default:
		return []tea.Msg{v}
	}
}

func firstOf[T tea.Msg](msgs []tea.Msg) (T, bool) {
	for _, msg := range msgs {
		if t, ok := msg.(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}

func sizeMsg(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }

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
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+g":
		return tea.KeyMsg{Type: tea.KeyCtrlG}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestInit_LoadsListAndCounts(t *testing.T) {
	fs := &fakeStore{
		listResp:   &store.ListResponse{Memories: []store.Memory{{ID: "mem_a"}, {ID: "mem_b"}}},
		countsResp: &store.CountsResponse{ByKind: map[string]int{"user_rule": 3}, Total: 3},
	}
	m := New(fs)
	msgs := collect(m.Init())
	items, ok := firstOf[itemsMsg](msgs)
	if !ok || items.gen != 0 || len(items.items) != 2 {
		t.Errorf("init list load = %+v, want gen 0 / 2 items", items)
	}
	if _, ok := firstOf[countsMsg](msgs); !ok {
		t.Error("init did not load counts")
	}
	if fs.listCalls != 1 || fs.countsCalls != 1 {
		t.Errorf("init calls list=%d counts=%d, want 1/1", fs.listCalls, fs.countsCalls)
	}
}

func TestCountsMsg_StoredForSidebar(t *testing.T) {
	m := New(&fakeStore{})
	m, _ = upd(t, m, countsMsg{counts: map[string]int{"task_pattern": 7}, total: 12})
	if m.total != 12 || m.counts["task_pattern"] != 7 {
		t.Errorf("counts not stored: total=%d task_pattern=%d", m.total, m.counts["task_pattern"])
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

func TestTab_CyclesPaneFocus(t *testing.T) {
	m := New(&fakeStore{}) // starts focusList
	m, _ = upd(t, m, key("tab"))
	if m.focus != focusDetail {
		t.Errorf("tab from list → %v, want detail", m.focus)
	}
	m, _ = upd(t, m, key("tab"))
	if m.focus != focusSidebar {
		t.Errorf("tab from detail → %v, want sidebar", m.focus)
	}
	m, _ = upd(t, m, key("shift+tab"))
	if m.focus != focusDetail {
		t.Errorf("shift+tab from sidebar → %v, want detail", m.focus)
	}
}

func TestSidebarNav_ChangesKindAndReloads(t *testing.T) {
	fs := &fakeStore{}
	m := New(fs)
	m.focus = focusSidebar
	m, cmd := upd(t, m, key("down")) // all → session_summary
	if m.query.kind != "session_summary" {
		t.Errorf("kind after one down = %q, want session_summary", m.query.kind)
	}
	runCmd(cmd)
	if fs.lastList.Kind != "session_summary" {
		t.Errorf("reload kind filter = %q, want session_summary", fs.lastList.Kind)
	}
}

func TestListNav_DetailFollowsCursor(t *testing.T) {
	fs := &fakeStore{getResp: &store.Memory{ID: "mem_b", Title: "T"}}
	m := New(fs)
	m.focus = focusList
	m.list.SetItems(listOf("mem_a", "mem_b"))
	m, cmd := upd(t, m, key("down")) // move to mem_b → detail should load it
	msgs := collect(cmd)
	if _, ok := firstOf[detailMsg](msgs); !ok {
		t.Fatal("list nav did not load detail")
	}
	if fs.lastGetID != "mem_b" {
		t.Errorf("detail loaded id=%q, want mem_b", fs.lastGetID)
	}
	_ = m
}

func TestDetailLoad_FetchesNeighbors(t *testing.T) {
	fs := &fakeStore{
		getResp:       &store.Memory{ID: "mem_b", Title: "T"},
		neighborsResp: []store.Neighbor{{ID: "mem_c", Title: "near", Score: 0.5}},
	}
	m := New(fs)
	m.focus = focusList
	m.list.SetItems(listOf("mem_a", "mem_b"))
	_, cmd := upd(t, m, key("down")) // move to mem_b → detail loads it + neighbors
	msg, ok := firstOf[detailMsg](collect(cmd))
	if !ok {
		t.Fatal("list nav did not load detail")
	}
	if fs.neighborsCalls != 1 || fs.lastNeighborsID != "mem_b" {
		t.Errorf("neighbors fetch = %d id=%q, want 1 mem_b", fs.neighborsCalls, fs.lastNeighborsID)
	}
	if len(msg.neighbors) != 1 || msg.neighbors[0].ID != "mem_c" {
		t.Errorf("detailMsg neighbors = %+v, want [mem_c]", msg.neighbors)
	}
}

func TestEnter_FocusesDetailPane(t *testing.T) {
	m := New(&fakeStore{})
	m.focus = focusList
	m, _ = upd(t, m, key("enter"))
	if m.focus != focusDetail {
		t.Errorf("enter → focus %v, want detail", m.focus)
	}
}

func TestCtrlG_TogglesGraphTab(t *testing.T) {
	m := New(&fakeStore{})
	m, _ = upd(t, m, key("ctrl+g"))
	if m.tab != tabGraph {
		t.Errorf("ctrl+g → tab %v, want graph", m.tab)
	}
	m, _ = upd(t, m, key("ctrl+g"))
	if m.tab != tabMemories {
		t.Errorf("second ctrl+g → tab %v, want memories", m.tab)
	}
}

func TestDelete_ConfirmPruneReload(t *testing.T) {
	fs := &fakeStore{}
	m := New(fs)
	m.list.SetItems(listOf("mem_x"))

	m, _ = upd(t, m, key("ctrl+d"))
	if m.mode != modeConfirm || m.confirmTarget.id != "mem_x" {
		t.Fatalf("ctrl+d → mode=%v target=%q, want confirm/mem_x", m.mode, m.confirmTarget.id)
	}

	m, cmd := upd(t, m, key("y"))
	if m.mode != modeNormal {
		t.Errorf("post-confirm mode = %v, want normal", m.mode)
	}
	if _, ok := runCmd(cmd).(deletedMsg); !ok {
		t.Fatal("confirm-y did not produce deletedMsg")
	}
	if fs.pruneCalls != 1 || fs.lastPrune.ID != "mem_x" || !fs.lastPrune.Apply {
		t.Errorf("prune = %d %+v, want 1 call ID=mem_x Apply=true", fs.pruneCalls, fs.lastPrune)
	}

	// deletedMsg triggers a reload of the current descriptor + a counts refresh.
	genBefore := m.gen
	m, cmd = upd(t, m, deletedMsg{})
	if m.gen != genBefore+1 {
		t.Errorf("deletedMsg did not bump gen: %d → %d", genBefore, m.gen)
	}
	if _, ok := firstOf[itemsMsg](collect(cmd)); !ok {
		t.Error("deletedMsg did not reload the list")
	}
}

func TestConfirm_CancelDoesNotPrune(t *testing.T) {
	fs := &fakeStore{}
	m := New(fs)
	m.list.SetItems(listOf("mem_x"))
	m, _ = upd(t, m, key("ctrl+d"))
	m, _ = upd(t, m, key("n"))
	if m.mode != modeNormal {
		t.Errorf("cancel → mode %v, want normal", m.mode)
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
