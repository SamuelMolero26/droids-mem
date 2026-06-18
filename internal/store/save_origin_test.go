package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/samuelmolero/droids-mem/internal/db"
	"github.com/samuelmolero/droids-mem/internal/store"
	_ "modernc.org/sqlite"
)

// newStoreWithDB is like newTestStore but also hands back the conn so origin
// eviction tests can seed controlled rows and inspect the table directly.
func newStoreWithDB(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Init(conn); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return store.New(conn), conn
}

func countAuto(t *testing.T, conn *sql.DB) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM memories WHERE kind='session_summary' AND origin='auto'`,
	).Scan(&n); err != nil {
		t.Fatalf("count auto: %v", err)
	}
	return n
}

func rowExists(t *testing.T, conn *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM memories WHERE id=?`, id).Scan(&n); err != nil {
		t.Fatalf("exists %s: %v", id, err)
	}
	return n == 1
}

// seedSummary inserts a controlled session_summary directly, so origin,
// created_at, and expand state are deterministic (Save would collide created_at
// across same-second calls and risk near-dup suppression of synthetic rows).
func seedSummary(t *testing.T, conn *sql.DB, id, origin, taskType string, createdAt int64, expandCount int) {
	t.Helper()
	var last sql.NullInt64
	if expandCount > 0 {
		last = sql.NullInt64{Int64: createdAt, Valid: true}
	}
	_, err := conn.Exec(`
		INSERT INTO memories
			(id, session_id, task_type, kind, title, what, learned, tags, fingerprint,
			 created_at, updated_at, scope, scrub_pattern_version, origin, expand_count, last_expanded_at)
		VALUES (?, 'sess', ?, 'session_summary', ?, 'w', ?, '', ?, ?, ?, 'shared', 1, ?, ?, ?)`,
		id, taskType, "t_"+id, "l_"+id, "fp_"+id, createdAt, createdAt, origin, expandCount, last)
	if err != nil {
		t.Fatalf("seed %s summary %s: %v", origin, id, err)
	}
}

func seedAuto(t *testing.T, conn *sql.DB, id string, createdAt int64, expandCount int) {
	t.Helper()
	seedSummary(t, conn, id, "auto", "claude_session", createdAt, expandCount)
}

func autoReq(title string) store.SaveRequest {
	return store.SaveRequest{
		TaskType: "claude_session",
		Kind:     "session_summary",
		Title:    title,
		What:     "session context body",
		Learned:  "what to remember next time: " + title,
		Origin:   "auto",
	}
}

