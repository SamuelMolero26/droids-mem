package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/state"
	"github.com/samuelmolero26/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

// ReservedAutoTaskType is the default bucket for an auto-session-summary when
// the staged payload does not pin a workflow task_type (ADR-0016).
const ReservedAutoTaskType = "claude_session"

// newSessionCmd groups the binary commands behind the Claude Code session-end
// auto-summary (ADR-0016). They are driven by hooks (Phase 5); here they are
// plain subcommands so the mechanism is testable without any settings.json
// wiring. Operator/machine surface only — not exposed over MCP.
func newSessionCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Claude Code session-end auto-summary plumbing (staging, intake gate, flush, recovery)",
	}
	cmd.AddCommand(
		newSessionStageCmd(),
		newSessionMarkChangeCmd(),
		newSessionCheckCmd(),
		newSessionFlushCmd(a),
		newSessionRecoverCmd(a),
		newSessionPullCmd(a),
		newSessionHookCmd(a),
	)
	return cmd
}

// DefaultRelevanceFloor — keep a search hit only when the fraction of prompt
// tokens found in its title+learned is ≥ this. The floor is mandatory
// (ADR-0016 pt 8): search terms are OR-joined, so a memory sharing one common
// word with a five-word prompt matches and would otherwise be injected. An
// absolute BM25 floor cannot do this job — rank magnitudes scale with corpus
// size (FTS5 IDF ≈ 0 on tiny DBs) — token overlap is corpus-size-invariant.
// 0.3 ≈ a third of the prompt's meaningful tokens; provisional until the
// T1.2 recall eval tunes it (ADR-0016 open item).
const DefaultRelevanceFloor = 0.3

// relevancePullLimit caps how many prior memories a single prompt may surface.
const relevancePullLimit = 3

// newSessionPullCmd is the relevance-gated recall enforcement (ADR-0016 pt 8):
// the UserPromptSubmit hook runs it over the prompt text so prior memories about
// the current task surface automatically — but only above a relevance floor, and
// each memory at most once per session. Below the floor it returns nothing.
func newSessionPullCmd(a *app) *cobra.Command {
	var ccID, query, format string
	var floor float64
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Surface relevant prior memories for a prompt (UserPromptSubmit hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			picked := relevancePull(cmd.Context(), s, ccID, query, floor)
			if format == "text" {
				writeRelevanceText(picked)
				return nil
			}
			writeJSON(map[string]any{"results": picked, "count": len(picked)})
			return nil
		},
	}
	cmd.Flags().StringVar(&ccID, "session", "", "Claude Code session id (required)")
	cmd.Flags().StringVar(&query, "query", "", "Prompt text to find prior memories for (required)")
	cmd.Flags().Float64Var(&floor, "floor", DefaultRelevanceFloor, "Relevance floor: keep hits whose prompt-token overlap is ≥ this (0..1)")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json (default) | text (for hook injection)")
	_ = cmd.MarkFlagRequired("session")
	_ = cmd.MarkFlagRequired("query")
	return cmd
}

// maxRelevanceCandidates is how many BM25 hits to consider before floor + dedupe
// filtering trims to relevancePullLimit.
const maxRelevanceCandidates = 10

// relevancePull runs the relevance-gated recall (ADR-0016 pt 8): search the
// prompt, keep hits whose prompt-token overlap meets the floor, drop ones
// already injected this session, cap to relevancePullLimit, and record the
// newly injected ids. A search failure returns nothing — recall must never
// break the user's prompt.
func relevancePull(ctx context.Context, s *store.Store, ccID, query string, floor float64) []store.SearchResult {
	resp, err := s.Search(ctx, store.SearchRequest{Query: query, Limit: maxRelevanceCandidates})
	if err != nil {
		return nil
	}
	injected, _ := state.InjectedSet(ccID)
	var picked []store.SearchResult
	var newIDs []string
	for _, r := range resp.Results {
		if len(picked) >= relevancePullLimit {
			break
		}
		if store.TokenOverlap(query, r.Title+" "+r.Learned) < floor || injected[r.ID] { // weaker than floor, or already shown
			continue
		}
		picked = append(picked, r)
		newIDs = append(newIDs, r.ID)
	}
	if len(newIDs) > 0 {
		_ = state.RecordInjected(ccID, newIDs)
	}
	return picked
}

