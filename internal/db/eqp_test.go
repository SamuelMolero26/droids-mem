package db_test

import (
	"database/sql"
	"strings"
	"testing"
)

// TestEQP_HotQueriesUseCompositeIndex locks in the planner choice for the
// three non-FTS hot queries that ORDER BY created_at DESC inside a
// (task_type, kind) filter. The composite idx_memories_task_kind_created
// must serve both filter + ordering, eliminating the temp B-tree sort.
//
// If a future schema change drops the composite or reorders its columns,
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
		name  string
		query string
		args  []any
	}{
		{
			name: "prune_session_summary",
			query: `EXPLAIN QUERY PLAN
				SELECT id FROM memories
				WHERE task_type = ? AND kind = 'session_summary'
				ORDER BY created_at DESC LIMIT ?`,
			args: []any{"crm", 5},
		},
		{
			name: "fetch_last_session",
			query: `EXPLAIN QUERY PLAN
				SELECT id FROM memories
				WHERE task_type = ? AND kind = 'session_summary'
				ORDER BY created_at DESC LIMIT 1`,
			args: []any{"crm"},
		},
		{
			name: "fetch_user_rules",
			query: `EXPLAIN QUERY PLAN
				SELECT id FROM memories
				WHERE task_type = ? AND kind = 'user_rule'
				ORDER BY created_at DESC`,
			args: []any{"crm"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := explainPlan(t, conn, tc.query, tc.args...)
			if !strings.Contains(plan, "idx_memories_task_kind_created") {
				t.Errorf("plan does not use idx_memories_task_kind_created:\n%s", plan)
			}
			if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
				t.Errorf("plan still sorts via temp B-tree (composite index not serving ORDER BY):\n%s", plan)
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
