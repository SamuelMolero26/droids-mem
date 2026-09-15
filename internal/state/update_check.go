package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const updateCheckFile = "update_check"

// updateCheckTTL bounds how often the TUI hits GitHub's releases API.
const updateCheckTTL = 24 * time.Hour

type cachedUpdateCheck struct {
	Latest    string    `json:"latest"`
	CheckedAt time.Time `json:"checked_at"`
}

// CachedLatestVersion returns the last-seen latest release tag if it was
// recorded within updateCheckTTL. ok is false on a cold cache, a stale one,
// or any read/parse error — callers treat that uniformly as "check again".
func CachedLatestVersion() (latest string, ok bool) {
	dir, err := Dir()
	if err != nil {
		return "", false
	}
	// #nosec G304 -- path is Dir()/update_check inside the trusted state dir.
	b, err := os.ReadFile(filepath.Join(dir, updateCheckFile))
	if err != nil {
		return "", false
	}
	var c cachedUpdateCheck
	if err := json.Unmarshal(b, &c); err != nil {
		return "", false
	}
	if time.Since(c.CheckedAt) > updateCheckTTL {
		return "", false
	}
	return c.Latest, true
}

// WriteLatestVersion persists the outcome of an update check so the next TUI
// launch within updateCheckTTL skips the network call. Best-effort: a write
// failure just means the next launch checks again.
func WriteLatestVersion(latest string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	b, err := json.Marshal(cachedUpdateCheck{Latest: latest, CheckedAt: time.Now()})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, updateCheckFile), b, 0o600)
}
