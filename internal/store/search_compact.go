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
// Transport-neutral on purpose: no CLI flag or MCP arg syntax.
const (
	msgNoSearchableText = "query contains no searchable text (no letters or digits); nothing can match"
	msgNoMatch          = "no memories matched; try different keywords or a broader scope"
)

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

// SearchCompactResponse is the list envelope both transports emit.
type SearchCompactResponse struct {
	Results []SearchCompactRow `json:"results"`
	Total   int                `json:"total"`
	Message string             `json:"message,omitempty"`
	Help    []string           `json:"help,omitempty"`
}

// ToCompactSearchResponse projects a full Search response onto the compact
// list surface. Search keeps returning full rows — session relevance
// (cmd_session.go) and hooks consume those directly; only the CLI/MCP
// boundary trims via this helper. help is the boundary's own escape-hatch
// syntax (AXI §9), attached only when there are stub IDs to expand.
func ToCompactSearchResponse(resp *SearchResponse, help string) SearchCompactResponse {
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
	if len(out.Results) > 0 {
		out.Help = []string{help}
	}
	return out
}
