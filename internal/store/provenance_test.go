package store_test

import (
	"context"
	"testing"
)

// TestRecordFiles_RoundTripAndDedup: RecordFiles persists paths under a
// session_id, the composite PK collapses repeat touches, and FilesForSession
// reads them back oldest-first.
func TestRecordFiles_RoundTripAndDedup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.RecordFiles(ctx, "sess_1", []string{"a.go", "b.go"}); err != nil {
		t.Fatalf("RecordFiles: %v", err)
	}
	if err := s.RecordFiles(ctx, "sess_1", []string{"a.go", "c.go"}); err != nil { // a.go repeats
		t.Fatalf("RecordFiles again: %v", err)
	}

	got, err := s.FilesForSession(ctx, "sess_1")
	if err != nil {
		t.Fatalf("FilesForSession: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("files = %v, want 3 deduped", got)
	}
	// A different session never leaks into another's provenance.
	other, err := s.FilesForSession(ctx, "sess_2")
	if err != nil {
		t.Fatalf("FilesForSession sess_2: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("sess_2 files = %v, want empty", other)
	}
}

// TestRecordFiles_NoopOnEmpty: a blank session_id or empty path list writes
// nothing rather than erroring.
func TestRecordFiles_NoopOnEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.RecordFiles(ctx, "", []string{"a.go"}); err != nil {
		t.Errorf("blank session should be no-op, got %v", err)
	}
	if err := s.RecordFiles(ctx, "sess_1", nil); err != nil {
		t.Errorf("empty paths should be no-op, got %v", err)
	}
	if got, _ := s.FilesForSession(ctx, "sess_1"); len(got) != 0 {
		t.Errorf("no-op writes persisted rows: %v", got)
	}
}
