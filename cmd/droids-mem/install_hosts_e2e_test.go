package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type hostInstallResp struct {
	Status string `json:"status"`
	Host   string `json:"host"`
	Config string `json:"config"`
}

func TestE2E_InstallCodexIdempotent(t *testing.T) {
	home := t.TempDir()

	// Pre-existing config must be preserved, not rewritten.
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o750); err != nil {
		t.Fatal(err)
	}
	pre := "model = \"gpt-5\"\n"
	cfgPath := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	var r1 hostInstallResp
	mustParseJSON(t, runInstall(t, home, "install", "--host", "codex"), &r1)
	if r1.Status != "installed" || r1.Host != "codex" {
		t.Fatalf("first install: %+v", r1)
	}

	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.HasPrefix(got, pre) {
		t.Errorf("existing config not preserved:\n%s", got)
	}
	for _, want := range []string{"[mcp_servers.droids-mem]", `args = ["serve", "--stdio"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}

	var r2 hostInstallResp
	mustParseJSON(t, runInstall(t, home, "install", "--host", "codex"), &r2)
	if r2.Status != "already_installed" {
		t.Errorf("re-install status = %q, want already_installed", r2.Status)
	}
	b2, _ := os.ReadFile(cfgPath)
	if string(b2) != got {
		t.Errorf("re-install modified the config")
	}
}

func TestE2E_InstallOpencodeIdempotent(t *testing.T) {
	home := t.TempDir()

	// Pre-existing keys must survive the merge.
	ocDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(ocDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ocDir, "opencode.json")
	if err := os.WriteFile(cfgPath, []byte(`{"theme":"dark","mcp":{"other":{"type":"local","command":["x"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var r1 hostInstallResp
	mustParseJSON(t, runInstall(t, home, "install", "--host", "opencode"), &r1)
	if r1.Status != "installed" || r1.Host != "opencode" {
		t.Fatalf("first install: %+v", r1)
	}

	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Theme string `json:"theme"`
		MCP   map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			Enabled bool     `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Theme != "dark" {
		t.Errorf("existing theme key lost")
	}
	if _, ok := cfg.MCP["other"]; !ok {
		t.Errorf("existing mcp entry lost")
	}
	dm, ok := cfg.MCP["droids-mem"]
	if !ok || dm.Type != "local" || !dm.Enabled {
		t.Fatalf("droids-mem entry wrong: %+v", dm)
	}
	if len(dm.Command) != 3 || dm.Command[1] != "serve" || dm.Command[2] != "--stdio" {
		t.Errorf("command = %v, want [<bin> serve --stdio]", dm.Command)
	}

	var r2 hostInstallResp
	mustParseJSON(t, runInstall(t, home, "install", "--host", "opencode"), &r2)
	if r2.Status != "already_installed" {
		t.Errorf("re-install status = %q, want already_installed", r2.Status)
	}
}
