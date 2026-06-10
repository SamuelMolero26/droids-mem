package store

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ScrubPatternVersion stamps each saved row so a future pattern update can
// be detected (e.g. by `migrate --rescrub` when bumping the constant). Bump
// in lockstep with any change to the pattern set or replacement tokens.
//
// v2: added github_pat (fine-grained PATs), gitlab_pat, google_api_key,
// npm_token. Rows stamped v1 predate these patterns; `migrate --rescrub`
// rewrites them on demand.
const ScrubPatternVersion = 2

// ScrubReport summarizes redactions applied to a single field. Save-path
// callers aggregate reports across title/what/learned into a per-row blob.
//
// FieldsRedacted is filled by the caller (Scrub processes one string and
// has no notion of field names). RedactionCount and PerPatternCounts come
// from the engine.
type ScrubReport struct {
	RedactionCount   int            `json:"redaction_count"`
	PerPatternCounts map[string]int `json:"per_pattern_counts"`
	FieldsRedacted   []string       `json:"fields_redacted,omitempty"`
	PatternVersion   int            `json:"pattern_version"`
}

// scrubPattern is one declared pattern in the v1.0 set. Order matters: ties
// during overlap resolution are broken by earlier declaration order.
//
// needles is an optional fast-path filter: if non-nil, the engine skips the
// regex unless at least one needle appears in the input. Cuts no-match cost
// from N regex sweeps to N substring scans (≈10× faster on plain prose).
// Needles MUST be substrings every match contains — overly aggressive needles
// silently drop true positives.
type scrubPattern struct {
	name     string
	token    string
	re       *regexp.Regexp
	needles  []string
	validate func(string) bool
}

// patterns is the canonical pattern declaration order (locked decision #12,
// minus SSN + credit_card — dropped because dev-lesson corpus rarely hits
// either and free-text false-positive rate was too high). Most-specific
// first, broad last. NEVER reorder without bumping ScrubPatternVersion —
// overlap resolution depends on this slice's index.
// #nosec G101 -- these are the secret-DETECTION patterns (regexes + redaction
// tokens), not credentials. Nothing here is a usable secret.
var patterns = []scrubPattern{
	{
		name:    "pem_key",
		token:   "[PEM_KEY]",
		re:      regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+PRIVATE KEY-----.*?-----END [A-Z0-9 ]+PRIVATE KEY-----`),
		needles: []string{"-----BEGIN"},
	},
	{
		name:    "jwt",
		token:   "[JWT]",
		re:      regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`),
		needles: []string{"eyJ"},
	},
	{
		name:    "aws_key",
		token:   "[AWS_KEY]",
		re:      regexp.MustCompile(`\b(?:AKIA|ASIA|AROA|AGPA|ANPA|ANVA|AIPA)[0-9A-Z]{16}\b`),
		needles: []string{"AKIA", "ASIA", "AROA", "AGPA", "ANPA", "ANVA", "AIPA"},
	},
	{
		name:    "github_token",
		token:   "[GITHUB_TOKEN]",
		re:      regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,255}\b`),
		needles: []string{"ghp_", "ghs_", "gho_", "ghu_", "ghr_"},
	},
	{
		// Fine-grained GitHub PATs (github_pat_...) — distinct prefix from the
		// classic gh[pousr]_ family, so they need their own pattern.
		name:    "github_pat",
		token:   "[GITHUB_TOKEN]",
		re:      regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{36,255}\b`),
		needles: []string{"github_pat_"},
	},
	{
		name:    "gitlab_pat",
		token:   "[GITLAB_TOKEN]",
		re:      regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`),
		needles: []string{"glpat-"},
	},
	{
		name:    "google_api_key",
		token:   "[GOOGLE_KEY]",
		re:      regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),
		needles: []string{"AIza"},
	},
	{
		name:    "npm_token",
		token:   "[NPM_TOKEN]",
		re:      regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`),
		needles: []string{"npm_"},
	},
	{
		name:    "stripe_key",
		token:   "[STRIPE_KEY]",
		re:      regexp.MustCompile(`\b(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{20,247}\b`),
		needles: []string{"sk_live_", "sk_test_", "pk_live_", "pk_test_", "rk_live_", "rk_test_"},
	},
	{
		name:    "slack_token",
		token:   "[SLACK_TOKEN]",
		re:      regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{20,}\b`),
		needles: []string{"xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-"},
	},
	{
		name:    "anthropic_key",
		token:   "[ANTHROPIC_KEY]",
		re:      regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{50,}\b`),
		needles: []string{"sk-ant-"},
	},
	{
		name:    "openai_key",
		token:   "[OPENAI_KEY]",
		re:      regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{20,200}\b`),
		needles: []string{"sk-"},
	},
	{
		// Strict E.164: '+' followed by 1-9, then 1-14 more digits. No separators.
		// Tightens false-positive rate on free text vs. permissive phone regexes.
		name:    "phone",
		token:   "[PHONE]",
		re:      regexp.MustCompile(`\+[1-9]\d{1,14}\b`),
		needles: []string{"+"},
	},
	{
		name:     "private_ipv4",
		token:    "[PRIVATE_IP]",
		re:       regexp.MustCompile(`\b(?:10|127)\.\d{1,3}\.\d{1,3}\.\d{1,3}\b|\b172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}\b|\b192\.168\.\d{1,3}\.\d{1,3}\b`),
		needles:  []string{"10.", "127.", "172.", "192.168."},
		validate: validateIPv4Octets,
	},
	{
		name:    "email",
		token:   "[EMAIL]",
		re:      regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`),
		needles: []string{"@"},
	},
}

