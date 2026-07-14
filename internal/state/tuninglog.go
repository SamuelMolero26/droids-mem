package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// TuningLogFile is the append-only threshold-tuning dataset (ADR-0026), one
// JSON object per line, under Dir(). Off unless DROIDS_MEM_TUNING_LOG=1.
const TuningLogFile = "tuning.jsonl"

// AppendTuningLog appends one JSON line describing rec to TuningLogFile, for
// after-the-fact tuning of the dedupe/relevance thresholds (ADR-0026). It is a
// no-op unless DROIDS_MEM_TUNING_LOG=1, and best-effort otherwise: every error
// is swallowed, because a diagnostic log must never break a save or a prompt.
//
// state stays shape-agnostic — callers own the record struct (save vs pull).
// Lines are far under PIPE_BUF (4 KB), so O_APPEND writes from the MCP server
// and the CLI hook process interleave atomically without a lock.
// ponytail: no rotation — one short line per human-frequency event stays in the
// low MBs; add rotation if that stops being true.
func AppendTuningLog(rec any) {
	if os.Getenv("DROIDS_MEM_TUNING_LOG") != "1" {
		return
	}
	dir, err := Dir()
	if err != nil {
		return
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	// #nosec G304 -- path is Dir()/tuning.jsonl inside the trusted state dir.
	f, err := os.OpenFile(filepath.Join(dir, TuningLogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}
