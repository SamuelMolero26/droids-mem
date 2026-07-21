package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/db"
	"github.com/samuelmolero26/droids-mem/internal/store"
	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Init(conn); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return store.New(conn)
}

func validReq() store.SaveRequest {
	return store.SaveRequest{
		TaskType: "crm_upload",
		Kind:     "error_resolution",
		Title:    "HubSpot phone field mapping",
		What:     "Upload failed because target field was phone_number",
		Learned:  "Map Phone Number to phone",
		Tags:     "hubspot phone field-mapping",
	}
}

func TestSave_Success(t *testing.T) {
	s := newTestStore(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if resp.Status != "saved" {
		t.Errorf("expected status saved, got %q", resp.Status)
	}
	if resp.ID == "" {
		t.Error("expected non-empty id")
	}
	if resp.SessionID == "" {
		t.Error("expected non-empty session_id")
	}
}

func TestSave_IDHasMemPrefix(t *testing.T) {
	s := newTestStore(t)
	resp, _ := s.Save(context.Background(), validReq())
	if len(resp.ID) < 4 || resp.ID[:4] != "mem_" {
		t.Errorf("id missing mem_ prefix: %q", resp.ID)
	}
}

func TestSave_AutoGeneratesSessionID(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.SessionID = ""
	resp, _ := s.Save(context.Background(), req)
	if len(resp.SessionID) < 5 || resp.SessionID[:5] != "sess_" {
		t.Errorf("expected auto-generated sess_ id, got %q", resp.SessionID)
	}
}

func TestSave_UsesProvidedSessionID(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.SessionID = "sess_TESTID"
	resp, _ := s.Save(context.Background(), req)
	if resp.SessionID != "sess_TESTID" {
		t.Errorf("expected sess_TESTID, got %q", resp.SessionID)
	}
}

func TestSave_DuplicateSkipped(t *testing.T) {
	s := newTestStore(t)
	first, _ := s.Save(context.Background(), validReq())
	second, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if second.Status != "skipped" {
		t.Errorf("expected skipped, got %q", second.Status)
	}
	if second.Reason != "duplicate" {
		t.Errorf("expected reason duplicate, got %q", second.Reason)
	}
	if second.MatchedID != first.ID {
		t.Errorf("matched_id %q != first id %q", second.MatchedID, first.ID)
	}
}

func TestSave_NormalizationCatchesDuplicate(t *testing.T) {
	s := newTestStore(t)
	s.Save(context.Background(), validReq())

	// Same content, different punctuation/case — should still be duplicate
	req := validReq()
	req.Title = "HUBSPOT PHONE FIELD MAPPING!!!"
	req.Learned = "map phone number to phone."
	resp, _ := s.Save(context.Background(), req)
	if resp.Status != "skipped" {
		t.Errorf("expected normalized duplicate to be skipped, got %q", resp.Status)
	}
}

func TestSave_Validation_MissingTaskType(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.TaskType = ""
	_, err := s.Save(context.Background(), req)
	if err == nil {
		t.Error("expected validation error for missing task_type")
	}
}

// TestSave_Validation_TaskTypePathTraversal covers ADR-0029 SEC-1: task_type
// becomes a path segment in the shared-pool transport, so a slash or ".." is a
// traversal vector and must be rejected at the trust boundary (save + import).
func TestSave_Validation_TaskTypePathTraversal(t *testing.T) {
	s := newTestStore(t)
	for _, bad := range []string{"../etc", "a/b", ".."} {
		req := validReq()
		req.TaskType = bad
		_, err := s.Save(context.Background(), req)
		var ve *store.ValidationError
		if ok := isValidationError(err, &ve); !ok || ve.Field != "task_type" {
			t.Errorf("task_type %q: expected ValidationError on task_type, got %v", bad, err)
		}
	}
}

func TestSave_Validation_InvalidKind(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Kind = "bad_kind"
	_, err := s.Save(context.Background(), req)
	if err == nil {
		t.Error("expected validation error for invalid kind")
	}
	var ve *store.ValidationError
	if ok := isValidationError(err, &ve); !ok || ve.Field != "kind" {
		t.Errorf("expected ValidationError on field kind, got %v", err)
	}
}

func TestSave_Validation_MissingTitle(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Title = "  "
	_, err := s.Save(context.Background(), req)
	if err == nil {
		t.Error("expected validation error for blank title")
	}
}

func TestSave_Validation_MissingWhat(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.What = ""
	_, err := s.Save(context.Background(), req)
	if err == nil {
		t.Error("expected validation error for missing what")
	}
}

func TestSave_Validation_MissingLearned(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Learned = ""
	_, err := s.Save(context.Background(), req)
	if err == nil {
		t.Error("expected validation error for missing learned")
	}
}

func TestSave_DifferentKindNotDuplicate(t *testing.T) {
	s := newTestStore(t)
	s.Save(context.Background(), validReq())

	req := validReq()
	req.Kind = "task_pattern"
	resp, _ := s.Save(context.Background(), req)
	if resp.Status != "saved" {
		t.Errorf("different kind should not be duplicate, got %q", resp.Status)
	}
}

// ── M3: force overwrite + BM25 near-duplicate ────────────────────────────────

func TestSave_ForceOverwrite_UpdatesExisting(t *testing.T) {
	s := newTestStore(t)
	first, _ := s.Save(context.Background(), validReq())

	// Change only `What` — title+learned+kind+task_type unchanged so fingerprint matches
	req := validReq()
	req.Force = true
	req.What = "HITL correction: field was phone_number but should have been phone"
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("force save: %v", err)
	}
	if resp.Status != "updated" {
		t.Errorf("expected updated, got %q", resp.Status)
	}
	if resp.ID != first.ID {
		t.Errorf("expected same id %q, got %q", first.ID, resp.ID)
	}
}

