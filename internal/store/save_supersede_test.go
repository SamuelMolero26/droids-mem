package store_test

import (
	"context"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// userRule returns a user_rule save request with the given title/learned so
// supersession tests can build distinct-but-related rules.
func userRule(title, learned string) store.SaveRequest {
	return store.SaveRequest{
		TaskType: "go_style",
		Kind:     "user_rule",
		Title:    title,
		What:     "preference stated during review",
		Learned:  learned,
		Tags:     "go style indentation",
	}
}

func searchHits(t *testing.T, s *store.Store, query string) []store.SearchResult {
	t.Helper()
	resp, err := s.Search(context.Background(), store.SearchRequest{Query: query, TaskType: "go_style"})
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return resp.Results
}

func containsID(results []store.SearchResult, id string) bool {
	for _, r := range results {
		if r.ID == id {
			return true
		}
	}
	return false
}

// TestSave_Supersede_ArchivesTarget: a save naming supersedes=<id> ARCHIVES the
// target on successful insert (ADR-0030, superseding ADR-0018's hard delete),
// echoes the id, removes it from memories/FTS, and lands it in the archive with
// an archived_at stamp.
func TestSave_Supersede_ArchivesTarget(t *testing.T) {
	s := newTestStore(t)
	old, err := s.Save(context.Background(), userRule("indent rule", "use tabs for indentation in go"))
	if err != nil {
		t.Fatalf("save target: %v", err)
	}

	req := userRule("indent rule v2", "use four spaces and never hard tabs")
	req.Supersedes = old.ID
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("supersede save: %v", err)
	}
	if resp.Status != "saved" {
		t.Fatalf("expected saved, got %q", resp.Status)
	}
	if resp.Superseded != old.ID {
		t.Errorf("expected superseded=%q, got %q", old.ID, resp.Superseded)
	}
	// "tabs" lived only in the old rule — it must be gone from the index.
	if hits := searchHits(t, s, "tabs"); containsID(hits, old.ID) {
		t.Errorf("superseded row %q still present in search", old.ID)
	}
	// The target is not deleted — it is archived, recoverable/auditable.
	arch, err := s.ArchiveList(context.Background(), "go_style")
	if err != nil {
		t.Fatalf("archive list: %v", err)
	}
	var found *store.ArchivedMemory
	for i := range arch.Memories {
		if arch.Memories[i].ID == old.ID {
			found = &arch.Memories[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("superseded row %q not found in archive", old.ID)
	}
	if found.ArchivedAt == 0 {
		t.Error("archived row has zero archived_at, want a timestamp")
	}
	if found.Learned != "use tabs for indentation in go" {
		t.Errorf("archived body not preserved: %q", found.Learned)
	}
}

// TestSave_Supersede_DryRunDoesNotArchive: a dry-run supersede rolls the whole
// txn back, so the target stays live and the archive stays empty.
func TestSave_Supersede_DryRunDoesNotArchive(t *testing.T) {
	s := newTestStore(t)
	old, err := s.Save(context.Background(), userRule("indent rule", "use tabs for indentation in go"))
	if err != nil {
		t.Fatalf("save target: %v", err)
	}
	req := userRule("indent rule v2", "use four spaces and never hard tabs")
	req.Supersedes = old.ID
	req.DryRun = true
	if _, err := s.Save(context.Background(), req); err != nil {
		t.Fatalf("dry-run supersede: %v", err)
	}
	if hits := searchHits(t, s, "tabs"); !containsID(hits, old.ID) {
		t.Error("dry-run supersede should NOT remove the target from search")
	}
	arch, err := s.ArchiveList(context.Background(), "go_style")
	if err != nil {
		t.Fatalf("archive list: %v", err)
	}
	if arch.Total != 0 {
		t.Errorf("dry-run supersede should archive nothing, got %d archived", arch.Total)
	}
}

// TestSave_Supersede_NoDeleteWhenSkipped: when the new save is deduped away by
// some OTHER row, the insert never lands, so the supersede target survives.
func TestSave_Supersede_NoDeleteWhenSkipped(t *testing.T) {
	s := newTestStore(t)
	target, err := s.Save(context.Background(), userRule("naming rule", "use Co. not Company in titles"))
	if err != nil {
		t.Fatalf("save target: %v", err)
	}
	// An unrelated rule the new save will collide with (exact fingerprint twin).
	blocker := userRule("indent rule", "use four spaces for indentation in go")
	if _, err := s.Save(context.Background(), blocker); err != nil {
		t.Fatalf("save blocker: %v", err)
	}

	dup := blocker             // identical title+learned+kind+task_type => same fingerprint
	dup.Supersedes = target.ID // ...but it claims to supersede the unrelated target
	resp, err := s.Save(context.Background(), dup)
	if err != nil {
		t.Fatalf("dup supersede save: %v", err)
	}
	if resp.Status != "skipped" {
		t.Fatalf("expected skipped, got %q", resp.Status)
	}
	if resp.Superseded != "" {
		t.Errorf("no insert happened, so nothing should be superseded; got %q", resp.Superseded)
	}
	if hits := searchHits(t, s, "Company"); !containsID(hits, target.ID) {
		t.Errorf("target %q was deleted despite the save being skipped", target.ID)
	}
}

// TestSave_Supersede_TargetExemptFromDedupe: the named target is excluded from
// the near-duplicate scan, so a reworded replacement is not self-defeatingly
// skipped as a dup of the very row it replaces. The control proves the
// exemption is load-bearing: the same near-dup WITHOUT supersedes is skipped.
func TestSave_Supersede_TargetExemptFromDedupe(t *testing.T) {
	base := userRule("use spaces for indentation in go files",
		"prefer spaces over hard tabs across the whole go codebase always")
	// Near-identical: one extra title token -> different fingerprint, Jaccard well above 0.85.
	reworded := userRule("use spaces for indentation in go files now",
		"prefer spaces over hard tabs across the whole go codebase always")

	// Control: without supersedes, the reworded rule trips the near-dup gate.
	control := newTestStore(t)
	if _, err := control.Save(context.Background(), base); err != nil {
		t.Fatalf("control base: %v", err)
	}
	cResp, err := control.Save(context.Background(), reworded)
	if err != nil {
		t.Fatalf("control reworded: %v", err)
	}
	if cResp.Status != "skipped" || cResp.Reason != "near_duplicate" {
		t.Fatalf("control precondition failed: expected near_duplicate skip, got status=%q reason=%q (test fixture no longer near-dup)", cResp.Status, cResp.Reason)
	}

	// Real path: naming the target as supersedes exempts it -> save lands, target archived.
	s := newTestStore(t)
	old, err := s.Save(context.Background(), base)
	if err != nil {
		t.Fatalf("save base: %v", err)
	}
	req := reworded
	req.Supersedes = old.ID
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("supersede save: %v", err)
	}
	if resp.Status != "saved" {
		t.Errorf("exempted target should let the save land; got %q (reason %q)", resp.Status, resp.Reason)
	}
	if resp.Superseded != old.ID {
		t.Errorf("expected superseded=%q, got %q", old.ID, resp.Superseded)
	}
}
