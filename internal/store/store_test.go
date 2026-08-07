package store_test

import (
	"context"
	"testing"
)

// TestListTaskTypes_LatestSessionResolvesNewestFirst guards the 7th
// newest-first site (issue #58): ListTaskTypes' correlated subquery picks
// each task_type's latest session_summary title for the TUI census. Two
// session_summary rows tie on created_at; without an `id DESC` tiebreak
// SQLite walks the tie group in index order and can surface the OLDER title
// instead of the newest.
func TestListTaskTypes_LatestSessionResolvesNewestFirst(t *testing.T) {
	s, conn := newStoreWithDB(t)
	const tt = "census_tt"
	const tied = int64(1700000000)

	seedSummary(t, conn, "mem_00000000000000000000000000", "manual", tt, tied, 0)
	seedSummary(t, conn, "mem_00000000000000000000000001", "manual", tt, tied, 0)

	out, err := s.ListTaskTypes(context.Background())
	if err != nil {
		t.Fatalf("ListTaskTypes: %v", err)
	}
	var got string
	found := false
	for _, ttc := range out {
		if ttc.TaskType == tt {
			got = ttc.LatestSession
			found = true
		}
	}
	if !found {
		t.Fatalf("task_type %q not present in ListTaskTypes output", tt)
	}
	if want := "t_mem_00000000000000000000000001"; got != want {
		t.Errorf("latest session title = %q, want %q (newest id on a same-second tie)", got, want)
	}
}
