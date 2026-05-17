package store_test

import (
	"testing"
	"time"

	"github.com/samuelmolero/droids-mem/internal/store"
)

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
		if _, err := s.Save(m); err != nil {
			t.Fatalf("seed context fixture: %v", err)
		}
	}
}

func TestContext_ReturnsLastSession(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	resp, err := s.Context(store.ContextRequest{TaskType: "crm_upload"})
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

func TestContext_NoSessionSummary_LastSessionIsNil(t *testing.T) {
	s := newTestStore(t)
	s.Save(store.SaveRequest{
		TaskType: "crm_upload",
		Kind:     "error_resolution",
		Title:    "Phone mapping",
		What:     "field wrong",
		Learned:  "use phone",
	})

	resp, err := s.Context(store.ContextRequest{TaskType: "crm_upload"})
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

	resp, err := s.Context(store.ContextRequest{TaskType: "crm_upload"})
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

	resp, err := s.Context(store.ContextRequest{TaskType: "crm_upload", Query: "phone mapping csv"})
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
		if m.Snippet == "" {
			t.Error("browse memory must have snippet")
		}
		if m.Kind == "session_summary" || m.Kind == "user_rule" {
			t.Errorf("browse tier should only contain error_resolution/task_pattern, got %q", m.Kind)
		}
	}
}

func TestContext_NoCrossTaskContamination(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)
	s.Save(store.SaveRequest{
		TaskType: "email_sync",
		Kind:     "error_resolution",
		Title:    "SMTP auth failure",
		What:     "535 error",
		Learned:  "use app password",
		Tags:     "smtp auth",
	})

	resp, _ := s.Context(store.ContextRequest{TaskType: "crm_upload", Query: "upload phone"})
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

	resp, _ := s.Context(store.ContextRequest{TaskType: "crm_upload", Query: "phone mapping"})
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
	resp, err := s.Context(store.ContextRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Context without query: %v", err)
	}
	_ = resp
}

func TestContext_Validation_MissingTaskType(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Context(store.ContextRequest{})
	if err == nil {
		t.Error("expected validation error for missing task_type")
	}
}

func TestContext_SessionSummaryRetention(t *testing.T) {
	s := newTestStore(t)

	for i := 0; i < 7; i++ {
		time.Sleep(time.Millisecond)
		s.Save(store.SaveRequest{
			TaskType: "crm_upload",
			Kind:     "session_summary",
			Title:    "Session summary run",
			What:     "completed",
			Learned:  "lesson learned during run",
			Tags:     "",
			Force:    i > 0, // force-update bypasses dedupe for the rolling-window test
		})
	}

	resp, err := s.Context(store.ContextRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	_ = resp
}
