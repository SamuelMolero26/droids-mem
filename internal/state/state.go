// Package state owns droids-mem's on-disk state directory: token file, mcp.pid,
// mcp.log. Keep tiny and dependency-free so cmd_serve and cmd_ensure_server can
// both reach for it without a circular import via store/db/mcpserver.
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"
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
//  3. A freshly generated `tok_<ULID>`, written 0600 to the token file
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
	tok := "tok_" + ulid.Make().String()
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}
	return tok, nil
}
