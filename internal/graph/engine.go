// mapper tier's engine: github.com/odvcencio/
// gotreesitter v0.49.0, a pure-Go tree-sitter runtime (no CGO). Discovery
// (mapper.go) and conversion (mapper_symbols.go) are built on top of it:
// mapper.go's mapperFiles is the real caller of grammars.DetectLanguage now,
// which is why the blank-identifier pin this file used to carry (to survive
// `go mod tidy` before the mapper had any caller) is gone — it would just be
// dead weight next to a real one. Neither discovery nor conversion is wired
// into buildIndex/writeGraphDB yet: their output stays unconsumed by this
// slice, and both have test-only callers today.
//
// The subset tags are opt-in inside the engine (//go:build !grammar_subset ||
// grammar_subset_X), so an untagged build embeds all 206 grammars (fat, never
// broken) and a build carrying the four grammar_subset_* tags embeds only the
// shipped four. tsx and javascript are separate languages from typescript:
// grammar_subset_typescript does not cover .tsx, and each is a distinct
// *gotreesitter.Language.
package graph
