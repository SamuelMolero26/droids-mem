package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"testing"
)

func liveSummaryIDs(t *testing.T, conn *sql.DB, taskType string) []string {
	t.Helper()
	rows, err := conn.Query(`SELECT id FROM memories WHERE task_type = ? AND kind = 'session_summary'`, taskType)
	if err != nil {
		t.Fatalf("query live ids: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("live id rows: %v", err)
	}
	sort.Strings(got)
	return got
}

// TestSave_RetentionKeepsNewestUnderSameSecondTies is the data-loss guard for
// issue #58. Six manual summaries share one created_at second and retention
// keeps the newest 5. Without an `id DESC` tiebreak SQLite walks the tie group
// in index order (oldest-first), so LIMIT 5 keeps the five OLDEST and deletes
// the newest — deterministic data loss.
func TestSave_RetentionKeepsNewestUnderSameSecondTies(t *testing.T) {
	s, conn := newStoreWithDB(t)
	ctx := context.Background()

	const tied = int64(1700000000)
	const tt = "retention_tt"

	// Six personal summaries, all tied on the same second. id order == save order.
	var personal []string
	for i := range 6 {
		id := fmt.Sprintf("mem_%026d", i)
		personal = append(personal, id)
		seedSummary(t, conn, id, "manual", tt, tied, 0)
	}

	// One real save fires the prune. It lands at now (far after `tied`), so it
	// is unambiguously newest and must survive.
	req := validReq()
	req.TaskType = tt
	req.Kind = "session_summary"
	req.Title = "fresh summary that triggers retention"
	req.What = "investigated the tie ordering and confirmed the keep set"
	req.Learned = "retention runs on every session_summary insert"
	resp, err := s.Save(ctx, req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if resp.Status != "saved" {
		t.Fatalf("trigger save was %q, want saved (matched %q)", resp.Status, resp.MatchedID)
	}

	got := liveSummaryIDs(t, conn, tt)

	// Keep-set: the fresh save + the 4 newest tied personal rows (i=2..5).
	// Evicted: i=0 and i=1, the two oldest.
	want := append([]string{resp.ID}, personal[2:]...)
	sort.Strings(want)

	if !slices.Equal(got, want) {
		t.Errorf("retention kept the wrong set under a same-second tie:\n got  = %v\n want = %v", got, want)
	}

	for _, id := range personal[2:] {
		if !slices.Contains(got, id) {
			t.Errorf("newest-first violated: tied row %s evicted while an older one survived", id)
		}
	}
}
