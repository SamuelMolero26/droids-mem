package store_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/db"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

func openProdDB(t *testing.T) (*sql.DB, error) {
	t.Helper()
	return db.Open()
}

func seedContextFixture(t *testing.T, s *store.Store) {
	t.Helper()
	memories := []store.SaveRequest{
		{
			TaskType: "crm_upload",
			Kind:     "session_summary",
			Title:    "Last run summary",
			What:     "Ran CRM upload for client A",
			Learned:  "Import completed, blank company rows need review",
			Tags:     "crm summary",
		},
		{
			TaskType: "crm_upload",
			Kind:     "error_resolution",
			Title:    "HubSpot phone field mapping",
			What:     "Upload failed because target field was phone_number",
			Learned:  "Map Phone Number to phone",
			Tags:     "hubspot phone",
		},
		{
			TaskType: "crm_upload",
			Kind:     "user_rule",
			Title:    "Company name abbreviation",
			What:     "User corrected company field format",
			Learned:  "Always abbreviate Company as Co.",
			Tags:     "company abbreviation",
		},
		{
			TaskType: "crm_upload",
			Kind:     "task_pattern",
			Title:    "CSV date normalization",
			What:     "Import fails when dates are not ISO-8601",
			Learned:  "Normalize all dates to ISO-8601 before upload",
			Tags:     "csv dates",
		},
	}
	for _, m := range memories {
		if _, err := s.Save(context.Background(), m); err != nil {
			t.Fatalf("seed context fixture: %v", err)
		}
	}
}

func TestContext_ReturnsLastSession(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if resp.LastSession == nil {
		t.Fatal("expected last_session, got nil")
	}
	if resp.LastSession.Kind != "session_summary" {
		t.Errorf("expected kind session_summary, got %q", resp.LastSession.Kind)
	}
	if resp.LastSession.Tier != "always" {
		t.Errorf("last_session tier = %q, want always", resp.LastSession.Tier)
	}
}

// TestContext_QueryWithSpecialChars is the direct regression for the reported
// crash: a raw user prompt carrying a comma reached the browse-tier MATCH and
// produced `fts5: syntax error near ","`, so mem_context returned isError and the
// agent saw "no parseable payload". phraseFTSQuery now quotes every token; the
// call must succeed and still surface the browse tier.
func TestContext_QueryWithSpecialChars(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	queries := []string{
		"Research costs, OpenAI vs Anthropic", // the original comma crash
		"map phone: field (hubspot)",          // colon + parens
		"phone OR mapping NOT csv",            // operator keywords as literals
	}
	for _, q := range queries {
		resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Query: q})
		if err != nil {
			t.Errorf("Context query %q errored (should parse): %v", q, err)
			continue
		}
		if resp == nil {
			t.Errorf("Context query %q returned nil response", q)
		}
	}
}

// TestContext_OnlyPunctuationQuery falls back to task_type tokens when the query
// has no real terms, rather than running MATCH on an empty expression.
func TestContext_OnlyPunctuationQuery(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Query: ",,, :::"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
}

func TestContext_NoSessionSummary_LastSessionIsNil(t *testing.T) {
	s := newTestStore(t)
	s.Save(context.Background(), store.SaveRequest{
		TaskType: "crm_upload",
		Kind:     "error_resolution",
		Title:    "Phone mapping",
		What:     "field wrong",
		Learned:  "use phone",
	})

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if resp.LastSession != nil {
		t.Error("expected nil last_session when no session_summary exists")
	}
}

func TestContext_UserRulesAlwaysIncluded(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(resp.UserRules) == 0 {
		t.Fatal("expected user_rules in always tier, got empty")
	}
	for _, m := range resp.UserRules {
		if m.Kind != "user_rule" {
			t.Errorf("user_rules contains kind %q, want only user_rule", m.Kind)
		}
		if m.Tier != "always" {
			t.Errorf("user_rule tier = %q, want always", m.Tier)
		}
		if m.Learned == "" {
			t.Error("user_rule must include full Learned body in always tier")
		}
	}
}

