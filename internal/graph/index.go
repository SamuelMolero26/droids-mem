package graph

import (
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

const (
	maxSourceBytes = 8 << 10 // per-symbol stored body cap
	maxDocBytes    = 600
	maxSigBytes    = 300
)

type symRow struct {
	id        int64
	qname     string
	name      string
	kind      string
	pkg       string
	file      string
	line      int
	exported  bool
	signature string
	doc       string
	source    string
}

// usableGoPackages normalizes packages.Load's possible outcomes on a Go-free
// tree into a single "no usable Go packages" nil result, so buildIndex never
// needs to branch on which of the shapes it got.
//
// Empirically observed (TestPackagesLoad_GoFreeTree_ObservedShape): a tree
// with no go.mod and no .go file yields err == nil and exactly one SYNTHETIC
// package (PkgPath "./...", zero GoFiles/CompiledGoFiles, one ListError-kind
// entry in Errors); a tree with a go.mod but zero .go files yields err == nil
// and an EMPTY slice. Per design D2, a genuine load error (e.g. a missing Go
// toolchain) folds into the same "unusable" outcome — the mapper tier can
// still index the tree even when the Go tier cannot load at all.
//
// A package is dropped as "no Go files"-class only when it carries NO
// GoFiles/CompiledGoFiles at all; a package that has real source files but
// also errors (a genuine type/parse failure) is kept and flows into the
// existing `broken` accounting below, unchanged from pre-PR-B behavior.
func usableGoPackages(pkgs []*packages.Package, err error) []*packages.Package {
	if err != nil {
		return nil
	}
	out := make([]*packages.Package, 0, len(pkgs))
	for _, p := range pkgs {
		if len(p.GoFiles) == 0 && len(p.CompiledGoFiles) == 0 && len(p.Errors) > 0 {
			continue // synthetic "no Go files"-class package: nothing to index
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mapperQNameCollisions counts colliding mapper-tier qnames observed across
// all builds (T4 part 1, design D-note "byQName Python collision"). Visibility
// only in this PR: PR-E's carried-edge remap is what actually consumes a
// per-build collided set (returned by buildByQName) to decide whether to
// drop a remap rather than point it at a possibly-wrong last-wins row.
var mapperQNameCollisions atomic.Uint64

// buildIndex loads, type-checks, and analyzes the repo, then atomically
// replaces dbPath with a fresh graph (build to .tmp, rename over). A repo
// whose Go tier does not type-check (majority-broken) returns an error and
// leaves any existing graph intact — UNLESS mapper symbols are available to
// compensate, in which case only the Go tier is suppressed (see the
// majority-broken cap below, C.11). A repo with neither a usable Go package
// nor a single mapper-tier symbol is a hard error (C.10) — PR-B deliberately
// left this open (always non-erroring) because mapper discovery wasn't wired
// in yet; see PR-B apply-progress "B.4 Deviation".
//
// PR-C process note (task C.8): mapper symbols now land in graph.db, but
// this PR must not merge to develop standalone — it under-reports every
// mapper caller until PR-D wires FactCalls-derived call edges on top of the
// symbols this PR produces. Definitions and calls land together.
func buildIndex(ctx context.Context, repo, dbPath, stampVal string) error {
	buildStarts.Add(1)
	cfg := &packages.Config{
		Context: ctx,
		Dir:     repo,
		Env:     goToolEnv(),
		Tests:   true, // index _test.go declarations as symbols/callers (see dedupeVariants)
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
	}
	rawPkgs, loadErr := packages.Load(cfg, "./...")
	// pkgs is the funneled, "usable" view: nil on any of the three Go-free
	// shapes (load error, empty slice, synthetic "no Go files" package —
	// see usableGoPackages). Everything below that used to index the raw
	// packages.Load result unconditionally now guards on len(pkgs) instead,
	// since pkgs[0] is no longer safe to deref unconditionally.
	pkgs := usableGoPackages(rawPkgs, loadErr)

	// broken is the raw (not deduped) subset with type errors, kept
	// un-deduped because callEdges' SSA walk needs to know per-variant
	// (plain vs. in-package test) whether a package failed to type-check.
	// dedupeVariants below is what symbol rows are emitted for — broken or
	// clean, because symbols are AST-derived and a body-local type error
	// never invalidates the AST. Ranging over pkgs is safe when pkgs is nil
	// (zero iterations), so broken is naturally empty on a Go-free tree.
	var broken []*packages.Package
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			broken = append(broken, p)
		}
	}

	// Mapper-tier discovery + conversion is independent of the Go tier's
	// health — it never inspects go/packages output at all — so it runs
	// unconditionally and early, before both the both-tiers-empty check
	// (C.10) and the majority-broken cap (C.11) decide anything. Best-effort:
	// a mapperFiles walk failure (root WalkDir error only) folds to zero
	// mapper symbols rather than failing the whole build — the Go tier can
	// still stand on its own.
	var mapperFileList []mapperFile
	var mapperSyms []mapperSym
	if mFiles, _, mErr := mapperFiles(repo); mErr == nil {
		mapperFileList = mFiles
		mapperSyms, _ = mapperSymbols(mFiles)
	}

	// C.10: a repo with neither a usable Go package nor a single mapper-tier
	// symbol has nothing to build from at all.
	if len(pkgs) == 0 && len(mapperSyms) == 0 {
		return fmt.Errorf("repo has no indexable Go packages or mapper-tier files: %s", repo)
	}

	// Greater-than-50%-broken safety cap: past this point the fresh Go
	// symbols and edges would be built mostly from stubs, so the previous
	// whole graph is a better answer than a fresh one that is mostly
	// carried-forward guesswork. Checked before any Go symbol/SSA work — no
	// point paying for it on a doomed build. Computed over the funneled
	// pkgs, not the raw packages.Load result: a Go-free tree's single
	// synthetic "no Go files" package would otherwise count as 100% broken
	// and re-trigger this exact hard error on every Go-free tree, undoing
	// the tolerance usableGoPackages exists to provide. On an empty pkgs,
	// broken is also empty, so this never fires (0*2 > 0 is false) — no
	// extra guard needed.
	//
	// C.11: when mapper symbols ARE available, the cap suppresses the Go
	// tier only instead of aborting the whole build — mapper content has
	// nothing to do with Go's brokenness, and discarding it too would be a
	// needless regression. Suppression reuses the exact len(pkgs) > 0 guards
	// the Go-free path (D2) already relies on, by zeroing pkgs/broken here.
	// The previous graph's Go symbols/edges are NOT preserved in this path —
	// a real, disclosed trade-off (meta.empty_reason below), not silent data
	// loss. A Go-only repo with no mapper content to compensate keeps the
	// exact pre-C.11 hard-error behavior: this cap's existing protection is
	// not weakened where it already applied.
	goSuppressed := false
	if len(broken)*2 > len(pkgs) {
		if len(mapperSyms) == 0 {
			return fmt.Errorf("repo does not type-check: %d of %d packages broken (majority), serving previous graph", len(broken), len(pkgs))
		}
		goSuppressed = true
		pkgs = nil
		broken = nil
	}

	module := ""
	var fset *token.FileSet
	if len(pkgs) > 0 {
		if pkgs[0].Module != nil {
			module = pkgs[0].Module.Path
		}
		fset = pkgs[0].Fset
	}

	files := map[string][]byte{}
	readFile := func(name string) []byte {
		if b, ok := files[name]; ok {
			return b
		}
		b, err := os.ReadFile(name) // #nosec G304 -- source files of the repo being indexed
		if err != nil {
			b = nil
		}
		files[name] = b
		return b
	}

	// symbols/byPos start from the Go tier (empty when pkgs is nil, e.g. a
	// Go-free tree or a suppressed majority-broken build). Mapper rows are
	// appended BEFORE positional ID assignment (C.2) — symbol IDs are
	// positional, so landing them after that loop would collide with id 0.
	symbols, byPos := goSymbols(pkgs, module, fset, readFile, repo)
	for _, ms := range mapperSyms {
		symbols = append(symbols, ms.row)
	}
	for i, s := range symbols {
		s.id = int64(i + 1)
	}

	// byQName is built from ALL symbols (Go + mapper) unconditionally, not
	// just when broken packages exist — mapper qnames can collide with each
	// other regardless of the Go tier's health (T4 part 1), and PR-E's
	// carried-edge remap needs the collision set from every build, not only
	// ones with a broken Go package.
	byQName, collidedQNames := buildByQName(symbols)
	mapperQNameCollisions.Add(uint64(len(collidedQNames)))

	// callEdges/implementsEdges both deref pkgs[0] (ssa.NewProgram's Fset,
	// implementsEdges' Fset), so they are gated behind len(pkgs) > 0 rather
	// than guarded internally — buildIndex is their only caller.
	edges := edgeSet{}
	var impls map[[2]int64]bool
	if len(pkgs) > 0 {
		var err error
		edges, err = callEdges(pkgs, broken, byPos)
		if err != nil {
			return err
		}
		impls = implementsEdges(pkgs, byPos)
	}

	// Mapper-tier call edges (PR-D): every mapper symbol's row.id is now
	// final (set by the positional-ID loop above), so mapperEdges can build
	// real (caller, callee) pairs. Merged via .add, never assigned outright —
	// callEdges above may have already populated edges for the Go tier, and
	// the two tiers' id spaces are disjoint by construction (D.7) so merging
	// can never collide. fanoutCapped counts CALLSITES whose rung-5 candidate
	// set exceeded fanoutCap (design D5/D6) — a build-level partiality fact,
	// not a per-edge one, persisted below as meta.fanout_capped.
	mapperEdgeSet, fanoutCapped := mapperEdges(mapperFileList, mapperSyms)
	for k, m := range mapperEdgeSet {
		edges.add(k, m)
	}

	// Empty-graph / suppression hint: meta.empty_reason distinguishes a
	// build failure from two legitimate non-failure outcomes that would
	// otherwise ship a symbol-less or Go-less graph silently.
	//   - "go_tier_suppressed_majority_broken" (C.11): mapper symbols exist,
	//     but the Go tier was dropped by the majority-broken cap above.
	//   - "no_indexable_symbols": a successful build that still indexed zero
	//     symbols — e.g. a real Go package with no top-level declarations and
	//     no mapper content (the only way this remains reachable after C.10).
	emptyReason := ""
	switch {
	case goSuppressed:
		emptyReason = "go_tier_suppressed_majority_broken"
	case len(symbols) == 0:
		emptyReason = "no_indexable_symbols"
	}

	// Carry-forward: a broken package's own functions have no SSA body, so
	// callEdges can never freshly discover an edge whose CALLER is in that
	// package (only its in-edges from clean callers survive, via the stub).
	// dbPath still holds the previous build's graph.db at this point —
	// writeGraphDB below is what replaces it. Best-effort: carriedEdges
	// itself collapses any failure to zero edges, never an error here.
	//
	// carriedUnits is recorded unconditionally for every broken package, not
	// just the ones carriedEdges actually recovered an edge for — its
	// semantics are "this unit's edges are not freshly analyzed" (design.md),
	// which holds even when carry-forward finds nothing to carry (e.g. the
	// first-ever build on a broken tree).
	var carriedUnits []string
	if len(broken) > 0 {
		brokenPkgNames := make(map[string]bool, len(broken))
		for _, p := range broken {
			brokenPkgNames[shortPkgPath(p.PkgPath, module)] = true
		}
		carriedUnits = slices.Sorted(maps.Keys(brokenPkgNames))

		for k, m := range carriedEdges(dbPath, brokenPkgNames, byQName) {
			edges.add(k, m)
		}
	}

	return writeGraphDB(ctx, dbPath, repo, module, stampVal, symbols, edges, impls, carriedUnits, emptyReason, fanoutCapped)
}

// goSymbols extracts symbol rows from the type-checked Go packages,
// populating byPos (declaration-position → row, used later to match SSA
// functions back to rows) as a side effect. This is the ONLY place that ever
// writes to byPos: mapperSymbols has no byPos parameter and structurally
// cannot reach it, which is what keeps the mapper tier out of Go's
// SSA-matching map by construction (tier disjointness, C.7). Safe when pkgs
// is nil (zero iterations), matching the Go-free/suppressed-Go-tier paths.
func goSymbols(pkgs []*packages.Package, module string, fset *token.FileSet, readFile func(string) []byte, repo string) ([]*symRow, map[string]*symRow) {
	var symbols []*symRow
	byPos := map[string]*symRow{}
	for _, p := range dedupeVariants(pkgs) {
		shortPkg := shortPkgPath(p.PkgPath, module)
		for _, f := range p.Syntax {
			for _, decl := range f.Decls {
				symbols = appendDeclSymbols(symbols, byPos, fset, readFile, decl, shortPkg, repo)
			}
		}
	}
	return symbols, byPos
}

// buildByQName builds the qname→id lookup carriedEdges' remap uses, and
// records every qname collision it observes along the way — never silently
// last-wins with no trace (T4 part 1). Mapper-tier qnames are the realistic
// source: Python's a/b/__init__.py and a/b.py both synthesize modulePath
// "a.b" (mapper.go's modulePath), so their symbols' qnames can collide.
// PR-E's carried-edge remap is what actually consumes the returned collided
// set (dropping rather than remapping onto a possibly-wrong row) — this PR
// only makes the collision visible.
func buildByQName(symbols []*symRow) (map[string]int64, map[string]bool) {
	byQName := make(map[string]int64, len(symbols))
	var collided map[string]bool
	for _, s := range symbols {
		if _, exists := byQName[s.qname]; exists {
			if collided == nil {
				collided = map[string]bool{}
			}
			collided[s.qname] = true
		}
		byQName[s.qname] = s.id
	}
	return byQName, collided
}

// dedupeVariants collapses packages.Load(Tests:true)'s multiple variants per
// PkgPath into exactly one, for symbol-row emission only. Tests:true makes
// packages.Load return up to four variants per tested package; the
// in-package test variant (its p.ID contains " [") re-parses the SAME
// production files as the plain variant under the SAME PkgPath. Emitting rows
// for every variant would duplicate every production symbol — symbols.qname
// has no UNIQUE constraint, so the duplicate insert succeeds silently and
// findSymbol then reports every symbol in the package as ambiguous. The
// in-package test variant is preferred when present because its p.Syntax
// additionally covers the package's _test.go declarations; the synthesized
// "<pkg>.test" main (external test binary) carries no symbols worth indexing
// and is skipped outright. SSA is still built from the full, un-deduped pkgs
// slice (see callEdges) — both variants' functions share identical
// file:line positions, so their edges collapse onto the one retained row and
// no edge is lost by deduping here.
func dedupeVariants(pkgs []*packages.Package) []*packages.Package {
	byPath := map[string]*packages.Package{}
	var order []string
	for _, p := range pkgs {
		if strings.HasSuffix(p.PkgPath, ".test") {
			continue // synthesized test-binary main, not repo source
		}
		existing, ok := byPath[p.PkgPath]
		if !ok {
			byPath[p.PkgPath] = p
			order = append(order, p.PkgPath)
			continue
		}
		if strings.Contains(p.ID, " [") && !strings.Contains(existing.ID, " [") {
			byPath[p.PkgPath] = p // prefer the in-package test variant
		}
	}
	out := make([]*packages.Package, 0, len(order))
	for _, path := range order {
		out = append(out, byPath[path])
	}
	return out
}

func shortPkgPath(pkgPath, module string) string {
	if module != "" && pkgPath == module {
		return filepath.Base(module)
	}
	if module != "" {
		if rel, ok := strings.CutPrefix(pkgPath, module+"/"); ok {
			return rel
		}
	}
	return pkgPath
}

// appendDeclSymbols extracts symbol rows from one top-level declaration.
func appendDeclSymbols(out []*symRow, byPos map[string]*symRow, fset *token.FileSet,
	readFile func(string) []byte, decl ast.Decl, pkg, repo string) []*symRow {

	slice := func(from, to token.Pos) string {
		pf, pt := fset.Position(from), fset.Position(to)
		src := readFile(pf.Filename)
		if src == nil || pf.Offset < 0 || pt.Offset > len(src) || pf.Offset >= pt.Offset {
			return ""
		}
		return string(src[pf.Offset:pt.Offset])
	}
	relFile := func(pos token.Pos) (string, int, string) {
		p := fset.Position(pos)
		rel, err := filepath.Rel(repo, p.Filename)
		if err != nil {
			rel = p.Filename
		}
		return rel, p.Line, p.Filename
	}

	add := func(namePos token.Pos, name, kind, sig, doc, source string) *symRow {
		file, line, _ := relFile(namePos)
		row := &symRow{
			qname:     pkg + "." + name,
			name:      name,
			kind:      kind,
			pkg:       pkg,
			file:      file,
			line:      line,
			exported:  ast.IsExported(lastDot(name)),
			signature: truncate(sig, maxSigBytes),
			doc:       truncate(strings.TrimSpace(doc), maxDocBytes),
			source:    truncate(source, maxSourceBytes),
		}
		return row
	}

	switch d := decl.(type) {
	case *ast.FuncDecl:
		name := d.Name.Name
		if recv := recvTypeName(d.Recv); recv != "" {
			name = recv + "." + d.Name.Name
		}
		sigEnd := d.End()
		if d.Body != nil {
			sigEnd = d.Body.Lbrace
		}
		sig := collapseWS(slice(d.Pos(), sigEnd))
		doc := ""
		if d.Doc != nil {
			doc = d.Doc.Text()
		}
		kind := "func"
		if d.Recv != nil {
			kind = "method"
		}
		row := add(d.Name.Pos(), name, kind, sig, doc, slice(d.Pos(), d.End()))
		out = append(out, row)
		// SSA functions are matched by declaration position; register both the
		// name identifier's line and the func keyword's line (same in practice,
		// cheap insurance if they differ).
		for _, pos := range []token.Pos{d.Name.Pos(), d.Pos()} {
			p := fset.Position(pos)
			byPos[fmt.Sprintf("%s:%d", p.Filename, p.Line)] = row
		}

	case *ast.GenDecl:
		for _, spec := range d.Specs {
			doc := ""
			if d.Doc != nil {
				doc = d.Doc.Text()
			}
			switch sp := spec.(type) {
			case *ast.TypeSpec:
				if sp.Doc != nil {
					doc = sp.Doc.Text()
				}
				src := slice(sp.Pos(), sp.End())
				kind := "type"
				if _, isIface := sp.Type.(*ast.InterfaceType); isIface {
					kind = "interface" // issue #48: interface vs concrete drives implements
				}
				row := add(sp.Name.Pos(), sp.Name.Name, kind,
					firstLine("type "+collapseWS(slice(sp.Pos(), sp.Type.Pos()))+"…"), doc, src)
				out = append(out, row)
				// Register the type's position so implementsEdges can map a
				// types.Object back to this row (same file:line key SSA uses).
				tp := fset.Position(sp.Name.Pos())
				byPos[fmt.Sprintf("%s:%d", tp.Filename, tp.Line)] = row
			case *ast.ValueSpec:
				if sp.Doc != nil {
					doc = sp.Doc.Text()
				}
				kind := "var"
				if d.Tok == token.CONST {
					kind = "const"
				}
				src := slice(sp.Pos(), sp.End())
				for _, n := range sp.Names {
					if n.Name == "_" {
						continue
					}
					out = append(out, add(n.Pos(), n.Name, kind, firstLine(kind+" "+collapseWS(src)), doc, src))
				}
			}
		}
	}
	return out
}

// edgeMeta carries the two independent facts stored per edge: dispatch
// ("static"/"interface", Go-only — "" for a mapper-tier edge, which has no
// dispatch concept) and precision ("resolved"/"syntactic").
type edgeMeta struct{ dispatch, precision string }

// edgeSet maps a (caller id, callee id) pair to its edgeMeta. Both fields
// merge independently via add: "static" wins a dispatch collision and
// "resolved" wins a precision collision — each is the stronger reachability
// claim for its axis. An empty incoming field never overwrites an
// already-set field, so a weaker/unset observation can never erase a
// stronger one already recorded for the same pair.
type edgeSet map[[2]int64]edgeMeta

// add merges m into the entry for k, field by field. A field wins over its
// counterpart per the edgeSet doc comment; an empty incoming field is a
// no-op for that field, never a clobber.
func (es edgeSet) add(k [2]int64, m edgeMeta) {
	existing, ok := es[k]
	if !ok {
		es[k] = m
		return
	}
	if existing.dispatch != "static" && m.dispatch != "" {
		existing.dispatch = m.dispatch
	}
	if existing.precision != "resolved" && m.precision != "" {
		existing.precision = m.precision
	}
	es[k] = existing
}

// callEdges builds SSA and a CHA call graph, then maps functions back to
// symbol rows by declaration position. CHA over-approximates interface
// dispatch — the safe direction for "what breaks if I change X" — and needs
// no main-function roots, so library repos index fully (RTA would not).
//
// SSA package creation is a manual packages.Visit walk, NOT
// ssautil.AllPackages: AllPackages filters on p.IllTyped, which is
// transitive, so a single body-local type error anywhere in the tree
// collapses the whole call graph (measured: ~20% of edges survive). Instead
// every package in the transitive import graph gets an ssa.Package —
// type-checked packages get full syntax, packages in broken get a
// types-only stub (prog.CreatePackage(p.Types, nil, nil, true)): its
// types.Package is complete and walkable even though it has no function
// bodies, so callers of its exported symbols still resolve.
//
// cg.DeleteSyntheticNodes() is deliberately NOT called: it deletes every
// node with fn.Syntax() == nil, which includes every function in a broken
// package's stub — splicing in→out through a deleted node drops every
// in-edge into that package too. Clean-tree parity with the old
// DeleteSyntheticNodes behavior is pinned by
// TestCallEdges_MatchesCleanGoldenEdgeSet.
func callEdges(pkgs []*packages.Package, broken []*packages.Package, byPos map[string]*symRow) (edgeSet, error) {
	brokenSet := make(map[*packages.Package]bool, len(broken))
	for _, p := range broken {
		brokenSet[p] = true
	}

	prog := ssa.NewProgram(pkgs[0].Fset, ssa.InstantiateGenerics)
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types == nil {
			return // unresolved dependency: nothing to build an ssa.Package from
		}
		if brokenSet[p] {
			prog.CreatePackage(p.Types, nil, nil, true) // types-only stub, no bodies
		} else {
			prog.CreatePackage(p.Types, p.Syntax, p.TypesInfo, true)
		}
	})

	prog.Build()
	cg := cha.CallGraph(prog)

	resolve := func(fn *ssa.Function) (*symRow, bool) {
		for fn.Parent() != nil { // attribute closures to their enclosing decl
			fn = fn.Parent()
		}
		if orig := fn.Origin(); orig != nil {
			fn = orig // generic instantiations share the origin's syntax
		}
		if !fn.Pos().IsValid() {
			return nil, false
		}
		p := prog.Fset.Position(fn.Pos())
		row, ok := byPos[fmt.Sprintf("%s:%d", p.Filename, p.Line)]
		return row, ok
	}

	edges := edgeSet{}
	err := callgraph.GraphVisitEdges(cg, func(e *callgraph.Edge) error {
		// Drop edges whose RAW callee is a closure. CHA resolves a dynamic
		// func()-typed call (defer cancel()) to every func()-shaped function in
		// the program, so a function's own deferred closure collects in-edges
		// from unrelated code, and resolve()'s Parent() collapse would bill them
		// to the enclosing declaration (issue #69). The check must run BEFORE
		// resolve(), which makes a closure indistinguishable from its enclosing
		// decl. A closure that escapes and is genuinely invoked elsewhere loses
		// that in-edge too — accepted, since crediting the call to the enclosing
		// declaration was never correct either. Caller-side collapse is left
		// untouched: a call made FROM inside a closure still counts.
		if e.Callee.Func.Parent() != nil {
			return nil
		}
		caller, ok := resolve(e.Caller.Func)
		if !ok {
			return nil
		}
		callee, ok := resolve(e.Callee.Func)
		if !ok || caller == callee {
			return nil
		}
		dispatch := "static"
		if e.Site != nil && e.Site.Common().IsInvoke() {
			dispatch = "interface"
		}
		key := [2]int64{caller.id, callee.id}
		edges.add(key, edgeMeta{dispatch: dispatch, precision: "resolved"})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk call graph: %w", err)
	}
	return edges, nil
}

// implementsEdges computes exact interface-satisfaction relations (issue #48):
// for every repo-local interface, the concrete repo types whose method set
// satisfies it. Uses types.Implements over the POINTER method set, so a type
// that satisfies an interface only through pointer-receiver methods (the common
// Go idiom) is still captured. Zero-method interfaces (any, interface{}) are
// skipped — every type satisfies them, so the edge is noise, not signal. Both
// endpoints map back to symbol rows by declaration position via byPos (which
// appendDeclSymbols now registers for types too); an endpoint that does not map
// (a dep type reachable through NeedDeps) is dropped, keeping the graph
// repo-local — the same boundary callEdges draws at a dependency.
func implementsEdges(pkgs []*packages.Package, byPos map[string]*symRow) map[[2]int64]bool {
	fset := pkgs[0].Fset
	resolve := func(obj types.Object) (*symRow, bool) {
		if obj == nil || !obj.Pos().IsValid() {
			return nil, false
		}
		p := fset.Position(obj.Pos())
		row, ok := byPos[fmt.Sprintf("%s:%d", p.Filename, p.Line)]
		return row, ok
	}

	type namedType struct {
		row *symRow
		typ *types.Named
	}
	var ifaces, concretes []namedType
	for _, p := range pkgs {
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			if named.TypeParams().Len() > 0 {
				continue // generic type: constraint satisfaction is out of v1 scope (#48)
			}
			row, ok := resolve(tn)
			if !ok {
				continue // not a repo-local symbol (or unmappable position)
			}
			if iface, ok := named.Underlying().(*types.Interface); ok {
				if iface.NumMethods() == 0 {
					continue // any/interface{}: universal satisfaction = noise
				}
				ifaces = append(ifaces, namedType{row, named})
			} else {
				concretes = append(concretes, namedType{row, named})
			}
		}
	}

	edges := map[[2]int64]bool{}
	for _, iface := range ifaces {
		it, _ := iface.typ.Underlying().(*types.Interface)
		for _, c := range concretes {
			// Pointer method set is the superset (value + pointer receivers), so
			// this catches types satisfying the interface only via *T methods.
			if types.Implements(types.NewPointer(c.typ), it) {
				edges[[2]int64{iface.row.id, c.row.id}] = true
			}
		}
	}
	return edges
}

