package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendTuningLog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DROIDS_MEM_HOME", dir)
	path := filepath.Join(dir, TuningLogFile)

	// Off by default: nothing written.
	t.Setenv("DROIDS_MEM_TUNING_LOG", "")
	AppendTuningLog(map[string]any{"ev": "save"})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file when disabled, got err=%v", err)
	}

	// Armed: each call appends one JSON line.
	t.Setenv("DROIDS_MEM_TUNING_LOG", "1")
	AppendTuningLog(map[string]any{"ev": "save", "jaccard": 0.72})
	AppendTuningLog(map[string]any{"ev": "pull", "overlap": 0.4})

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), b)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("line 0 not valid JSON: %v", err)
	}
	if rec["ev"] != "save" || rec["jaccard"] != 0.72 {
		t.Fatalf("unexpected record: %v", rec)
	}
}
