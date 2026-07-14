package graph

import (
	"fmt"
	"strconv"
	"strings"
)

// TOON rendering of graph responses (ADR-0027). The code-graph surface answers
// in TOON instead of JSON: neighbor arrays are uniform signature stubs, the
// ideal tabular case, and hub queries (many callers) shed ~30% of their tokens.
// Both the CLI (`graph symbol`/`graph package`) and the MCP tools share this
// one encoder, so the two surfaces never drift.
//
// The render is hybrid, not a pure TOON document: scalars are `key: value`
// lines, uniform arrays are TOON tables, and the multiline `source` body is a
// fenced code block — an agent reads real Go, not a \n-escaped string, which is
// both fewer tokens and more readable than the old JSON.

// RenderSymbol encodes a symbol-anchored response.
func RenderSymbol(r *SymbolResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "repo: %s\n", r.Repo)
	writeFreshness(&b, r.Freshness)

	if r.Symbol != nil {
		s := r.Symbol
		fmt.Fprintf(&b, "symbol: %s  %s  %s\n", s.QName, s.Kind, loc(s.File, s.Line))
		fmt.Fprintf(&b, "signature: %s\n", s.Signature)
		if s.Doc != "" {
			fmt.Fprintf(&b, "doc: %s\n", s.Doc)
		}
		if s.Source != "" {
			fmt.Fprintf(&b, "source:\n%s\n", fence(s.Source))
		}
	}

	writeNeighbors(&b, "callers", r.Callers)
	writeNeighbors(&b, "callees", r.Callees)
	writeNeighbors(&b, "path", r.Path)
	writeNeighbors(&b, "matches", r.Matches)

	if r.TransitiveCallers != nil {
		fmt.Fprintf(&b, "transitive_callers: %d\n", *r.TransitiveCallers)
	}
	if r.Truncated {
		b.WriteString("truncated: true\n")
	}
	if r.Hint != "" {
		fmt.Fprintf(&b, "hint: %s\n", r.Hint)
	}
	return strings.TrimRight(b.String(), "\n")
}

// RenderPackage encodes a scope-anchored (package surface) response.
func RenderPackage(r *PackageResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "repo: %s\n", r.Repo)
	writeFreshness(&b, r.Freshness)
	fmt.Fprintf(&b, "package: %s\n", r.Package)

	if len(r.Symbols) > 0 {
		keys := []string{"qname", "kind", "signature", "doc", "loc"}
		b.WriteString(header("symbols", len(r.Symbols), keys))
		for _, s := range r.Symbols {
			b.WriteString(row(s.QName, s.Kind, s.Signature, s.Doc, loc(s.File, s.Line)))
		}
	} else {
		b.WriteString("symbols: none\n")
	}

	fmt.Fprintf(&b, "unexported: %d\n", r.Unexported)
	if r.Truncated {
		b.WriteString("truncated: true\n")
	}
	if r.Hint != "" {
		fmt.Fprintf(&b, "hint: %s\n", r.Hint)
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeFreshness emits a line only when the graph is stale — the actionable
// case. Absence means fresh, so the common path costs zero tokens.
func writeFreshness(b *strings.Builder, f Freshness) {
	if !f.Stale {
		return
	}
	if f.IndexError != "" {
		fmt.Fprintf(b, "freshness: STALE (repo no longer type-checks; serving last good index: %s)\n", f.IndexError)
		return
	}
	b.WriteString("freshness: STALE (repo no longer type-checks; serving last good index)\n")
}

func writeNeighbors(b *strings.Builder, name string, ns []Neighbor) {
	if len(ns) == 0 {
		return
	}
	keys := []string{"qname", "signature", "loc", "depth"}
	b.WriteString(header(name, len(ns), keys))
	for _, n := range ns {
		b.WriteString(row(n.QName, n.Signature, loc(n.File, n.Line), strconv.Itoa(n.Depth)))
	}
}

func header(name string, n int, keys []string) string {
	return name + "[" + strconv.Itoa(n) + "]{" + strings.Join(keys, ",") + "}:\n"
}

func row(vals ...string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = quote(v)
	}
	return "  " + strings.Join(out, ",") + "\n"
}

// quote wraps a value in double quotes only when it carries the row delimiter
// or a quote — signatures ("func(a, b)") always trip this; qnames and locs
// never do.
func quote(s string) string {
	if strings.ContainsAny(s, ",\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func loc(file string, line int) string {
	return file + ":" + strconv.Itoa(line)
}

// fence wraps a Go source body in a ```go code block. The fence length adapts
// to the longest backtick run inside the body (Go raw-string literals contain
// backticks), so a body containing a raw-string regex still closes cleanly
// instead of terminating the fence early.
func fence(src string) string {
	longest, run := 0, 0
	for _, c := range src {
		if c == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	ticks := strings.Repeat("`", max(3, longest+1))
	return ticks + "go\n" + src + "\n" + ticks
}
