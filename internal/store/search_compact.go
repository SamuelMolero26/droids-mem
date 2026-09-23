package store

import "fmt"

// LearnedPreviewMaxRunes bounds the learned_preview shown in default list
// output (CLI search, MCP mem_search). Full Learned stays in the store and
// behind the mem_get / get escape hatch; the default list carries only this
// rune-safe prefix.
const LearnedPreviewMaxRunes = 500

// LearnedPreview returns s unchanged when it fits in LearnedPreviewMaxRunes,
// else the first LearnedPreviewMaxRunes runes plus a suffix naming the total
// rune count. Rune-safe (never cuts a multi-byte rune); a short input carries
// no marker. Boundaries use this for the compact default row.
func LearnedPreview(s string) string {
	runes := []rune(s)
	if len(runes) <= LearnedPreviewMaxRunes {
		return s
	}
	return string(runes[:LearnedPreviewMaxRunes]) + fmt.Sprintf("... (%d chars total)", len(runes))
}

// Definitive-empty messages for Search. Tests assert these literals, never the
// const names, so a wording change fails loud instead of drifting silently.
//
// The no-match message is scope-aware and transport-neutral: a scoped search
// names no transport-specific flag, so both the CLI and MCP surfaces can use it
// verbatim and append their own actionable syntax at the boundary. Unexported —
// boundaries match via NoMatchMessage(true), never by naming a constant.
const (
	msgNoSearchableText = "query contains no searchable text (no letters or digits); nothing can match"
	msgNoMatchScoped    = "no memories matched; try a broader scope or different keywords"
	msgNoMatchGlobal    = "no memories matched; try different keywords"
)

// NoMatchMessage returns the transport-neutral no-match message for the scope
// actually searched: scoped (any filter applied) suggests broadening, global
// only suggests different keywords. The CLI/MCP boundary appends its own
// actionable syntax (--all-projects vs all_projects=true), never here.
func NoMatchMessage(scoped bool) string {
	if scoped {
		return msgNoMatchScoped
	}
	return msgNoMatchGlobal
}

// SearchCompactRow is the single default list projection (AXI §2 minimal
// schema): identity + title + a rune-safe learned_preview, never the full
// Learned. Pinned/needs_review ride along only when true so the lifecycle
// trust signal survives without a full-detail flag. Ranking internals (score,
// overlap, expand counts, timestamps) are dropped — result order already
// implies relevance. The full body is one get/mem_get away (see Help).
type SearchCompactRow struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	TaskType       string `json:"task_type"`
	LearnedPreview string `json:"learned_preview"`
	Pinned         bool   `json:"pinned,omitempty"`
	NeedsReview    bool   `json:"needs_review,omitempty"`
}

// SearchCompactResponse is the list envelope both transports emit. Help is
// never set here — each boundary sets its own escape-hatch syntax (or leaves
// it empty) after projecting.
type SearchCompactResponse struct {
	Results []SearchCompactRow `json:"results"`
	Total   int                `json:"total"`
	Message string             `json:"message,omitempty"`
	Help    []string           `json:"help,omitempty"`
}

// ToCompactSearchResponse projects a full Search response onto the compact
// list surface. Search keeps returning full rows — session relevance
// (cmd_session.go) and hooks consume those directly; only the CLI/MCP
// boundary trims via this helper. Message passes through verbatim; the
// boundary appends transport-specific suffixes and sets Help.
func ToCompactSearchResponse(resp *SearchResponse) SearchCompactResponse {
	out := SearchCompactResponse{
		Results: []SearchCompactRow{},
		Total:   resp.Total,
		Message: resp.Message,
	}
	for _, r := range resp.Results {
		out.Results = append(out.Results, SearchCompactRow{
			ID:             r.ID,
			Kind:           r.Kind,
			Title:          r.Title,
			TaskType:       r.TaskType,
			LearnedPreview: LearnedPreview(r.Learned),
			Pinned:         r.Pinned,
			NeedsReview:    r.NeedsReview,
		})
	}
	return out
}