// sessionNeedsStage reports whether a Stop-time checkpoint should fire: the
// change threshold is met AND the staged summary is missing or stale.
func sessionNeedsStage(ccID string) bool {
	count, err := state.ChangeCount(ccID)
	if err != nil || count < state.IntakeThreshold {
		return false
	}
	stagedAt, hasStaged, err := state.StagedModTime(ccID)
	if err != nil {
		return false
	}
	if !hasStaged {
		return true
	}
	lastChange, ok, _ := state.CountModTime(ccID)
	return ok && stagedAt.Before(lastChange)
}

func writeRelevanceText(rs []store.SearchResult) {
	if len(rs) == 0 {
		return // inject nothing when nothing applies
	}
	var b strings.Builder
	b.WriteString("Relevant memories from prior sessions:\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "- [%s] %s — %s\n", r.Kind, r.Title, r.Learned)
	}
	fmt.Print(b.String())
}

func newSessionStageCmd() *cobra.Command {
	var ccID, sessionID, taskType, kind, title, what, learned, tags string
	cmd := &cobra.Command{
		Use:   "stage",
		Short: "Write (replace) the staged auto-summary for a Claude Code session",
		RunE: func(cmd *cobra.Command, args []string) error {
			if taskType == "" {
				taskType = ReservedAutoTaskType
			}
			if kind == "" {
				kind = "session_summary"
			}
			if err := state.StageSummary(ccID, state.StagedSummary{
				SessionID: sessionID,
				TaskType:  taskType,
				Kind:      kind,
				Title:     title,
				What:      what,
				Learned:   learned,
				Tags:      tags,
			}); err != nil {
				writeError("stage_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(map[string]any{"staged": true, "session": ccID})
			return nil
		},
	}
	cmd.Flags().StringVar(&ccID, "session", "", "Claude Code session id (required)")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "droids-mem session_id to reuse (mints one at flush if omitted)")
	cmd.Flags().StringVar(&taskType, "task-type", "", "Task type bucket (default claude_session)")
	cmd.Flags().StringVar(&kind, "kind", "", "Memory kind (default session_summary)")
	cmd.Flags().StringVar(&title, "title", "", "Summary title (required)")
	cmd.Flags().StringVar(&what, "what", "", "Session context body (required)")
	cmd.Flags().StringVar(&learned, "learned", "", "What to remember next time (required)")
	cmd.Flags().StringVar(&tags, "tags", "", "Space-delimited tags")
	_ = cmd.MarkFlagRequired("session")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("what")
	_ = cmd.MarkFlagRequired("learned")
	return cmd
}

func newSessionMarkChangeCmd() *cobra.Command {
	var ccID string
	cmd := &cobra.Command{
		Use:   "mark-change",
		Short: "Increment the meaningful-change counter for a session (PostToolUse hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := state.IncrementChange(ccID)
			if err != nil {
				writeError("mark_change_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(map[string]any{"count": n})
			return nil
		},
	}
	cmd.Flags().StringVar(&ccID, "session", "", "Claude Code session id (required)")
	_ = cmd.MarkFlagRequired("session")
	return cmd
}

func newSessionCheckCmd() *cobra.Command {
	var ccID string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report whether the session should stage now (Stop hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			count, err := state.ChangeCount(ccID)
			if err != nil {
				writeError("check_failed", err.Error(), true)
				exitWith(ExitError)
			}
			_, hasStaged, err := state.StagedModTime(ccID)
			if err != nil {
				writeError("check_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(map[string]any{
				"count":         count,
				"threshold":     state.IntakeThreshold,
				"threshold_met": count >= state.IntakeThreshold,
				"has_staged":    hasStaged,
				"needs_stage":   sessionNeedsStage(ccID),
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&ccID, "session", "", "Claude Code session id (required)")
	_ = cmd.MarkFlagRequired("session")
	return cmd
}

func newSessionFlushCmd(a *app) *cobra.Command {
	var ccID string
	cmd := &cobra.Command{
		Use:   "flush",
		Short: "Flush the staged auto-summary to the store if the intake gate passes (SessionEnd hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			res := flushSession(cmd.Context(), s, ccID)
			// SessionEnd always clears: a passing flush persisted the summary; a
			// failing one means the Run was low-value, so discard the sentinels.
			_ = state.ClearSession(ccID)
			if res.err != nil {
				writeError("flush_failed", res.err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(res.json())
			return nil
		},
	}
	cmd.Flags().StringVar(&ccID, "session", "", "Claude Code session id (required)")
	_ = cmd.MarkFlagRequired("session")
	return cmd
}

func newSessionRecoverCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Flush orphaned staged summaries from crashed runs; sweep stale ones (SessionStart hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			recovered, swept := recoverOrphans(cmd.Context(), s)
			writeJSON(map[string]any{"recovered": recovered, "swept": swept})
			return nil
		},
	}
	return cmd
}

// recoverOrphans flushes staged summaries left by crashed runs (idle past the
// cutoff) and sweeps the rest, leaving concurrently-live sessions untouched
// (ADR-0016 pt 2 recovery flush). Returns the recovered and swept session ids.
func recoverOrphans(ctx context.Context, s *store.Store) (recovered, swept []string) {
	recovered, swept = []string{}, []string{}
	ids, err := state.ListStagedSessions()
	if err != nil {
		return recovered, swept
	}
	now := time.Now()
	for _, id := range ids {
		mtime, ok, err := state.StagedModTime(id)
		if err != nil || !ok {
			continue
		}
		// Only touch idle sessions — a live session re-stages at its checkpoints,
		// so a recent staged file may belong to a running run.
		if now.Sub(mtime) < state.RecoverIdleCutoff {
			continue
		}
		res := flushSession(ctx, s, id)
		_ = state.ClearSession(id)
		if res.flushed {
			recovered = append(recovered, id)
		} else {
			swept = append(swept, id)
		}
	}
	return recovered, swept
}

// flushResult carries the outcome of a single flush attempt.
type flushResult struct {
	flushed   bool
	id        string // memory id, when flushed
	sessionID string // droids-mem session_id used
	reason    string // why not flushed, when !flushed
	err       error
}

func (r flushResult) json() map[string]any {
	m := map[string]any{"flushed": r.flushed}
	if r.flushed {
		m["id"] = r.id
		m["session_id"] = r.sessionID
	} else {
		m["reason"] = r.reason
	}
	return m
}

// flushSession applies the intake gate and, if it passes, saves the staged
// summary as an origin='auto' session_summary (ADR-0016). It does NOT clear the
// sentinels — callers decide that (SessionEnd always clears; recovery clears).
func flushSession(ctx context.Context, s *store.Store, ccID string) flushResult {
	staged, err := state.ReadStaged(ccID)
	if err != nil {
		return flushResult{reason: "unreadable_staged", err: err}
	}
	if staged == nil {
		return flushResult{reason: "no_staged"}
	}
	count, err := state.ChangeCount(ccID)
	if err != nil {
		return flushResult{reason: "unreadable_count", err: err}
	}
	if count < state.IntakeThreshold {
		return flushResult{reason: "below_threshold"}
	}

	resp, err := s.Save(ctx, store.SaveRequest{
		SessionID: staged.SessionID,
		TaskType:  staged.TaskType,
		Kind:      staged.Kind,
		Title:     staged.Title,
		What:      staged.What,
		Learned:   staged.Learned,
		Tags:      staged.Tags,
		Origin:    "auto",
	})
	if err != nil {
		return flushResult{reason: "save_failed", err: err}
	}
	return flushResult{flushed: true, id: resp.ID, sessionID: resp.SessionID}
}