func TestContext_BrowseTierIsSnippetOnly(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Query: "phone mapping csv"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(resp.Browse) == 0 {
		t.Fatal("expected browse memories, got empty")
	}
	for _, m := range resp.Browse {
		if m.Tier != "browse" {
			t.Errorf("browse memory tier = %q, want browse", m.Tier)
		}
		if m.Learned != "" {
			t.Errorf("browse memory must NOT include Learned body (only Snippet), got %q", m.Learned)
		}
		if m.Kind == "session_summary" {
			t.Errorf("browse tier must not contain session_summary")
		}
		if m.Kind == "user_rule" {
			// rule stubs (ADR-0011) are title-only — no snippet
			if m.Snippet != "" {
				t.Errorf("rule stub must not have snippet, got %q", m.Snippet)
			}
		} else if m.Snippet == "" {
			t.Error("browse memory must have snippet")
		}
	}
}

func TestContext_UserRuleOverflowStubs(t *testing.T) {
	s := newTestStore(t)
	rules := []struct{ title, learned string }{
		{"Company name abbreviation", "Always abbreviate Company as Co."},
		{"Phone formatting", "Strip non-digits before upload to HubSpot"},
		{"Date column handling", "Reject ambiguous DD/MM dates, ask the user"},
		{"Duplicate contact policy", "Merge by email, never by name alone"},
		{"Currency normalization", "Convert all amounts to USD with daily rate"},
		{"Empty field default", "Leave blank rather than inventing placeholder data"},
		{"Owner assignment", "New contacts default to the regional sales owner"},
	}
	for _, r := range rules {
		if _, err := s.Save(context.Background(), store.SaveRequest{
			TaskType: "crm_upload",
			Kind:     "user_rule",
			Title:    r.title,
			What:     "User corrected behaviour: " + r.learned,
			Learned:  r.learned,
		}); err != nil {
			t.Fatalf("seed rule %q: %v", r.title, err)
		}
	}

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if resp.UserRulesTotal != len(rules) {
		t.Errorf("user_rules_total = %d, want %d", resp.UserRulesTotal, len(rules))
	}
	if len(resp.UserRules) != 5 {
		t.Fatalf("always-tier rules = %d, want 5", len(resp.UserRules))
	}
	var stubs []store.ContextMemory
	for _, m := range resp.Browse {
		if m.Kind == "user_rule" {
			stubs = append(stubs, m)
		}
	}
	if len(stubs) != len(rules)-5 {
		t.Fatalf("rule stubs in browse = %d, want %d", len(stubs), len(rules)-5)
	}
	for _, st := range stubs {
		if st.Tier != "browse" {
			t.Errorf("stub tier = %q, want browse", st.Tier)
		}
		if st.Title == "" || st.ID == "" {
			t.Error("stub must carry id + title")
		}
		if st.Learned != "" || st.Snippet != "" {
			t.Errorf("stub must be title-only, got learned=%q snippet=%q", st.Learned, st.Snippet)
		}
	}
	// no rule may appear in both tiers
	seen := map[string]bool{}
	for _, m := range resp.UserRules {
		seen[m.ID] = true
	}
	for _, st := range stubs {
		if seen[st.ID] {
			t.Errorf("rule %s appears in both always tier and browse stubs", st.ID)
		}
	}
}

func TestContext_FewRules_NoStubs(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s) // exactly 1 user_rule

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if resp.UserRulesTotal != 1 {
		t.Errorf("user_rules_total = %d, want 1", resp.UserRulesTotal)
	}
	for _, m := range resp.Browse {
		if m.Kind == "user_rule" {
			t.Errorf("no rule stubs expected when rules fit always tier, found %s", m.ID)
		}
	}
}

func TestContext_NoCrossTaskContamination(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)
	s.Save(context.Background(), store.SaveRequest{
		TaskType: "email_sync",
		Kind:     "error_resolution",
		Title:    "SMTP auth failure",
		What:     "535 error",
		Learned:  "use app password",
		Tags:     "smtp auth",
	})

	resp, _ := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Query: "upload phone"})
	for _, m := range resp.Browse {
		if m.Title == "SMTP auth failure" {
			t.Error("email_sync memory leaked into crm_upload browse tier")
		}
	}
	for _, m := range resp.UserRules {
		if m.Title == "SMTP auth failure" {
			t.Error("email_sync memory leaked into crm_upload user_rules")
		}
	}
}

