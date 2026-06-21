package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// readExpand reads the Expand signal columns for a Memory id directly, since
// they are deliberately not surfaced on the Memory/get response.
func readExpand(t *testing.T, s *store.Store, id string) (count int, last sql.NullInt64) {
	t.Helper()
	if err := s.DB().QueryRow(
		`SELECT expand_count, last_expanded_at FROM memories WHERE id = ?`, id,
	).Scan(&count, &last); err != nil {
		t.Fatalf("read expand columns for %s: %v", id, err)
	}
	return count, last
}

func TestGet_IncrementsExpandSignal(t *testing.T) {
	s := newTestStore(t)
	saved, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if count, _ := readExpand(t, s, saved.ID); count != 0 {
		t.Fatalf("pre-Get expand_count = %d, want 0", count)
	}

	m, err := s.Get(context.Background(), saved.ID)
	if err != nil || m == nil {
		t.Fatalf("Get: m=%v err=%v", m, err)
	}

	count, last := readExpand(t, s, saved.ID)
	if count != 1 {
		t.Errorf("expand_count after Get = %d, want 1", count)
	}
	if !last.Valid || last.Int64 <= 0 {
		t.Errorf("last_expanded_at = %v, want a positive unix seconds value", last)
	}
}

func TestGet_MultipleIncrements(t *testing.T) {
	s := newTestStore(t)
	saved, _ := s.Save(context.Background(), validReq())

	for i := 0; i < 3; i++ {
		if _, err := s.Get(context.Background(), saved.ID); err != nil {
			t.Fatalf("Get #%d: %v", i, err)
		}
	}
	if count, _ := readExpand(t, s, saved.ID); count != 3 {
		t.Errorf("expand_count after 3 Gets = %d, want 3", count)
	}
}

func TestGetRow_DoesNotIncrement(t *testing.T) {
	s := newTestStore(t)
	saved, _ := s.Save(context.Background(), validReq())

	m, err := s.GetRow(context.Background(), saved.ID)
	if err != nil || m == nil {
		t.Fatalf("GetRow: m=%v err=%v", m, err)
	}

	count, last := readExpand(t, s, saved.ID)
	if count != 0 {
		t.Errorf("GetRow moved expand_count to %d, want 0", count)
	}
	if last.Valid {
		t.Errorf("GetRow set last_expanded_at = %v, want NULL", last)
	}
}

func TestGet_MissReturnsNilWithoutError(t *testing.T) {
	s := newTestStore(t)
	m, err := s.Get(context.Background(), "mem_does_not_exist")
	if err != nil {
		t.Errorf("Get miss returned error: %v", err)
	}
	if m != nil {
		t.Errorf("Get miss returned non-nil memory: %v", m)
	}
}

// Force-save is an in-place UPDATE that does not list the Expand signal columns,
// so counts must survive HITL correction. Pins the invariant against a future
// rewrite of the force path (ADR-0013).
func TestForceSave_PreservesExpandSignal(t *testing.T) {
	s := newTestStore(t)
	saved, _ := s.Save(context.Background(), validReq())

	if _, err := s.Get(context.Background(), saved.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if count, _ := readExpand(t, s, saved.ID); count != 1 {
		t.Fatalf("setup expand_count = %d, want 1", count)
	}

	// Same fingerprint (title+learned+task_type+kind unchanged) + force → in-place overwrite.
	req := validReq()
	req.What = "different context body that does not affect the fingerprint"
	req.Force = true
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("force Save: %v", err)
	}
	if resp.Status != "updated" {
		t.Fatalf("force Save status = %q, want updated", resp.Status)
	}
	if resp.ID != saved.ID {
		t.Fatalf("force Save id = %q, want same row %q", resp.ID, saved.ID)
	}

	if count, _ := readExpand(t, s, saved.ID); count != 1 {
		t.Errorf("expand_count after force-save = %d, want 1 (preserved)", count)
	}
}
