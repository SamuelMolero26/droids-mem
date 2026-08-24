package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/release"
	"github.com/spf13/cobra"
)

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Download and install the latest droids-mem release",
		Long: `Checks GitHub for the latest release and, if newer than the running
binary, downloads the matching platform asset, verifies its SHA256 checksum
against the release's published sidecar, and atomically replaces the current
executable.

Installed via Homebrew? Run 'brew upgrade droids-mem' instead — this command
refuses to touch a Homebrew-managed install.`,
		Annotations: map[string]string{bootGateBypass: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			rel, err := release.Fetch(ctx)
			if err != nil {
				writeError("upgrade_check_failed", err.Error(), true)
				exitWith(ExitError)
			}
			if !release.IsNewer(version, rel.Version()) {
				writeJSON(map[string]string{"status": "up_to_date", "version": version})
				return nil
			}
			if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				writeError("unsupported_platform", "no release binary published for "+runtime.GOOS, false)
				exitWith(ExitError)
			}

			exe, err := selfPath()
			if err != nil {
				writeError("upgrade_failed", err.Error(), true)
				exitWith(ExitError)
			}
			if strings.Contains(exe, string(filepath.Separator)+"Cellar"+string(filepath.Separator)) {
				writeError("homebrew_managed", "this binary lives under a Homebrew Cellar", false,
					withSuggestion("run: brew upgrade droids-mem"))
				exitWith(ExitConflict)
			}

			assetName := fmt.Sprintf("droids-mem-%s-%s-%s", rel.Tag, runtime.GOOS, runtime.GOARCH)
			assetURL, ok := rel.AssetURL(assetName)
			if !ok {
				writeError("asset_not_found", "no release asset named "+assetName, false)
				exitWith(ExitError)
			}
			shaURL, ok := rel.AssetURL(assetName + ".sha256")
			if !ok {
				writeError("asset_not_found", "no checksum sidecar for "+assetName, false)
				exitWith(ExitError)
			}

			wantSum, err := fetchChecksum(ctx, shaURL)
			if err != nil {
				writeError("upgrade_failed", "fetch checksum: "+err.Error(), true)
				exitWith(ExitError)
			}
			tmp, gotSum, err := downloadToTemp(ctx, assetURL, filepath.Dir(exe), os.Stderr)
			if err != nil {
				writeError("upgrade_failed", "download: "+err.Error(), true)
				exitWith(ExitError)
			}
			defer func() { _ = os.Remove(tmp) }() // no-op once the rename below succeeds
			if gotSum != wantSum {
				writeError("checksum_mismatch", fmt.Sprintf("downloaded asset checksum %s does not match published %s", gotSum, wantSum), true)
				exitWith(ExitError)
			}

			// #nosec G302 -- this replaces the running executable, it must stay executable.
			if err := os.Chmod(tmp, 0o755); err != nil {
				writeError("upgrade_failed", "chmod: "+err.Error(), true)
				exitWith(ExitError)
			}
			if err := os.Rename(tmp, exe); err != nil {
				writeError("upgrade_failed", "install: "+err.Error(), true)
				exitWith(ExitError)
			}

			writeJSON(map[string]string{"status": "upgraded", "from": version, "to": rel.Version()})
			return nil
		},
	}
}

// selfPath resolves the running binary's real path (symlinks followed) so a
// `brew`-installed binary's actual Cellar location is what gets checked and
// replaced, not a wrapper symlink on PATH.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve running binary: %w", err)
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	return real, nil
}

// fetchChecksum GETs a release's ".sha256" sidecar and returns the hex digest
// (the sidecar is `sha256sum` output: "<hex>  <filename>\n").
func fetchChecksum(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if err != nil {
		return "", err
	}
	sum, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	return sum, nil
}

// downloadToTemp streams url into a new file alongside dir (so the later
// os.Rename onto the running executable is same-filesystem and atomic),
// hashing as it writes. progress (nil-able) receives a "\r"-updated status
// line as bytes arrive. The caller removes the temp file on any failure path.
func downloadToTemp(ctx context.Context, url, dir string, progress io.Writer) (path, sha256Hex string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("unexpected status %s", resp.Status)
	}

	f, err := os.CreateTemp(dir, "droids-mem-upgrade-*")
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	h := sha256.New()
	dst := io.MultiWriter(f, h)
	if progress != nil {
		bar := &progressBar{out: progress, total: resp.ContentLength}
		defer bar.done()
		dst = io.MultiWriter(f, h, bar)
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		_ = os.Remove(f.Name())
		return "", "", err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

// progressBar renders a throttled "\rDownloading… NN.N% (a MB/b MB)" status
// line as Write is called. total <= 0 (server omitted Content-Length) falls
// back to a running byte count with no percentage.
type progressBar struct {
	out       io.Writer
	total     int64
	written   int64
	lastPrint time.Time
}

func (p *progressBar) Write(b []byte) (int, error) {
	p.written += int64(len(b))
	// Always print the final chunk regardless of throttle, so the bar ends
	// at a real 100% instead of whatever the last throttled tick showed.
	if done := p.total > 0 && p.written >= p.total; !done && time.Since(p.lastPrint) < 100*time.Millisecond {
		return len(b), nil
	}
	p.lastPrint = time.Now()
	if p.total > 0 {
		fmt.Fprintf(p.out, "\rDownloading… %5.1f%% (%s/%s)", 100*float64(p.written)/float64(p.total),
			humanBytes(p.written), humanBytes(p.total))
	} else {
		fmt.Fprintf(p.out, "\rDownloading… %s", humanBytes(p.written))
	}
	return len(b), nil
}

// done ends the progress line with a newline so subsequent output (the final
// JSON result, or the next command) doesn't collide with it.
func (p *progressBar) done() { fmt.Fprintln(p.out) }

func humanBytes(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}