// writeGraphDB builds the new db at dbPath+".tmp" and renames it into place,
// so readers never observe a half-built graph. edges carries an edgeMeta
// (dispatch + precision) per pair (callEdges/carriedEdges compute it),
// persisted per-edge in the edges.dispatch and edges.precision columns.
// carriedUnits lists the short package names whose edges are not freshly
// analyzed this build (broken packages, carried forward from the previous
// graph.db where possible) — stored wholesale as meta.carried_units,
// newline-joined (package names never contain a newline, so no escaping is
// needed). emptyReason is "no_indexable_symbols" when the build indexed zero
// symbols (a healthy, not a failed, outcome), or "" otherwise — stored as
// meta.empty_reason. fanoutCapped is the count of mapper-tier CALLSITES
// whose rung-5 candidate set exceeded fanoutCap (design D5/D6) — stored as
// meta.fanout_capped, always written (even "0") so its absence never has to
// be interpreted as "unknown" vs "none".
func writeGraphDB(ctx context.Context, dbPath, repo, module, stampVal string, symbols []*symRow, edges edgeSet, impls map[[2]int64]bool, carriedUnits []string, emptyReason string, fanoutCapped int) error {
	if err := ctx.Err(); err != nil {
		return err // cancelled before work started
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return fmt.Errorf("create graph dir: %w", err)
	}
	removeStaleTemps(dbPath)
	// Per-build temp name: async rebuilds run concurrently in the same process
	// (ensureFresh releases repoLock before launching the goroutine, and a
	// superseding build starts before the old one exits), so pid alone is not
	// unique. pid keeps cross-process builds distinct; the nonce keeps in-process
	// ones distinct. The rename is atomic — last writer wins.
	tmp := fmt.Sprintf("%s.tmp.%d.%d", dbPath, os.Getpid(), buildNonce.Add(1))
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", "file:"+tmp+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("create graph db: %w", err)
	}
	err = func() error {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("apply graph schema: %w", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		symIns, err := tx.PrepareContext(ctx, `INSERT INTO symbols
			(id, qname, name, kind, package, file, line, exported, signature, doc, source)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer symIns.Close()
		for _, s := range symbols {
			if _, err := symIns.ExecContext(ctx, s.id, s.qname, s.name, s.kind, s.pkg, s.file, s.line,
				s.exported, s.signature, s.doc, s.source); err != nil {
				return fmt.Errorf("insert symbol %s: %w", s.qname, err)
			}
		}
		edgeIns, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO edges (caller, callee, dispatch, precision) VALUES (?,?,?,?)`)
		if err != nil {
			return err
		}
		defer edgeIns.Close()
		for e, m := range edges {
			if _, err := edgeIns.ExecContext(ctx, e[0], e[1], m.dispatch, m.precision); err != nil {
				return err
			}
		}
		implIns, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO implements (iface, impl, precision) VALUES (?,?,?)`)
		if err != nil {
			return err
		}
		defer implIns.Close()
		for e := range impls {
			if _, err := implIns.ExecContext(ctx, e[0], e[1], "resolved"); err != nil {
				return err
			}
		}
		// FTS mirror for the search fallback; rowid == symbols.id for the join back.
		if _, err := tx.ExecContext(ctx, `INSERT INTO symbols_fts(rowid, qname, name, doc, signature)
			SELECT id, qname, name, doc, signature FROM symbols`); err != nil {
			return fmt.Errorf("populate symbols_fts: %w", err)
		}
		for k, v := range map[string]string{
			"stamp": stampVal, "repo": repo, "module": module, "indexed_at": nowUTC(),
			"carried_units": strings.Join(carriedUnits, "\n"),
			"empty_reason":  emptyReason,
			"fanout_capped": strconv.Itoa(fanoutCapped),
		} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?,?)`, k, v); err != nil {
				return err
			}
		}
		return tx.Commit()
	}()
	cerr := db.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close graph db: %w", cerr)
	}
	// A superseded async build was cancelled (ensureFresh: bs.cancel()); its
	// result is stale, so discard it rather than renaming it over the graph.
	if err := ctx.Err(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Owner-only before publishing. SQLite creates the file at the umask
	// default (typically 0644), and this one holds up to maxSourceBytes of
	// verbatim source per symbol for every repo indexed. internal/db applies
	// the same tightening to mem.db, on the reasoning that the 0700 state dir
	// shields the files today but the files themselves hold unencrypted
	// content — that argument is at least as strong here. Chmod before the
	// rename so the published path is never world-readable.
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tighten graph db perms: %w", err)
	}
	return os.Rename(tmp, dbPath)
}

