package graph

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// aliasConfig holds the resolved TypeScript path mapping for one repo.
// It is loaded once per build from tsconfig.json / jsconfig.json at the repo root
// and reused for every specifier in that build (read once, map[string][]string).
type aliasConfig struct {
	baseUrl string
	paths   map[string][]string
}

// loadAliasConfig reads tsconfig.json (preferred) or jsconfig.json at repoRoot.
// It returns nil when neither file exists or neither parses — the caller then
// falls back to the naive "@/→./" mapping that covers 95% of create-next-app
// trees. When a file exists but has no compilerOptions.paths, it returns a
// non-nil config with an empty paths map: an existing config that does not
// alias "@/", so "@/..." must NOT fall back to "./" in that case.
func loadAliasConfig(repoRoot string) *aliasConfig {
	cfg, _ := loadAliasConfigTracked(repoRoot)
	return cfg
}

// aliasConfigFiles returns every readable root candidate and local extends
// file whose contents can affect loadAliasConfig. stamp uses this shared
// discovery so an alias input cannot change without invalidating its graph.
func aliasConfigFiles(repoRoot string) []string {
	_, files := loadAliasConfigTracked(repoRoot)
	return files
}

func loadAliasConfigTracked(repoRoot string) (*aliasConfig, []string) {
	var files []string
	// Walk up from repoRoot looking for tsconfig/jsconfig, mirroring
	// canonicalRepo's walkUpFor: a repo may be a subdirectory of the checkout
	// that actually holds the config (e.g. packages/web inside a monorepo).
	//
	// B4 note (walk-up baseUrl semantics): when the config is found at an
	// ancestor dir != repoRoot (monorepo), its baseUrl is kept verbatim and
	// interpreted as repoRoot-relative. TS spec says baseUrl is relative to
	// the config file, so an ancestor's "src" would mean ancestor/src, not
	// repo/src — but known only contains repoRoot-relative files, so "src/foo"
	// correctly probes repoRoot/src/foo and naturally misses ancestor files
	// (which are not indexed). Rebasing to ancestor-relative would be wrong
	// here; the verbatim behavior matches the mapper's indexed-file universe.
	// This is intentionally not rebased; document only.
	//
	// The walk STOPS at the checkout boundary (a directory holding .git,
	// inclusive). Without that bound it reached the filesystem root, so any
	// stray tsconfig.json in $HOME or above became this repo's config — and a
	// non-nil config with no "@/" entry suppresses the naive "@/→./" fallback,
	// silently dropping every "@/" edge in a repo that ships no config of its
	// own. A repo outside any checkout stops at repoRoot for the same reason.
	for cur := repoRoot; ; {
		for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
			p := filepath.Join(cur, name)
			data, err := os.ReadFile(p) // #nosec G304 -- cur is an ancestor of canonicalRepo, p is a fixed config name
			if err != nil {
				continue
			}
			files = append(files, p)
			cfg := parseAliasConfig(data, filepath.Dir(p), 0, &files)
			if cfg == nil {
				continue
			}
			if cfg.baseUrl == "" {
				cfg.baseUrl = "."
			}
			if cfg.paths == nil {
				cfg.paths = map[string][]string{}
			}
			// Clean baseUrl: use slash-separated, no trailing slash, "." stays.
			cfg.baseUrl = path.Clean(filepath.ToSlash(cfg.baseUrl))
			if cfg.baseUrl == "" {
				cfg.baseUrl = "."
			}
			return cfg, files
		}
		if isCheckoutRoot(cur) {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return nil, files
}

// isCheckoutRoot reports whether dir holds a .git entry (directory for a
// normal clone, file for a worktree or submodule) — the boundary the alias
// walk-up must not cross.
func isCheckoutRoot(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// parseAliasConfig parses one tsconfig/jsconfig blob, handling extends
// minimally: if "extends" is a string (or single-element array) that names a
// file relative to dir, that file is read and its compilerOptions become the
// base, with the current file's values overriding it. Absolute or
// node_modules-style extends (e.g. "next/tsconfig.json") are ignored when the
// file does not exist — best-effort, never a build failure.
func parseAliasConfig(data []byte, dir string, depth int, files *[]string) *aliasConfig {
	if depth > 5 {
		return nil
	}
	cleaned := stripJSONComments(data)
	// Try direct unmarshal; if it fails due to trailing commas, strip them and retry.
	var raw struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions *struct {
			BaseUrl *string             `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal(cleaned, &raw); err != nil {
		cleaned2 := stripTrailingCommas(cleaned)
		if err2 := json.Unmarshal(cleaned2, &raw); err2 != nil {
			return nil
		}
	}

	// Resolve extends first so current file can override.
	var baseCfg *aliasConfig
	if len(raw.Extends) > 0 {
		var extStr string
		if err := json.Unmarshal(raw.Extends, &extStr); err != nil {
			var extArr []string
			if err2 := json.Unmarshal(raw.Extends, &extArr); err2 == nil && len(extArr) > 0 {
				extStr = extArr[0]
			}
		}
		if extStr != "" {
			extPath := extStr
			if !filepath.IsAbs(extPath) {
				extPath = filepath.Join(dir, extPath)
			}
			if filepath.Ext(extPath) == "" {
				extPath += ".json"
			}
			if bData, err := os.ReadFile(extPath); err == nil { // #nosec G304 -- extPath is joined from dir+extends, dir is config dir
				if files != nil && depth < 5 {
					*files = append(*files, extPath)
				}
				baseCfg = parseAliasConfig(bData, filepath.Dir(extPath), depth+1, files)
			}
		}
	}

	curBase := ""
	var curPaths map[string][]string
	if raw.CompilerOptions != nil {
		if raw.CompilerOptions.BaseUrl != nil {
			curBase = *raw.CompilerOptions.BaseUrl
		}
		curPaths = raw.CompilerOptions.Paths
	}

	// Merge: base is default, current overrides.
	out := &aliasConfig{paths: map[string][]string{}}
	if baseCfg != nil {
		out.baseUrl = baseCfg.baseUrl
		for k, v := range baseCfg.paths {
			out.paths[k] = v
		}
	}
	if curBase != "" {
		out.baseUrl = curBase
	} else if out.baseUrl == "" && baseCfg == nil {
		out.baseUrl = ""
	}
	for k, v := range curPaths {
		out.paths[k] = v
	}
	return out
}

// matchSpec selects the paths entry that governs spec and returns its targets
// plus the text the "*" captured. Exact keys win; among wildcard keys the
// longest prefix before "*" wins, ties broken by key order so the choice is
// deterministic across map iterations.
//
// A paths entry may list several fallback targets (e.g. "@/*": ["src/*",
// "lib/*"]). Selection stops here — matchSpec has no view of the repo's
// indexed files — and resolveSpecifier (mapper_calls.go) probes the returned
// targets in order, first indexed hit winning, which is TS's own
// "first target that exists" rule.
func (c *aliasConfig) matchSpec(spec string) ([]string, string) {
	if c == nil {
		return nil, ""
	}
	if targets := c.paths[spec]; len(targets) > 0 {
		return targets, ""
	}

	bestKey := ""
	bestPrefixLen := -1
	var bestTargets []string
	for k, v := range c.paths {
		if len(v) == 0 {
			continue
		}
		starIdx := strings.Index(k, "*")
		if starIdx < 0 {
			continue
		}
		prefix := k[:starIdx]
		suffix := k[starIdx+1:]
		if len(spec) < len(prefix)+len(suffix) || !strings.HasPrefix(spec, prefix) || !strings.HasSuffix(spec, suffix) {
			continue
		}
		if starIdx > bestPrefixLen || (starIdx == bestPrefixLen && (bestKey == "" || k < bestKey)) {
			bestKey = k
			bestPrefixLen = starIdx
			bestTargets = v
		}
	}
	if bestKey == "" {
		return nil, ""
	}
	prefix := bestKey[:bestPrefixLen]
	suffix := bestKey[bestPrefixLen+1:]
	return bestTargets, spec[len(prefix) : len(spec)-len(suffix)]
}

// stripJSONComments removes // line comments and /* block comments */ outside
// of JSON strings, preserving newlines so line numbers stay meaningful. It is
// string-aware: comment markers inside "..." are not stripped.
func stripJSONComments(data []byte) []byte {
	var out []byte
	inString := false
	escape := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(data) {
			if data[i+1] == '/' {
				// line comment: skip to newline, keep newline
				i += 2
				for i < len(data) && data[i] != '\n' {
					i++
				}
				if i < len(data) {
					out = append(out, '\n')
				}
				continue
			}
			if data[i+1] == '*' {
				i += 2
				for i+1 < len(data) {
					if data[i] == '*' && data[i+1] == '/' {
						// Leave i on the closing '/' so the loop's i++ steps
						// past it. Advancing by 2 here would let that i++
						// swallow the character after the comment — the comma
						// in `1/*c*/,` — and the resulting JSON no longer
						// parses, which silently drops every alias in the file.
						i++
						break
					}
					if data[i] == '\n' {
						out = append(out, '\n')
					}
					i++
				}
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// stripTrailingCommas removes commas that are followed only by whitespace and
// then '}' or ']', outside of JSON strings. It is string-aware like
// stripJSONComments, so commas inside strings are preserved.
func stripTrailingCommas(data []byte) []byte {
	var out []byte
	inString := false
	escape := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			// look ahead for whitespace then } or ]
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				// swallow comma, do not emit
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
