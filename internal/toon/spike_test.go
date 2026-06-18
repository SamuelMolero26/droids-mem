package toon

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// PHASE 0 SPIKE (ADR 0017 measure-first kill-gate). Run:
//
//	go test ./internal/toon -run Spike -v
//
// Test-only by design — a throwaway pipe-delimited TOON v3.0 browse-tier codec
// kept solely as the reproducible evidence behind the ADR 0017 ship decision.
// Two jobs:
//   1. T1.0 — round-trip fidelity (MUST pass, incl. adversarial prose).
//   2. T1.1 — net token win JSON vs TOON, incl. prose quoting + a one-time
//      prompt-tax charge; reports PASS/FAIL vs the ~20% net-win gate.
//
// VERDICT (recorded in ADR 0017): net win at 20 rows = 11.9% < 20% bar → FAIL.
// If this package is ever promoted, replace this file with a real edge encoder.

// row mirrors the orient browse-tier fields of store.ContextMemory.
type row struct {
	ID        string
	Kind      string
	Title     string
	Tier      string
	Snippet   string
	CreatedAt int64
}

var browseCols = []string{"id", "kind", "title", "tier", "snippet", "created_at"}

const delim = '|'

// encodeBrowse renders rows as a pipe-delimited TOON tabular block.
func encodeBrowse(rows []row) string {
	var b strings.Builder
	fmt.Fprintf(&b, "browse[%d]{%s}:\n", len(rows), strings.Join(browseCols, string(delim)))
	for _, r := range rows {
		fields := []string{
			encodeField(r.ID), encodeField(r.Kind), encodeField(r.Title),
			encodeField(r.Tier), encodeField(r.Snippet),
			strconv.FormatInt(r.CreatedAt, 10),
		}
		b.WriteString(strings.Join(fields, string(delim)))
		b.WriteByte('\n')
	}
	return b.String()
}

var quoter = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

func needsQuote(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "|\"\n\r\t:") {
		return true
	}
	return s != strings.TrimSpace(s)
}

func encodeField(s string) string {
	if !needsQuote(s) {
		return s
	}
	return `"` + quoter.Replace(s) + `"`
}

func decodeBrowse(s string) ([]row, error) {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty input")
	}
	var n int
	if _, err := fmt.Sscanf(lines[0], "browse[%d]{", &n); err != nil {
		return nil, fmt.Errorf("bad header %q: %w", lines[0], err)
	}
	data := lines[1:]
	if len(data) != n {
		return nil, fmt.Errorf("header says %d rows, got %d", n, len(data))
	}
	out := make([]row, 0, n)
	for _, line := range data {
		f := splitRow(line)
		if len(f) != len(browseCols) {
			return nil, fmt.Errorf("row %q: %d fields, want %d", line, len(f), len(browseCols))
		}
		ca, err := strconv.ParseInt(f[5], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("row %q: bad created_at: %w", line, err)
		}
		out = append(out, row{ID: f[0], Kind: f[1], Title: f[2], Tier: f[3], Snippet: f[4], CreatedAt: ca})
	}
	return out, nil
}

func splitRow(line string) []string {
	var fields []string
	var cur strings.Builder
	i, n := 0, len(line)
	for i < n {
		if line[i] == '"' {
			i++
			for i < n {
				c := line[i]
				if c == '\\' && i+1 < n {
					switch line[i+1] {
					case '\\':
						cur.WriteByte('\\')
					case '"':
						cur.WriteByte('"')
					case 'n':
						cur.WriteByte('\n')
					case 'r':
						cur.WriteByte('\r')
					case 't':
						cur.WriteByte('\t')
					default:
						cur.WriteByte(line[i+1])
					}
					i += 2
					continue
				}
				if c == '"' {
					i++
					break
				}
				cur.WriteByte(c)
				i++
			}
		} else {
			for i < n && line[i] != byte(delim) {
				cur.WriteByte(line[i])
				i++
			}
		}
		fields = append(fields, cur.String())
		cur.Reset()
		if i < n && line[i] == byte(delim) {
			i++
			if i == n {
				fields = append(fields, "")
			}
		}
	}
	return fields
}

// ---- measurement ----

// jsonRow marshals with the exact json tags of an orient browse-tier
// store.ContextMemory item, so the JSON baseline matches the real payload.
type jsonRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Tier      string `json:"tier"`
	Snippet   string `json:"snippet"`
	CreatedAt int64  `json:"created_at"`
}

func toJSONRows(rows []row) []jsonRow {
	out := make([]jsonRow, len(rows))
	for i, r := range rows {
		out[i] = jsonRow(r)
	}
	return out
}