// buildNonce makes concurrent same-process graph builds write distinct temp
// files. See writeGraphDB.
var buildNonce atomic.Uint64

// buildStarts counts buildIndex entries. Tests assert that an already-failed
// stamp does not relaunch; buildNonce cannot serve that role because it is only
// taken in writeGraphDB, which a type-check failure returns long before.
var buildStarts atomic.Uint64

// staleTempAge bounds how long a graph build may plausibly run. buildIndex
// measures ~2.5s; an hour is far past any real build, so anything older is
// orphaned litter from a builder that was SIGKILLed/crashed before its rename.
// ponytail: an age guard, not a lockfile — good enough for a local tool, and it
// never touches a concurrently-live sibling's in-progress temp (that one is young).
const staleTempAge = time.Hour

// removeStaleTemps deletes leftover <db>.tmp.<pid> files from builders that died
// before renaming. Age-guarded so a live build on another pid is never removed;
// best-effort, so any glob/remove error is ignored (the build proceeds regardless).
func removeStaleTemps(dbPath string) {
	matches, err := filepath.Glob(dbPath + ".tmp.*")
	if err != nil {
		return
	}
	for _, p := range matches {
		if info, err := os.Stat(p); err == nil && time.Since(info.ModTime()) > staleTempAge {
			_ = os.Remove(p)
		}
	}
}

// ---------- small text helpers ----------

func recvTypeName(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	t := fl.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstLine(s string) string {
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return before
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}

func lastDot(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}