func TestContext_NoDuplicateIDsAcrossTiers(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	resp, _ := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Query: "phone mapping"})
	seen := make(map[string]bool)
	if resp.LastSession != nil {
		seen[resp.LastSession.ID] = true
	}
	for _, m := range resp.UserRules {
		if seen[m.ID] {
			t.Errorf("duplicate id %q across tiers", m.ID)
		}
		seen[m.ID] = true
	}
	for _, m := range resp.Browse {
		if seen[m.ID] {
			t.Errorf("duplicate id %q across tiers", m.ID)
		}
		seen[m.ID] = true
	}
}

func TestContext_QueryFallsBackToTaskType(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	// no query provided — should not error, falls back to task_type tokens
	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Context without query: %v", err)
	}
	_ = resp
}

func TestContext_Validation_MissingTaskType(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Context(context.Background(), store.ContextRequest{})
	if err == nil {
		t.Error("expected validation error for missing task_type")
	}
}

// TestContext_ConcurrentWritesNoCorruption stresses Context() against
// concurrent writers. With BEGIN DEFERRED wrapping the 4 reads on a
// dedicated conn (and SetMaxOpenConns(1) serializing the pool), every
// Context response must be internally consistent — no errors, no missing
// always-tier rows, no partial reads. Pre-fix, the 3 selects on the pool
// could interleave with writer commits and produce inconsistent bundles.
func TestContext_ConcurrentWritesNoCorruption(t *testing.T) {
	// Concurrent goroutines need a shared backing DB; `:memory:` per-conn
	// gives each pool conn its own database. Use a tempfile + the
	// production db.Open() path so SetMaxOpenConns(1) and PRAGMAs apply.
	dir := t.TempDir()
	t.Setenv("DROIDS_MEM_DB", dir+"/mem.db")
	conn, err := openProdDB(t)
	if err != nil {
		t.Fatalf("open prod db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	s := store.New(conn)
	seedContextFixture(t, s)

	const (
		readers = 8
		writers = 4
		iters   = 50
	)
	var wg sync.WaitGroup
	errs := make(chan error, readers*iters+writers*iters)

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Query: "phone csv"})
				if err != nil {
					errs <- err
					return
				}
				// invariant: user_rules + last_session always populated (seed has both)
				if resp.LastSession == nil {
					errs <- &snapshotErr{"last_session disappeared mid-snapshot"}
					return
				}
				if len(resp.UserRules) == 0 {
					errs <- &snapshotErr{"user_rules empty mid-snapshot"}
					return
				}
			}
		}()
	}

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// distinct content per iter to avoid Layer 1/2 dedupe
				req := store.SaveRequest{
					TaskType: "crm_upload",
					Kind:     "error_resolution",
					Title:    "concurrent write " + string(rune('A'+id)),
					What:     "iteration body " + string(rune('a'+i%26)),
					Learned:  "lesson " + string(rune('a'+i%26)) + string(rune('a'+id)),
					Force:    true,
				}
				if _, err := s.Save(context.Background(), req); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent stress: %v", err)
	}
}

type snapshotErr struct{ msg string }

func (e *snapshotErr) Error() string { return e.msg }

func TestContext_SessionSummaryRetention(t *testing.T) {
	s := newTestStore(t)

	for i := 0; i < 7; i++ {
		time.Sleep(time.Millisecond)
		s.Save(context.Background(), store.SaveRequest{
			TaskType: "crm_upload",
			Kind:     "session_summary",
			Title:    "Session summary run",
			What:     "completed",
			Learned:  "lesson learned during run",
			Tags:     "",
			Force:    i > 0, // force-update bypasses dedupe for the rolling-window test
		})
	}

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	_ = resp
}
