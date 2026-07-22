package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

const (
	searchDebounce = 200 * time.Millisecond
	minSearchRunes = 3   // below this, search is skipped and the list falls back to List
	listPageLimit  = 100 // static-filter page size
	searchLimit    = 20  // live-search result cap
	sidebarWidth   = 26  // fixed sidebar column
)

// focus is which of the three panes owns the arrow keys (ADR-0021). Printable
// keys always feed search regardless of focus; tab cycles this.
type focus int

const (
	focusSidebar focus = iota
	focusList
	focusDetail
)

// mode is a modal sub-state that steals keys from the normal flow.
type mode int

const (
	modeNormal mode = iota
	modeConfirm
)

// sidebarKinds is the fixed KINDS rotation shown in the sidebar; "" is the
// "all" row (no kind filter). Order matches the mockup.
var sidebarKinds = []string{"", "session_summary", "task_pattern", "user_rule", "error_resolution"}

func kindLabel(k string) string {
	if k == "" {
		return "all"
	}
	return k
}

// queryDesc is the single source of truth for what the list shows (ADR-0015). A
// non-empty search (≥ minSearchRunes) drives store.Search; otherwise store.List
// with the kind filter. Both the initial load and every refresh re-issue it.
type queryDesc struct {
	search string
	kind   string
}

// --- messages ---

type itemsMsg struct {
	gen   int
	items []list.Item
	err   error
}
type detailMsg struct {
	gen       int
	mem       *store.Memory
	neighbors []store.Neighbor
	err       error
}
type countsMsg struct {
	counts map[string]int
	total  int
	err    error
}
type deletedMsg struct{ err error }
type tickMsg struct{ gen int }

// Model is the root BubbleTea model for the Memory inspector.
type Model struct {
	store memStore
	mode  mode
	focus focus

	search textinput.Model
	list   list.Model
	detail viewport.Model

	kindIdx int
	counts  map[string]int
	total   int

	query queryDesc

	// gen unifies list-load debounce and stale-result rejection; detailGen does
	// the same for the cursor-following detail pane (a separate stream so a list
	// reload and a detail load never fight over one counter).
	gen       int
	detailGen int

	loadedDetailID string

	confirmTarget listItem
	status        string

	width, height int
	ready         bool
}

// New builds an inspector model over the given store.
func New(s memStore) Model {
	ti := textinput.New()
	ti.Placeholder = "search memories… (≥3 chars)"
	ti.Prompt = "/ "
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(colSelect) // cyan caret
	ti.Focus()

	l := list.New(nil, memDelegate{}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false) // we drive search ourselves via store.Search
	l.SetShowStatusBar(false)

	return Model{
		store:  s,
		mode:   modeNormal,
		focus:  focusList,
		list:   l,
		search: ti,
		counts: map[string]int{},
	}
}

func (m Model) Init() tea.Cmd {
	// gen 0 initial list load + one-shot census.
	return tea.Batch(m.loadCmd(m.gen, m.query), m.countsCmd())
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
		if n := len(msg.items); n > 0 && m.list.Index() >= n {
			m.list.Select(n - 1)
		}
		return m, m.syncDetail() // list changed → refresh the detail pane

	case detailMsg:
		if msg.gen != m.detailGen {
			return m, nil // superseded by a newer selection
		}
		if msg.err != nil {
			m.detail.SetContent(bodyStyle.Render("open failed: " + msg.err.Error()))
			return m, nil
		}
		m.detail.SetContent(renderDetail(msg.mem, msg.neighbors, m.detail.Width))
		m.detail.GotoTop()
		return m, nil

	case countsMsg:
		if msg.err == nil {
			m.counts, m.total = msg.counts, msg.total
		}
		return m, nil

	case deletedMsg:
		if msg.err != nil {
			m.status = "delete failed: " + msg.err.Error()
		} else {
			m.status = "deleted"
		}
		m.mode = modeNormal
		m.gen++
		return m, tea.Batch(m.loadCmd(m.gen, m.query), m.countsCmd())

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
	if m.mode == modeConfirm {
		switch msg.String() {
		case "y", "Y":
			id := m.confirmTarget.id
			m.mode = modeNormal
			return m, m.deleteCmd(id)
		default: // n / esc / anything else cancels
			m.mode = modeNormal
			return m, nil
		}
	}
	return m.handleNormalKey(msg)
}

