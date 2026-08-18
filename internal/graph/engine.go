// mapper tier's engine: github.com/odvcencio/
// gotreesitter v0.49.0, a pure-Go tree-sitter runtime (no CGO). The mapper
// itself is not built yet; this file
// exists only to make the engine a real dependency — go mod tidy drops a
// requirement no package imports — and to declare the four-grammar subset
// that the ci.yml / release.yml build tags select.
//
// The subset tags are opt-in inside the engine (//go:build !grammar_subset ||
// grammar_subset_X), so an untagged build embeds all 206 grammars (fat, never
// broken) and a build carrying the four grammar_subset_* tags embeds only the
// shipped four. tsx and javascript are separate languages from typescript:
// grammar_subset_typescript does not cover .tsx, and each is a distinct
// *gotreesitter.Language.
package graph

import "github.com/odvcencio/gotreesitter/grammars"

// detectLanguage pins the engine's per-file language detection — the mapper's
// entry point on a blank identifier so the import
// survives go mod tidy until the mapper calls it for real.
var _ func(filename string) *grammars.LangEntry = grammars.DetectLanguage
