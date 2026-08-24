// Mapper-tier conversion: mapperFile (discovery's decision) → *symRow
// (production rows), by parsing and projecting an outline with gotreesitter.
// Nothing here writes to graph.db and nothing here reuses the Go semantic
// tier's byPos lookup (design.md decision 8) — this file builds its own
// per-file structures and takes no Go-pipeline argument.
package graph

import (
	"os"
	"strings"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// mapperEngine bundles the per-language state needed to parse and outline a
// mapper-tier file. gts.Parser is deliberately NOT part of it: v0.49.0
// documents Parser as not safe for concurrent use, and it carries
// reuse/incremental state bound to a prior document. Query.Exec is
// documented concurrency-safe and OutlineTree is documented read-only, so
// reusing one Outliner per language across the whole run is sound.
type mapperEngine struct {
	lang *gts.Language
	// parsers replaces the per-file gts.NewParser(lang) every pass used to
	// do. A Parser builds its parse tables (buildSmallTokenLookup and
	// friends) lazily and caches them ON THE PARSER, not on the shared
	// Language — so constructing one per file rebuilt every table per file.
	// Profiling a 200-file TS tree put gts.NewParser at 50.6% of all bytes
	// allocated in a mapper build, ~6.4 MB per file to parse ~500 bytes of
	// source.
	//
	// gts.ParserPool is the library's own answer, and it addresses both
	// halves of the reason the per-file construction existed: it is
	// documented concurrency-safe, and it resets mutable parser state on
	// checkout so no reuse/incremental state bleeds from the previous file.
	parsers  *gts.ParserPool
	outliner *gts.Outliner
	// calls is the compiled FactCalls extractor for lang, added here rather
	// than as a parallel cache (design D3): FactProgram.Extract rejects a
	// tree parsed with any OTHER *gts.Language by pointer identity
	// (fact_program.go:124), so co-locating it with the exact lang value
	// mapper_calls.go's parser used makes that match structural instead of
	// incidental. nil when compilation fails — mapper_calls.go treats that
	// as a per-file skip, the same policy outliner==nil already gets below.
	calls *gts.FactProgram
	// imports is the compiled module-specifier query for the JS family only
	// (mapper_imports.go's tsImportsQuery). Python needs no query — gts's own
	// ExtractImports covers it — so this stays nil there, and mapperImports
	// dispatches on language rather than on this field being set.
	imports *gts.Query
	// bindings is the companion local-name query (tsBindingsQuery), JS family
	// only for the same reason. Kept separate from imports rather than folded
	// into it: that query is corpus-validated for specifier recall, and adding
	// captures to its patterns would change what each match carries.
	bindings *gts.Query
}

// mapperEngines caches one mapperEngine per language NAME for the lifetime
// of a single mapperSymbols call — per-run, not package-level. Manager
// builds different repos concurrently, and a shared package-level cache
// would need locking for no gain (design.md decision 3).
type mapperEngines map[string]*mapperEngine

// get returns (creating and caching, if necessary) the mapperEngine for
// entry. A failure to load the language or compile its outliner is not an
// error here — it is surfaced by lang/outliner staying nil, and the caller
// (mapperSymbols) turns that into a per-file skip.
func (e mapperEngines) get(entry *grammars.LangEntry) *mapperEngine {
	if entry == nil {
		return &mapperEngine{}
	}
	if eng, ok := e[entry.Name]; ok {
		return eng
	}
	eng := &mapperEngine{}
	if lang := entry.Language(); lang != nil {
		eng.lang = lang
		eng.parsers = gts.NewParserPool(lang)
		if outliner, err := gts.NewOutliner(lang, grammars.ResolveTagsQuery(*entry)); err == nil {
			eng.outliner = outliner
		}
		if calls, err := gts.NewFactProgram(lang, gts.FactCalls); err == nil {
			eng.calls = calls
		}
		if jsFamilyLanguages[entry.Name] {
			if q, err := gts.NewQuery(tsImportsQuery, lang); err == nil {
				eng.imports = q
			}
			if q, err := gts.NewQuery(tsBindingsQuery, lang); err == nil {
				eng.bindings = q
			}
		}
	}
	e[entry.Name] = eng
	return eng
}

// mapperSym is the build-time carrier mapperSymbols returns: the persisted
// *symRow plus PR-D's call-attribution inputs (the outline symbol's byte
// range and its lexical container chain), which never persist to graph.db
// themselves (design.md decision 8 — keeps symRow clean, keeps the two tiers
// decoupled). Introduced in PR-C so PR-D's FactCalls attribution adds no
// signature churn to mapperSymbols.
type mapperSym struct {
	row        *symRow
	start, end uint32 // gts byte range, build-time only, NEVER persisted
	container  string // enclosing class chain, for PR-D's ladder rungs 1/2
}

// mapperSymbols parses and outlines every file in files, converting each
// accepted gts.OutlineSymbol into a production *symRow wrapped in a
// mapperSym carrier. Per-file failures (unreadable file, unloadable
// language, unparseable source, declined outline) are skip-and-continue: one
// bad file never loses the rest of the run, and every skip is counted in the
// returned mapperStats (design.md decision 4).
func mapperSymbols(files []mapperFile) ([]mapperSym, mapperStats) {
	var stats mapperStats
	engines := mapperEngines{}
	var out []mapperSym

	for _, f := range files {
		// #nosec G304 -- discovery admits only regular files under repo, size-capped
		// at maxMapperFileBytes; symlinks are dropped there so this read cannot
		// resolve outside the indexed repo.
		src, err := os.ReadFile(f.abs)
		if err != nil {
			stats.readErr++
			continue // unreadable file is skip-and-continue, not fatal
		}

		eng := engines.get(f.entry)
		if eng.lang == nil {
			stats.parseErr++ // no working language: the grammar failed to load, so no tree can ever be produced
			continue
		}

		out = append(out, outlineMapperFile(eng, f, src, &stats)...)
	}
	return out, stats
}

// outlineMapperFile is one file's parse-and-outline, extracted from
// mapperSymbols' loop so the tree has a SCOPE rather than a loop iteration.
// That is what makes `defer tree.Release()` correct here: it runs on every
// exit path, including the two mid-way declines, where a release placed at
// the bottom of the loop body would be skipped. A `defer` written directly
// inside the loop would instead queue every release until the whole pass
// returned, which returns no arena in time to be reused and so defeats the
// point entirely.
//
// Releasing is safe because nothing this returns is arena-backed:
// gts.OutlineSymbol is values throughout (Kind/Name/NodeType strings, Range/
// NameRange as byte offsets and points, Children recursively the same), its
// strings come from QueryCapture.Text which is a `string(source[a:b])` copy,
// and every symRow field is derived from src — the caller's own buffer —
// never from the tree. No *gts.Node escapes.
func outlineMapperFile(eng *mapperEngine, f mapperFile, src []byte, stats *mapperStats) []mapperSym {
	tree, err := eng.parsers.Parse(src) // pooled: see mapperEngine.parsers
	if err != nil {
		stats.parseErr++
		return nil // unparsable file is skip-and-continue, not fatal
	}
	defer tree.Release()
	return outlineMapperTree(eng, f, src, tree, stats)
}

// outlineMapperTree is the outline extraction itself, over a tree the CALLER
// owns and releases. scanMapperFiles (mapper_scan.go) drives it directly so a
// single parse feeds all four mapper passes; outlineMapperFile above stays the
// one-pass entry that parses its own tree, and is what the per-pass tests and
// benchmarks still exercise.
//
// The release-safety argument in outlineMapperFile's comment is what lets the
// tree outlive this call in the scan driver too: nothing returned here is
// arena-backed.
func outlineMapperTree(eng *mapperEngine, f mapperFile, src []byte, tree *gts.Tree, stats *mapperStats) []mapperSym {
	if eng.outliner == nil {
		stats.outlineDecline++
		return nil
	}
	syms, report := eng.outliner.OutlineTree(tree)
	if report.DeclineReason != "" {
		stats.outlineDecline++
		return nil
	}

	var out []mapperSym
	for _, s := range syms {
		out = append(out, buildMapperSymbols(s, "", f, src, tree, eng.lang, true, false)...)
	}
	return out
}

// buildMapperSymbols converts one gts.OutlineSymbol and its Children,
// flattened, into production *symRow values. container is the dot-joined
// lexical container chain built from OutlineSymbol.Children byte
// containment (empty at the top level); topLevel and parentExported drive
// TS/TSX/JS export inheritance (design.md decision 6).
func buildMapperSymbols(sym gts.OutlineSymbol, container string, f mapperFile, src []byte, tree *gts.Tree, lang *gts.Language, topLevel, parentExported bool) []mapperSym {
	exported := mapperExported(sym, f.entry.Name, tree, lang, topLevel, parentExported)

	qname := f.modulePath + ":"
	if container != "" {
		qname += container + "."
	}
	qname += sym.Name

	row := &symRow{
		qname:     qname,
		name:      sym.Name,
		kind:      normalizeOutlineKind(sym.Kind),
		pkg:       f.modulePath,
		file:      f.rel,
		line:      int(sym.NameRange.StartPoint.Row) + 1,
		exported:  exported,
		signature: truncate(collapseWS(firstLine(mapperSlice(src, sym.Range))), maxSigBytes),
		doc:       "", // always empty in this slice — see design.md decision 10
		source:    truncate(mapperSlice(src, sym.Range), maxSourceBytes),
	}
	out := []mapperSym{{row: row, start: sym.Range.StartByte, end: sym.Range.EndByte, container: container}}

	childContainer := sym.Name
	if container != "" {
		childContainer = container + "." + sym.Name
	}
	for _, c := range sym.Children {
		out = append(out, buildMapperSymbols(c, childContainer, f, src, tree, lang, false, exported)...)
	}
	return out
}

// mapperSlice returns the source bytes at r, or "" for an invalid/
// out-of-bounds range (defensive: a tags query bug should never panic).
func mapperSlice(src []byte, r gts.Range) string {
	// Compared in uint64, not int: on a 32-bit build int(uint32) above
	// MaxInt32 goes negative, the bound passes, and the slice panics.
	if r.StartByte > r.EndByte || uint64(r.EndByte) > uint64(len(src)) {
		return ""
	}
	return string(src[r.StartByte:r.EndByte])
}

// mapperExported implements the spec's per-language exported rule: Python
// uses the leading-underscore convention independently at every depth;
// TS/TSX/JS climbs the definition node's ancestors for an export_statement,
// but only for top-level symbols — a nested symbol inherits its container's
// flag instead (design.md decision 6).
func mapperExported(sym gts.OutlineSymbol, lang string, tree *gts.Tree, gtsLang *gts.Language, topLevel, parentExported bool) bool {
	if lang == "python" {
		return !strings.HasPrefix(sym.Name, "_")
	}
	if !topLevel {
		return parentExported
	}
	return mapperClimbsToExport(tree, gtsLang, sym.Range)
}

// mapperClimbsToExport climbs ancestors of the node at rng looking for an
// export_statement, stopping at the first lexical scope node so a symbol
// nested in an ordinary block can never inherit an unrelated top-level
// export elsewhere in the file.
func mapperClimbsToExport(tree *gts.Tree, lang *gts.Language, rng gts.Range) bool {
	root := tree.RootNode()
	if root == nil {
		return false
	}
	n := root.NamedDescendantForByteRange(rng.StartByte, rng.EndByte)
	for n != nil {
		n = n.Parent()
		if n == nil {
			return false
		}
		t := n.Type(lang)
		if t == "export_statement" {
			return true
		}
		if mapperIsScopeNode(t) {
			return false
		}
	}
	return false
}

// mapperIsScopeNode reports whether nodeType bounds a lexical scope
// (program root, or any *_block/*_body node) — the climb-stop boundary for
// mapperClimbsToExport.
func mapperIsScopeNode(nodeType string) bool {
	if nodeType == "program" {
		return true
	}
	return strings.HasSuffix(nodeType, "_block") || strings.HasSuffix(nodeType, "_body")
}

// normalizeOutlineKind maps gts.OutlineSymbol.Kind onto the Go tier's
// vocabulary for the three kinds that differ; every other kind (including
// ones the Go tier has no equivalent for) passes through unchanged.
func normalizeOutlineKind(kind string) string {
	switch kind {
	case "function":
		return "func"
	case "variable":
		return "var"
	case "constant":
		return "const"
	default:
		return kind
	}
}
