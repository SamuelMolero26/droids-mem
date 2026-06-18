// Scrub engine lives in internal/scrub (declarative spec, detector classes,
// entropy gate — see docs/adr/0008-layered-scrub-detectors.md). This file
// re-exports the surface store consumers and the CLI already depend on, so
// save-path policy code (validate, tag strict-reject, rescrub, scrub-stats)
// keeps reading naturally while the engine stays independently testable and
// configurable.
package store

import "github.com/samuelmolero26/droids-mem/internal/scrub"

// ScrubReport is the per-string redaction summary produced by the engine.
type ScrubReport = scrub.ScrubReport

// CorpusReport and CorpusCaseResult back `droids-mem scrub --test`.
type (
	CorpusReport     = scrub.CorpusReport
	CorpusCaseResult = scrub.CorpusCaseResult
)

// ScrubPatternVersion is the spec-declared pattern version stamped on every
// saved row and ScrubReport.
var ScrubPatternVersion = scrub.Version

// Scrub runs the engine on one string. See scrub.Scrub.
func Scrub(text string) (string, ScrubReport) {
	return scrub.Scrub(text)
}

// RunCorpus runs the embedded fixture corpus. See scrub.RunCorpus.
func RunCorpus() (*CorpusReport, error) {
	return scrub.RunCorpus()
}
