package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// homePayload is the content-first view printed by a bare `droids-mem` (AXI §8):
// live corpus state plus a short menu of next steps, not a usage manual.
type homePayload struct {
	Bin         string                `json:"bin"`
	Description string                `json:"description"`
	TaskTypes   []store.TaskTypeCount `json:"task_types"`
	Total       int                   `json:"total"`
	Help        []string              `json:"help"`
}

// homeView assembles the bare-invocation dashboard from the corpus census.
// Returned (not printed) so it is unit-testable without the os.Exit path the
// command handlers take.
func homeView(ctx context.Context, s *store.Store) (homePayload, error) {
	counts, err := s.Counts(ctx)
	if err != nil {
		return homePayload{}, err
	}
	types, err := s.ListTaskTypes(ctx)
	if err != nil {
		return homePayload{}, err
	}

	help := []string{
		`Run 'droids-mem context --task-type <slug>' to load a project's start-of-run bundle`,
		`Run 'droids-mem search --query "<phrase>"' to find past lessons`,
		`Run 'droids-mem tui' to browse the corpus interactively`,
	}
	if counts.Total == 0 {
		// Definitive empty state (AXI §5): say the zero, point at the first action.
		help = []string{
			`No memories yet. Run 'droids-mem save --task-type <slug> --kind task_pattern --title "..." --what "..." --learned "..."' to record the first one`,
		}
	}

	return homePayload{
		Bin:         collapseHome(binPath()),
		Description: "Persistent memory + code graph for AI agents (SQLite + FTS5, zero external deps)",
		TaskTypes:   types,
		Total:       counts.Total,
		Help:        help,
	}, nil
}

// binPath is the absolute path of the running executable, falling back to the
// invocation name when the OS can't resolve it.
func binPath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return os.Args[0]
}

// collapseHome rewrites a leading $HOME to ~ for a compact, portable display.
func collapseHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