// tokens — rune/4 heuristic (ADR 0017; no tiktoken). Both encodings measured
// identically, so the RATIO is robust to heuristic error.
func tokens(s string) int { return len([]rune(s)) / 4 }

// promptTax — one-time tokens to teach the model the TOON tabular format,
// charged once per bundle (the arXiv finding warns this can cancel the win in
// short context).
const promptTax = 120

var kinds = []string{"error_resolution", "task_pattern", "task_pattern", "error_resolution"}

// realistic snippets: ~120 runes, commas frequent (forces TOON quoting).
var snippets = []string{
	"DB locked under concurrent writes, switched journal_mode to WAL and added busy_timeout; readers no longer block the single writer.",
	"Fingerprint excluded `what` by design, so two saves with different context but identical learned collapse to one row, which is intended.",
	"Scrub ran before fingerprint, redacting the bearer token to [SECRET]; the redacted form is what gets persisted and hashed, not the raw.",
	"Context bundle browse tier capped at ten error_resolution plus ten task_pattern, ranked by BM25; always tier carries rules in full body.",
	"Near-duplicate caught at Jaccard 0.87 over the BM25 top-20, so the second save was skipped with a match echo pointing at the first id.",
}

func makeRows(n int) []row {
	out := make([]row, n)
	for i := range n {
		out[i] = row{
			ID:        fmt.Sprintf("mem_01J%013dZ", i),
			Kind:      kinds[i%len(kinds)],
			Title:     fmt.Sprintf("Resolve %s edge case in loader path", kinds[i%len(kinds)]),
			Tier:      "browse",
			Snippet:   snippets[i%len(snippets)],
			CreatedAt: 1750000000 + int64(i*37),
		}
	}
	return out
}

func adversarialRows() []row {
	return []row{
		{ID: "mem_a", Kind: "error_resolution", Title: "comma, in title", Tier: "browse",
			Snippet: "value with, commas, and a \"quoted\" segment plus a pipe | char", CreatedAt: 1},
		{ID: "mem_b", Kind: "task_pattern", Title: "newline\nin field", Tier: "browse",
			Snippet: "line one\nline two\twith tab and trailing space ", CreatedAt: 2},
		{ID: "mem_c", Kind: "user_rule", Title: "unicode ünïçødé 日本語", Tier: "browse",
			Snippet: "emoji 🚀 and colon: separated, backslash \\ and quote \" together", CreatedAt: 3},
		{ID: "", Kind: "task_pattern", Title: "", Tier: "browse", Snippet: "", CreatedAt: 4},
	}
}

func TestSpikeRoundTrip(t *testing.T) {
	cases := map[string][]row{
		"realistic":   makeRows(20),
		"adversarial": adversarialRows(),
	}
	for name, rows := range cases {
		enc := encodeBrowse(rows)
		got, err := decodeBrowse(enc)
		if err != nil {
			t.Fatalf("%s: decode failed: %v\n---\n%s", name, err, enc)
		}
		if !reflect.DeepEqual(rows, got) {
			t.Fatalf("%s: round-trip mismatch\nwant %#v\ngot  %#v", name, rows, got)
		}
	}
	t.Log("T1.0 round-trip: PASS (realistic + adversarial)")
}

func TestSpikeTokenWin(t *testing.T) {
	t.Logf("prompt-tax charged once per bundle = %d tokens (rune/4 heuristic)", promptTax)
	t.Logf("%-6s %8s %8s %10s %10s", "rows", "json_tk", "toon_tk", "payload%", "net%")

	var netAt20 float64
	for _, n := range []int{5, 10, 20} {
		rows := makeRows(n)
		jb, _ := json.Marshal(toJSONRows(rows))
		jt := tokens(string(jb))
		tt := tokens(encodeBrowse(rows))

		payloadWin := 100 * float64(jt-tt) / float64(jt)
		netWin := 100 * float64(jt-(tt+promptTax)) / float64(jt)
		if n == 20 {
			netAt20 = netWin
		}
		t.Logf("%-6d %8d %8d %9.1f%% %9.1f%%", n, jt, tt, payloadWin, netWin)
	}

	const gate = 20.0
	t.Logf("--- GATE: net win at 20 rows = %.1f%% vs %.0f%% bar ---", netAt20, gate)
	if netAt20 >= gate {
		t.Logf("RESULT: PASS — schedule Phase 6 (TOON build)")
	} else {
		t.Logf("RESULT: FAIL — TOON does not clear the bar; ADR 0017 -> Rejected")
	}
}
