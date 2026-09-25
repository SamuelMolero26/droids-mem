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

// ---------- stdio transport (Claude Code registration) ----------

// fakeClaude puts a stub `claude` first on PATH that appends its argv to a log
// and exits 0, except for `mcp get` which exits 1 so the caller treats the
// server as not-yet-registered. Returns the log path and the PATH-prefixed env.
func fakeClaude(t *testing.T, home string) (logPath string, env []string) {
	t.Helper()
	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "argv.log")

	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"case \"$1 $2\" in 'mcp get') exit 1 ;; esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o700); err != nil { // #nosec G306 -- test stub must be executable
		t.Fatal(err)
	}

	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}
	env = append(env, "HOME="+home, "PATH="+binDir+":"+os.Getenv("PATH"))
	return logPath, env
}

func runWithEnv(t *testing.T, env []string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return out
}

func TestE2E_InstallStdio(t *testing.T) {
	// The Claude Code registration must use the stdio transport: the host spawns
	// the server as a child and owns its lifecycle, so there is no port to find, no
	// daemon to keep alive, and no bearer token to leak. A registration that still
	// names http:// would drag the whole daemon back in.
	t.Run("registers_stdio_not_http", func(t *testing.T) {
		home := t.TempDir()
		logPath, env := fakeClaude(t, home)

		out := runWithEnv(t, env, "install", "--all")

		var resp struct {
			MCPRegistration string `json:"mcp_registration"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("parse install output: %v\nraw: %s", err, out)
		}
		if resp.MCPRegistration != "ok" {
			t.Fatalf("mcp_registration = %q, want ok", resp.MCPRegistration)
		}

		b, err := os.ReadFile(logPath) // #nosec G304 -- test-local temp path
		if err != nil {
			t.Fatalf("read claude argv log: %v", err)
		}
		log := string(b)

		var addLine string
		for line := range strings.SplitSeq(log, "\n") {
			if strings.HasPrefix(line, "mcp add") {
				addLine = line
			}
		}
		if addLine == "" {
			t.Fatalf("no `claude mcp add` invocation recorded:\n%s", log)
		}

		for _, want := range []string{"--scope user", "serve --stdio"} {
			if !strings.Contains(addLine, want) {
				t.Errorf("registration missing %q:\n%s", want, addLine)
			}
		}
		for _, forbidden := range []string{"--transport http", "http://", "Authorization", "Bearer"} {
			if strings.Contains(addLine, forbidden) {
				t.Errorf("registration still carries HTTP-transport artefact %q:\n%s", forbidden, addLine)
			}
		}
	})

	// A host that spawns the server per session needs no background daemon, so the
	// bootstrap must not start one — that is the whole point of the migration.
	t.Run("all_does_not_start_a_daemon", func(t *testing.T) {
		home := t.TempDir()
		_, env := fakeClaude(t, home)

		out := runWithEnv(t, env, "install", "--all")

		var resp map[string]any
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("parse install output: %v\nraw: %s", err, out)
		}
		if _, present := resp["server"]; present {
			t.Errorf("install --all still reports a `server` bootstrap step: %v", resp["server"])
		}
	})
}

// --host codex --project writes the graph block into ./AGENTS.md (created
// when missing); without --project no AGENTS.md is touched.
func TestE2E_InstallCodexProjectWritesAgentsMd(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())

	runInstall(t, home, "install", "--host", "codex")
	if _, err := os.Stat("AGENTS.md"); !os.IsNotExist(err) {
		t.Fatalf("AGENTS.md written without --project: %v", err)
	}

	runInstall(t, home, "install", "--host", "codex", "--project")
	b, err := os.ReadFile("AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "## droids-mem code graph"); n != 1 {
		t.Errorf("graph marker appears %d times, want 1:\n%s", n, b)
	}

	runInstall(t, home, "install", "--host", "codex", "--project")
	b2, _ := os.ReadFile("AGENTS.md")
	if string(b2) != string(b) {
		t.Errorf("re-run modified AGENTS.md")
	}
}

// A host install that fails must not leave the graph block behind.
func TestE2E_InstallCodexFailureLeavesNoAgentsMd(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	// ~/.codex as a regular file makes the config write fail.
	if err := os.WriteFile(filepath.Join(home, ".codex"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binaryPath, "install", "--host", "codex", "--project")
	cmd.Env = append(os.Environ(), "HOME="+home)
	if err := cmd.Run(); err == nil {
		t.Fatal("install succeeded, want failure")
	}
	if _, err := os.Stat("AGENTS.md"); !os.IsNotExist(err) {
		t.Errorf("AGENTS.md written by a failed install: %v", err)
	}
}

func TestE2E_UninstallCodexProjectRemovesGraphBlock(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	pre := "# team rules\n"
	if err := os.WriteFile("AGENTS.md", []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	runInstall(t, home, "install", "--host", "codex", "--project")
	runInstall(t, home, "uninstall", "--host", "codex", "--project")

	b, err := os.ReadFile("AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != pre {
		t.Errorf("AGENTS.md after uninstall = %q, want %q", b, pre)
	}
}

// Without a registered MCP server the graph tools do not exist, so the block
// telling the agent to use them must not be written.
func TestE2E_InstallAllSkipsGraphBlockWithoutMCP(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(binaryPath, "install", "--all")
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+t.TempDir()) // no claude CLI
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		GraphBlock string `json:"graph_block"`
	}
	mustParseJSON(t, out, &r)
	if !strings.HasPrefix(r.GraphBlock, "skipped") {
		t.Errorf("graph_block = %q, want skipped", r.GraphBlock)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if strings.Contains(string(b), "## droids-mem code graph") {
		t.Errorf("graph block written without MCP registration")
	}
}

// --all fans out to every detected host, skips absent ones without creating
// their config dirs, and uninstall --all reverses it.
func TestE2E_InstallAllCoversCodexAndOpencode(t *testing.T) {
	home := t.TempDir()
	_, env := fakeClaude(t, home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o750); err != nil {
		t.Fatal(err)
	}
	// opencode deliberately absent.

	type hostRes struct {
		Status string `json:"status"`
	}
	var r struct {
		Codex    hostRes `json:"codex"`
		Opencode string  `json:"opencode"`
	}
	mustParseJSON(t, runWithEnv(t, env, "install", "--all"), &r)
	if r.Codex.Status != "installed" {
		t.Errorf("codex = %+v, want installed", r.Codex)
	}
	if !strings.HasPrefix(r.Opencode, "skipped") {
		t.Errorf("opencode = %q, want skipped", r.Opencode)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode")); !os.IsNotExist(err) {
		t.Errorf("absent host's config dir was created: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); !strings.Contains(string(b), "[mcp_servers.droids-mem]") {
		t.Errorf("codex config missing registration:\n%s", b)
	}

	var u struct {
		Codex hostRes `json:"codex"`
	}
	mustParseJSON(t, runWithEnv(t, env, "uninstall", "--all"), &u)
	if u.Codex.Status != "uninstalled" {
		t.Errorf("uninstall codex = %+v, want uninstalled", u.Codex)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); strings.Contains(string(b), "droids-mem") {
		t.Errorf("codex registration survived uninstall --all:\n%s", b)
	}
}

// --all --project reports graph_index and agents_md once at the top level,
// never per host (each host used to start its own index).
func TestE2E_InstallAllProjectReportsIndexOnce(t *testing.T) {
	home := t.TempDir()
	_, env := fakeClaude(t, home)
	for _, d := range []string{".codex", filepath.Join(".config", "opencode")} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(t.TempDir()) // no .git: the index step reports skipped instead of spawning

	var r map[string]any
	mustParseJSON(t, runWithEnv(t, env, "install", "--all", "--project"), &r)

	if _, ok := r["graph_index"].(string); !ok {
		t.Errorf("top-level graph_index missing: %v", r["graph_index"])
	}
	if s, _ := r["agents_md"].(string); !strings.HasPrefix(s, "appended") {
		t.Errorf("agents_md = %v, want appended", r["agents_md"])
	}
	for _, h := range []string{"codex", "opencode"} {
		m, _ := r[h].(map[string]any)
		if m == nil || m["status"] != "installed" {
			t.Fatalf("%s = %v, want installed map", h, r[h])
		}
		for _, k := range []string{"graph_index", "agents_md"} {
			if _, dup := m[k]; dup {
				t.Errorf("%s reports %s itself; must be top-level only", h, k)
			}
		}
	}
	b, _ := os.ReadFile("AGENTS.md")
	if n := strings.Count(string(b), "## droids-mem code graph"); n != 1 {
		t.Errorf("AGENTS.md graph block count = %d, want 1", n)
	}
}
