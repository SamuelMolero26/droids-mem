package store_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
