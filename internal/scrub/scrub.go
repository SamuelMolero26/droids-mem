package scrub

import (
	"sort"
	"strconv"
	"strings"
)

// ScrubReport summarizes redactions applied to a single string. Save-path
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

// rawMatch is one detector hit before overlap resolution. start/end is the
// redaction span — the capture group when the detector declares one, the
// whole match otherwise. detectorIdx is the position in `detectors`; lower
// wins ties.
type rawMatch struct {
	start, end  int
	detectorIdx int
}

// Scrub runs every detector against text in a single pass, resolves overlaps
// (longest redaction span wins; ties broken by earlier declaration in
// spec.yaml), and splices in replacement tokens left-to-right via
// strings.Builder so indices stay valid and no intermediate copies are
// allocated.
//
// Returns the redacted text and a ScrubReport. Caller fills FieldsRedacted
// after stitching together multi-field results.
func Scrub(text string) (string, ScrubReport) {
	report := ScrubReport{
		PerPatternCounts: map[string]int{},
		PatternVersion:   Version,
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
		d := detectors[m.detectorIdx]
		b.WriteString(d.token)
		report.PerPatternCounts[d.name]++
		report.RedactionCount++
		cursor = m.end
	}
	b.WriteString(text[cursor:])
	return b.String(), report
}

// windowMargin is the left margin prepended to each needle window so the
// regex sees enough preceding context for \b word-boundary decisions.
const windowMargin = 16

func collectMatches(text string) []rawMatch {
	// Lowercase at most once per call, shared by every detector that asked
	// for case-insensitive needles. Index alignment between lower and text
	// is only byte-exact for ASCII input; non-ASCII falls back to full-text
	// sweeps below.
	ascii := isASCII(text)
	lower := ""
	if anyNeedlesCI && ascii {
		lower = strings.ToLower(text)
	}

	var raws []rawMatch
	for i, d := range detectors {
		haystack := text
		if d.needlesCI {
			if !ascii {
				// Cannot align ci-needle offsets on non-ASCII input — run
				// the full (?i) regex sweep instead. Correct, just slower;
				// rare for this corpus.
				raws = appendMatches(raws, d, i, text, d.re.FindAllStringSubmatchIndex(text, -1), 0)
				continue
			}
			haystack = lower
		}
		if !needlePresent(haystack, d.needles) {
			continue
		}
		if d.window > 0 {
			raws = appendWindowedMatches(raws, d, i, text, haystack)
			continue
		}
		raws = appendMatches(raws, d, i, text, d.re.FindAllStringSubmatchIndex(text, -1), 0)
	}
	return raws
}

// appendWindowedMatches runs d's regex only on a bounded window after each
// needle occurrence instead of sweeping the whole text. This keeps detectors
// whose needles are common prose words (token, auth, key) inside the scrub
// latency budget: each hit costs a ~window-byte scan, not a full-body scan.
// The detector's regex bounds its match length below the window, so no match
// can be truncated by the window edge.
func appendWindowedMatches(raws []rawMatch, d detector, idx int, text, haystack string) []rawMatch {
	seen := map[int]bool{}
	for _, n := range d.needles {
		off := 0
		for {
			rel := strings.Index(haystack[off:], n)
			if rel < 0 {
				break
			}
			pos := off + rel
			if d.guardChars != "" && !guardNearby(haystack, pos, d.guardChars) {
				off = pos + len(n)
				continue
			}
			start := pos - windowMargin
			if start < 0 {
				start = 0
			}
			end := pos + d.window
			if end > len(text) {
				end = len(text)
			}
			for _, loc := range d.re.FindAllStringSubmatchIndex(text[start:end], -1) {
				if seen[loc[0]+start] {
					continue
				}
				seen[loc[0]+start] = true
				raws = appendMatches(raws, d, idx, text, [][]int{loc}, start)
			}
			off = pos + len(n)
		}
	}
	return raws
}

// appendMatches converts regex submatch locations (offset by base) into
// rawMatch redaction spans, applying group selection and the structural
// validator.
func appendMatches(raws []rawMatch, d detector, idx int, text string, locs [][]int, base int) []rawMatch {
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		if d.group > 0 {
			gs, ge := loc[2*d.group], loc[2*d.group+1]
			if gs < 0 {
				continue
			}
			start, end = gs, ge
		}
		start += base
		end += base
		if d.validate != nil && !d.validate(text[start:end]) {
			continue
		}
		raws = append(raws, rawMatch{start: start, end: end, detectorIdx: idx})
	}
	return raws
}

// guardSpan bounds how far after a needle hit guardNearby looks for one of
// the detector's guard chars. Covers the longest keyword + quotes + spacing.
const guardSpan = 64

// guardNearby reports whether any guard char appears within guardSpan bytes
// after pos. A prose mention of "token" has no nearby ':'/'=' and is skipped
// before the (comparatively expensive) window regex runs.
func guardNearby(haystack string, pos int, guardChars string) bool {
	end := pos + guardSpan
	if end > len(haystack) {
		end = len(haystack)
	}
	return strings.ContainsAny(haystack[pos:end], guardChars)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
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
// "longest redaction span wins, ties broken by earlier declaration order".
// Sorts by length desc then idx asc, then sweeps accepting matches that
// don't overlap any already-accepted span.
func resolveOverlaps(raws []rawMatch) []rawMatch {
	sort.Slice(raws, func(a, b int) bool {
		la := raws[a].end - raws[a].start
		lb := raws[b].end - raws[b].start
		if la != lb {
			return la > lb
		}
		if raws[a].detectorIdx != raws[b].detectorIdx {
			return raws[a].detectorIdx < raws[b].detectorIdx
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
