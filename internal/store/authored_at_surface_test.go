package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// seedBackdated saves one memory and rewrites its authored_at to a value that
// differs from created_at, so a projection returning 0 or created_at fails.
func seedBackdated(t *testing.T, s *store.Store, conn *sql.DB, req store.SaveRequest) int64 {
	t.Helper()
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("Save %q: %v", req.Title, err)
	}
	_, created := readStamps(t, conn, resp.ID)
	old := created - 400*day
	if _, err := conn.Exec(`UPDATE memories SET authored_at = ? WHERE id = ?`, old, resp.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	return old
}

func TestSearchCompact_CarriesAuthoredAt(t *testing.T) {
	s, conn := newStoreWithDB(t)
	old := seedBackdated(t, s, conn, store.SaveRequest{
		TaskType: "p", Kind: "task_pattern", Title: "zebra gateway retry",
		What: "w", Learned: "retry with backoff", Tags: "zebra",
	})
	resp, err := s.Search(context.Background(), store.SearchRequest{Query: "zebra"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := store.ToCompactSearchResponse(resp, "h")
	if len(got.Results) != 1 {
		t.Fatalf("want 1 row, got %d", len(got.Results))
	}
	if got.Results[0].AuthoredAt != old {
		t.Errorf("compact authored_at = %d, want stored %d", got.Results[0].AuthoredAt, old)
	}
}

func TestContext_RowsCarryAuthoredAt(t *testing.T) {
	s, conn := newStoreWithDB(t)
	base := store.SaveRequest{TaskType: "p", What: "w", Tags: "t"}
	mk := func(kind, title, learned string) int64 {
		r := base
		r.Kind, r.Title, r.Learned = kind, title, learned
		return seedBackdated(t, s, conn, r)
	}
	wantSession := mk("session_summary", "session one", "did things")
	wantRule := mk("user_rule", "rule one", "always do x")
	wantBrowse := mk("error_resolution", "browse one", "fix y")

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "p"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if resp.LastSession == nil || resp.LastSession.AuthoredAt != wantSession {
		t.Errorf("last_session authored_at = %+v, want %d", resp.LastSession, wantSession)
	}
	if len(resp.UserRules) != 1 || resp.UserRules[0].AuthoredAt != wantRule {
		t.Errorf("user_rules authored_at = %+v, want %d", resp.UserRules, wantRule)
	}
	if len(resp.Browse) != 1 || resp.Browse[0].AuthoredAt != wantBrowse {
		t.Errorf("browse authored_at = %+v, want %d", resp.Browse, wantBrowse)
	}
}
