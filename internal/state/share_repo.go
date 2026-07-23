package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const shareRepoFile = "share_repo"

// ShareRepoPath returns Dir()/share_repo — the file that remembers the last git
// repo the TUI shared into, so the next share reuses it as a default (ADR-0028).
func ShareRepoPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, shareRepoFile), nil
}

// LoadShareRepo returns the remembered share-repo path, or "" if none is set.
func LoadShareRepo() (string, error) {
	path, err := ShareRepoPath()
	if err != nil {
		return "", err
	}
	// #nosec G304 -- path is Dir()/share_repo inside the trusted state dir.
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read share repo: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// SaveShareRepo persists the repo path so the next share defaults to it.
func SaveShareRepo(path string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	p, err := ShareRepoPath()
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(strings.TrimSpace(path)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write share repo: %w", err)
	}
	return nil
}
