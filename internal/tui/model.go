package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/samuelmolero/droids-mem/internal/store"
)

const (
	searchDebounce = 200 * time.Millisecond
	minSearchRunes = 3   // below this, search is skipped and the list falls back to List
	listPageLimit  = 100 // static-filter page size
	searchLimit    = 20  // live-search result cap
)

type viewState int

const (
	stateList viewState = iota
	stateDetail
	stateConfirm
)

// queryDesc is the single source of truth for what the list shows. A non-empty
// search (≥ minSearchRunes) drives store.Search; otherwise store.List with the
// kind/task_type filters. Both the initial load and every refresh (filter
// change, post-delete) re-issue this descriptor, so the view never refreshes
// into a different shape than the user was looking at (ADR-0015).
type queryDesc struct {
	search   string
	kind     string
	taskType string
}

// --- messages ---

type itemsMsg struct {
	gen   int
	items []list.Item
	err   error
}
type detailMsg struct {
	mem *store.Memory
	err error
}
type deletedMsg struct{ err error }
type tickMsg struct{ gen int }

// Model is the root BubbleTea model for the Memory inspector.
type Model struct {
	store memStore
	state viewState

	list   list.Model
	search textinput.Model
	detail viewport.Model

	query queryDesc

	// gen is the monotonic generation counter unifying search debounce and
	// stale-result rejection: any load is stamped with the gen current when it
	// was scheduled, and results/ticks whose gen != m.gen are dropped.
	gen int

	confirmTarget listItem
	status        string

	width, height int
	ready         bool
}

// New builds an inspector model over the given store.
func New(s memStore) Model {
	ti := textinput.New()
	ti.Placeholder = "search (≥3 chars)…"
	ti.Prompt = "/ "
	ti.Focus()

	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Memories"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false) // we drive search ourselves via store.Search
	l.SetShowStatusBar(false)

	return Model{
		store:  s,
		state:  stateList,
		list:   l,
		search: ti,
	}
}

func (m Model) Init() tea.Cmd {
	// gen 0 initial load.
	return m.loadCmd(m.gen, m.query)
}

// kindCycle is the static kind filter rotation driven by tab.
var kindCycle = []string{"", "error_resolution", "task_pattern", "user_rule", "session_summary"}

func nextKind(cur string) string {
	for i, k := range kindCycle {
		if k == cur {
			return kindCycle[(i+1)%len(kindCycle)]
		}
	}
	return ""
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.ready = true
		return m, nil

	case itemsMsg:
		if msg.gen != m.gen {
			return m, nil // superseded load
		}
		if msg.err != nil {
			m.status = "load failed: " + msg.err.Error()
			return m, nil
		}
		m.status = ""
		m.list.SetItems(msg.items)
		// clamp cursor to new bounds (post-delete the list is shorter).
		if n := len(msg.items); n > 0 && m.list.Index() >= n {
			m.list.Select(n - 1)
		}
		return m, nil

	case detailMsg:
		if msg.err != nil {
			m.status = "open failed: " + msg.err.Error()
			m.state = stateList
			return m, nil
		}
		if msg.mem == nil {
			m.status = "memory not found (deleted?)"
			m.state = stateList
			return m, nil
		}
		m.detail.SetContent(renderDetail(msg.mem))
		m.detail.GotoTop()
		return m, nil

	case deletedMsg:
		if msg.err != nil {
			m.status = "delete failed: " + msg.err.Error()
		} else {
			m.status = "deleted"
		}
		m.state = stateList
		m.gen++
		return m, m.loadCmd(m.gen, m.query)

	case tickMsg:
		if msg.gen != m.gen {
			return m, nil // a newer keystroke superseded this debounce window
		}
		return m, m.loadCmd(m.gen, m.query)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.state {
	case stateDetail:
		switch msg.String() {
		case "esc", "q":
			m.state = stateList
			return m, nil
		}
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd

	case stateConfirm:
		switch msg.String() {
		case "y", "Y":
			id := m.confirmTarget.id
			m.state = stateList
			return m, m.deleteCmd(id)
		default: // n / esc / anything else cancels
			m.state = stateList
			return m, nil
		}

	default: // stateList
		return m.handleListKey(msg)
	}
}

func (m Model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if it, ok := m.list.SelectedItem().(listItem); ok {
			m.state = stateDetail
			m.status = ""
			return m, m.detailCmd(it.id)
		}
		return m, nil
	case "ctrl+d":
		if it, ok := m.list.SelectedItem().(listItem); ok {
			m.confirmTarget = it
			m.state = stateConfirm
		}
		return m, nil
	case "tab":
		m.query.kind = nextKind(m.query.kind)
		m.gen++
		return m, m.loadCmd(m.gen, m.query)
	case "esc":
		if m.search.Value() != "" {
			m.search.SetValue("")
			m.query.search = ""
			m.gen++
			return m, m.loadCmd(m.gen, m.query)
		}
		return m, tea.Quit
	case "up", "down", "pgup", "pgdown", "home", "end":
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	// Everything else is text input into the search box. A changed value bumps
	// gen and schedules a debounce; the actual load fires on the matching tick.
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	if v := m.search.Value(); v != m.query.search {
		m.query.search = v
		m.gen++
		return m, tea.Batch(cmd, debounceCmd(m.gen))
	}
	return m, cmd
}

// --- commands ---

func (m Model) loadCmd(gen int, q queryDesc) tea.Cmd {
	s := m.store
	return func() tea.Msg {
		ctx := context.Background()
		if len([]rune(q.search)) >= minSearchRunes {
			resp, err := s.Search(ctx, store.SearchRequest{
				Query: q.search, Kind: q.kind, TaskType: q.taskType, Limit: searchLimit,
			})
			if err != nil {
				return itemsMsg{gen: gen, err: err}
			}
			return itemsMsg{gen: gen, items: itemsFromSearch(resp)}
		}
		resp, err := s.List(ctx, store.ListRequest{Kind: q.kind, TaskType: q.taskType, Limit: listPageLimit})
		if err != nil {
			return itemsMsg{gen: gen, err: err}
		}
		return itemsMsg{gen: gen, items: itemsFromList(resp)}
	}
}

func (m Model) detailCmd(id string) tea.Cmd {
	s := m.store
	return func() tea.Msg {
		mem, err := s.GetRow(context.Background(), id) // non-counting: no Expand signal
		return detailMsg{mem: mem, err: err}
	}
}

func (m Model) deleteCmd(id string) tea.Cmd {
	s := m.store
	return func() tea.Msg {
		_, err := s.Prune(context.Background(), store.PruneRequest{ID: id, Apply: true})
		return deletedMsg{err: err}
	}
}

func debounceCmd(gen int) tea.Cmd {
	return tea.Tick(searchDebounce, func(time.Time) tea.Msg { return tickMsg{gen: gen} })
}

func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	listH := m.height - 3 // search line + status line + padding
	if listH < 1 {
		listH = 1
	}
	m.list.SetSize(m.width, listH)
	m.detail = viewport.New(m.width, m.height-2)
}
