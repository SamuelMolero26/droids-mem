// Package scrub is the deterministic secret-scrub engine: a single-pass
// redaction stage driven by the embedded declarative detector spec
// (spec.yaml). The engine knows nothing about save-path policy — which
// fields get scrubbed, tag strict-reject, empty-after-scrub — that lives in
// internal/store. Architecture rationale: docs/adr/0008-layered-scrub-detectors.md.
package scrub

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed spec.yaml
var specYAML []byte

// Version is the pattern version declared in spec.yaml. Stamped on every
// saved row (scrub_pattern_version) and every ScrubReport so a future spec
// change is detectable by `migrate --rescrub` and `doctor --scrub-stats`.
var Version int

// specFile mirrors spec.yaml.
type specFile struct {
	Version   int            `yaml:"version"`
	Detectors []specDetector `yaml:"detectors"`
}

type specDetector struct {
	Name       string   `yaml:"name"`
	Class      string   `yaml:"class"`
	Token      string   `yaml:"token"`
	Regex      string   `yaml:"regex"`
	Group      int      `yaml:"group"`
	Needles    []string `yaml:"needles"`
	NeedlesCI  bool     `yaml:"needles_ci"`
	Window     int      `yaml:"window"`
	GuardChars string   `yaml:"guard_chars"`
	Validator  string   `yaml:"validator"`
	MinEntropy float64  `yaml:"min_entropy"`
}

// detector is one compiled entry. Order in the slice is declaration order —
// overlap resolution ties break toward the lower index.
type detector struct {
	name       string
	token      string
	re         *regexp.Regexp
	group      int
	needles    []string
	needlesCI  bool
	window     int
	guardChars string
	validate   func(string) bool
}

// detectors is the compiled spec, populated at package init. Any spec error
// (bad YAML, bad regex, unknown validator) panics: a binary with a broken
// scrub spec must not start, and any test importing this package catches it.
var detectors []detector

// anyNeedlesCI records whether at least one detector wants case-insensitive
// needle matching, so Scrub lowercases the input at most once per call.
var anyNeedlesCI bool

func init() {
	var sf specFile
	if err := yaml.Unmarshal(specYAML, &sf); err != nil {
		panic(fmt.Sprintf("scrub: parse spec.yaml: %v", err))
	}
	if sf.Version < 1 {
		panic("scrub: spec.yaml must declare version >= 1")
	}
	if len(sf.Detectors) == 0 {
		panic("scrub: spec.yaml declares no detectors")
	}
	Version = sf.Version

	compiled := make([]detector, 0, len(sf.Detectors))
	seen := map[string]bool{}
	for _, sd := range sf.Detectors {
		if sd.Name == "" || sd.Token == "" || sd.Regex == "" {
			panic(fmt.Sprintf("scrub: detector %q missing name/token/regex", sd.Name))
		}
		if seen[sd.Name] {
			panic(fmt.Sprintf("scrub: duplicate detector name %q", sd.Name))
		}
		seen[sd.Name] = true

		re, err := regexp.Compile(sd.Regex)
		if err != nil {
			panic(fmt.Sprintf("scrub: detector %q regex: %v", sd.Name, err))
		}
		if sd.Group > 0 && sd.Group > re.NumSubexp() {
			panic(fmt.Sprintf("scrub: detector %q group %d exceeds %d capture groups", sd.Name, sd.Group, re.NumSubexp()))
		}

		if sd.Window > 0 && len(sd.Needles) == 0 {
			panic(fmt.Sprintf("scrub: detector %q declares window without needles", sd.Name))
		}
		if sd.GuardChars != "" && sd.Window <= 0 {
			panic(fmt.Sprintf("scrub: detector %q declares guard_chars without window", sd.Name))
		}
		d := detector{
			name:       sd.Name,
			token:      sd.Token,
			re:         re,
			group:      sd.Group,
			needles:    sd.Needles,
			needlesCI:  sd.NeedlesCI,
			window:     sd.Window,
			guardChars: sd.GuardChars,
		}
		if d.needlesCI {
			anyNeedlesCI = true
			for i, n := range d.needles {
				d.needles[i] = strings.ToLower(n)
			}
		}

		switch sd.Validator {
		case "":
			// no structural gate
		case "ipv4_octets":
			d.validate = validateIPv4Octets
		case "entropy":
			min := sd.MinEntropy
			if min <= 0 {
				panic(fmt.Sprintf("scrub: detector %q uses entropy validator without min_entropy", sd.Name))
			}
			d.validate = func(s string) bool { return shannonBitsPerChar(s) >= min }
		default:
			panic(fmt.Sprintf("scrub: detector %q unknown validator %q", sd.Name, sd.Validator))
		}

		compiled = append(compiled, d)
	}
	detectors = compiled
}
