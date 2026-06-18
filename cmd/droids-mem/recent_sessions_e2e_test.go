package main_test

import (
	"path/filepath"
	"testing"
)

// recent-sessions is keyed on origin='auto'. Manual session summaries (the only
// kind creatable via the CLI today — 'auto' is set by the Phase-4 flush path,
// not a user-facing save mode) must never appear. Proves command wiring + JSON
// shape + the origin filter; the positive auto case lands with Phase 4.
func TestE2E_RecentSessionsExcludesManual(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload",
		"--kind", "session_summary",
		"--title", "Manual run summary",
		"--what", "CRM upload completed for client A",
		"--learned", "Phone mapping fixed; company abbreviation noted",
		"--session-id", "sess_manual",
	)

	out := cli(t, dbPath, nil, "recent-sessions", "--limit", "5")
	var resp struct {
		Sessions []struct {
			ID     string `json:"id"`
			Origin string `json:"origin"`
		} `json:"sessions"`
		Total int `json:"total"`
	}
	mustParseJSON(t, out, &resp)

	if resp.Total != 0 || len(resp.Sessions) != 0 {
		t.Errorf("recent-sessions must exclude manual summaries: total=%d sessions=%d", resp.Total, len(resp.Sessions))
	}
}
