package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/samuelmolero26/droids-mem/internal/state"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

const (
	searchDebounce = 200 * time.Millisecond
	minSearchRunes = 3   // below this, search is skipped and the list falls back to List
	listPageLimit  = 100 // static-filter page size
	searchLimit    = 20  // live-search result cap
	sidebarWidth   = 26  // fixed sidebar column
)

// scopeFilters is the SCOPE rotation `s` cycles through; "" is "all" (no scope
// filter). Order matches the mockup: all → personal → shared.
var scopeFilters = []string{"", "personal", "shared"}

func scopeLabel(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

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
	modeShare // share-confirm dialog: flip selected memories into the shared pool
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
	scope  string // "" = all; "personal"|"shared" (SCOPE filter, cycled by `s`)
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
	shared int
	err    error
}
type deletedMsg struct{ err error }
type sharedMsg struct {
	n   int // memories flipped to shared + pushed to the pool repo
	err error
}
type pulledMsg struct { // teammate's pool pulled + imported
	res store.ImportResult
	err error
}
type scopeChangedMsg struct{ err error } // one row's scope flipped (unshare); reload quietly
type tickMsg struct{ gen int }

// Model is the root BubbleTea model for the Memory inspector.
type Model struct {
	store memStore
	mode  mode
	focus focus

	search    textinput.Model
	repoInput textinput.Model // pool git repo path, edited in the share modal (ADR-0028)
	list      list.Model
	detail    viewport.Model

	kindIdx  int
	scopeIdx int
	counts   map[string]int
	total    int
	shared   int // scope=='shared' census, feeds the SCOPE sidebar section

	// selected is the multi-select set for sharing (row id → chosen). Empty means
	// the share action targets the cursor row instead.
	selected map[string]bool

	query queryDesc

	// gen unifies list-load debounce and stale-result rejection; detailGen does
	// the same for the cursor-following detail pane (a separate stream so a list
	// reload and a detail load never fight over one counter).
	gen       int
	detailGen int

	loadedDetailID string

	confirmTarget listItem
	status        string

	// push/pull do the git side of sharing; defaulted to the real git-shelling
	// funcs, overridden in tests so the model logic runs without a live repo.
	push pushFunc
	pull pullFunc

	width, height int
	ready         bool
}

type pushFunc func(ctx context.Context, repo string, s memStore, n int) error
type pullFunc func(ctx context.Context, repo string, s memStore) (store.ImportResult, error)

