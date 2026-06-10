package main_test

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelmolero/droids-mem/internal/mcpserver"
)

// Dry-run must exercise the full save pipeline without persisting anything.
func TestE2E_DryRunDoesNotPersist(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	out := cli(t, dbPath, []int{10}, "save",
		"--task-type", "crm_upload", "--kind", "error_resolution",
		"--title", "Dry run title", "--what", "dry run what", "--learned", "dry run lesson",
		"--dry-run")
	var preview struct {
		Status string `json:"status"`
		Would  string `json:"would"`
	}
	mustParseJSON(t, out, &preview)
	if preview.Status != "dry_run" || preview.Would != "saved" {
		t.Fatalf("preview = %+v, want status=dry_run would=saved", preview)
	}

	listOut := cli(t, dbPath, nil, "list", "--task-type", "crm_upload")
	var list struct {
		Total int `json:"total"`
	}
	mustParseJSON(t, listOut, &list)
	if list.Total != 0 {
		t.Fatalf("dry-run persisted %d memories, want 0", list.Total)
	}

	// Dry-run against an existing duplicate predicts the skip.
	cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload", "--kind", "error_resolution",
		"--title", "Dry run title", "--what", "dry run what", "--learned", "dry run lesson")
	out = cli(t, dbPath, []int{10}, "save",
		"--task-type", "crm_upload", "--kind", "error_resolution",
		"--title", "Dry run title", "--what", "dry run what", "--learned", "dry run lesson",
		"--dry-run")
	mustParseJSON(t, out, &preview)
	if preview.Would != "skipped" {
		t.Fatalf("duplicate dry-run would = %q, want skipped", preview.Would)
	}
}

func TestE2E_TaskTypeCapRejected(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	cli(t, dbPath, []int{2}, "save",
		"--task-type", strings.Repeat("x", 65), "--kind", "task_pattern",
		"--title", "t", "--what", "w", "--learned", "l")
}

func TestE2E_ScopeFlag(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	out := cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload", "--kind", "task_pattern",
		"--title", "Scoped", "--what", "w", "--learned", "l",
		"--scope", "personal")
	var resp struct {
		Status string `json:"status"`
	}
	mustParseJSON(t, out, &resp)
	if resp.Status != "saved" {
		t.Fatalf("status = %q, want saved", resp.Status)
	}

	cli(t, dbPath, []int{2}, "save",
		"--task-type", "crm_upload", "--kind", "task_pattern",
		"--title", "Bad scope", "--what", "w", "--learned", "l",
		"--scope", "global")
}

// /identity must answer the HMAC challenge with a proof derived from the
// bearer token, and reject requests without a nonce.
func TestServe_IdentityChallenge(t *testing.T) {
	s := startServer(t)
	defer s.stop()

	resp, err := http.Get("http://" + s.addr + "/identity?nonce=e2e-nonce")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("identity status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		Server string `json:"server"`
		Proof  string `json:"proof"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse identity: %v\nraw: %s", err, body)
	}
	if want := mcpserver.IdentityProof(testToken, "e2e-nonce"); payload.Proof != want {
		t.Fatalf("proof = %q, want %q", payload.Proof, want)
	}
	if payload.Server != mcpserver.ServerName {
		t.Fatalf("server = %q, want %q", payload.Server, mcpserver.ServerName)
	}

	noNonce, err := http.Get("http://" + s.addr + "/identity")
	if err != nil {
		t.Fatalf("identity no-nonce: %v", err)
	}
	defer noNonce.Body.Close()
	if noNonce.StatusCode != http.StatusBadRequest {
		t.Fatalf("no-nonce status = %d, want 400", noNonce.StatusCode)
	}
}
