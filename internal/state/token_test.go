package state_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/state"
)

// freshToken mints a token in an isolated state dir.
func freshToken(t *testing.T) string {
	t.Helper()
	t.Setenv("DROIDS_MEM_HOME", t.TempDir())
	t.Setenv("DROIDS_MEM_MCP_TOKEN", "")
	tok, err := state.LoadOrCreateToken()
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	return tok
}

// The bearer token is the only barrier between another local account and the
// whole memory corpus, so it must come from a CSPRNG. A ULID would fail this:
// oklog/ulid's default entropy is math/rand seeded with time.Now().UnixNano()
// AND monotonic, so tokens minted in one process climb in lexical order.
func TestLoadOrCreateToken_NotSequential(t *testing.T) {
	const n = 16
	toks := make([]string, n)
	for i := range n {
		toks[i] = freshToken(t)
	}
	if slices.IsSorted(toks) {
		t.Errorf("tokens are lexically ascending in mint order — entropy is predictable, not a CSPRNG:\n%s",
			strings.Join(toks, "\n"))
	}
}

// A token must carry enough entropy that guessing it is infeasible. 128 bits is
// the floor; the encoded form is checked so a truncation regression is caught.
func TestLoadOrCreateToken_Entropy(t *testing.T) {
	tok := freshToken(t)
	body, ok := strings.CutPrefix(tok, "tok_")
	if !ok {
		t.Fatalf("token %q missing tok_ prefix", tok)
	}
	// base64url of 32 bytes, unpadded.
	if len(body) < 22 {
		t.Errorf("token body %q is %d chars — under 128 bits of entropy", body, len(body))
	}
	if strings.ContainsAny(body, "+/=") {
		t.Errorf("token body %q is not URL-safe unpadded base64", body)
	}
}

func TestLoadOrCreateToken_PersistsAt0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DROIDS_MEM_HOME", dir)
	t.Setenv("DROIDS_MEM_MCP_TOKEN", "")

	first, err := state.LoadOrCreateToken()
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	second, err := state.LoadOrCreateToken()
	if err != nil {
		t.Fatalf("LoadOrCreateToken (reload): %v", err)
	}
	if first != second {
		t.Errorf("token not persisted: %q then %q", first, second)
	}

	fi, err := os.Stat(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
	}
}
