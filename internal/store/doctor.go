package store

import (
	"fmt"
	"os"
)

// Growth warning thresholds (ADR-0010). Both fire at 80% of their ceiling so
// the operator hears about growth before it degrades anything.
const (
	// warnDBSizeBytes: 80% of the PRD §3.2 25 MB storage target.
	warnDBSizeBytes = 20 * 1024 * 1024
	// warnRowCount: 80% of the ~10k-row ceiling the Jaccard dedupe threshold
	// was tuned for (see jaccardDupeThreshold).
	warnRowCount = 8000
)

type DoctorReport struct {
	Status       string   `json:"status"`
	IntegrityOK  bool     `json:"integrity_ok"`
	Rebuilt      bool     `json:"rebuilt"`
	Optimized    bool     `json:"optimized"`
	Vacuumed     bool     `json:"vacuumed"`
	BytesBefore  int64    `json:"bytes_before"`
	BytesAfter   int64    `json:"bytes_after"`
	BytesFreed   int64    `json:"bytes_freed"`
	TotalRows    int64    `json:"total_rows"`
	Warnings     []string `json:"warnings,omitempty"`
	IntegrityErr string   `json:"integrity_error,omitempty"`
}

// Doctor runs FTS integrity-check, rebuilds the FTS index if divergent,
// optimizes it, and VACUUMs the database. Returns a structured report.
//
// dbPath is needed only to stat the file before/after for bytes_freed.
func (s *Store) Doctor(dbPath string) (*DoctorReport, error) {
	rep := &DoctorReport{Status: "ok"}

	if info, err := os.Stat(dbPath); err == nil {
		rep.BytesBefore = info.Size()
	}

	// integrity-check raises an error if the FTS index is out of sync with
	// the base table. We capture and report rather than failing the call.
	if _, err := s.db.Exec(`INSERT INTO memories_fts(memories_fts) VALUES('integrity-check')`); err != nil {
		rep.IntegrityOK = false
		rep.IntegrityErr = err.Error()
		if _, rerr := s.db.Exec(`INSERT INTO memories_fts(memories_fts) VALUES('rebuild')`); rerr != nil {
			return nil, fmt.Errorf("rebuild fts: %w", rerr)
		}
		rep.Rebuilt = true
	} else {
		rep.IntegrityOK = true
	}

	if _, err := s.db.Exec(`INSERT INTO memories_fts(memories_fts) VALUES('optimize')`); err != nil {
		return nil, fmt.Errorf("optimize fts: %w", err)
	}
	rep.Optimized = true

	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return nil, fmt.Errorf("vacuum: %w", err)
	}
	rep.Vacuumed = true

	if info, err := os.Stat(dbPath); err == nil {
		rep.BytesAfter = info.Size()
		rep.BytesFreed = rep.BytesBefore - rep.BytesAfter
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&rep.TotalRows); err != nil {
		return nil, fmt.Errorf("count memories: %w", err)
	}

	rep.Warnings = growthWarnings(rep.BytesAfter, rep.TotalRows)

	return rep, nil
}

// growthWarnings implements the ADR-0010 observability checks: no automatic
// retention exists for knowledge kinds, so doctor is where growth becomes
// visible before it degrades anything.
func growthWarnings(dbBytes, totalRows int64) []string {
	var warns []string
	if dbBytes > warnDBSizeBytes {
		warns = append(warns, fmt.Sprintf(
			"database is %d bytes, over the %d-byte warning threshold (80%% of the 25 MB target); review with `prune --suggest-dupes`",
			dbBytes, int64(warnDBSizeBytes)))
	}
	if totalRows > warnRowCount {
		warns = append(warns, fmt.Sprintf(
			"%d memories, over the %d-row warning threshold; near-duplicate detection was tuned for <10k rows — review with `prune --suggest-dupes`",
			totalRows, warnRowCount))
	}
	return warns
}
