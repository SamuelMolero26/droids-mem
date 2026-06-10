package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type scrubCase struct {
	Name     string         `yaml:"name"`
	Category string         `yaml:"category"`
	Input    string         `yaml:"input"`
	Expected string         `yaml:"expected"`
	Counts   map[string]int `yaml:"counts"`
}

type scrubCorpus struct {
	Cases []scrubCase `yaml:"cases"`
}

func loadCorpus(t *testing.T) scrubCorpus {
	t.Helper()
	path := filepath.Join("testdata", "scrub", "corpus.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c scrubCorpus
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus has no cases")
	}
	// Strip the [CUT] defang marker (see corpusCutMarker in corpus.go): the
	// at-rest YAML breaks every token shape so secret scanners stay quiet;
	// the test exercises the reassembled real shape.
	for i := range c.Cases {
		c.Cases[i].Input = strings.ReplaceAll(c.Cases[i].Input, corpusCutMarker, "")
		c.Cases[i].Expected = strings.ReplaceAll(c.Cases[i].Expected, corpusCutMarker, "")
	}
	return c
}

// TestScrub is the canonical table-driven test for the scrub engine. Each
// subtest is named `<category>/<case_name>` so filters like
// `go test -run TestScrub/negative` sweep just false-positive coverage.
func TestScrub(t *testing.T) {
	corpus := loadCorpus(t)
	for _, tc := range corpus.Cases {

		t.Run(tc.Category+"/"+tc.Name, func(t *testing.T) {
			got, report := Scrub(tc.Input)
			if got != tc.Expected {
				t.Errorf("output mismatch\n  input:    %q\n  expected: %q\n  got:      %q",
					tc.Input, tc.Expected, got)
			}
			wantCounts := tc.Counts
			if wantCounts == nil {
				wantCounts = map[string]int{}
			}
			gotCounts := report.PerPatternCounts
			if gotCounts == nil {
				gotCounts = map[string]int{}
			}
			if !reflect.DeepEqual(gotCounts, wantCounts) {
				t.Errorf("per_pattern_counts mismatch\n  expected: %v\n  got:      %v", wantCounts, gotCounts)
			}
			wantTotal := 0
			for _, n := range wantCounts {
				wantTotal += n
			}
			if report.RedactionCount != wantTotal {
				t.Errorf("redaction_count = %d, want %d", report.RedactionCount, wantTotal)
			}
			if report.PatternVersion != ScrubPatternVersion {
				t.Errorf("pattern_version = %d, want %d", report.PatternVersion, ScrubPatternVersion)
			}
		})
	}
}

func TestScrub_EmptyString(t *testing.T) {
	got, report := Scrub("")
	if got != "" {
		t.Errorf("empty input mutated to %q", got)
	}
	if report.RedactionCount != 0 {
		t.Errorf("redaction_count = %d, want 0", report.RedactionCount)
	}
	if report.PatternVersion != ScrubPatternVersion {
		t.Errorf("pattern_version = %d, want %d", report.PatternVersion, ScrubPatternVersion)
	}
}

func TestScrub_OverlapLongerWins(t *testing.T) {
	// sk-ant-* matches both anthropic_key (idx 6) and openai_key (idx 7).
	// Anthropic regex consumes more chars, so longer-wins rule must pick it.
	input := "sk-ant-api03-" + "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXY"
	got, report := Scrub(input)
	if got != "[ANTHROPIC_KEY]" {
		t.Errorf("expected anthropic to win, got %q", got)
	}
	if report.PerPatternCounts["anthropic_key"] != 1 {
		t.Errorf("anthropic_key count = %d, want 1", report.PerPatternCounts["anthropic_key"])
	}
	if _, openAIFired := report.PerPatternCounts["openai_key"]; openAIFired {
		t.Errorf("openai_key should not fire when anthropic_key wins")
	}
}

// BenchmarkScrub10KB_NoMatches is the typical-case benchmark — distilled
// lesson bodies rarely contain secrets. Target: p95 < 500 µs per v1.0 plan.
//
// Run: go test ./internal/store -bench=BenchmarkScrub -benchmem -run=^$
func BenchmarkScrub10KB_NoMatches(b *testing.B) {
	body := buildPlainBody(10 * 1024)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Scrub(body)
	}
}

// BenchmarkScrub10KB_Sparse models a realistic lesson body — 10 KB of prose
// with ~3 secrets total. This is what the 500 µs p95 target is measured
// against in production.
func BenchmarkScrub10KB_Sparse(b *testing.B) {
	body := buildSparseBody(10 * 1024)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Scrub(body)
	}
}

// BenchmarkScrub10KB_Dense is the pathological stress case — every ~270 chars
// holds a secret-shape token. Not realistic for distilled lessons but
// useful for catching regression in the regex sweep or overlap resolver.
func BenchmarkScrub10KB_Dense(b *testing.B) {
	body := buildDenseBody()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Scrub(body)
	}
}

func buildPlainBody(size int) string {
	chunk := "Normal prose with no patterns. Just words and sentences here. "
	var out []byte
	for len(out) < size {
		out = append(out, chunk...)
	}
	return string(out[:size])
}

// buildSparseBody constructs ~size bytes of plain prose with exactly three
// secrets sprinkled in. Mirrors the realistic case where the agent quotes
// one error context and one config blob.
func buildSparseBody(size int) string {
	prefix := "Lesson learned: the credential ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB " +
		"leaked into logs from host 192.168.1.42 and was reported to alice@example.com. "
	suffix := buildPlainBody(size - len(prefix))
	return prefix + suffix
}

// buildDenseBody constructs ~10 KB of mixed lesson-style prose sprinkled
// with secrets every ~270 chars. Stress case for the regex sweep.
func buildDenseBody() string {
	chunk := "lesson body text describes API behavior. " +
		"observed ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB and AKIAIOSFODNN7EXAMPLE. " +
		"reach out to alice@example.com or 192.168.1.1 next iteration. " +
		"normal sentences pad the rest of this corpus body so the scrub engine " +
		"sees realistic prose-to-secret ratios across the full 10 KB span. "
	var out []byte
	for len(out) < 10*1024 {
		out = append(out, chunk...)
	}
	return string(out[:10*1024])
}
