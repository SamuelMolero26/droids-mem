package scrub

import "math"

// shannonBitsPerChar returns the Shannon entropy of s in bits per rune.
// Used as a deterministic gate on usage-shape detector candidates: a
// placeholder like "changeme" (~2.75) stays untouched while a generated
// secret like "x7Kp9q2mNv8wLz4r" (4.0) is redacted. Pure function — same
// input always yields the same output, preserving the scrub determinism
// guarantee (ADR-0008).
func shannonBitsPerChar(s string) float64 {
	if s == "" {
		return 0
	}
	counts := make(map[rune]int, len(s))
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	h := 0.0
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}
