package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// ---------- scope ----------

func TestSave_DefaultsScopeToShared(t *testing.T) {
	s := newTestStore(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	scope := readScope(t, s, resp.ID)
	if scope != "shared" {
		t.Errorf("default scope = %q, want 'shared'", scope)
	}
}

func TestSave_AcceptsPersonalScope(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Scope = "personal"
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	scope := readScope(t, s, resp.ID)
	if scope != "personal" {
		t.Errorf("scope = %q, want 'personal'", scope)
	}
}

func TestSave_RejectsInvalidScope(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Scope = "team"
	_, err := s.Save(context.Background(), req)
	if err == nil {
		t.Fatal("expected validation error for invalid scope")
	}
	ve := mustValidationError(t, err)
	if ve.Field != "scope" {
		t.Errorf("field = %q, want 'scope'", ve.Field)
	}
	if !ve.Retryable {
		t.Error("scope error should be retryable")
	}
}

// ---------- field caps ----------

func TestSave_FieldTooLarge_Title(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Title = strings.Repeat("a", store.MaxTitleLen+1)
	_, err := s.Save(context.Background(), req)
	ve := mustValidationError(t, err)
	if ve.Code != "field_too_large" {
		t.Errorf("code = %q, want 'field_too_large'", ve.Code)
	}
	if ve.Field != "title" {
		t.Errorf("field = %q, want 'title'", ve.Field)
	}
	if !ve.Retryable {
		t.Error("field_too_large should be retryable")
	}
	if ve.Suggestion == "" {
		t.Error("expected suggestion text")
	}
	if ve.Limit != store.MaxTitleLen {
		t.Errorf("limit = %d, want %d", ve.Limit, store.MaxTitleLen)
	}
	if ve.Actual != store.MaxTitleLen+1 {
		t.Errorf("actual = %d, want %d", ve.Actual, store.MaxTitleLen+1)
	}
	wantMsg := fmt.Sprintf("max %d bytes (got %d)", store.MaxTitleLen, store.MaxTitleLen+1)
	if ve.Message != wantMsg {
		t.Errorf("message = %q, want %q", ve.Message, wantMsg)
	}
}

// Field caps are byte limits (CONTEXT.md "Field cap"), so multi-byte runes
// count at their UTF-8 width: 67 three-byte CJK runes = 201 bytes > 200.
func TestSave_FieldTooLarge_Title_CountsBytes(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Title = strings.Repeat("世", store.MaxTitleLen/3+1)
	_, err := s.Save(context.Background(), req)
	ve := mustValidationError(t, err)
	if ve.Code != "field_too_large" || ve.Field != "title" {
		t.Errorf("code/field = %q/%q, want field_too_large/title", ve.Code, ve.Field)
	}
	if ve.Actual != len(req.Title) {
		t.Errorf("actual = %d, want byte length %d", ve.Actual, len(req.Title))
	}
}

func TestSave_FieldTooLarge_What(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.What = strings.Repeat("x", store.MaxWhatLen+1)
	_, err := s.Save(context.Background(), req)
	ve := mustValidationError(t, err)
	if ve.Code != "field_too_large" || ve.Field != "what" {
		t.Errorf("code/field = %q/%q, want field_too_large/what", ve.Code, ve.Field)
	}
}

func TestSave_FieldTooLarge_Learned(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Learned = strings.Repeat("y", store.MaxLearnedLen+1)
	_, err := s.Save(context.Background(), req)
	ve := mustValidationError(t, err)
	if ve.Code != "field_too_large" || ve.Field != "learned" {
		t.Errorf("code/field = %q/%q, want field_too_large/learned", ve.Code, ve.Field)
	}
}

func TestSave_FieldTooLarge_Tags(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Tags = strings.Repeat("z ", store.MaxTagsLen)
	_, err := s.Save(context.Background(), req)
	ve := mustValidationError(t, err)
	if ve.Code != "field_too_large" || ve.Field != "tags" {
		t.Errorf("code/field = %q/%q, want field_too_large/tags", ve.Code, ve.Field)
	}
}

// ---------- tag pre-scrub ----------

func TestSave_TagContainsSecret_EmailTag(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Tags = "hubspot alice@example.com phone"
	_, err := s.Save(context.Background(), req)
	ve := mustValidationError(t, err)
	if ve.Code != "tag_contains_secret" {
		t.Errorf("code = %q, want 'tag_contains_secret'", ve.Code)
	}
	if ve.Field != "tags" {
		t.Errorf("field = %q, want 'tags'", ve.Field)
	}
	if !ve.Retryable {
		t.Error("tag_contains_secret should be retryable")
	}
	if len(ve.OffendingTags) != 1 || ve.OffendingTags[0] != "alice@example.com" {
		t.Errorf("offending_tags = %v, want [alice@example.com]", ve.OffendingTags)
	}
	if !contains(ve.MatchedPatterns, "email") {
		t.Errorf("matched_patterns = %v, want to contain 'email'", ve.MatchedPatterns)
	}
}

func TestSave_TagContainsSecret_AwsToken(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Tags = "infra AKIAIOSFODNN7EXAMPLE deploy"
	_, err := s.Save(context.Background(), req)
	ve := mustValidationError(t, err)
	if ve.Code != "tag_contains_secret" {
		t.Errorf("code = %q, want 'tag_contains_secret'", ve.Code)
	}
	if !contains(ve.MatchedPatterns, "aws_key") {
		t.Errorf("matched_patterns = %v, want to contain 'aws_key'", ve.MatchedPatterns)
	}
}

func TestSave_TagsScrubCleanPasses(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Tags = "hubspot phone field-mapping"
	if _, err := s.Save(context.Background(), req); err != nil {
		t.Fatalf("clean tags should pass: %v", err)
	}
}

// ---------- scrub on text fields ----------

func TestSave_ScrubsEmailFromLearned(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Learned = "Reach out to alice@example.com when the upload fails"
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if resp.Scrub == nil {
		t.Fatal("expected scrub block in response")
	}
	if resp.Scrub.RedactionCount != 1 {
		t.Errorf("redaction_count = %d, want 1", resp.Scrub.RedactionCount)
	}
	if resp.Scrub.PerPatternCounts["email"] != 1 {
		t.Errorf("email count = %d, want 1", resp.Scrub.PerPatternCounts["email"])
	}
	if !contains(resp.Scrub.FieldsRedacted, "learned") {
		t.Errorf("fields_redacted = %v, want to contain 'learned'", resp.Scrub.FieldsRedacted)
	}
	if resp.Scrub.PatternVersion != store.ScrubPatternVersion {
		t.Errorf("pattern_version = %d, want %d", resp.Scrub.PatternVersion, store.ScrubPatternVersion)
	}

	stored := readLearned(t, s, resp.ID)
	if strings.Contains(stored, "alice@example.com") {
		t.Errorf("stored learned still contains raw email: %q", stored)
	}
	if !strings.Contains(stored, "[EMAIL]") {
		t.Errorf("stored learned missing [EMAIL] token: %q", stored)
	}
}

func TestSave_NoScrubBlockWhenClean(t *testing.T) {
	s := newTestStore(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if resp.Scrub != nil {
		t.Errorf("expected no scrub block on clean save, got %+v", resp.Scrub)
	}
}

func TestSave_ScrubCountsJSONPersisted(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.What = "Email contact: bob@example.com from host 10.0.0.5 reported it"
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw := readScrubCounts(t, s, resp.ID)
	if !raw.Valid {
		t.Fatal("expected scrub_counts JSON to be non-NULL")
	}
	var stored store.ScrubReport
	if err := json.Unmarshal([]byte(raw.String), &stored); err != nil {
		t.Fatalf("unmarshal scrub_counts: %v", err)
	}
	if stored.RedactionCount != 2 {
		t.Errorf("stored redaction_count = %d, want 2", stored.RedactionCount)
	}
	if stored.PerPatternCounts["email"] != 1 {
		t.Errorf("stored email count = %d, want 1", stored.PerPatternCounts["email"])
	}
	if stored.PerPatternCounts["private_ipv4"] != 1 {
		t.Errorf("stored private_ipv4 count = %d, want 1", stored.PerPatternCounts["private_ipv4"])
	}
}

func TestSave_ScrubCountsNullWhenClean(t *testing.T) {
	s := newTestStore(t)
	resp, _ := s.Save(context.Background(), validReq())
	raw := readScrubCounts(t, s, resp.ID)
	if raw.Valid {
		t.Errorf("expected NULL scrub_counts on clean save, got %q", raw.String)
	}
}

// ---------- empty-after-scrub ----------

func TestSave_EmptyAfterScrub_RejectsLearned(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Learned = "alice@example.com bob@example.com"
	_, err := s.Save(context.Background(), req)
	ve := mustValidationError(t, err)
	if ve.Code != "scrub_emptied_learned" {
		t.Errorf("code = %q, want 'scrub_emptied_learned'", ve.Code)
	}
	if ve.Field != "learned" {
		t.Errorf("field = %q, want 'learned'", ve.Field)
	}
	if ve.Retryable {
		t.Error("scrub_emptied_learned should NOT be retryable")
	}
	if ve.Scrub == nil {
		t.Fatal("expected scrub block on scrub_emptied_learned error")
	}
	if ve.Scrub.RedactionCount < 2 {
		t.Errorf("expected at least 2 redactions in scrub block, got %d", ve.Scrub.RedactionCount)
	}
}

// ---------- skip responses echo matched row ----------

func TestSave_SkipDuplicateEchoesMatchedRow(t *testing.T) {
	s := newTestStore(t)
	first, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	second, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if second.Status != "skipped" || second.Reason != "duplicate" {
		t.Fatalf("expected skipped/duplicate, got status=%q reason=%q", second.Status, second.Reason)
	}
	if second.MatchedID != first.ID {
		t.Errorf("matched_id = %q, want %q", second.MatchedID, first.ID)
	}
	if second.MatchedTitle != validReq().Title {
		t.Errorf("matched_title = %q, want %q", second.MatchedTitle, validReq().Title)
	}
	if second.MatchedLearned != validReq().Learned {
		t.Errorf("matched_learned = %q, want %q", second.MatchedLearned, validReq().Learned)
	}
}

// ---------- helpers ----------

func mustValidationError(t *testing.T, err error) *store.ValidationError {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ve *store.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *store.ValidationError, got %T: %v", err, err)
	}
	return ve
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func readScope(t *testing.T, s *store.Store, id string) string {
	t.Helper()
	var scope string
	if err := s.DB().QueryRow(`SELECT scope FROM memories WHERE id = ?`, id).Scan(&scope); err != nil {
		t.Fatalf("read scope: %v", err)
	}
	return scope
}

func readLearned(t *testing.T, s *store.Store, id string) string {
	t.Helper()
	var learned string
	if err := s.DB().QueryRow(`SELECT learned FROM memories WHERE id = ?`, id).Scan(&learned); err != nil {
		t.Fatalf("read learned: %v", err)
	}
	return learned
}

func readScrubCounts(t *testing.T, s *store.Store, id string) sql.NullString {
	t.Helper()
	var raw sql.NullString
	if err := s.DB().QueryRow(`SELECT scrub_counts FROM memories WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read scrub_counts: %v", err)
	}
	return raw
}