func TestSave_DefaultOriginIsManual(t *testing.T) {
	s, conn := newStoreWithDB(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	var origin string
	if err := conn.QueryRow(`SELECT origin FROM memories WHERE id=?`, resp.ID).Scan(&origin); err != nil {
		t.Fatalf("read origin: %v", err)
	}
	if origin != "manual" {
		t.Errorf("default origin = %q, want manual", origin)
	}
}

func TestSave_OriginAutoPersisted(t *testing.T) {
	s, conn := newStoreWithDB(t)
	resp, err := s.Save(context.Background(), autoReq("first auto run"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	var origin string
	if err := conn.QueryRow(`SELECT origin FROM memories WHERE id=?`, resp.ID).Scan(&origin); err != nil {
		t.Fatalf("read origin: %v", err)
	}
	if origin != "auto" {
		t.Errorf("origin = %q, want auto", origin)
	}
}

func TestSave_InvalidOriginRejected(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Origin = "bogus"
	_, err := s.Save(context.Background(), req)
	var ve *store.ValidationError
	if !errors.As(err, &ve) || ve.Field != "origin" {
		t.Fatalf("expected origin ValidationError, got %v", err)
	}
}

// 30 seeded autos + one fresh save (newest) = 31; budget M=30 evicts exactly the
// oldest, leaving 30.
func TestSave_AutoBudgetEvictsOldest(t *testing.T) {
	s, conn := newStoreWithDB(t)
	for i := range 30 {
		seedAuto(t, conn, fmt.Sprintf("auto_%d", 1000+i), int64(1000+i), 0)
	}
	if _, err := s.Save(context.Background(), autoReq("the newest run")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := countAuto(t, conn); got != 30 {
		t.Fatalf("auto count = %d, want 30", got)
	}
	if rowExists(t, conn, "auto_1000") {
		t.Error("oldest auto (auto_1000) should have been evicted")
	}
	if !rowExists(t, conn, "auto_1029") {
		t.Error("recent auto (auto_1029) should have survived")
	}
}

// Value-aware precedence: with one eviction due, the oldest NEVER-expanded row
// outside the newest-K grace dies — NOT the even-older expanded row (protected
// by proven value) and NOT anything inside the grace window.
func TestSave_AutoEvictionProtectsExpandedOverNeverExpanded(t *testing.T) {
	s, conn := newStoreWithDB(t)
	// 30 seeded: created_at 1000..1029. Oldest (1000) is expanded; rest are not.
	for i := range 30 {
		expand := 0
		if i == 0 {
			expand = 1 // auto_1000 has been pulled before → proven value
		}
		seedAuto(t, conn, fmt.Sprintf("auto_%d", 1000+i), int64(1000+i), expand)
	}
	// 31st (now) is newest → forces exactly one eviction.
	if _, err := s.Save(context.Background(), autoReq("newest run")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := countAuto(t, conn); got != 30 {
		t.Fatalf("auto count = %d, want 30", got)
	}
	if !rowExists(t, conn, "auto_1000") {
		t.Error("expanded auto_1000 must be protected by proven value, not evicted")
	}
	if rowExists(t, conn, "auto_1001") {
		t.Error("oldest never-expanded outside grace (auto_1001) should be the eviction")
	}
}

func TestRecentSessions_ReturnsAutoNewestFirst(t *testing.T) {
	s, conn := newStoreWithDB(t)
	seedSummary(t, conn, "auto_old", "auto", "claude_session", 100, 0)
	seedSummary(t, conn, "auto_new", "auto", "claude_session", 300, 0)
	seedSummary(t, conn, "auto_mid", "auto", "claude_session", 200, 0)
	seedSummary(t, conn, "manual_x", "manual", "crm_upload", 250, 0) // must be excluded

	resp, err := s.RecentSessions(context.Background(), store.RecentSessionsRequest{Limit: 10})
	if err != nil {
		t.Fatalf("RecentSessions: %v", err)
	}
	want := []string{"auto_new", "auto_mid", "auto_old"}
	if len(resp.Sessions) != len(want) {
		t.Fatalf("got %d sessions, want %d (manual must be excluded)", len(resp.Sessions), len(want))
	}
	for i, id := range want {
		if resp.Sessions[i].ID != id {
			t.Errorf("session[%d] = %q, want %q (recency order)", i, resp.Sessions[i].ID, id)
		}
	}
}

func TestRecentSessions_RespectsLimit(t *testing.T) {
	s, conn := newStoreWithDB(t)
	for i := range 3 {
		seedSummary(t, conn, fmt.Sprintf("auto_%d", i), "auto", "claude_session", int64(100+i), 0)
	}
	resp, err := s.RecentSessions(context.Background(), store.RecentSessionsRequest{Limit: 2})
	if err != nil {
		t.Fatalf("RecentSessions: %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2 (limit)", len(resp.Sessions))
	}
	if resp.Sessions[0].ID != "auto_2" || resp.Sessions[1].ID != "auto_1" {
		t.Errorf("limited set = [%s %s], want [auto_2 auto_1]", resp.Sessions[0].ID, resp.Sessions[1].ID)
	}
}

// Manual retention is scoped to origin='manual': saving a manual summary that
// trips the per-task_type newest-5 cap must never evict auto summaries (separate
// budget).
func TestSave_ManualPruneIgnoresAuto(t *testing.T) {
	s, conn := newStoreWithDB(t)
	seedAuto(t, conn, "auto_a", 500, 0)
	seedAuto(t, conn, "auto_b", 501, 0)
	// 5 manual summaries already at the cap in crm_upload.
	for i := range 5 {
		seedSummary(t, conn, fmt.Sprintf("manual_%d", i), "manual", "crm_upload", int64(600+i), 0)
	}

	// One more distinct manual save → 6 manual → manual prune evicts oldest to 5.
	req := store.SaveRequest{
		TaskType: "crm_upload",
		Kind:     "session_summary",
		Title:    "fresh manual recap of the upload retry",
		What:     "investigated the timeout and adjusted batch size",
		Learned:  "cap CRM batch uploads at 200 rows to dodge the gateway timeout",
	}
	if _, err := s.Save(context.Background(), req); err != nil {
		t.Fatalf("manual save: %v", err)
	}

	var manual int
	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM memories WHERE kind='session_summary' AND origin='manual' AND task_type='crm_upload'`,
	).Scan(&manual); err != nil {
		t.Fatalf("count manual: %v", err)
	}
	if manual != 5 {
		t.Errorf("manual summaries = %d, want 5 (per-task_type cap)", manual)
	}
	if got := countAuto(t, conn); got != 2 {
		t.Errorf("auto summaries = %d, want 2 (untouched by manual prune)", got)
	}
}
