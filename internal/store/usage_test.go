package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// seedSized inserts a fully-controlled row so payload byte counts are
// deterministic: Save would run scrub + dedupe over the content and collide
// created_at across same-second calls.
func seedSized(t *testing.T, conn *sql.DB, id, taskType, kind, title, what, learned, tags string) {
	t.Helper()
	_, err := conn.Exec(`
		INSERT INTO memories
			(id, session_id, task_type, kind, title, what, learned, tags, fingerprint,
			 created_at, updated_at)
		VALUES (?, 'sess', ?, ?, ?, ?, ?, ?, ?, 1700000000, 1700000000)`,
		id, taskType, kind, title, what, learned, tags, "fp_"+id)
	if err != nil {
		t.Fatalf("seed sized %s: %v", id, err)
	}
}

func payloadBytes(title, what, learned, tags string) int64 {
	return int64(len(title) + len(what) + len(learned) + len(tags))
}

func TestProjectSizes_SortedByBytesDesc(t *testing.T) {
	s, conn := newTestStoreWithConn(t)

	// alpha: 10 small rows × 10 bytes = 100 bytes, all user_rule.
	for i := range 10 {
		seedSized(t, conn, fmt.Sprintf("alpha-%d", i), "alpha", "user_rule", "a", "bb", "ccc", "dddd")
	}
	// beta: 2 rows, 100 + 20 = 120 bytes across two kinds.
	seedSized(t, conn, "beta-0", "beta", "task_pattern", strings.Repeat("t", 10), strings.Repeat("w", 20), strings.Repeat("l", 30), strings.Repeat("g", 40))
	seedSized(t, conn, "beta-1", "beta", "session_summary", "12345", "12345", "12345", "12345")

	rows, err := s.ProjectSizes(context.Background())
	if err != nil {
		t.Fatalf("ProjectSizes: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ProjectSizes rows = %d, want 2", len(rows))
	}
	betaBytes := payloadBytes(strings.Repeat("t", 10), strings.Repeat("w", 20), strings.Repeat("l", 30), strings.Repeat("g", 40)) +
		payloadBytes("12345", "12345", "12345", "12345")
	alphaBytes := 10 * payloadBytes("a", "bb", "ccc", "dddd")
	if rows[0].TaskType != "beta" || rows[0].Count != 2 || rows[0].Bytes != betaBytes {
		t.Errorf("rows[0] = %+v, want beta count=2 bytes=%d", rows[0], betaBytes)
	}
	if rows[1].TaskType != "alpha" || rows[1].Count != 10 || rows[1].Bytes != alphaBytes {
		t.Errorf("rows[1] = %+v, want alpha count=10 bytes=%d", rows[1], alphaBytes)
	}

	// ByKind splits count plus bytes per kind, bytes-desc.
	if len(rows[0].ByKind) != 2 {
		t.Fatalf("beta ByKind = %+v, want 2 kinds", rows[0].ByKind)
	}
	if rows[0].ByKind[0].Kind != "task_pattern" || rows[0].ByKind[0].Count != 1 || rows[0].ByKind[0].Bytes != 100 {
		t.Errorf("beta ByKind[0] = %+v, want task_pattern count=1 bytes=100", rows[0].ByKind[0])
	}
	if rows[0].ByKind[1].Kind != "session_summary" || rows[0].ByKind[1].Count != 1 || rows[0].ByKind[1].Bytes != 20 {
		t.Errorf("beta ByKind[1] = %+v, want session_summary count=1 bytes=20", rows[0].ByKind[1])
	}
}

func TestProjectSizes_NonASCIIBonusCountsBytes(t *testing.T) {
	s, conn := newTestStoreWithConn(t)

	// `café 𝄞` is 6 runes but 10 UTF-8 bytes (c,a,f + 2-byte é + space + 4-byte 𝄞).
	const title = "café 𝄞"
	if got := len([]rune(title)); got != 6 {
		t.Fatalf("test premise broken: title is %d runes, want 6", got)
	}
	seedSized(t, conn, "uni-0", "uni", "user_rule", title, "", "", "")

	rows, err := s.ProjectSizes(context.Background())
	if err != nil {
		t.Fatalf("ProjectSizes: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ProjectSizes rows = %d, want 1", len(rows))
	}
	if rows[0].Bytes != 10 {
		t.Errorf("uni bytes = %d, want 10 (octet_length, not char count)", rows[0].Bytes)
	}
}

func TestProjectSizes_EmptyCorpus(t *testing.T) {
	s, _ := newTestStoreWithConn(t)

	rows, err := s.ProjectSizes(context.Background())
	if err != nil {
		t.Fatalf("ProjectSizes: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ProjectSizes rows = %d, want 0", len(rows))
	}
	var totalCount int
	var totalBytes int64
	for _, r := range rows {
		totalCount += r.Count
		totalBytes += r.Bytes
	}
	if totalCount != 0 || totalBytes != 0 {
		t.Errorf("totals = %d/%d, want 0/0", totalCount, totalBytes)
	}
}

func TestProjectSizes_TiebreakNameAsc(t *testing.T) {
	s, conn := newTestStoreWithConn(t)

	// Identical payloads → identical bytes; names must break the tie ascending.
	seedSized(t, conn, "m-0", "mango", "user_rule", "same", "same", "same", "same")
	seedSized(t, conn, "a-0", "apple", "user_rule", "same", "same", "same", "same")

	rows, err := s.ProjectSizes(context.Background())
	if err != nil {
		t.Fatalf("ProjectSizes: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ProjectSizes rows = %d, want 2", len(rows))
	}
	if rows[0].TaskType != "apple" || rows[1].TaskType != "mango" {
		t.Errorf("tiebreak order = %q,%q, want apple,mango", rows[0].TaskType, rows[1].TaskType)
	}
	if rows[0].Bytes != rows[1].Bytes {
		t.Errorf("tie premise broken: bytes %d != %d", rows[0].Bytes, rows[1].Bytes)
	}
}
