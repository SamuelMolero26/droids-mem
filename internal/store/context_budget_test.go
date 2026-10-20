package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

func seedBudgetRules(t *testing.T, s *store.Store, taskType string, count, learnedSize int) {
	t.Helper()
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO memories
			(id, session_id, task_type, kind, title, what, learned, tags, fingerprint,
			 created_at, updated_at, scope, scrub_pattern_version, origin, authored_at)
		VALUES (?, ?, ?, 'user_rule', ?, ?, ?, '', ?, ?, ?, 'personal', 1, 'manual', ?)
	`)
	if err != nil {
		t.Fatalf("prepare seed: %v", err)
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for i := range count {
		id := fmt.Sprintf("mem_%026d", i)
		title := fmt.Sprintf("Budget rule %05d", i)
		what := fmt.Sprintf("Context for budget rule %05d", i)
		learned := fmt.Sprintf("%05d%s", i, strings.Repeat("<", learnedSize))
		fingerprint := fmt.Sprintf("%064x", i+1)
		if _, err := stmt.Exec(
			id,
			"sess_budget",
			taskType,
			title,
			what,
			learned,
			fingerprint,
			now,
			now,
			now,
		); err != nil {
			t.Fatalf("seed rule %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatalf("close seed statement: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed transaction: %v", err)
	}
}

func TestContext_EncodedPayloadStaysBoundedAcrossModes(t *testing.T) {
	s := newTestStore(t)
	seedBudgetRules(t, s, "escaped_budget", 36, 3500)

	for _, mode := range []store.ContextMode{store.ModeOrient, store.ModeDeep, store.ModeRefresh} {
		t.Run(string(mode), func(t *testing.T) {
			resp, err := s.Context(context.Background(), store.ContextRequest{
				TaskType: "escaped_budget",
				Mode:     mode,
			})
			if err != nil {
				t.Fatalf("Context: %v", err)
			}
			encoded, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			if len(encoded) > store.MaxContextPayloadBytes {
				t.Fatalf("encoded response = %d bytes, want <= %d", len(encoded), store.MaxContextPayloadBytes)
			}
			if resp.StubbedCount == 0 {
				t.Fatal("escaped bodies should force at least one returned stub")
			}
			if resp.UserRulesTotal != 36 {
				t.Errorf("user_rules_total = %d, want 36", resp.UserRulesTotal)
			}
		})
	}
}

func TestContext_HardOmissionsAreExactAndDeterministic(t *testing.T) {
	s := newTestStore(t)
	seedBudgetRules(t, s, "omission_budget", 5000, 100)

	first, err := s.Context(context.Background(), store.ContextRequest{
		TaskType: "omission_budget",
		Mode:     store.ModeDeep,
	})
	if err != nil {
		t.Fatalf("first Context: %v", err)
	}
	second, err := s.Context(context.Background(), store.ContextRequest{
		TaskType: "omission_budget",
		Mode:     store.ModeDeep,
	})
	if err != nil {
		t.Fatalf("second Context: %v", err)
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first response: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second response: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("repeated calls selected different context")
	}
	if len(firstJSON) > store.MaxContextPayloadBytes {
		t.Fatalf("encoded response = %d bytes, want <= %d", len(firstJSON), store.MaxContextPayloadBytes)
	}
	if first.OmittedCount == 0 || first.Reason == "" {
		t.Fatalf("omission metadata missing: %+v", first)
	}
	if got := first.OmittedByKind["user_rule"]; got != first.OmittedCount {
		t.Errorf("user_rule omissions = %d, want omitted_count %d", got, first.OmittedCount)
	}
	if len(first.UserRules)+first.OmittedCount != first.UserRulesTotal {
		t.Errorf("returned + omitted = %d, want total %d", len(first.UserRules)+first.OmittedCount, first.UserRulesTotal)
	}
	if first.UserRulesTotal != 5000 {
		t.Errorf("user_rules_total = %d, want 5000", first.UserRulesTotal)
	}
}
