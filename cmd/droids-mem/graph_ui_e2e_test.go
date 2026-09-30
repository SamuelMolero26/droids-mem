package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The viewer URL carries a 12-hour key, so `graph ui` prints it only when the
// user needs it: the browser could not be opened, or --no-open was passed.
// A fake open/xdg-open first on PATH stands in for the OS opener.
func TestE2E_GraphUIPrintsURLOnlyWhenNotOpened(t *testing.T) {
	env := newDaemonEnv(t)
	repo := t.TempDir()
	writeGoFile(t, filepath.Join(repo, "go.mod"), "module example.com/ui\n\ngo 1.21\n")
	writeGoFile(t, filepath.Join(repo, "a.go"), "package ui\n\nfunc A() {}\n")

	bin := t.TempDir()
	script := "#!/bin/sh\ntouch \"$FAKE_OPEN_MARK\"\nsleep \"$FAKE_OPEN_SLEEP\"\nexit \"$FAKE_OPEN_EXIT\"\n"
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil { // #nosec G306 -- test opener must be executable
			t.Fatal(err)
		}
	}

	cases := []struct {
		name       string
		openerExit string
		openerNap  string
		args       []string
		wantURL    bool
		wantOpened bool
	}{
		{"opened", "0", "0", nil, false, true},
		{"open failed", "1", "0", nil, true, true},
		{"no-open", "0", "0", []string{"--no-open"}, true, false},
		// xdg-open can run the browser in the foreground: an opener still
		// running is taken as opened, and graph ui must not wait for it.
		{"opener still running", "1", "30", nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mark := filepath.Join(t.TempDir(), "opened")
			cmd := exec.Command(binaryPath, append([]string{"graph", "ui", "--repo", repo}, tc.args...)...)
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_OPEN_MARK="+mark,
				"FAKE_OPEN_EXIT="+tc.openerExit,
				"FAKE_OPEN_SLEEP="+tc.openerNap,
				"DROIDS_MEM_HOME="+env.home,
				"DROIDS_MEM_DB="+filepath.Join(env.home, "mem.db"),
				"DROIDS_MEM_MCP_ADDR="+env.addr,
			)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("graph ui: %v\nstdout: %s", err, out)
			}
			var got map[string]string
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("parse %q: %v", out, err)
			}
			if got["status"] != "ok" {
				t.Fatalf("status = %q, want ok: %s", got["status"], out)
			}
			if u, has := got["url"]; has != tc.wantURL || (has && !strings.Contains(u, "#k=")) {
				t.Fatalf("url = %q (present %v), want present %v", u, has, tc.wantURL)
			}
			if _, err := os.Stat(mark); (err == nil) != tc.wantOpened {
				t.Fatalf("opener ran = %v, want %v", err == nil, tc.wantOpened)
			}
		})
	}
}
