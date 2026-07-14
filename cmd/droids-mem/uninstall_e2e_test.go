package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type uninstallResp struct {
	Status        string   `json:"status"`
	Settings      string   `json:"settings"`
	EventsRemoved []string `json:"events_removed"`
	Host          string   `json:"host"`
	Config        string   `json:"config"`
}

// install then uninstall must return settings.json to its exact prior bytes,
// touching only droids-mem's own hook entries.
func TestE2E_UninstallHooksRoundTrip(t *testing.T) {
	home := t.TempDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}

	// Pre-existing unrelated settings, in the tool's own canonical form so the
	// round-trip comparison isn't a formatting artifact.
	pre, err := json.MarshalIndent(map[string]any{"model": "sonnet"}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pre = append(pre, '\n')
	if err := os.WriteFile(settingsPath, pre, 0o600); err != nil {
		t.Fatal(err)
	}

	runInstall(t, home, "install") // reuses the HOME-pinned binary runner

	var r uninstallResp
	mustParseJSON(t, runInstall(t, home, "uninstall"), &r)
	if r.Status != "uninstalled" || len(r.EventsRemoved) != 5 {
		t.Fatalf("uninstall should remove 5 events, got %+v", r)
	}

	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(pre) {
		t.Errorf("round-trip not byte-identical:\ngot  %q\nwant %q", got, pre)
	}

	// Second uninstall removes nothing.
	var r2 uninstallResp
	mustParseJSON(t, runInstall(t, home, "uninstall"), &r2)
	if len(r2.EventsRemoved) != 0 {
		t.Errorf("second uninstall removed %v, want none", r2.EventsRemoved)
	}
}

func TestE2E_UninstallCodexRoundTrip(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o750); err != nil {
		t.Fatal(err)
	}
	pre := "model = \"gpt-5\"\n"
	if err := os.WriteFile(cfgPath, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	runInstall(t, home, "install", "--host", "codex")

	var r uninstallResp
	mustParseJSON(t, runInstall(t, home, "uninstall", "--host", "codex"), &r)
	if r.Status != "uninstalled" || r.Host != "codex" {
		t.Fatalf("uninstall codex: %+v", r)
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != pre {
		t.Errorf("codex round-trip not byte-identical:\ngot  %q\nwant %q", got, pre)
	}

	var r2 uninstallResp
	mustParseJSON(t, runInstall(t, home, "uninstall", "--host", "codex"), &r2)
	if r2.Status != "already_absent" {
		t.Errorf("second codex uninstall = %q, want already_absent", r2.Status)
	}
}

func TestE2E_UninstallOpencodeRoundTrip(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o750); err != nil {
		t.Fatal(err)
	}
	pre, err := json.MarshalIndent(map[string]any{"theme": "dark"}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pre = append(pre, '\n')
	if err := os.WriteFile(cfgPath, pre, 0o600); err != nil {
		t.Fatal(err)
	}

	runInstall(t, home, "install", "--host", "opencode")

	var r uninstallResp
	mustParseJSON(t, runInstall(t, home, "uninstall", "--host", "opencode"), &r)
	if r.Status != "uninstalled" || r.Host != "opencode" {
		t.Fatalf("uninstall opencode: %+v", r)
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != string(pre) {
		t.Errorf("opencode round-trip not byte-identical:\ngot  %q\nwant %q", got, pre)
	}

	var r2 uninstallResp
	mustParseJSON(t, runInstall(t, home, "uninstall", "--host", "opencode"), &r2)
	if r2.Status != "already_absent" {
		t.Errorf("second opencode uninstall = %q, want already_absent", r2.Status)
	}
}
