package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// TestGetRow_ProjectsAuthoredAt pins the D8 read surface: GetRow (the choke
// point behind mem_get, the CLI, and the TUI detail pane) must project
// authored_at as its own field, distinct from created_at.
func TestGetRow_ProjectsAuthoredAt(t *testing.T) {
	s, conn := newTestStoreWithConn(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	past := time.Now().Add(-72 * time.Hour).Unix()
	if _, err := conn.Exec(`UPDATE memories SET authored_at = ? WHERE id = ?`, past, resp.ID); err != nil {
		t.Fatalf("seed authored_at: %v", err)
	}

	m, err := s.GetRow(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("GetRow: %v", err)
	}
	if m.AuthoredAt != past {
		t.Errorf("AuthoredAt = %d, want %d", m.AuthoredAt, past)
	}
	if m.AuthoredAt == m.CreatedAt {
		t.Fatal("fixture bug: AuthoredAt should differ from CreatedAt")
	}
}

func TestList_ProjectsAuthoredAt(t *testing.T) {
	s, conn := newTestStoreWithConn(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	past := time.Now().Add(-72 * time.Hour).Unix()
	if _, err := conn.Exec(`UPDATE memories SET authored_at = ? WHERE id = ?`, past, resp.ID); err != nil {
		t.Fatalf("seed authored_at: %v", err)
	}

	listResp, err := s.List(context.Background(), store.ListRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listResp.Memories) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(listResp.Memories))
	}
	if got := listResp.Memories[0].AuthoredAt; got != past {
		t.Errorf("AuthoredAt = %d, want %d", got, past)
	}
}
