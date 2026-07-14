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

// ExportShared returns every scope='shared' memory as a SharedMemory. Rows are
// already scrubbed in-db (scrub runs on save), so export moves no secrets.
// Personal rows never appear here — that is the whole point of the scope column.
func (s *Store) ExportShared(ctx context.Context) ([]SharedMemory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind, task_type, title, what, learned, tags
		FROM memories
		WHERE scope = 'shared'
		ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("export query: %w", err)
	}
	defer rows.Close()

	out := []SharedMemory{}
	for rows.Next() {
		var m SharedMemory
		if err := rows.Scan(&m.Kind, &m.TaskType, &m.Title, &m.What, &m.Learned, &m.Tags); err != nil {
			return nil, fmt.Errorf("scan shared memory: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export rows: %w", err)
	}
	return out, nil
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
// lessons. Only a genuine store failure (write-lock, disk) aborts.
func (s *Store) ImportShared(ctx context.Context, r io.Reader) (ImportResult, error) {
	var res ImportResult
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m SharedMemory
		if err := json.Unmarshal(line, &m); err != nil {
			res.Failed++
			continue
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
				res.Failed++
				continue
			}
			return res, fmt.Errorf("import: %w", err)
		}
		if resp.Status == "skipped" {
			res.Skipped++
		} else {
			res.Imported++
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("import scan: %w", err)
	}
	return res, nil
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