func (m Model) handleNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		m.focus = (m.focus + 1) % 3
		return m, nil
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
		return m, nil
	case "enter":
		m.focus = focusDetail
		return m, nil
	case "ctrl+d":
		if it, ok := m.list.SelectedItem().(listItem); ok {
			m.confirmTarget = it
			m.mode = modeConfirm
		}
		return m, nil
	case "esc":
		if m.search.Value() != "" {
			m.search.SetValue("")
			m.query.search = ""
			m.gen++
			return m, m.loadCmd(m.gen, m.query)
		}
		if m.focus == focusDetail {
			m.focus = focusList
			return m, nil
		}
		return m, tea.Quit
	case "up", "down", "pgup", "pgdown", "home", "end":
		return m.handleNav(msg)
	}

	// Everything else is text input into the search box (always-on, ADR-0021). A
	// changed value bumps gen and schedules a debounce; the load fires on the tick.
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	if v := m.search.Value(); v != m.query.search {
		m.query.search = v
		m.gen++
		return m, tea.Batch(cmd, debounceCmd(m.gen))
	}
	return m, cmd
}

// handleNav routes arrow/paging keys to the focused pane.
func (m Model) handleNav(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.focus {
	case focusSidebar:
		switch msg.String() {
		case "up":
			if m.kindIdx > 0 {
				m.kindIdx--
			}
		case "down":
			if m.kindIdx < len(sidebarKinds)-1 {
				m.kindIdx++
			}
		}
		if k := sidebarKinds[m.kindIdx]; k != m.query.kind {
			m.query.kind = k
			m.gen++
			return m, m.loadCmd(m.gen, m.query)
		}
		return m, nil
	case focusDetail:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	default: // focusList
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, tea.Batch(cmd, m.syncDetail())
	}
}

// --- commands ---

func (m Model) loadCmd(gen int, q queryDesc) tea.Cmd {
	s := m.store
	return func() tea.Msg {
		ctx := context.Background()
		if len([]rune(q.search)) >= minSearchRunes {
			resp, err := s.Search(ctx, store.SearchRequest{Query: q.search, Kind: q.kind, Limit: searchLimit})
			if err != nil {
				return itemsMsg{gen: gen, err: err}
			}
			return itemsMsg{gen: gen, items: itemsFromSearch(resp)}
		}
		resp, err := s.List(ctx, store.ListRequest{Kind: q.kind, Limit: listPageLimit})
		if err != nil {
			return itemsMsg{gen: gen, err: err}
		}
		return itemsMsg{gen: gen, items: itemsFromList(resp)}
	}
}

// syncDetail issues a detail load if the selected row changed since the last one
// (detail follows the cursor, ADR-0021). Stale loads are dropped by detailGen.
func (m *Model) syncDetail() tea.Cmd {
	id := ""
	if it, ok := m.list.SelectedItem().(listItem); ok {
		id = it.id
	}
	if id == m.loadedDetailID {
		return nil
	}
	m.loadedDetailID = id
	m.detailGen++
	return m.detailCmd(m.detailGen, id)
}

func (m Model) detailCmd(gen int, id string) tea.Cmd {
	s := m.store
	return func() tea.Msg {
		if id == "" {
			return detailMsg{gen: gen, mem: nil}
		}
		ctx := context.Background()
		mem, err := s.GetRow(ctx, id) // non-counting: no Expand signal
		if err != nil || mem == nil {
			return detailMsg{gen: gen, mem: mem, err: err}
		}
		neighbors, _ := s.Neighbors(ctx, id, 0) // best-effort: no connections on error
		return detailMsg{gen: gen, mem: mem, neighbors: neighbors}
	}
}

func (m Model) countsCmd() tea.Cmd {
	s := m.store
	return func() tea.Msg {
		resp, err := s.Counts(context.Background())
		if err != nil {
			return countsMsg{err: err}
		}
		return countsMsg{counts: resp.ByKind, total: resp.Total}
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
	// Bordered panes (ADR-0021 update). Fixed rows: header(1) + search(1) + top
	// rule(1) + bottom rule(1) + footer(1) = 5 chrome rows; the 6th is the
	// mobile-cta placeholder that's always empty.
	bodyH := max(1, m.height-6)
	// Sidebar is fixed width; the rest splits into list + detail. No vrule
	// columns needed — pane borders serve as separators.
	inner := max(20, m.width-sidebarWidth)
	detailW := inner * 34 / 100
	listW := inner - detailW
	// Subtract 2 from each dimension for the border (left+right, top+bottom).
	m.list.SetSize(listW-2, bodyH-2)
	m.detail = viewport.New(detailW-2, bodyH-2)
}
