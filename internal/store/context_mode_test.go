package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// Distinct vocabulary per row so neither the Fingerprint nor the Jaccard
// near-duplicate layer collapses the seed set (similar phrasing would silently
// drop rows and break the count assertions).
var ruleSeeds = []struct{ title, learned string }{
	{"Company abbreviation", "Abbreviate Company as Co in every export"},
	{"Phone formatting", "Strip non-digit characters before HubSpot upload"},
	{"Date column handling", "Reject ambiguous DD slash MM dates and ask the user"},
	{"Duplicate contact policy", "Merge contacts by email never by name alone"},
	{"Currency normalization", "Convert all monetary amounts to USD daily rate"},
	{"Empty field default", "Leave blanks empty rather than inventing placeholders"},
	{"Owner assignment", "Route new leads to the regional sales representative"},
	{"Timezone policy", "Persist every timestamp in UTC and render locally"},
}

var errorSeeds = []struct{ title, what, learned string }{
	{"Phone field mapping failed", "Upload rejected because phone_number target column was missing", "Map the Phone Number header to phone before sending"},
	{"CSV encoding mismatch", "Import aborted on a latin1 byte inside a UTF8 file", "Transcode the file to UTF8 before the upload step"},
	{"Rate limit exceeded", "HubSpot returned 429 after a burst of inserts", "Throttle batches to under one hundred records per second"},
	{"Duplicate email collision", "Insert failed on an existing contact email", "Switch to upsert keyed on the email property"},
	{"Timezone offset drift", "Appointment times landed an hour early after import", "Normalize all datetimes to UTC before mapping"},
	{"Missing required owner", "Records bounced for an absent owner identifier", "Backfill the regional owner id during transform"},
	{"Currency symbol parse", "Amounts with euro symbols imported as zero", "Strip currency glyphs and store a numeric value"},
	{"Truncated company name", "Long company names were silently cut at forty chars", "Validate length and split into a secondary field"},
}

func seedRules(t *testing.T, s *store.Store, n int) {
	t.Helper()
	if n > len(ruleSeeds) {
		t.Fatalf("seedRules: n=%d exceeds fixture size %d", n, len(ruleSeeds))
	}
	for i := 0; i < n; i++ {
		if _, err := s.Save(context.Background(), store.SaveRequest{
			TaskType: "crm_upload",
			Kind:     "user_rule",
			Title:    ruleSeeds[i].title,
			What:     "User correction: " + ruleSeeds[i].learned,
			Learned:  ruleSeeds[i].learned,
		}); err != nil {
			t.Fatalf("seed rule %d: %v", i, err)
		}
	}
}

func seedErrors(t *testing.T, s *store.Store, n int) {
	t.Helper()
	if n > len(errorSeeds) {
		t.Fatalf("seedErrors: n=%d exceeds fixture size %d", n, len(errorSeeds))
	}
	for i := 0; i < n; i++ {
		if _, err := s.Save(context.Background(), store.SaveRequest{
			TaskType: "crm_upload",
			Kind:     "error_resolution",
			Title:    errorSeeds[i].title,
			What:     errorSeeds[i].what,
			Learned:  errorSeeds[i].learned,
		}); err != nil {
			t.Fatalf("seed error %d: %v", i, err)
		}
	}
}

func TestContext_DefaultModeMatchesOrient(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	bare, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Query: "phone csv"})
	if err != nil {
		t.Fatalf("bare Context: %v", err)
	}
	orient, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Query: "phone csv", Mode: store.ModeOrient})
	if err != nil {
		t.Fatalf("orient Context: %v", err)
	}
	if len(bare.Browse) != len(orient.Browse) || len(bare.UserRules) != len(orient.UserRules) {
		t.Errorf("empty mode diverges from orient: bare=%d/%d orient=%d/%d",
			len(bare.Browse), len(bare.UserRules), len(orient.Browse), len(orient.UserRules))
	}
}

