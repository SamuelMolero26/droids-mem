package store_test

import (
	"context"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

func ttReq(taskType, title, learned string) store.SaveRequest {
	return store.SaveRequest{
		TaskType: taskType,
		Kind:     "task_pattern",
		Title:    title,
		What:     "context for " + title,
		Learned:  learned,
	}
}

// A save whose task_type differs from an existing one only by separator/case
// (droids_mem vs droids-mem) is silent fragmentation — the response must point
// the agent back at the established slug.
func TestSave_TaskTypeHint_SeparatorNearMiss(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Save(ctx, ttReq("droids-mem", "Alpha lesson about caching", "cache the fetcher")); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if _, err := s.Save(ctx, ttReq("droids-mem", "Bravo lesson on retries", "backoff exponentially")); err != nil {
		t.Fatalf("seed 2: %v", err)
	}

	resp, err := s.Save(ctx, ttReq("droids_mem", "Charlie lesson on indexes", "add composite index"))
	if err != nil {
		t.Fatalf("near-miss save: %v", err)
	}
	if resp.TaskTypeHint != "droids-mem" {
		t.Fatalf("want hint droids-mem, got %q", resp.TaskTypeHint)
	}
}

func TestSave_TaskTypeHint_ExactSlugNoHint(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Save(ctx, ttReq("droids-mem", "Alpha lesson about caching", "cache the fetcher")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp, err := s.Save(ctx, ttReq("droids-mem", "Delta lesson on locks", "prefer per-account locks"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if resp.TaskTypeHint != "" {
		t.Fatalf("exact slug must not hint, got %q", resp.TaskTypeHint)
	}
}

func TestSave_TaskTypeHint_NewProjectNoHint(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Save(ctx, ttReq("droids-mem", "Alpha lesson about caching", "cache the fetcher")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp, err := s.Save(ctx, ttReq("unrelated-crm", "Echo lesson on uploads", "map phone field"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if resp.TaskTypeHint != "" {
		t.Fatalf("genuinely new project must not hint, got %q", resp.TaskTypeHint)
	}
}