// New builds an inspector model over the given store.
func New(s memStore) Model {
	ti := textinput.New()
	ti.Placeholder = "search memories… (≥3 chars)"
	ti.Prompt = "/ "
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(colSelect) // cyan caret
	ti.Focus()

	ri := textinput.New()
	ri.Placeholder = "path to git repo"
	ri.Prompt = ""
	ri.Cursor.Style = lipgloss.NewStyle().Foreground(colSelect)

	// selected is shared by reference with the delegate so ticked rows render a
	// dot without threading state through every Render; it's cleared in place
	// (never reassigned) so the delegate keeps pointing at the live set.
	selected := map[string]bool{}
	l := list.New(nil, memDelegate{selected: selected}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false) // we drive search ourselves via store.Search
	l.SetShowStatusBar(false)

	return Model{
		store:     s,
		mode:      modeNormal,
		focus:     focusList,
		list:      l,
		search:    ti,
		repoInput: ri,
		counts:    map[string]int{},
		selected:  selected,
		push:      pushShared,
		pull:      pullShared,
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
			m.counts, m.total, m.shared = msg.counts, msg.total, msg.shared
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

	case sharedMsg:
		m.mode = modeNormal
		if msg.err != nil {
			m.status = "share failed: " + msg.err.Error()
			return m, nil
		}
		clear(m.selected) // clear in place — the delegate shares this map instance
		m.status = fmt.Sprintf("✓ pushed %d %s", msg.n, plural(msg.n, "memory", "memories"))
		m.gen++
		return m, tea.Batch(m.loadCmd(m.gen, m.query), m.countsCmd())

	case pulledMsg:
		if msg.err != nil {
			m.status = "pull failed: " + msg.err.Error()
			return m, nil
		}
		r := msg.res
		m.status = fmt.Sprintf("✓ pulled: %d imported, %d skipped, %d failed", r.Imported, r.Skipped, r.Failed)
		m.gen++
		return m, tea.Batch(m.loadCmd(m.gen, m.query), m.countsCmd())

	case scopeChangedMsg:
		if msg.err != nil {
			m.status = "unshare failed: " + msg.err.Error()
			return m, nil
		}
		m.status = ""
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
	if m.mode == modeShare {
		switch msg.String() {
		case "enter":
			repo := strings.TrimSpace(m.repoInput.Value())
			if repo == "" {
				m.status = "share failed: no repo set"
				return m, nil
			}
			ids := m.shareTargets()
			m.mode = modeNormal
			return m, m.shareCmd(ids, repo)
		case "esc":
			m.mode = modeNormal
			return m, nil
		default: // keystrokes edit the repo-path field
			var cmd tea.Cmd
			m.repoInput, cmd = m.repoInput.Update(msg)
			return m, cmd
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
	case "ctrl+s": // share: open the confirm dialog for the selection (or cursor row)
		if len(m.shareTargets()) > 0 {
			repo, _ := state.LoadShareRepo() // prefill the remembered repo for reuse
			m.repoInput.SetValue(repo)
			m.repoInput.CursorEnd()
			m.repoInput.Focus()
			m.mode = modeShare
		}
		return m, nil
	case "ctrl+p": // pull + import a teammate's pool from the remembered repo
		repo, _ := state.LoadShareRepo()
		if strings.TrimSpace(repo) == "" {
			m.status = "pull failed: no repo set — share once to set it"
			return m, nil
		}
		m.status = "pulling…"
		return m, m.pullCmd(repo)
	case "ctrl+x": // unshare: flip the cursor row back to personal
		if it, ok := m.list.SelectedItem().(listItem); ok && it.shared {
			return m, m.setScopeCmd(it.id, "personal")
		}
		return m, nil
	case " ": // toggle the cursor row in the multi-select set (empty search box
		// only — otherwise space is a literal separator in a multi-word query).
		if m.search.Value() != "" {
			break
		}
		if it, ok := m.list.SelectedItem().(listItem); ok {
			if m.selected[it.id] {
				delete(m.selected, it.id)
			} else {
				m.selected[it.id] = true
			}
		}
		return m, nil
	case "esc":
		if m.search.Value() != "" {
			m.search.SetValue("")
			m.query.search = ""
			m.gen++
			return m, m.loadCmd(m.gen, m.query)
		}
		if m.query.scope != "" { // clear an active scope filter before quitting
			m.query.scope, m.scopeIdx = "", 0
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

	// `q` quits outright from an empty search box (once the box has text, `q` is
	// a literal search rune — search is always-on, ADR-0021).
	if msg.String() == "q" && m.search.Value() == "" {
		return m, tea.Quit
	}

	// `s` cycles the SCOPE filter, but only from an empty search box — once the
	// box has text, `s` is a literal search rune (search is always-on, ADR-0021).
	// ponytail: the cost is you can't begin a search with 's' from empty; scope
	// cycling is the rarer action and the mockup binds it to a bare `s`.
	if msg.String() == "s" && m.search.Value() == "" {
		m.scopeIdx = (m.scopeIdx + 1) % len(scopeFilters)
		m.query.scope = scopeFilters[m.scopeIdx]
		m.gen++
		return m, m.loadCmd(m.gen, m.query)
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
		resp, err := s.List(ctx, store.ListRequest{Kind: q.kind, Scope: q.scope, Limit: listPageLimit})
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
		ctx := context.Background()
		resp, err := s.Counts(ctx)
		if err != nil {
			return countsMsg{err: err}
		}
		shared, err := s.CountShared(ctx)
		if err != nil {
			return countsMsg{err: err}
		}
		return countsMsg{counts: resp.ByKind, total: resp.Total, shared: shared}
	}
}

// shareTargets is the id set a share action operates on: the multi-select set if
// non-empty, else the cursor row alone (so `^s` with nothing ticked still works).
func (m Model) shareTargets() []string {
	if len(m.selected) > 0 {
		ids := make([]string, 0, len(m.selected))
		for id := range m.selected {
			ids = append(ids, id)
		}
		return ids
	}
	if it, ok := m.list.SelectedItem().(listItem); ok {
		return []string{it.id}
	}
	return nil
}

// shareCmd flips each id to scope='shared', then exports the whole pool into
// repo and pushes it (ADR-0028, full-automation share). One bad flip fails the
// batch — sharing is a deliberate action, not a resilient import. The repo is
// remembered on success so the next share defaults to it (reuse).
func (m Model) shareCmd(ids []string, repo string) tea.Cmd {
	s, push := m.store, m.push
	return func() tea.Msg {
		ctx := context.Background()
		n := 0
		for _, id := range ids {
			if _, err := s.SetScope(ctx, id, "shared"); err != nil {
				return sharedMsg{n: n, err: err}
			}
			n++
		}
		if err := push(ctx, repo, s, n); err != nil {
			return sharedMsg{n: n, err: err}
		}
		_ = state.SaveShareRepo(repo) // remember for reuse; a save miss isn't fatal
		return sharedMsg{n: n}
	}
}

// pullCmd pulls repo and imports its pool into the local store (^p).
func (m Model) pullCmd(repo string) tea.Cmd {
	s, pull := m.store, m.pull
	return func() tea.Msg {
		res, err := pull(context.Background(), repo, s)
		return pulledMsg{res: res, err: err}
	}
}

// setScopeCmd flips one memory's scope (the `^x` unshare path). Its result
// reloads the list + census without a toast — unsharing is a quiet correction.
func (m Model) setScopeCmd(id, scope string) tea.Cmd {
	s := m.store
	return func() tea.Msg {
		_, err := s.SetScope(context.Background(), id, scope)
		return scopeChangedMsg{err: err}
	}
}

// plural picks the singular or plural noun for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
	// Borderless (ADR-0021 visual match). Fixed rows: header(1) + underline(1) +
	// search(1) + top rule(1) + bottom rule(1) + footer(1) = 6.
	bodyH := max(1, m.height-6)
	// Two 1-col vertical dividers separate the three columns; sidebar is fixed,
	// detail ~34% of the remainder, list takes the rest.
	inner := max(20, m.width-sidebarWidth-2)
	detailW := inner * 34 / 100
	listW := inner - detailW
	m.list.SetSize(listW, bodyH)
	m.detail = viewport.New(detailW, bodyH)
}
