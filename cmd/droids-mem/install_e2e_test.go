package main_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runInstall executes the binary with HOME pinned to a temp dir so install
// targets <home>/.claude/settings.json.
func runInstall(t *testing.T, home string, args ...string) []byte {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") {
			env = append(env, kv)
		}
	}
	env = append(env, "HOME="+home)

	cmd := exec.Command(binaryPath, args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			t.Fatalf("install %v exited %d (stderr: %s)", args, ee.ExitCode(), ee.Stderr)
		}
		t.Fatalf("install %v: %v", args, err)
	}
	return out
}

type installResp struct {
	Status      string   `json:"status"`
	Settings    string   `json:"settings"`
	EventsAdded []string `json:"events_added"`
	Command     string   `json:"command"`
}

func TestE2E_InstallWiresHooksIdempotently(t *testing.T) {
	home := t.TempDir()

	var r1 installResp
	mustParseJSON(t, runInstall(t, home, "install"), &r1)
	if r1.Status != "installed" || len(r1.EventsAdded) != 5 {
		t.Fatalf("first install should add 5 events, got %+v", r1)
	}
	if !strings.Contains(r1.Command, "session hook") {
		t.Errorf("hook command = %q, want it to invoke `session hook`", r1.Command)
	}

	// settings.json now has all five events, each pointing at the binary.
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	for _, ev := range []string{"PostToolUse", "Stop", "SessionEnd", "SessionStart", "UserPromptSubmit"} {
		entries, ok := settings.Hooks[ev]
		if !ok || len(entries) == 0 || len(entries[0].Hooks) == 0 {
			t.Fatalf("event %s not wired: %+v", ev, settings.Hooks[ev])
		}
		if !strings.Contains(entries[0].Hooks[0].Command, "session hook") {
			t.Errorf("event %s command = %q", ev, entries[0].Hooks[0].Command)
		}
	}

	// Re-run is idempotent — nothing added the second time.
	var r2 installResp
	mustParseJSON(t, runInstall(t, home, "install"), &r2)
	if len(r2.EventsAdded) != 0 {
		t.Errorf("re-install should add nothing, got %v", r2.EventsAdded)
	}
}

// Existing settings + a user's own hook must survive install.
func TestE2E_InstallPreservesExistingSettings(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"my-own-hook"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	runInstall(t, home, "install")

	b, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	var settings struct {
		Model string `json:"model"`
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	if settings.Model != "opus" {
		t.Errorf("install clobbered model setting: %q", settings.Model)
	}
	// Stop now holds the user's hook AND ours.
	stop := settings.Hooks["Stop"]
	if len(stop) != 2 {
		t.Fatalf("Stop should have 2 entries (user + ours), got %d", len(stop))
	}
}

// ---------- non-Claude hosts (codex, opencode) ----------

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
