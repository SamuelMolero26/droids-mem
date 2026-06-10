package main

import (
	"os"

	"github.com/SamuelMolero26/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

// scrubCheckReport is the shape `scrub --check <file>` emits. Mirrors the
// per-row ScrubReport with the file path attached so the operator can pipe
// many files into a wrapper script and still tell the outputs apart.
type scrubCheckReport struct {
	File           string         `json:"file"`
	RedactionCount int            `json:"redaction_count"`
	PerPattern     map[string]int `json:"per_pattern_counts"`
	PatternVersion int            `json:"pattern_version"`
	Scrubbed       string         `json:"scrubbed"`
}

// newScrubCmd wires the operator-facing scrub subcommand. Both modes run
// entirely in memory — no DB connection, no boot gate — so the command is
// safe to run on a stale-baseline database.
func newScrubCmd() *cobra.Command {
	var (
		checkPath string
		runTests  bool
	)
	cmd := &cobra.Command{
		Use:   "scrub",
		Short: "Run the v1.0 scrub engine ad-hoc (file check, fixture suite)",
		Long: `scrub exposes the in-process redaction engine for two human-driven
workflows:

  --check <file>   Read the file, run every scrub pattern against its contents,
                   and print the scrubbed body + per-pattern counts as JSON.
                   No database access. Useful when wiring a new lesson source
                   into the corpus to confirm what would be redacted.

  --test           Run the embedded fixture corpus (testdata/scrub/corpus.yaml,
                   baked into the binary) and print a pass/fail report. Exit
                   non-zero when any case fails so CI can gate on it.`,
		Example: `  droids-mem scrub --check ./draft-lesson.md
  droids-mem scrub --test`,
		Annotations: map[string]string{bootGateBypass: "true"},
		RunE: func(_ *cobra.Command, _ []string) error {
			hasCheck := checkPath != ""
			if hasCheck == runTests {
				writeError("usage_error", "specify exactly one of --check <file> or --test", false,
					withSuggestion("re-run with `droids-mem scrub --check <file>` or `--test`"),
				)
				exitWith(ExitUsage)
			}
			if checkPath != "" {
				return runScrubCheck(checkPath)
			}
			return runScrubTests()
		},
	}
	cmd.Flags().StringVar(&checkPath, "check", "",
		"Path to a file whose contents should be passed through the scrub patterns.")
	cmd.Flags().BoolVar(&runTests, "test", false,
		"Run the embedded fixture corpus and report pass/fail per case.")
	return cmd
}

func runScrubCheck(path string) error {
	// #nosec G304 -- path is the operator-supplied --check argument; reading
	// the file the operator named is the command's entire purpose.
	raw, err := os.ReadFile(path)
	if err != nil {
		writeError("file_read_failed", err.Error(), false,
			withField("check"),
			withInput(path),
			withSuggestion("verify the path exists and is readable"),
		)
		exitWith(ExitError)
	}
	scrubbed, report := store.Scrub(string(raw))
	perPattern := report.PerPatternCounts
	if perPattern == nil {
		perPattern = map[string]int{}
	}
	writeJSON(scrubCheckReport{
		File:           path,
		RedactionCount: report.RedactionCount,
		PerPattern:     perPattern,
		PatternVersion: report.PatternVersion,
		Scrubbed:       scrubbed,
	})
	return nil
}

func runScrubTests() error {
	rep, err := store.RunCorpus()
	if err != nil {
		writeError("corpus_load_failed", err.Error(), false)
		exitWith(ExitError)
	}
	writeJSON(rep)
	if rep.Failed > 0 {
		exitWith(ExitError)
	}
	return nil
}
