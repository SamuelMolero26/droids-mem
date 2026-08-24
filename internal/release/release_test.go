package release

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.2.1", "1.2.1", false}, // up to date
		{"1.2.1", "1.3.0", true},  // newer available
		{"1.2.1", "1.2.0", false}, // ahead of latest (local dev build)
		{"1.2.1", "not-a-version", false},
		{"not-a-version", "1.2.1", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestRelease_AssetURL(t *testing.T) {
	r := Release{Assets: []Asset{{Name: "droids-mem-v1.2.1-darwin-arm64", URL: "https://example/dl"}}}
	if u, ok := r.AssetURL("droids-mem-v1.2.1-darwin-arm64"); !ok || u != "https://example/dl" {
		t.Errorf("AssetURL hit = (%q, %v), want (https://example/dl, true)", u, ok)
	}
	if _, ok := r.AssetURL("missing"); ok {
		t.Error("AssetURL miss should return ok=false")
	}
}

func TestRelease_Version(t *testing.T) {
	if got := (Release{Tag: "v1.2.1"}).Version(); got != "1.2.1" {
		t.Errorf("Version() = %q, want 1.2.1", got)
	}
}
