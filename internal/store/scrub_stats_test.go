package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/SamuelMolero26/droids-mem/internal/store"
)

func TestScrubStats_EmptyCorpus(t *testing.T) {
	s := newTestStore(t)
	store.ResetScrubRejectionCountersForTest()

	rep, err := s.ScrubStats()
	if err != nil {
		t.Fatalf("ScrubStats: %v", err)
	}
	if rep.Status != "ok" {
		t.Errorf("status = %q, want 'ok'", rep.Status)
	}
	if rep.RowsTotal != 0 || rep.RowsWithRedactions != 0 || rep.TotalRedactions != 0 {
		t.Errorf("expected zeros, got total=%d redacted=%d redactions=%d",
			rep.RowsTotal, rep.RowsWithRedactions, rep.TotalRedactions)
	}
	if rep.RedactionRate != 0 {
		t.Errorf("redaction_rate = %v, want 0", rep.RedactionRate)
	}
	if len(rep.PerPattern) != 0 {
		t.Errorf("per_pattern = %v, want empty", rep.PerPattern)
	}
	if rep.PatternVersion != store.ScrubPatternVersion {
		t.Errorf("pattern_version = %d, want %d", rep.PatternVersion, store.ScrubPatternVersion)
	}
}

func TestScrubStats_AggregatesAcrossRows(t *testing.T) {
	s := newTestStore(t)
	store.ResetScrubRejectionCountersForTest()

	clean := validReq()
	clean.Title = "clean lesson title"
	if _, err := s.Save(context.Background(), clean); err != nil {
		t.Fatalf("save clean: %v", err)
	}

	withEmail := validReq()
	withEmail.Title = "lesson with email contact"
	withEmail.What = "Reach out to alice@example.com for the schema"
	if _, err := s.Save(context.Background(), withEmail); err != nil {
		t.Fatalf("save with email: %v", err)
	}

	withKeyAndIP := validReq()
	withKeyAndIP.Title = "lesson host plus token"
	withKeyAndIP.What = "Host 192.168.1.42 needs token ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB"
	withKeyAndIP.Learned = "Rotate tokens nightly and pin the bastion host."
	if _, err := s.Save(context.Background(), withKeyAndIP); err != nil {
		t.Fatalf("save with secrets: %v", err)
	}

	rep, err := s.ScrubStats()
	if err != nil {
		t.Fatalf("ScrubStats: %v", err)
	}
	if rep.RowsTotal != 3 {
		t.Errorf("rows_total = %d, want 3", rep.RowsTotal)
	}
	if rep.RowsWithRedactions != 2 {
		t.Errorf("rows_with_redactions = %d, want 2", rep.RowsWithRedactions)
	}
	if rep.TotalRedactions != 3 {
		t.Errorf("total_redactions = %d, want 3 (email + ip + token)", rep.TotalRedactions)
	}
	wantRate := 2.0 / 3.0
	if rep.RedactionRate < wantRate-1e-9 || rep.RedactionRate > wantRate+1e-9 {
		t.Errorf("redaction_rate = %v, want ~%v", rep.RedactionRate, wantRate)
	}
	if rep.PerPattern["email"] != 1 {
		t.Errorf("per_pattern[email] = %d, want 1", rep.PerPattern["email"])
	}
	if rep.PerPattern["private_ipv4"] != 1 {
		t.Errorf("per_pattern[private_ipv4] = %d, want 1", rep.PerPattern["private_ipv4"])
	}
	if rep.PerPattern["github_token"] != 1 {
		t.Errorf("per_pattern[github_token] = %d, want 1", rep.PerPattern["github_token"])
	}
}

func TestScrubStats_RejectedCounters(t *testing.T) {
	s := newTestStore(t)
	store.ResetScrubRejectionCountersForTest()

	// Tag-secret rejection: an email in a tag.
	tagReq := validReq()
	tagReq.Tags = "hubspot alice@example.com phone"
	_, err := s.Save(context.Background(), tagReq)
	if err == nil {
		t.Fatal("expected tag_contains_secret rejection")
	}
	var ve *store.ValidationError
	if !errors.As(err, &ve) || ve.Code != "tag_contains_secret" {
		t.Fatalf("expected tag_contains_secret, got %v", err)
	}

	// Scrub-emptied-learned: learned body is nothing but a redactable token.
	emptyReq := validReq()
	emptyReq.Title = "lesson with sole secret in learned"
	emptyReq.Learned = "alice@example.com"
	_, err = s.Save(context.Background(), emptyReq)
	if err == nil {
		t.Fatal("expected scrub_emptied_learned rejection")
	}
	if !errors.As(err, &ve) || ve.Code != "scrub_emptied_learned" {
		t.Fatalf("expected scrub_emptied_learned, got %v", err)
	}

	rep, err := s.ScrubStats()
	if err != nil {
		t.Fatalf("ScrubStats: %v", err)
	}
	if rep.RejectedSaves.TagContainsSecret != 1 {
		t.Errorf("rejected.tag_contains_secret = %d, want 1",
			rep.RejectedSaves.TagContainsSecret)
	}
	if rep.RejectedSaves.ScrubEmptiedLearned != 1 {
		t.Errorf("rejected.scrub_emptied_learned = %d, want 1",
			rep.RejectedSaves.ScrubEmptiedLearned)
	}
}

func TestRunCorpus_AllPass(t *testing.T) {
	rep, err := store.RunCorpus()
	if err != nil {
		t.Fatalf("RunCorpus: %v", err)
	}
	if rep.Total == 0 {
		t.Fatal("embedded corpus has no cases")
	}
	if rep.Failed != 0 {
		for _, c := range rep.Cases {
			if !c.Pass {
				t.Errorf("case %s/%s failed: %s", c.Category, c.Name, c.Diff)
			}
		}
	}
	if rep.Passed+rep.Failed != rep.Total {
		t.Errorf("passed+failed=%d, total=%d", rep.Passed+rep.Failed, rep.Total)
	}
	if rep.PatternVersion != store.ScrubPatternVersion {
		t.Errorf("pattern_version = %d, want %d", rep.PatternVersion, store.ScrubPatternVersion)
	}
}
