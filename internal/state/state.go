// Package state owns droids-mem's on-disk state directory: token file, mcp.pid,
// mcp.log. Keep tiny and dependency-free so cmd_serve and cmd_ensure_server can
// both reach for it without a circular import via store/db/mcpserver.
package state

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	tokenFile = "token"
	PidFile   = "mcp.pid"
	LogFile   = "mcp.log"
)

// Dir returns ~/.droids-mem (or DROIDS_MEM_HOME if set, for tests/sandboxing).
func Dir() (string, error) {
	if v := os.Getenv("DROIDS_MEM_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".droids-mem"), nil
}

// tokenBytes is the raw entropy behind a minted bearer token. 256 bits — well
// past the 128-bit floor, and free: the token is written once and read back.
const tokenBytes = 32

// newToken mints a bearer token from crypto/rand. It must NOT come from a ULID
// or any other seeded PRNG: oklog/ulid's default entropy is math/rand seeded
// with time.Now().UnixNano(), so a token minted that way is recoverable from
// the token file's own mtime, and the reader is monotonic on top of that. This
// token is the only thing standing between another local account and the whole
// memory corpus (the listener answers any process on loopback; the 0600 file
// mode does not).
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return "tok_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// TokenPath returns Dir()/token.
func TokenPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, tokenFile), nil
}

// LoadOrCreateToken returns the bearer token in this precedence:
//  1. DROIDS_MEM_MCP_TOKEN env (callers can force a specific token)
//  2. ~/.droids-mem/token (persisted across runs)
//  3. A freshly generated `tok_<256 random bits>`, written 0600 to the token file
//
// File mode is 0600 so other local users cannot read the token. Parent dir
// is created 0700 if missing.
func LoadOrCreateToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("DROIDS_MEM_MCP_TOKEN")); t != "" {
		return t, nil
	}
	path, err := TokenPath()
	if err != nil {
		return "", err
	}
	// #nosec G304 -- path is Dir()/token inside the trusted state dir, not
	// user-controlled input.
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}
	return tok, nil
}
