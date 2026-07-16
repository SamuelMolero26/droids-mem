package store_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// exportLines runs ExportShared into a buffer and decodes the JSONL back into
// SharedMemory rows, so tests assert on the actual wire output.
func exportLines(t *testing.T, s *store.Store) []store.SharedMemory {
	t.Helper()
	var buf bytes.Buffer
	if err := s.ExportShared(context.Background(), &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	var out []store.SharedMemory
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var m store.SharedMemory
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("decode export line: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// TestShare_ExportOnlyShared proves the trust boundary: a personal-by-default
// save never leaks; only a memory promoted via SetScope('shared') exports.
func TestShare_ExportOnlyShared(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Default save is personal (ADR-0028) → excluded from export.
	if _, err := s.Save(ctx, validReq()); err != nil {
		t.Fatalf("save personal: %v", err)
	}
	if got := exportLines(t, s); len(got) != 0 {
		t.Fatalf("personal row leaked into export: %d rows", len(got))
	}

	// Promote a second memory and confirm exactly it exports.
	req := validReq()
	req.Title = "Shared lesson"
	req.Learned = "worth sharing"
	resp, err := s.Save(ctx, req)
	if err != nil {
		t.Fatalf("save sharable: %v", err)
	}
	found, err := s.SetScope(ctx, resp.ID, "shared")
	if err != nil || !found {
		t.Fatalf("SetScope shared: found=%v err=%v", found, err)
	}
	if got := exportLines(t, s); len(got) != 1 || got[0].Title != "Shared lesson" {
		t.Fatalf("export = %+v, want the one shared row", got)
	}
}

// TestShare_ImportDedupesAcrossSources proves cross-source dedupe is free: a
// second import of the same pool adds nothing (Layer-1 fingerprint match).
func TestShare_ImportDedupesAcrossSources(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Two identical lines + one blank + one malformed row.
	pool := `{"kind":"task_pattern","task_type":"go","title":"use errgroup","what":"for fan-out","learned":"errgroup bounds goroutine fan-out"}
{"kind":"task_pattern","task_type":"go","title":"use errgroup","what":"for fan-out","learned":"errgroup bounds goroutine fan-out"}

not json
`
	res, err := s.ImportShared(ctx, strings.NewReader(pool))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 1 || res.Skipped != 1 || res.Failed != 1 {
		t.Fatalf("first import = %+v, want imported=1 skipped=1 failed=1", res)
	}

	// Re-importing the same pool is idempotent — everything dedupes.
	res, err = s.ImportShared(ctx, strings.NewReader(pool))
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if res.Imported != 0 || res.Skipped != 2 || res.Failed != 1 {
		t.Fatalf("re-import = %+v, want imported=0 skipped=2 failed=1", res)
	}

	// Imported rows land shared, so they round-trip back out.
	if out := exportLines(t, s); len(out) != 1 {
		t.Fatalf("exported %d rows, want 1", len(out))
	}
}

// TestShare_ExportOrderIsContentStable proves export bytes depend only on
// content, not insert order or a per-machine clock — the property that keeps a
// git-tracked pool file diff-clean across teammates. Two stores get the same two
// shared memories in opposite insert order; their exports must be byte-identical.
func TestShare_ExportOrderIsContentStable(t *testing.T) {
	mk := func(order ...int) []byte {
		s := newTestStore(t)
		ctx := context.Background()
		// Fully distinct content so neither trips the near-duplicate gate — the
		// test is about ordering, so both rows must persist.
		a := validReq()
		a.Title, a.What, a.Learned, a.Tags = "alpha lesson", "redis cache eviction context", "prefer lru eviction", "redis caching"
		b := validReq()
		b.Title, b.What, b.Learned, b.Tags = "beta lesson", "postgres index tuning context", "add covering index", "postgres indexing"
		reqs := []store.SaveRequest{a, b}
		for _, i := range order {
			resp, err := s.Save(ctx, reqs[i])
			if err != nil {
				t.Fatalf("save: %v", err)
			}
			if _, err := s.SetScope(ctx, resp.ID, "shared"); err != nil {
				t.Fatalf("scope: %v", err)
			}
		}
		var buf bytes.Buffer
		if err := s.ExportShared(ctx, &buf); err != nil {
			t.Fatalf("export: %v", err)
		}
		return buf.Bytes()
	}
	if !bytes.Equal(mk(0, 1), mk(1, 0)) {
		t.Fatal("export bytes differ by insert order — ordering is not content-stable")
	}
}

// TestSave_RejectsUnsafeTaskType proves SEC-1: a task_type that isn't a safe
// single slug is rejected at the save trust boundary, before it could become a
// path segment on shared export.
func TestSave_RejectsUnsafeTaskType(t *testing.T) {
	s := newTestStore(t)
	for _, bad := range []string{"../../etc", "a/b", "..", ".hidden"} {
		req := validReq()
		req.TaskType = bad
		_, err := s.Save(context.Background(), req)
		var ve *store.ValidationError
		if !errors.As(err, &ve) || ve.Field != "task_type" {
			t.Fatalf("task_type %q: got err %v, want task_type ValidationError", bad, err)
		}
	}
}

// TestImportShared_RejectsUnsafeTaskType proves A8: a pool line whose task_type
// would escape the repo is counted in Failed and never stored.
func TestImportShared_RejectsUnsafeTaskType(t *testing.T) {
	s := newTestStore(t)
	line := `{"task_type":"../escape","kind":"task_pattern","title":"x","what":"y","learned":"z","tags":""}`
	res, err := s.ImportShared(context.Background(), strings.NewReader(line))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Failed != 1 || res.Imported != 0 {
		t.Fatalf("import = %+v, want failed=1 imported=0", res)
	}
}

// TestSetScope_NotFound reports false for an unknown id so the CLI can exit 3.
func TestSetScope_NotFound(t *testing.T) {
	s := newTestStore(t)
	found, err := s.SetScope(context.Background(), "mem_nope", "shared")
	if err != nil {
		t.Fatalf("SetScope: %v", err)
	}
	if found {
		t.Fatal("SetScope reported found for a missing id")
	}
}
