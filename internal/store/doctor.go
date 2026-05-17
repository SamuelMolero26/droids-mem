package store

import (
	"fmt"
	"os"
)

type DoctorReport struct {
	Status       string `json:"status"`
	IntegrityOK  bool   `json:"integrity_ok"`
	Rebuilt      bool   `json:"rebuilt"`
	Optimized    bool   `json:"optimized"`
	Vacuumed     bool   `json:"vacuumed"`
	BytesBefore  int64  `json:"bytes_before"`
	BytesAfter   int64  `json:"bytes_after"`
	BytesFreed   int64  `json:"bytes_freed"`
	IntegrityErr string `json:"integrity_error,omitempty"`
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

	return rep, nil
}