// rawMatch is one pattern hit before overlap resolution. patternIdx is the
// position in `patterns` — lower wins ties.
type rawMatch struct {
	start, end int
	patternIdx int
}

// Scrub runs every enabled pattern against text in a single pass, resolves
// overlaps (longest match wins; ties broken by earlier pattern declaration),
// and splices in replacement tokens left-to-right via strings.Builder so
// indices stay valid and no intermediate copies are allocated.
//
// Returns the redacted text and a ScrubReport. Caller fills FieldsRedacted
// after stitching together multi-field results.
func Scrub(text string) (string, ScrubReport) {
	report := ScrubReport{
		PerPatternCounts: map[string]int{},
		PatternVersion:   ScrubPatternVersion,
	}
	if text == "" {
		return text, report
	}

	raws := collectMatches(text)
	if len(raws) == 0 {
		return text, report
	}
	accepted := resolveOverlaps(raws)
	if len(accepted) == 0 {
		return text, report
	}

	sort.Slice(accepted, func(a, b int) bool { return accepted[a].start < accepted[b].start })

	var b strings.Builder
	b.Grow(len(text))
	cursor := 0
	for _, m := range accepted {
		b.WriteString(text[cursor:m.start])
		p := patterns[m.patternIdx]
		b.WriteString(p.token)
		report.PerPatternCounts[p.name]++
		report.RedactionCount++
		cursor = m.end
	}
	b.WriteString(text[cursor:])
	return b.String(), report
}

func collectMatches(text string) []rawMatch {
	var raws []rawMatch
	for i, p := range patterns {
		if !needlePresent(text, p.needles) {
			continue
		}
		for _, idx := range p.re.FindAllStringIndex(text, -1) {
			if p.validate != nil && !p.validate(text[idx[0]:idx[1]]) {
				continue
			}
			raws = append(raws, rawMatch{start: idx[0], end: idx[1], patternIdx: i})
		}
	}
	return raws
}

// needlePresent returns true when at least one needle appears in text. An
// empty needle list means "no prefilter" — fall through to the regex.
func needlePresent(text string, needles []string) bool {
	if len(needles) == 0 {
		return true
	}
	for _, n := range needles {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
}

// resolveOverlaps picks a maximal non-overlapping set under the rule
// "longest wins, ties broken by earlier declaration order". Sorts by length
// desc then idx asc, then sweeps accepting matches that don't overlap any
// already-accepted span.
func resolveOverlaps(raws []rawMatch) []rawMatch {
	sort.Slice(raws, func(a, b int) bool {
		la := raws[a].end - raws[a].start
		lb := raws[b].end - raws[b].start
		if la != lb {
			return la > lb
		}
		if raws[a].patternIdx != raws[b].patternIdx {
			return raws[a].patternIdx < raws[b].patternIdx
		}
		return raws[a].start < raws[b].start
	})
	var accepted []rawMatch
	for _, m := range raws {
		overlap := false
		for _, a := range accepted {
			if m.start < a.end && a.start < m.end {
				overlap = true
				break
			}
		}
		if !overlap {
			accepted = append(accepted, m)
		}
	}
	return accepted
}

// validateIPv4Octets confirms each dotted-decimal octet in s fits 0-255.
// The structural regex doesn't bound octet values on its own.
func validateIPv4Octets(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}