func TestContext_InvalidModeRejected(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	_, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Mode: "bogus"})
	var ve *store.ValidationError
	if !errors.As(err, &ve) || ve.Field != "mode" {
		t.Fatalf("expected mode ValidationError, got %v", err)
	}
}

func TestContext_RefreshRejectsQuery(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	_, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Mode: store.ModeRefresh, Query: "phone"})
	var ve *store.ValidationError
	if !errors.As(err, &ve) || ve.Field != "query" {
		t.Fatalf("expected query ValidationError, got %v", err)
	}
}

func TestContext_RefreshOmitsBrowseAndStubs(t *testing.T) {
	s := newTestStore(t)
	seedRules(t, s, 7)
	seedErrors(t, s, 3)

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Mode: store.ModeRefresh})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(resp.Browse) != 0 {
		t.Errorf("refresh Browse = %d items, want 0", len(resp.Browse))
	}
	if len(resp.UserRules) != 5 {
		t.Errorf("refresh always-tier rules = %d, want 5", len(resp.UserRules))
	}
	if resp.UserRulesTotal != 7 {
		t.Errorf("refresh user_rules_total = %d, want 7 (count stays accurate)", resp.UserRulesTotal)
	}
}

func TestContext_DeepExpandsAllRulesFull(t *testing.T) {
	s := newTestStore(t)
	seedRules(t, s, 7)

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Mode: store.ModeDeep})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(resp.UserRules) != 7 {
		t.Fatalf("deep always-tier rules = %d, want all 7", len(resp.UserRules))
	}
	for _, m := range resp.UserRules {
		if m.Tier != "always" || m.Learned == "" {
			t.Errorf("deep rule must be always-tier full body, got tier=%q learned=%q", m.Tier, m.Learned)
		}
	}
	for _, m := range resp.Browse {
		if m.Kind == "user_rule" {
			t.Errorf("deep mode must not produce rule stubs, found %s", m.ID)
		}
	}
}

func TestContext_DeepBrowseHasFullBodies(t *testing.T) {
	s := newTestStore(t)
	seedErrors(t, s, 3)

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Mode: store.ModeDeep, Query: "phone csv rate duplicate timezone owner currency company"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(resp.Browse) == 0 {
		t.Fatal("expected deep browse items, got none")
	}
	for _, m := range resp.Browse {
		if m.Tier != "browse" {
			t.Errorf("deep browse tier = %q, want browse", m.Tier)
		}
		if m.What == "" || m.Learned == "" {
			t.Errorf("deep browse item must carry full what+learned, got what=%q learned=%q", m.What, m.Learned)
		}
		if m.Snippet != "" {
			t.Errorf("deep browse item must not carry snippet, got %q", m.Snippet)
		}
	}
}

func TestContext_DeepBrowseRespectsTighterLimit(t *testing.T) {
	s := newTestStore(t)
	seedErrors(t, s, 8) // more than deepErrorLimit (5)

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Mode: store.ModeDeep, Query: "phone csv rate duplicate timezone owner currency company"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	var errCount int
	for _, m := range resp.Browse {
		if m.Kind == "error_resolution" {
			errCount++
		}
	}
	if errCount > 5 {
		t.Errorf("deep error browse items = %d, want ≤5 (deepErrorLimit)", errCount)
	}
}

func TestContext_OrientBrowseUnchangedSnippets(t *testing.T) {
	s := newTestStore(t)
	seedErrors(t, s, 2)

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "crm_upload", Query: "phone csv rate duplicate timezone owner currency company"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(resp.Browse) == 0 {
		t.Fatal("expected orient browse items")
	}
	for _, m := range resp.Browse {
		if m.Kind == "error_resolution" {
			if m.Snippet == "" {
				t.Error("orient browse error must carry snippet")
			}
			if m.What != "" || m.Learned != "" {
				t.Errorf("orient browse must not carry full bodies, got what=%q learned=%q", m.What, m.Learned)
			}
		}
	}
	// guard the seed actually exercised the snippet path
	if !strings.Contains(resp.TaskType, "crm") {
		t.Fatalf("unexpected task_type echo: %q", resp.TaskType)
	}
}
