// Package tui implements the Memory inspector — an interactive terminal browser
// over the local memory corpus (ADR-0015). It talks to the store in-process
// through the narrow memStore port so the model logic is testable against a
// fake, with no live SQLite.
package tui

import (
	"context"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// memStore is the surface the inspector needs. *store.Store satisfies it in
// production; tests inject a fake. Reads use GetRow (the non-counting fetch,
// ADR-0013) so operator browsing never moves the Expand signal; deletes route
// through Prune so FTS-sync + transaction discipline stay in one place
// (ADR-0014); Counts feeds the static sidebar census (ADR-0021).
type memStore interface {
	List(context.Context, store.ListRequest) (*store.ListResponse, error)
	Search(context.Context, store.SearchRequest) (*store.SearchResponse, error)
	GetRow(context.Context, string) (*store.Memory, error)
	Prune(context.Context, store.PruneRequest) (*store.PruneResponse, error)
	Counts(context.Context) (*store.CountsResponse, error)
	Neighbors(context.Context, string, int) ([]store.Neighbor, error)
}
