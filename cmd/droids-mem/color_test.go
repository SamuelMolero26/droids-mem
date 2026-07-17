package main

import (
	"strings"
	"testing"
)

func TestPaint(t *testing.T) {
	orig := colorOn
	defer func() { colorOn = orig }()

	colorOn = false
	if got := paint("hi", cMint); got != "hi" {
		t.Errorf("color off: paint should pass through, got %q", got)
	}

	colorOn = true
	got := paint("hi", cBold, cMint)
	if !strings.HasPrefix(got, cBold+cMint) || !strings.HasSuffix(got, cReset) || !strings.Contains(got, "hi") {
		t.Errorf("color on: want bold+mint...reset around text, got %q", got)
	}

	// No codes → passthrough even when on.
	if got := paint("x"); got != "x" {
		t.Errorf("no codes: want passthrough, got %q", got)
	}
}
