// Package release talks to GitHub's "latest release" API for droids-mem.
// Shared by the TUI's update-check banner and the `upgrade` command so both
// parse the same response shape and compare versions the same way.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// API is the GitHub "latest release" endpoint. No auth needed — public repo.
const API = "https://api.github.com/repos/SamuelMolero26/droids-mem/releases/latest"

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Version is the release's bare semver (tag with any leading "v" stripped).
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// AssetURL returns the download URL for the asset with the given exact name.
func (r Release) AssetURL(name string) (string, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, true
		}
	}
	return "", false
}

// Fetch retrieves the latest release from the GitHub API. The deadline is
// applied here rather than left to callers: this is a courtesy check on both
// paths (banner and `upgrade`), and a caller that forgets the bound turns a
// slow server into a hang.
func Fetch(ctx context.Context) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github releases: unexpected status %s", resp.Status)
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, err
	}
	return rel, nil
}

// IsNewer reports whether latest is a valid, strictly-newer semver than
// current. Either side may carry a leading "v" or not: release builds inject
// the git tag verbatim (`-X main.version=v1.2.1`) while Release.Version
// strips it, so the two arguments routinely disagree on the prefix. Both are
// normalized before comparing — prepending "v" to an already-prefixed string
// yields "vv1.2.1", which is not valid semver and silently made every
// released binary report "up to date". Unparseable input (e.g. current ==
// "dev") reports false rather than erroring — callers treat "no update" and
// "can't tell" the same way.
func IsNewer(current, latest string) bool {
	cv := semver.Canonical("v" + strings.TrimPrefix(current, "v"))
	lv := semver.Canonical("v" + strings.TrimPrefix(latest, "v"))
	return cv != "" && lv != "" && semver.Compare(lv, cv) > 0
}

// FetchTimeout bounds the metadata call (not any asset download) so a
// courtesy check never stalls a caller — 3s is generous for a JSON GET.
const FetchTimeout = 3 * time.Second
