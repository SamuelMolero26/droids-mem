package state

import (
	"testing"
)

func withHome(t *testing.T) {
	t.Helper()
	t.Setenv("DROIDS_MEM_HOME", t.TempDir())
}

func TestStageAndReadRoundTrip(t *testing.T) {
	withHome(t)
	in := StagedSummary{
		SessionID: "sess_abc",
		TaskType:  "claude_session",
		Kind:      "session_summary",
		Title:     "refactored the loader",
		What:      "context body",
		Learned:   "cap batches at 200",
		Tags:      "loader perf",
	}
	if err := StageSummary("cc-1", in); err != nil {
		t.Fatalf("StageSummary: %v", err)
	}
	got, err := ReadStaged("cc-1")
	if err != nil {
		t.Fatalf("ReadStaged: %v", err)
	}
	if got == nil {
		t.Fatal("ReadStaged returned nil for a staged session")
	}
	if got.Title != in.Title || got.SessionID != in.SessionID || got.TaskType != in.TaskType {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.StagedAt == 0 {
		t.Error("StagedAt should be auto-stamped")
	}
}

func TestReadStaged_AbsentIsNil(t *testing.T) {
	withHome(t)
	got, err := ReadStaged("nope")
	if err != nil {
		t.Fatalf("ReadStaged: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for absent session, got %+v", got)
	}
}

func TestStageReplaces(t *testing.T) {
	withHome(t)
	_ = StageSummary("cc-1", StagedSummary{Title: "first", TaskType: "t", Learned: "l"})
	_ = StageSummary("cc-1", StagedSummary{Title: "second", TaskType: "t", Learned: "l"})
	got, _ := ReadStaged("cc-1")
	if got.Title != "second" {
		t.Errorf("stage should replace: got %q", got.Title)
	}
}

func TestChangeCounter(t *testing.T) {
	withHome(t)
	if n, _ := ChangeCount("cc-1"); n != 0 {
		t.Errorf("initial count = %d, want 0", n)
	}
	for want := 1; want <= 3; want++ {
		if n, err := IncrementChange("cc-1"); err != nil || n != want {
			t.Fatalf("IncrementChange = %d, %v; want %d", n, err, want)
		}
	}
	if n, _ := ChangeCount("cc-1"); n != 3 {
		t.Errorf("count after 3 increments = %d, want 3", n)
	}
}

func TestClearSession(t *testing.T) {
	withHome(t)
	_ = StageSummary("cc-1", StagedSummary{Title: "x", TaskType: "t", Learned: "l"})
	_, _ = IncrementChange("cc-1")
	if err := ClearSession("cc-1"); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	if got, _ := ReadStaged("cc-1"); got != nil {
		t.Error("staged file should be gone after clear")
	}
	if n, _ := ChangeCount("cc-1"); n != 0 {
		t.Error("count should be gone after clear")
	}
	// idempotent
	if err := ClearSession("cc-1"); err != nil {
		t.Errorf("ClearSession on empty should be no-op: %v", err)
	}
}

func TestListStagedSessions(t *testing.T) {
	withHome(t)
	_ = StageSummary("cc-a", StagedSummary{Title: "a", TaskType: "t", Learned: "l"})
	_ = StageSummary("cc-b", StagedSummary{Title: "b", TaskType: "t", Learned: "l"})
	_, _ = IncrementChange("cc-c") // count only, no staged → must NOT be listed
	ids, err := ListStagedSessions()
	if err != nil {
		t.Fatalf("ListStagedSessions: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("listed %v, want 2 staged sessions", ids)
	}
}

func TestInjectedSet(t *testing.T) {
	withHome(t)
	if s, _ := InjectedSet("cc-1"); len(s) != 0 {
		t.Errorf("initial injected set = %v, want empty", s)
	}
	if err := RecordInjected("cc-1", []string{"mem_1", "mem_2"}); err != nil {
		t.Fatalf("RecordInjected: %v", err)
	}
	if err := RecordInjected("cc-1", []string{"mem_3"}); err != nil {
		t.Fatalf("RecordInjected: %v", err)
	}
	set, _ := InjectedSet("cc-1")
	for _, id := range []string{"mem_1", "mem_2", "mem_3"} {
		if !set[id] {
			t.Errorf("injected set missing %s", id)
		}
	}
	if len(set) != 3 {
		t.Errorf("injected set size = %d, want 3", len(set))
	}
	if err := ClearSession("cc-1"); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	if s, _ := InjectedSet("cc-1"); len(s) != 0 {
		t.Error("injected set should be cleared")
	}
}

func TestSafeID_RejectsTraversal(t *testing.T) {
	withHome(t)
	for _, bad := range []string{"", "../escape", "a/b", "has space", "semi;colon"} {
		if err := StageSummary(bad, StagedSummary{Title: "x", TaskType: "t", Learned: "l"}); err == nil {
			t.Errorf("StageSummary(%q) should reject unsafe id", bad)
		}
	}
}
