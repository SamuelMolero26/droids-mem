package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Session-end auto-summary staging (ADR-0016). Per Claude Code session, two
// sentinel files live under Dir()/sessions/, keyed by the CC session id:
//
//	<id>.staged — JSON StagedSummary, the model-composed payload to flush
//	<id>.count  — the meaningful-change tally (plain integer)
//
// All hot-path reads/writes here are file-only: no DB, no model. The DB is
// touched once, at flush, by the cmd layer.

const (
	stagedExt   = ".staged"
	countExt    = ".count"
	injectedExt = ".injected"
	filesExt    = ".files"

	// IntakeThreshold (N) — meaningful changes a Run must accumulate before its
	// staged summary is eligible to flush (ADR-0016 pt 5). Mechanical half of
	// the intake gate; the model-judgment half is "did a staged summary exist".
	// 8 file edits ≈ substantial work; 3 tripped the Stop gate on trivial runs.
	IntakeThreshold = 8

	// RecoverIdleCutoff — a staged file untouched for longer than this is treated
	// as belonging to a crashed Run and is eligible for the session-start
	// recovery flush. Shields a concurrently-live session (which re-stages at its
	// checkpoints) from having its in-progress staged file stolen.
	RecoverIdleCutoff = 30 * time.Minute

	// StagedTTL — a staged file older than this is reaped even if it cannot be
	// flushed (corrupt/below-gate), so the sessions dir never accumulates litter.
	StagedTTL = 7 * 24 * time.Hour
)

