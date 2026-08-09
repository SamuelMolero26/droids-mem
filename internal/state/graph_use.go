package state

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const lastGraphFile = "graph_last"

// RecordGraphUse stamps which code-graph tool just ran, so a status line can
// show `droids-mem:graph_symbol` while an agent is querying the graph instead
// of leaving the whole subsystem invisible. Cosmetic: failures are swallowed.
func RecordGraphUse(tool string) {
	dir, err := Dir()
	if err != nil {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	// ponytail: the file's mtime IS the timestamp — nothing to write or parse.
	_ = os.WriteFile(filepath.Join(dir, lastGraphFile), []byte(tool+"\n"), 0o600)
}

// LastGraphUse returns the tool recorded by RecordGraphUse if it ran within the
// given window. Anything unreadable reports no use — an indicator that guesses
// is worse than one that stays quiet.
func LastGraphUse(within time.Duration) (string, bool) {
	dir, err := Dir()
	if err != nil {
		return "", false
	}
	p := filepath.Join(dir, lastGraphFile)
	fi, err := os.Stat(p)
	if err != nil || time.Since(fi.ModTime()) > within {
		return "", false
	}
	// #nosec G304 -- path is Dir()/graph_last inside the trusted state dir.
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	tool := strings.TrimSpace(string(b))
	return tool, tool != ""
}
