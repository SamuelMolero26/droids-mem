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

// seedSummaryScope is seedSummary plus an explicit scope, direct-INSERT so it
// bypasses both dedupe layers like seedSummary does. Local to this file:
// seedSummary itself hardcodes scope='personal' because every other caller in
// this package wants that; the fence test is the one place that needs shared
// rows too.
func seedSummaryScope(t *testing.T, conn *sql.DB, id, origin, taskType, scope string, createdAt int64) {
	t.Helper()
	seedSummary(t, conn, id, origin, taskType, createdAt, 0)
	if _, err := conn.Exec(`UPDATE memories SET scope = ? WHERE id = ?`, scope, id); err != nil {
		t.Fatalf("set scope for %s: %v", id, err)
	}
}

// TestSave_RetentionScopeFence pins the import bug D9 fixes: importLine stamps
// scope='shared' but leaves origin='manual' and created_at=now, so a bulk
// import lands a whole tie group of foreign rows that (without the fence)
// would beat every local summary on recency alone and evict the user's own
// history. 5 personal rows sit at the newest-5 cap; 10 shared rows land
// strictly newer. The fenced prune must never count or evict against them.
func TestSave_RetentionScopeFence(t *testing.T) {
	s, conn := newStoreWithDB(t)
	ctx := context.Background()

	const tt = "fence_tt"
	const personalAt = int64(1700000000)
	const sharedAt = int64(1700001000) // strictly newer than every personal row

	var personal []string
	for i := range 5 {
		id := fmt.Sprintf("mem_p%025d", i)
		personal = append(personal, id)
		seedSummaryScope(t, conn, id, "manual", tt, "personal", personalAt)
	}

	var shared []string
	for i := range 10 {
		id := fmt.Sprintf("mem_s%025d", i)
		shared = append(shared, id)
		seedSummaryScope(t, conn, id, "manual", tt, "shared", sharedAt+int64(i))
	}

	// One more distinct personal save trips the newest-5 prune.
	req := validReq()
	req.TaskType = tt
	req.Kind = "session_summary"
	req.Title = "sixth personal summary triggering the fenced prune"
	req.What = "checked that shared rows never count against the personal cap"
	req.Learned = "pruneSessionSummariesConn must fence on scope = 'personal'"
	resp, err := s.Save(ctx, req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if resp.Status != "saved" {
		t.Fatalf("trigger save was %q, want saved (matched %q)", resp.Status, resp.MatchedID)
	}

	got := liveSummaryIDs(t, conn, tt)

	// All 10 shared rows must survive untouched (unbounded by this fence, per
	// the accepted risk in the spec) and the personal set must obey its own
	// newest-5 cap: the fresh save plus the 4 newest seeded personal rows.
	want := append([]string{resp.ID}, personal[1:]...)
	want = append(want, shared...)
	sort.Strings(want)

	if !slices.Equal(got, want) {
		t.Errorf("scope-fenced retention kept the wrong set:\n got  = %v\n want = %v", got, want)
	}
	for _, id := range shared {
		if !slices.Contains(got, id) {
			t.Errorf("scope fence violated: shared row %s was evicted by personal retention", id)
		}
	}
	if slices.Contains(got, personal[0]) {
		t.Errorf("personal retention did not evict its own oldest row %s (shared rows may have consumed its slot)", personal[0])
	}
}
