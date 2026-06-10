package store

import (
	_ "embed"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// corpusCutMarker is stripped from fixture inputs/expectations at load time.
// Fixtures embed it mid-token (e.g. "ghp_[CUT]FIXTURE...") so the at-rest
// YAML never contains a string matching any secret scanner's token shape —
// GitHub push protection and GitGuardian both match on shape regardless of
// FIXTURE/NOTAREAL words. The runtime value, with the marker removed, still
// exercises the real pattern.
const corpusCutMarker = "[CUT]"

//go:embed testdata/scrub/corpus.yaml
var embeddedCorpus []byte

// CorpusCase is one fixture entry: a category, an input, the expected scrubbed
// output, and the per-pattern counts the engine should report. Mirrors the
// schema parsed by scrub_test.go's loader — kept in sync by hand so the test
// stays self-contained without importing this exported surface.
type CorpusCase struct {
	Name     string         `yaml:"name" json:"name"`
	Category string         `yaml:"category" json:"category"`
	Input    string         `yaml:"input" json:"-"`
	Expected string         `yaml:"expected" json:"-"`
	Counts   map[string]int `yaml:"counts" json:"-"`
}

// CorpusCaseResult is one case after Scrub has been run. Failures populate
// Diff with a human-readable explanation; passes leave it empty so callers
// can grep for it.
type CorpusCaseResult struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Pass     bool   `json:"pass"`
	Diff     string `json:"diff,omitempty"`
}

// CorpusReport is the structured output of `droids-mem scrub --test`. Exit
// non-zero when Failed > 0 so the command can be wired into CI without parsing.
type CorpusReport struct {
	Total          int                `json:"total"`
	Passed         int                `json:"passed"`
	Failed         int                `json:"failed"`
	PatternVersion int                `json:"pattern_version"`
	Cases          []CorpusCaseResult `json:"cases"`
}

// RunCorpus loads the embedded YAML fixture, runs Scrub on each case, and
// returns a structured pass/fail report. Embedded (rather than read from
// disk) so the CLI is portable — the binary ships with the fixture baked in.
func RunCorpus() (*CorpusReport, error) {
	var c struct {
		Cases []CorpusCase `yaml:"cases"`
	}
	if err := yaml.Unmarshal(embeddedCorpus, &c); err != nil {
		return nil, fmt.Errorf("parse embedded corpus: %w", err)
	}
	rep := &CorpusReport{
		Total:          len(c.Cases),
		PatternVersion: ScrubPatternVersion,
		Cases:          make([]CorpusCaseResult, 0, len(c.Cases)),
	}
	for _, tc := range c.Cases {
		tc.Input = strings.ReplaceAll(tc.Input, corpusCutMarker, "")
		tc.Expected = strings.ReplaceAll(tc.Expected, corpusCutMarker, "")
		got, report := Scrub(tc.Input)
		want := tc.Counts
		if want == nil {
			want = map[string]int{}
		}
		gotCounts := report.PerPatternCounts
		if gotCounts == nil {
			gotCounts = map[string]int{}
		}
		res := CorpusCaseResult{Name: tc.Name, Category: tc.Category, Pass: true}
		if got != tc.Expected {
			res.Pass = false
			res.Diff = fmt.Sprintf("output mismatch: want %q got %q", tc.Expected, got)
		} else if !reflect.DeepEqual(gotCounts, want) {
			res.Pass = false
			res.Diff = fmt.Sprintf("counts mismatch: want %v got %v", want, gotCounts)
		}
		if res.Pass {
			rep.Passed++
		} else {
			rep.Failed++
		}
		rep.Cases = append(rep.Cases, res)
	}
	return rep, nil
}
