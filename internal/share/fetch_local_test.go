package share

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// recordingStore captures what ImportShared was handed, so a test can tell
// "import ran and saw the pool" from "import never ran".
type recordingStore struct {
	imported string
	calls    int
}

func (r *recordingStore) ExportShared(context.Context, io.Writer) error { return nil }

func (r *recordingStore) ImportShared(_ context.Context, rd io.Reader) (store.ImportResult, error) {
	r.calls++
	b, err := io.ReadAll(rd)
	if err != nil {
		return store.ImportResult{}, err
	}
	r.imported = string(b)
	return store.ImportResult{Imported: strings.Count(strings.TrimSpace(r.imported), "\n") + 1}, nil
}

// localPool builds a git repo with a shared.jsonl and deliberately no remote.
func localPool(t *testing.T, line string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := runGit(ctx, dir, "init", "-q"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runGit(ctx, dir, "config", "user.email", "t@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, dir, "config", "user.name", "t"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sharedFile), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, dir, "add", sharedFile); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, dir, "commit", "-q", "-m", "seed"); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A pool with no remote has nothing to pull, but it still has a pool on disk.
// fetchInto ran `git pull` unconditionally, which fails with "no tracking
// information" and aborts before the import — so a local-only pool could never
// be consumed, and the boot auto-Fetch logged that failure on every start.
// Push already guards the same call with hasRemote; Fetch did not.
func TestFetch_LocalOnlyPoolStillImports(t *testing.T) {
	pool := localPool(t, `{"kind":"task_pattern","task_type":"probe","title":"t","what":"w","learned":"l"}`)
	rs := &recordingStore{}

	res, err := Fetch(context.Background(), pool, rs)
	if err != nil {
		t.Fatalf("Fetch on a remote-less pool: %v", err)
	}
	if rs.calls != 1 {
		t.Fatalf("ImportShared called %d times, want 1 — the pull failure aborted before the import", rs.calls)
	}
	if !strings.Contains(rs.imported, `"task_type":"probe"`) {
		t.Errorf("import did not receive the pool contents, got %q", rs.imported)
	}
	if res.Imported == 0 {
		t.Errorf("result reports nothing imported: %+v", res)
	}
}

// A pool that is a git repo but has no shared.jsonl yet is not an error — that
// is a freshly created pool. Guarding the pull must not change this.
func TestFetch_LocalOnlyPoolWithNoFileIsNotAnError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := runGit(ctx, dir, "init", "-q"); err != nil {
		t.Fatalf("init: %v", err)
	}
	rs := &recordingStore{}

	if _, err := Fetch(ctx, dir, rs); err != nil {
		t.Fatalf("Fetch on an empty pool: %v", err)
	}
	if rs.calls != 0 {
		t.Errorf("ImportShared ran with no pool file present")
	}
}