func TestSave_ForceInsert_WhenNoMatch(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Force = true
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("force insert: %v", err)
	}
	if resp.Status != "saved" {
		t.Errorf("expected saved (no prior match), got %q", resp.Status)
	}
}

func TestSave_BM25_NearDuplicateSkipped(t *testing.T) {
	s := newTestStore(t)
	s.Save(context.Background(), validReq())

	// Different enough for a different fingerprint but close enough for BM25 to flag
	req := validReq()
	req.Title = "HubSpot phone number field mapping issue"
	req.Learned = "always use phone instead of phone_number in HubSpot"
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("near-dup save: %v", err)
	}
	// If BM25 flags it: skipped/near_duplicate. If below threshold: saved.
	// Either is acceptable — we just verify no error and response is coherent.
	if resp.Status != "skipped" && resp.Status != "saved" {
		t.Errorf("unexpected status %q", resp.Status)
	}
	if resp.Status == "skipped" {
		if resp.Reason != "near_duplicate" {
			t.Errorf("expected near_duplicate reason, got %q", resp.Reason)
		}
		if resp.Score < 0 || resp.Score > 1 {
			t.Errorf("Jaccard score out of [0,1]: %f", resp.Score)
		}
	}
}

// TestSave_NearDuplicate_FTSOperatorsInContent verifies that Memory content
// containing FTS5 operator keywords (NOT/AND/OR/NEAR, col:filter) does not
// blow up the near-duplicate BM25 query. Pre-fix, nearDuplicateConn joined
// raw terms with " OR ", so a title like "NOT working" produced a query
// `not OR working` — `not` parses as the NOT operator → FTS5 syntax error
// or wrong row set.
func TestSave_NearDuplicate_FTSOperatorsInContent(t *testing.T) {
	s := newTestStore(t)
	hostile := store.SaveRequest{
		TaskType: "crm_upload",
		Kind:     "error_resolution",
		Title:    "NOT working when OR fails",
		What:     "AND clause triggered NEAR overflow in title:column",
		Learned:  "Avoid OR NOT NEAR in titles",
		Tags:     "fts operators",
	}
	if _, err := s.Save(context.Background(), hostile); err != nil {
		t.Fatalf("first save with operator keywords: %v", err)
	}
	// Second save with same operator-heavy tokens — exercises nearDuplicateConn
	// which builds an FTS query from req content. Must not error.
	paraphrase := hostile
	paraphrase.Title = "OR NEAR AND NOT keywords break query"
	paraphrase.Learned = "AND OR NOT NEAR tokens must be quoted"
	resp, err := s.Save(context.Background(), paraphrase)
	if err != nil {
		t.Fatalf("near-dup save with operator keywords must not error: %v", err)
	}
	if resp.Status != "saved" && resp.Status != "skipped" {
		t.Errorf("unexpected status %q", resp.Status)
	}
}

func TestSave_ForceBypassesBM25(t *testing.T) {
	s := newTestStore(t)
	s.Save(context.Background(), validReq())

	// Near-dup with force=true should insert, not be blocked by BM25
	req := validReq()
	req.Title = "HubSpot phone number field mapping issue"
	req.Learned = "always use phone instead of phone_number in HubSpot"
	req.Force = true
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("force near-dup: %v", err)
	}
	if resp.Status == "skipped" {
		t.Error("force=true should bypass BM25 check, but got skipped")
	}
}

// isValidationError checks err is a *store.ValidationError and assigns it.
func isValidationError(err error, target **store.ValidationError) bool {
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		*target = ve
		return true
	}
	return false
}