// StagedSummary is the model-composed mem_save payload parked on disk during a
// Run and flushed to the store at session end. SessionID is the droids-mem
// session_id (reuse-or-mint at flush, ADR-0016 pt 4); the CC session id is the
// filename key, not a field.
type StagedSummary struct {
	SessionID string `json:"session_id,omitempty"`
	TaskType  string `json:"task_type"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	What      string `json:"what"`
	Learned   string `json:"learned"`
	Tags      string `json:"tags,omitempty"`
	StagedAt  int64  `json:"staged_at"`
	// CountAtStage is the meaningful-change tally captured when this summary was
	// staged. The Stop-hook staleness check compares the live tally against it —
	// an mtime comparison can't work, because the staging command itself runs
	// through a counted tool (Bash) and would mark every fresh stage stale.
	CountAtStage int `json:"count_at_stage"`
	// Declined marks a deliberate "nothing worth recalling" decision. It
	// satisfies the Stop-hook gate exactly like a real stage (including the
	// count-based staleness re-check) but flush skips it — without this, a
	// model that skips per the instructions gets re-blocked on every Stop.
	Declined bool `json:"declined,omitempty"`
}

func sessionsDir() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "sessions"), nil
}

// safeID guards against path traversal: the CC session id becomes a filename, so
// it must be a short, slug-safe token.
func safeID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("session id required")
	}
	if len(id) > 128 {
		return "", fmt.Errorf("session id too long")
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return "", fmt.Errorf("session id has unsafe character %q", r)
		}
	}
	return id, nil
}

func sessionPath(id, ext string) (string, error) {
	sid, err := safeID(id)
	if err != nil {
		return "", err
	}
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sid+ext), nil
}

func ensureSessionsDir() (string, error) {
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create sessions dir: %w", err)
	}
	return dir, nil
}

// StageSummary writes (replacing) the staged summary for a CC session.
func StageSummary(ccID string, s StagedSummary) error {
	if _, err := ensureSessionsDir(); err != nil {
		return err
	}
	path, err := sessionPath(ccID, stagedExt)
	if err != nil {
		return err
	}
	if s.StagedAt == 0 {
		s.StagedAt = time.Now().Unix()
	}
	if n, err := ChangeCount(ccID); err == nil {
		s.CountAtStage = n
	}
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal staged summary: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write staged summary: %w", err)
	}
	return nil
}

// ReadStaged returns the staged summary for a CC session, or nil if none exists.
func ReadStaged(ccID string) (*StagedSummary, error) {
	path, err := sessionPath(ccID, stagedExt)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path is sessionsDir/<safeID>.staged
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read staged summary: %w", err)
	}
	var s StagedSummary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse staged summary: %w", err)
	}
	return &s, nil
}

// StagedModTime returns the staged file's modification time and whether it exists.
func StagedModTime(ccID string) (time.Time, bool, error) {
	path, err := sessionPath(ccID, stagedExt)
	if err != nil {
		return time.Time{}, false, err
	}
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("stat staged summary: %w", err)
	}
	return fi.ModTime(), true, nil
}

// InjectedSet returns the memory ids already surfaced to a CC session by the
// relevance-pull (ADR-0016 pt 8 dedupe — inject each memory at most once per
// session).
func InjectedSet(ccID string) (map[string]bool, error) {
	path, err := sessionPath(ccID, injectedExt)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path is sessionsDir/<safeID>.injected
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read injected set: %w", err)
	}
	set := map[string]bool{}
	for line := range strings.SplitSeq(string(b), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			set[id] = true
		}
	}
	return set, nil
}

// RecordInjected appends memory ids to the CC session's injected set.
func RecordInjected(ccID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := ensureSessionsDir(); err != nil {
		return err
	}
	path, err := sessionPath(ccID, injectedExt)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304
	if err != nil {
		return fmt.Errorf("open injected set: %w", err)
	}
	if _, err := f.WriteString(strings.Join(ids, "\n") + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write injected set: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close injected set: %w", err)
	}
	return nil
}

// AppendFiles records file paths touched during a CC session (ADR-0021 Phase 2
// provenance capture). Append-only + read-time dedup, mirroring the injected
// set — a session may touch a file many times; the flush collapses duplicates.
// Hot-path safe: file-only, no DB. ponytail: no per-session cap — one run's
// file set is small and the flush PK dedupes; add a cap only if a run's .files
// sentinel ever grows pathological.
func AppendFiles(ccID string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := ensureSessionsDir(); err != nil {
		return err
	}
	path, err := sessionPath(ccID, filesExt)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304
	if err != nil {
		return fmt.Errorf("open files sentinel: %w", err)
	}
	if _, err := f.WriteString(strings.Join(paths, "\n") + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write files sentinel: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close files sentinel: %w", err)
	}
	return nil
}

// ReadFiles returns the deduped, order-preserving set of file paths captured for
// a CC session (empty if none).
func ReadFiles(ccID string) ([]string, error) {
	path, err := sessionPath(ccID, filesExt)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path is sessionsDir/<safeID>.files
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read files sentinel: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for line := range strings.SplitSeq(string(b), "\n") {
		p := strings.TrimSpace(line)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// ClearSession removes all sentinel files for a CC session. Missing files are
// not an error (idempotent — flush and recovery both call it).
func ClearSession(ccID string) error {
	for _, ext := range []string{stagedExt, countExt, injectedExt, filesExt} {
		path, err := sessionPath(ccID, ext)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

// ChangeCount returns the meaningful-change tally for a CC session (0 if none).
func ChangeCount(ccID string) (int, error) {
	path, err := sessionPath(ccID, countExt)
	if err != nil {
		return 0, err
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path is sessionsDir/<safeID>.count
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read change count: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("parse change count: %w", err)
	}
	return n, nil
}

// IncrementChange bumps and returns the meaningful-change tally for a CC session.
func IncrementChange(ccID string) (int, error) {
	if _, err := ensureSessionsDir(); err != nil {
		return 0, err
	}
	n, err := ChangeCount(ccID)
	if err != nil {
		return 0, err
	}
	n++
	path, err := sessionPath(ccID, countExt)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(n)), 0o600); err != nil {
		return 0, fmt.Errorf("write change count: %w", err)
	}
	return n, nil
}

// ListStagedSessions returns the CC session ids that currently have a staged
// summary on disk — the candidate set for the session-start recovery flush.
func ListStagedSessions() ([]string, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name := e.Name(); strings.HasSuffix(name, stagedExt) {
			ids = append(ids, strings.TrimSuffix(name, stagedExt))
		}
	}
	return ids, nil
}
