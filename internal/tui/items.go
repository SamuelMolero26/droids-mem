package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/SamuelMolero26/droids-mem/internal/store"
)

// listItem is the thin projection the list renders — the common denominator of
// store.Memory (List) and store.SearchResult (Search), so the list never
// branches on which source filled it (ADR-0015). The full body is always
// fetched fresh via GetRow on enter, never read from this projection.
type listItem struct {
	id        string
	kind      string
	title     string
	taskType  string
	createdAt int64
}

func (i listItem) FilterValue() string { return i.title }
func (i listItem) Title() string       { return i.title }
func (i listItem) Description() string {
	return fmt.Sprintf("%s · %s · %s", i.kind, i.taskType, time.Unix(i.createdAt, 0).Format("2006-01-02"))
}

func itemsFromList(resp *store.ListResponse) []list.Item {
	out := make([]list.Item, 0, len(resp.Memories))
	for _, m := range resp.Memories {
		out = append(out, listItem{id: m.ID, kind: m.Kind, title: m.Title, taskType: m.TaskType, createdAt: m.CreatedAt})
	}
	return out
}

func itemsFromSearch(resp *store.SearchResponse) []list.Item {
	out := make([]list.Item, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, listItem{id: r.ID, kind: r.Kind, title: r.Title, taskType: r.TaskType, createdAt: r.CreatedAt})
	}
	return out
}
