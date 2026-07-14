package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// SharedMemory is the wire shape of one exported memory (ADR-0028). It carries
// only the content needed to reconstruct a save on another machine — no id, no
// timestamps, no author. The pool is anonymous by design: identity is exactly
// what the Scrub pipeline removes, so attribution would fight it.
type SharedMemory struct {
	Kind     string `json:"kind"`
	TaskType string `json:"task_type"`
	Title    string `json:"title"`
	What     string `json:"what"`
	Learned  string `json:"learned"`
	Tags     string `json:"tags"`
}

// ExportShared streams every scope='shared' memory to w as JSONL, one compact
// object per line. Rows are already scrubbed in-db (scrub runs on save), so
// export moves no secrets. Personal rows never appear — the point of the scope
// column. The `, id` tiebreak makes the output byte-stable across re-exports of
// an unchanged corpus, so the git-tracked pool file diffs cleanly.
func (s *Store) ExportShared(ctx context.Context, w io.Writer) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind, task_type, title, what, learned, tags
		FROM memories
		WHERE scope = 'shared'
		ORDER BY created_at, id`)
	if err != nil {
		return fmt.Errorf("export query: %w", err)
	}
	defer rows.Close()

	enc := json.NewEncoder(w) // Encode writes one line + '\n' = JSONL
	for rows.Next() {
		var m SharedMemory
		if err := rows.Scan(&m.Kind, &m.TaskType, &m.Title, &m.What, &m.Learned, &m.Tags); err != nil {
			return fmt.Errorf("scan shared memory: %w", err)
		}
		if err := enc.Encode(&m); err != nil {
			return fmt.Errorf("export write: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("export rows: %w", err)
	}
	return nil
}

// ImportResult reports how an import batch landed.
type ImportResult struct {
	Imported int `json:"imported"` // rows newly saved
	Skipped  int `json:"skipped"`  // rows dropped as duplicate/near-duplicate
	Failed   int `json:"failed"`   // rows rejected as malformed or by validation/scrub
}

// ImportShared streams a pulled shared pool (JSONL, one memory per line) into
// the local store (ADR-0028). Each row flows through the normal Save path,
// which re-scrubs (defense in depth) and runs both dedupe layers — so
// cross-source conflict handling is the same Jaccard≥0.85 gate used for local
// saves, for free. Imported rows land scope='shared' so they stay in the pool
// and re-export transitively; dedupe stops the loop.
//
// A pool crosses a trust boundary, so a single bad row (malformed JSON, or a
// row Save rejects — e.g. a secret in a tag) is skipped and counted in Failed,
// never aborting the batch: one poisoned line can't block a teammate's good
// lessons. bufio.Reader (not Scanner) is deliberate — Scanner aborts the whole
// stream on a line over its 64KB token cap, which a crafted pool line could
// trigger to defeat exactly that guarantee; Reader grows to any line length, so
// an oversized row just fails its own Unmarshal. Only a genuine store failure
// (write-lock, disk) aborts.
func (s *Store) ImportShared(ctx context.Context, r io.Reader) (ImportResult, error) {
	var res ImportResult
	br := bufio.NewReader(r)
	for {
		raw, readErr := br.ReadBytes('\n')
		if line := bytes.TrimSpace(raw); len(line) > 0 {
			if err := s.importLine(ctx, line, &res); err != nil {
				return res, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return res, nil
			}
			return res, fmt.Errorf("import read: %w", readErr)
		}
	}
}

// importLine saves one JSONL row, tallying into res. Returns a non-nil error
// only for a genuine store failure that should abort the batch; a malformed or
// Save-rejected row is counted in res.Failed and returns nil.
func (s *Store) importLine(ctx context.Context, line []byte, res *ImportResult) error {
	var m SharedMemory
	if json.Unmarshal(line, &m) != nil {
		res.Failed++ // malformed row — tallied, not batch-fatal
		return nil   //nolint:nilerr // intentional: malformed row tallied in Failed
	}
	resp, err := s.Save(ctx, SaveRequest{
		TaskType: m.TaskType,
		Kind:     m.Kind,
		Title:    m.Title,
		What:     m.What,
		Learned:  m.Learned,
		Tags:     m.Tags,
		Scope:    "shared",
	})
	if err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			// A validation/scrub rejection is a bad row, not a batch failure:
			// count it and keep going so one poisoned line can't halt the pool.
			res.Failed++
			return nil
		}
		return fmt.Errorf("import: %w", err)
	}
	if resp.Status == "skipped" {
		res.Skipped++
	} else {
		res.Imported++
	}
	return nil
}

// SetScope flips one memory's scope by id (the `share`/`unshare` grant,
// ADR-0028). Returns false when no row matches, so the caller can report
// not-found. ponytail: no scope validation here — both callers hardcode valid
// values and the schema CHECK constraint rejects anything else. updated_at
// stays put: nothing reads it for freshness, and a scope flip isn't an edit.
func (s *Store) SetScope(ctx context.Context, id, scope string) (bool, error) {
	r, err := s.db.ExecContext(ctx,
		`UPDATE memories SET scope = ? WHERE id = ?`, scope, id)
	if err != nil {
		return false, fmt.Errorf("set scope: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n > 0, nil
}
