package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/samuelmolero26/droids-mem/internal/release"
	"github.com/samuelmolero26/droids-mem/internal/state"
)

type updateMsg struct {
	// latest is the newest release version, or "" if no update is available
	// (or the check failed — silently, this is a courtesy banner, not a gate).
	latest string
}

// checkUpdateCmd compares the running version against the latest GitHub
// release. Cached for 24h via state.CachedLatestVersion so repeat launches
// in the same day never hit the network. current is the raw build version
// ("dev", "1.2.1", ...); "dev" never reports an update (nothing to compare).
func checkUpdateCmd(current string) tea.Cmd {
	return func() tea.Msg {
		if current == "dev" || current == "" {
			return updateMsg{}
		}
		latest, ok := state.CachedLatestVersion()
		if !ok {
			ctx, cancel := context.WithTimeout(context.Background(), release.FetchTimeout)
			defer cancel()
			rel, err := release.Fetch(ctx)
			if err != nil {
				return updateMsg{}
			}
			latest = rel.Version()
			_ = state.WriteLatestVersion(latest)
		}
		if release.IsNewer(current, latest) {
			return updateMsg{latest: latest}
		}
		return updateMsg{}
	}
}
