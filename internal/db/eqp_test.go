package db_test

import (
	"database/sql"
	"strings"
	"testing"
)

// TestEQP_HotQueriesUseCompositeIndex locks in the planner choice for the
// newest-first-ordered hot queries. Each case mirrors a real production query
// (a WHERE-scoped filter, ORDER BY created_at DESC, id DESC) and asserts that
// the named composite index serves both the filter and the full ordering,
// eliminating any temp B-tree sort.
//
// "USE TEMP B-TREE" (not the full "...FOR ORDER BY" string) is the substring
// under test: when an index covers only a PREFIX of the ordering, SQLite
// emits "USE TEMP B-TREE FOR LAST TERM OF ORDER BY", which a full-string
// match against "USE TEMP B-TREE FOR ORDER BY" never catches — a false
// negative that let issue #58's tie-group bug ship undetected.
//
// If a future schema change drops a composite index or reorders its columns,
// these assertions fail loud.
func TestEQP_HotQueriesUseCompositeIndex(t *testing.T) {
	conn := newTestDB(t)
	for i, kind := range []string{"session_summary", "session_summary", "user_rule", "user_rule", "error_resolution"} {
		now := int64(1000000 + i)
		_, err := conn.Exec(`
			INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
			VALUES (?, 'sess', 'crm', ?, 't', 'w', 'l', '', ?, ?, ?)
		`, "mem_"+string(rune('a'+i)), kind, "fp_"+string(rune('a'+i)), now, now)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cases := []struct {
		name      string
		query     string
		args      []any
		wantIndex string
	}{
		{
			name: "prune_session_summary",
			query: `EXPLAIN QUERY PLAN
				SELECT id FROM memories
				WHERE task_type = ? AND kind = 'session_summary'
				ORDER BY created_at DESC, id DESC LIMIT ?`,
			args:      []any{"crm", 5},
			wantIndex: "idx_memories_task_kind_created",
		},
		{
			name: "fetch_last_session",
			query: `EXPLAIN QUERY PLAN
				SELECT id FROM memories
				WHERE task_type = ? AND kind = 'session_summary'
				ORDER BY created_at DESC, id DESC LIMIT 1`,
			args:      []any{"crm"},
			wantIndex: "idx_memories_task_kind_created",
		},
		{
			name: "fetch_user_rules",
			query: `EXPLAIN QUERY PLAN
				SELECT id FROM memories
				WHERE task_type = ? AND kind = 'user_rule'
				ORDER BY created_at DESC, id DESC`,
			args:      []any{"crm"},
			wantIndex: "idx_memories_task_kind_created",
		},
		{
			// prune_auto_summaries / recent_sessions read by origin.
			name: "recent_sessions_by_origin",
			query: `EXPLAIN QUERY PLAN
				SELECT id FROM memories
				WHERE origin = ?
				ORDER BY created_at DESC, id DESC LIMIT ?`,
			args:      []any{"auto", 10},
			wantIndex: "idx_memories_origin_created",
		},
		{
			// list's unfiltered recency read.
			name: "list_unfiltered_recency",
			query: `EXPLAIN QUERY PLAN
				SELECT id FROM memories
				ORDER BY created_at DESC, id DESC LIMIT ?`,
			args:      []any{20},
			wantIndex: "idx_memories_created_at",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := explainPlan(t, conn, tc.query, tc.args...)
			if !strings.Contains(plan, tc.wantIndex) {
				t.Errorf("plan does not use %s:\n%s", tc.wantIndex, plan)
			}
			if strings.Contains(plan, "USE TEMP B-TREE") {
				t.Errorf("plan still sorts via temp B-tree (index not fully serving ORDER BY):\n%s", plan)
			}
		})
	}
}

// TestEQP_DropsLegacyIndex confirms the superseded idx_memories_task_kind
// was dropped by the DDL DROP INDEX IF EXISTS line.
func TestEQP_DropsLegacyIndex(t *testing.T) {
	conn := newTestDB(t)
	var count int
	conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_memories_task_kind'`).Scan(&count)
	if count != 0 {
		t.Errorf("legacy idx_memories_task_kind still present (DROP INDEX did not run)")
	}
}

func explainPlan(t *testing.T, conn *sql.DB, q string, args ...any) string {
	t.Helper()
	rows, err := conn.Query(q, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan EQP: %v", err)
		}
		sb.WriteString(detail)
		sb.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EQP rows: %v", err)
	}
	return sb.String()
}
