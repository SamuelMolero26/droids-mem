package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/db"
	_ "modernc.org/sqlite"
)

func TestGrowthWarnings_UnderThresholds(t *testing.T) {
	if w := growthWarnings(1024, 100); w != nil {
		t.Errorf("expected no warnings, got %v", w)
	}
	// exactly at threshold is still fine — warnings fire strictly above
	if w := growthWarnings(warnDBSizeBytes, warnRowCount); w != nil {
		t.Errorf("expected no warnings at exact thresholds, got %v", w)
	}
}

func TestGrowthWarnings_OverDBSize(t *testing.T) {
	w := growthWarnings(warnDBSizeBytes+1, 100)
	if len(w) != 1 {
		t.Fatalf("warnings = %d, want 1", len(w))
	}
	if !strings.Contains(w[0], "prune --suggest-dupes") {
		t.Errorf("size warning should point at prune --suggest-dupes, got %q", w[0])
	}
}

func TestGrowthWarnings_OverRowCount(t *testing.T) {
	w := growthWarnings(1024, warnRowCount+1)
	if len(w) != 1 {
		t.Fatalf("warnings = %d, want 1", len(w))
	}
	if !strings.Contains(w[0], "near-duplicate") {
		t.Errorf("row warning should mention near-duplicate tuning, got %q", w[0])
	}
}

func TestGrowthWarnings_BothOver(t *testing.T) {
	if w := growthWarnings(warnDBSizeBytes+1, warnRowCount+1); len(w) != 2 {
		t.Fatalf("warnings = %d, want 2", len(w))
	}
}

// TestDoctor_WarnsOnOversizedPayloads covers rows that bypassed the combined
// cap (pre-cap saves, shared-pool imports). The multi-byte row is under the cap
// in characters but over it in bytes, so the count must measure bytes.
func TestDoctor_WarnsOnOversizedPayloads(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Init(conn); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	for i, learned := range []string{
		"short lesson",
		strings.Repeat("世", 2100), // 2100 chars, 6300 bytes
	} {
		if _, err := conn.Exec(`
			INSERT INTO memories
				(id, session_id, task_type, kind, title, what, learned, tags, fingerprint,
				 created_at, updated_at, scope, scrub_pattern_version, origin, authored_at)
			VALUES (?, 'sess_doctor', 'doctor', 'task_pattern', ?, 'context', ?, '', ?, 1, 1, 'personal', 1, 'manual', 1)
		`, fmt.Sprintf("mem_%026d", i), fmt.Sprintf("row %d", i), learned, fmt.Sprintf("%064x", i+1)); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	rep, err := New(conn).Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if len(rep.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly the oversized-payload warning", rep.Warnings)
	}
	want := fmt.Sprintf("1 memories exceed the %d-byte payload cap", MaxMemoryPayloadBytes)
	if !strings.Contains(rep.Warnings[0], want) || !strings.Contains(rep.Warnings[0], "--supersedes") {
		t.Errorf("warning = %q, want %q and a --supersedes hint", rep.Warnings[0], want)
	}
}
