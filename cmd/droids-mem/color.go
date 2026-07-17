package main

import (
	"os"
	"strings"
)

// Palette lifted from the Paper "Droids-mem Tui New" file (board 1Y-0): a
// near-black base with a mint success accent and a pink danger accent — the two
// semantic poles a CLI needs (shared/confirmed vs abort/warn) — plus muted grays
// for hierarchy. Rendered as 24-bit truecolor ANSI; any modern terminal shows
// them, and the enabled-gate turns them off for pipes/JSON so machine output is
// never corrupted.
const (
	cMint   = "\x1b[38;2;18;204;165m"  // #12CCA5 — shared / success
	cPink   = "\x1b[38;2;250;41;104m"  // #FA2968 — abort / danger
	cDim    = "\x1b[38;2;91;90;89m"    // #5B5A59 — secondary text
	cTaupe  = "\x1b[38;2;173;142;125m" // #AD8E7D — labels / kind
	cBright = "\x1b[38;2;220;220;220m" // #DCDCDC — values
	cBold   = "\x1b[1m"
	cReset  = "\x1b[0m"
)

// colorOn is resolved once: true only when stdout is a terminal and NO_COLOR is
// unset (https://no-color.org). A pipe, file, or JSON consumer gets plain text.
var colorOn = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

// paint wraps s in an ANSI code (or codes) when color is on, else returns s
// unchanged. Multiple codes stack (e.g. bold + color).
func paint(s string, codes ...string) string {
	if !colorOn || len(codes) == 0 {
		return s
	}
	return strings.Join(codes, "") + s + cReset
}
