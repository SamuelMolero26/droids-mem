// Mapper-tier call collection, containment attribution, and the resolution
// ladder: FactCalls -> which symbol the call happens inside -> which
// symbol(s) it could target -> a syntactic edgeSet entry. Ported from
// spike/mapper/cmd/qname/main.go's resolve() (design D4), plus rung 2a
// (receiver names something imported into THIS FILE), which the spike never
// had — it needs binding names and specifier resolution, both of which live
// in this file alongside the rung that consumes them.
package graph

import (
	"os"
	"path"
	"sort"
	"strings"

	gts "github.com/odvcencio/gotreesitter"
)

// fanoutCap bounds rung 5's repo-wide fallback. Ported verbatim from the
// spike (main.go:30) — labeled, never silently sliced (design D5/D6): a
// truncated resolve() always reports the full pre-cap total alongside the
// capped hits, and the build-level callsite count that hit the cap is
// persisted as meta.fanout_capped.
const fanoutCap = 8

// mapperFileCalls is one file's FactCalls output, paired with just enough
// identity (repo-relative path, matching mapperSym.row.file; language name,
// for the ladder's rung-0 skip) to attribute and resolve each call.
type mapperFileCalls struct {
	file string
	lang string
	refs []gts.CallRef
}

// collectMapperCalls parses every file in files a second time — mapperSymbols
// (mapper_symbols.go) already parsed each once for outlining, but gts.Parser
// carries reuse state and is not safe to share, and a tree is not returned
// from that pass — and extracts FactCalls via each language's compiled
// FactProgram (mapperEngine.calls). Per-file failures are skip-and-continue,
// mirroring mapperSymbols' own policy exactly, including the identical
// #nosec justification: discovery already bounds files to regular, in-repo,
// size-capped entries.
func collectMapperCalls(files []mapperFile) ([]mapperFileCalls, mapperStats) {
	var stats mapperStats
	engines := mapperEngines{}
	var out []mapperFileCalls

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
			stats.parseErr++
			continue
		}

		if refs := callsFromMapperFile(eng, f, src, &stats); len(refs) > 0 {
			out = append(out, mapperFileCalls{file: f.rel, lang: f.entry.Name, refs: refs})
		}
	}
	return out, stats
}

// extractMapperCalls is one file's parse-and-extract, extracted from
// collectMapperCalls' loop for the same reason outlineMapperFile is (see its
// comment): it gives the tree a scope, so `defer tree.Release()` returns its
// borrowed arenas on every exit path without queueing behind the whole pass.
//
// Releasing is safe because gts.CallRef is a pure value struct — strings and
// uint32 byte offsets, no *gts.Node — so the returned slice holds nothing
// that points into the tree.
func extractMapperCalls(eng *mapperEngine, src []byte, stats *mapperStats) []gts.CallRef {
	tree, err := eng.parsers.Parse(src) // pooled: see mapperEngine.parsers
	if err != nil {
		stats.parseErr++
		return nil // unparsable file is skip-and-continue, not fatal
	}
	defer tree.Release()

	if eng.calls == nil {
		stats.outlineDecline++ // FactProgram failed to compile for this language
		return nil
	}
	return eng.calls.Extract(tree).Calls
}

var _ = extractMapperCalls // keep used: direct FactCalls testing even though collectMapperCalls now parses inline for JSX co-extraction

// callsFromMapperFile is one file's parse-and-extract, scoped so
// `defer tree.Release()` runs on every exit path — the same reason
// outlineMapperFile and importsFromMapperFile are shaped this way.
func callsFromMapperFile(eng *mapperEngine, f mapperFile, src []byte, stats *mapperStats) []gts.CallRef {
	tree, err := eng.parsers.Parse(src) // pooled: see mapperEngine.parsers
	if err != nil {
		stats.parseErr++
		return nil // unparsable file is skip-and-continue, not fatal
	}
	defer tree.Release()
	return callsFromMapperTree(eng, f, src, tree, stats)
}

// callsFromMapperTree is the call extraction itself, over a tree the CALLER
// owns and releases (see outlineMapperTree). Releasing while the returned
// refs live on is safe: gts.CallRef is a pure value struct whose Name and
// Receiver come from Node.Text, which is `string(source[a:b])` — a fresh copy
// off the caller's own buffer, never arena memory. No *gts.Node escapes.
func callsFromMapperTree(eng *mapperEngine, f mapperFile, src []byte, tree *gts.Tree, stats *mapperStats) []gts.CallRef {
	var refs []gts.CallRef
	if eng.calls != nil {
		refs = eng.calls.Extract(tree).Calls
	} else {
		stats.outlineDecline++ // FactProgram failed to compile for this language
	}
	if jsxCapableLanguages[f.entry.Name] {
		if jsx := jsxCallRefs(tree, eng.lang, f.entry.Name, src); len(jsx) > 0 {
			refs = append(refs, jsx...)
		}
	}
	return refs
}

// jsxCapableLanguages is the subset of jsFamilyLanguages whose grammar can
// actually produce JSX nodes, and it is deliberately NOT jsFamilyLanguages.
// The non-tsx `typescript` grammar cannot: in a .ts file `<Foo />` is a type
// assertion, and parsing it yields ERROR nodes, never jsx_opening_element or
// jsx_self_closing_element (probed against gotreesitter v0.49.0 — .ts gives
// jsx=0/ERROR=2, .tsx and .js both give jsx=2/ERROR=0). Walking a .ts tree
// for JSX is therefore provably dead work, and a pure-.ts repo is the common
// case: gating on the full JS family cost +2.66% on a 200-file cold build
// (p=0.005, n=10 interleaved) to find nothing. .js stays in — real-world
// React ships JSX in .js constantly, and the javascript grammar parses it.
var jsxCapableLanguages = map[string]bool{
	"tsx":        true,
	"javascript": true,
}

// jsxCallRefs walks tree for JSX component uses — jsx_opening_element and
// jsx_self_closing_element — and emits a CallRef per use so the existing
// containment attribution (innermostContainer) and resolution ladder apply
// unchanged. FactCalls only sees call_expression, so <FadeIn /> etc never
// produced a callsite before.
//
// Each JSX tag yields Name = last dotted segment, Receiver = prefix before
// the last dot ("" for <Button />). Lowercase html tags (div, span) are
// skipped — they would never resolve to a component symbol and would only
// add noise. Namespaced tags (Foo:Bar) are skipped for now.
func jsxCallRefs(tree *gts.Tree, lang *gts.Language, langName string, src []byte) []gts.CallRef {
	root := tree.RootNode()
	if root == nil {
		return nil
	}
	var out []gts.CallRef
	stack := []*gts.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		t := n.Type(lang)
		if t == "jsx_opening_element" || t == "jsx_self_closing_element" {
			if ref, ok := jsxTagCallRef(n, lang, langName, src, t); ok {
				out = append(out, ref)
			}
		}
		for i := n.ChildCount() - 1; i >= 0; i-- {
			if c := n.Child(i); c != nil {
				stack = append(stack, c)
			}
		}
	}
	return out
}

func jsxTagCallRef(n *gts.Node, lang *gts.Language, langName string, src []byte, nodeType string) (gts.CallRef, bool) {
	nameNode := n.ChildByFieldName("name", lang)
	if nameNode == nil {
		return gts.CallRef{}, false
	}
	full := strings.TrimSpace(nameNode.Text(src))
	if full == "" || strings.Contains(full, ":") {
		return gts.CallRef{}, false
	}
	if full[0] < 'A' || full[0] > 'Z' {
		return gts.CallRef{}, false
	}
	parts := strings.Split(full, ".")
	name := parts[len(parts)-1]
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return gts.CallRef{}, false
	}
	receiver := ""
	if len(parts) > 1 {
		receiver = strings.Join(parts[:len(parts)-1], ".")
	}
	idx := strings.LastIndex(full, name)
	var nameStart, nameEnd uint32
	if idx >= 0 {
		nameStart = nameNode.StartByte() + uint32(idx) //nolint:gosec // G115: idx < 2<<20 (maxMapperFileBytes)
		nameEnd = nameStart + uint32(len(name))        //nolint:gosec // G115: len(name) < file size
	} else {
		nameStart = nameNode.StartByte()
		nameEnd = nameNode.EndByte()
	}
	return gts.CallRef{
		Lang:          langName,
		Kind:          "call",
		Name:          name,
		Receiver:      receiver,
		NodeType:      nodeType,
		StartByte:     n.StartByte(),
		EndByte:       n.EndByte(),
		NameStartByte: nameStart,
		NameEndByte:   nameEnd,
	}, true
}

// mapperCallsite is one attributed call, in exactly the shape the ladder
// (resolve, below) needs — mirrors spike/mapper/cmd/qname/main.go's
// callsite struct field-for-field.
type mapperCallsite struct {
	callerID  int64
	name      string // called (bare) name
	receiver  string // receiver text, "" for a bare call
	pkg       string // caller's own package/module
	file      string // caller's own file
	container string // caller's OWN fully-qualified chain (container+"."+name), for rung 1
	lang      string // language name, for rung 0's go-skip
}

// attributeMapperCalls maps every FactCalls callsite in fileCalls to its
// innermost enclosing mapperSym via containment (design D3): per file, sort
// that file's symbols by start byte once, then per call binary-search the
// insertion point and walk left for the first (innermost) containing symbol.
// A call with no enclosing symbol (a module-level statement) is DROPPED, not
// attributed to the file — the graph has no file-node to attribute it to.
func attributeMapperCalls(mapperSyms []mapperSym, fileCalls []mapperFileCalls) []mapperCallsite {
	byFile := map[string][]mapperSym{}
	for _, ms := range mapperSyms {
		byFile[ms.row.file] = append(byFile[ms.row.file], ms)
	}
	for file, syms := range byFile {
		sorted := append([]mapperSym(nil), syms...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
		byFile[file] = sorted
	}

	var out []mapperCallsite
	for _, fc := range fileCalls {
		syms := byFile[fc.file]
		if len(syms) == 0 {
			continue
		}
		for _, ref := range fc.refs {
			caller, ok := innermostContainer(syms, ref.StartByte, ref.EndByte)
			if !ok {
				continue // module-level call: dropped, not attributed (design D3)
			}
			out = append(out, mapperCallsite{
				callerID:  caller.row.id,
				name:      ref.Name,
				receiver:  ref.Receiver,
				pkg:       caller.row.pkg,
				file:      caller.row.file,
				container: callerChain(caller),
				lang:      fc.lang,
			})
		}
	}
	return out
}

// innermostContainer returns the innermost symbol in syms (which MUST already
// be sorted ascending by start) whose byte range contains [start,end),
// scanning backward from the binary-search insertion point. Nested symbol
// ranges are strictly contained by their ancestors' — a child always sorts
// after its parent by start — so the first containing symbol found scanning
// from the largest start downward is necessarily the innermost one: any
// earlier (smaller-start) containing symbol can only be an ANCESTOR of it,
// never an unrelated sibling that happens to overlap.
func innermostContainer(syms []mapperSym, start, end uint32) (mapperSym, bool) {
	idx := sort.Search(len(syms), func(i int) bool { return syms[i].start > start })
	for i := idx - 1; i >= 0; i-- {
		if syms[i].start <= start && end <= syms[i].end {
			return syms[i], true
		}
	}
	return mapperSym{}, false
}

// callerChain returns the caller symbol's OWN fully-qualified container
// chain — ms.container is the symbol's ENCLOSING chain (e.g. "Foo" for a
// method "bar" inside class Foo), so the symbol's own chain is that plus its
// own name ("Foo.bar"). This is the ladder's rung-1 input (spike's
// callsite.container), never the caller's grand-container.
func callerChain(ms mapperSym) string {
	if ms.container == "" {
		return ms.row.name
	}
	return ms.container + "." + ms.row.name
}

// specifierExtensions is the probe order for a relative module specifier
// written without one. Direct-file order matches the JS resolution order
// TypeScript and bundlers use; ".mjs" is included because mapper discovery
// indexes it, so omitting it would leave a real file unreachable.
var specifierExtensions = []string{".ts", ".tsx", ".js", ".jsx", ".mjs"}

// resolveSpecifier maps a module specifier as written in importer (itself a
// repo-relative, slash-separated path) to the repo-relative mapper file it
// names, or "" when it names none.
//
// Only RELATIVE specifiers resolve. A bare one ("axios", "@scope/pkg") names
// a dependency, not repo source, and nothing here tries to walk node_modules
// for it. The empty result is not an error path: rung 2a expresses a miss
// exactly this way, and a missed rung falls through un-narrowed.
//
// known is the set of repo-relative mapper files this build discovered, so
// resolution never touches the filesystem — a specifier can only resolve to
// a file the graph actually indexed, which is also what keeps a "../.."
// escape from the repo root from resolving to anything.
func resolveSpecifier(importer, spec string, known map[string]bool) string {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return ""
	}
	base := path.Join(path.Dir(importer), spec)
	if known[base] {
		return base // the specifier carried its own extension
	}
	for _, ext := range specifierExtensions {
		if known[base+ext] {
			return base + ext
		}
	}
	for _, ext := range specifierExtensions {
		if idx := base + "/index" + ext; known[idx] {
			return idx
		}
	}
	return ""
}

// resolveBindings turns mapperImports' raw binding -> SPECIFIER map into
// rung 2a's binding -> repo FILE map, against the set of files this build
// actually discovered. A binding whose specifier names no indexed file is
// dropped here rather than carried as an unresolvable entry: the ladder's
// only question is "which file", and no entry and an unresolvable entry mean
// the same thing to it — a rung-2a miss.
func resolveBindings(files []mapperFile, bindings mapperImportBindings) map[string]map[string]string {
	if len(bindings) == 0 {
		return nil
	}
	known := make(map[string]bool, len(files))
	for _, f := range files {
		known[f.rel] = true
	}
	out := make(map[string]map[string]string, len(bindings))
	for importer, byName := range bindings {
		for name, spec := range byName {
			target := resolveSpecifier(importer, spec, known)
			if target == "" {
				continue
			}
			if out[importer] == nil {
				out[importer] = map[string]string{}
			}
			out[importer][name] = target
		}
	}
	return out
}

// mapperLadderIndex is the repo-wide lookup the resolution ladder searches:
// every mapper symbol from this build, grouped by bare name, plus the set of
// names seen as class-like definitions (design D4).
type mapperLadderIndex struct {
	syms    []mapperSym
	byName  map[string][]int
	classes map[string]bool
	// imports is rung 2a's input: importer file -> local binding name ->
	// the repo-relative file that binding's specifier RESOLVED to. Already
	// resolved by the time it lands here (mapperEdges does that), so the
	// ladder never touches specifiers or the filesystem. Nil is a legitimate
	// value — every rung-2a lookup then misses and the ladder behaves
	// exactly as it did before this rung existed.
	imports map[string]map[string]string
}

// buildMapperLadderIndex indexes syms for the ladder. classes mirrors the
// spike's ix.classes: any symbol whose kind is class-like makes its NAME
// resolvable as a receiver at rung 2b. importsByFile feeds rung 2a.
func buildMapperLadderIndex(syms []mapperSym, importsByFile map[string]map[string]string) *mapperLadderIndex {
	idx := &mapperLadderIndex{syms: syms, byName: map[string][]int{}, classes: map[string]bool{}, imports: importsByFile}
	for i, s := range syms {
		idx.byName[s.row.name] = append(idx.byName[s.row.name], i)
		if isMapperClassLike(s.row.kind) {
			idx.classes[s.row.name] = true
		}
	}
	return idx
}

// isMapperClassLike mirrors spike's isClassLike over the Go-tier-normalized
// kind vocabulary mapperSymbols already writes (mapper_symbols.go's
// normalizeOutlineKind) — "class"/"interface"/"struct"/"type" all count.
func isMapperClassLike(kind string) bool {
	return strings.Contains(kind, "class") || strings.Contains(kind, "interface") ||
		strings.Contains(kind, "struct") || strings.Contains(kind, "type")
}

func (ix *mapperLadderIndex) filter(idxs []int, keep func(mapperSym) bool) []int {
	var out []int
	for _, i := range idxs {
		if keep(ix.syms[i]) {
			out = append(out, i)
		}
	}
	return out
}

// topContainer returns the first dotted segment of a caller's own qualified
// chain — the enclosing CLASS for rung 1's this/self resolution (e.g.
// "Widget" from "Widget.render").
func topContainer(chain string) string {
	if chain == "" {
		return ""
	}
	return strings.SplitN(chain, ".", 2)[0]
}

// resolve applies the resolution ladder (spec "Resolution Ladder Order and
// Fallthrough", design D4 — ported from spike/mapper/cmd/qname/main.go:291-340)
// and returns the resolved candidate indices into ix.syms plus the TRUE
// pre-cap candidate count. The two differ only when rung 5's cap truncates
// (design D5/D6) — every other rung either returns its full (uncapped) hit
// set or falls through unchanged.
//
// Every rung is a filter guarded by len(hit) > 0: a rung matching nothing is
// DISCARDED and the walk continues with the un-narrowed set from before that
// rung ran — this is what makes a zero-candidate outcome structurally
// impossible. Rung 0 is the one rung that ASSIGNS (cands = hit) rather than
// returning, so a hit there narrows the set the REST of the ladder searches;
// every other rung returns immediately on a hit.
func (ix *mapperLadderIndex) resolve(c mapperCallsite) (hits []int, total int) {
	cands := ix.byName[c.name]
	if len(cands) == 0 {
		return nil, 0
	}
	// 0. Receiver-arity constraint: a member call (foo.get()) can only target
	// a member, a bare call (get()) can only target a free function — purely
	// syntactic, no type information needed. Skipped for go: a selector may be
	// a package qualifier (pkg.Func()) there, not a receiver. Never actually
	// reached with lang=="go" in the mapper tier (Go-free by construction),
	// but the guard is kept verbatim for spike fidelity.
	if c.lang != "go" {
		want := c.receiver != ""
		if hit := ix.filter(cands, func(s mapperSym) bool {
			return (s.container != "") == want
		}); len(hit) > 0 {
			cands = hit
		}
	}
	// 1. this/self -> enclosing class member
	if c.receiver == "this" || c.receiver == "self" {
		if cls := topContainer(c.container); cls != "" {
			if hit := ix.filter(cands, func(s mapperSym) bool {
				return s.container == cls || strings.HasPrefix(s.container, cls+".")
			}); len(hit) > 0 {
				return hit, len(hit)
			}
		}
	}
	// 2a. receiver names something imported into THIS file: narrow to the
	// members of classes defined in the file that import resolved to. Sits
	// above 2b rather than replacing it because 2b's repo-wide class match
	// is still the right answer when nothing was imported — and because a
	// receiver name can legitimately collide with an unrelated class
	// elsewhere in the repo, which is exactly the case 2a gets right and 2b
	// gets wrong. A miss falls through with cands untouched.
	if c.receiver != "" {
		if target := ix.imports[c.file][c.receiver]; target != "" {
			if hit := ix.filter(cands, func(s mapperSym) bool {
				return s.row.file == target && ix.classes[topContainer(s.container)]
			}); len(hit) > 0 {
				return hit, len(hit)
			}
		}
	}
	// 2b. receiver names a known class, repo-wide (2a, above, is import-scoped)
	if c.receiver != "" && ix.classes[c.receiver] {
		if hit := ix.filter(cands, func(s mapperSym) bool {
			return s.container == c.receiver || strings.HasPrefix(s.container, c.receiver+".")
		}); len(hit) > 0 {
			return hit, len(hit)
		}
	}
	// 3. same file
	if hit := ix.filter(cands, func(s mapperSym) bool { return s.row.file == c.file }); len(hit) > 0 {
		return hit, len(hit)
	}
	// 4. same package
	if hit := ix.filter(cands, func(s mapperSym) bool { return s.row.pkg == c.pkg }); len(hit) > 0 {
		return hit, len(hit)
	}
	// 5. repo-wide, capped — labeled by the caller (mapperEdges), never a
	// silent slice: total always reports the true pre-cap count.
	if len(cands) > fanoutCap {
		return cands[:fanoutCap], len(cands)
	}
	return cands, len(cands)
}

// mapperEdges is the mapper tier's edge producer (design D4/D5/D6): collect
// FactCalls per file, attribute each callsite to its innermost enclosing
// symbol, resolve targets through the ladder, and dedupe via edgeSet.add.
// Every emitted edge carries precision "syntactic" and no dispatch concept
// (the empty string — design D1's table: mapper edges have no dispatch
// axis). Self-edges are dropped, mirroring callEdges' existing rule
// (index.go). fanoutCapped counts CALLSITES (not edges) whose rung-5
// candidate set exceeded fanoutCap — a build-level partiality fact, feeding
// meta.fanout_capped, not a per-edge one.
// fileCalls comes from the caller rather than a collectMapperCalls call here:
// buildIndex takes it from scanMapperFiles, which extracted it from the same
// parse that produced mapperSyms.
func mapperEdges(files []mapperFile, mapperSyms []mapperSym, bindings mapperImportBindings, fileCalls []mapperFileCalls) (edgeSet, int) {
	callsites := attributeMapperCalls(mapperSyms, fileCalls)
	idx := buildMapperLadderIndex(mapperSyms, resolveBindings(files, bindings))

	edges := edgeSet{}
	fanoutCapped := 0
	for _, c := range callsites {
		hits, total := idx.resolve(c)
		if total > len(hits) {
			fanoutCapped++
		}
		for _, i := range hits {
			calleeID := idx.syms[i].row.id
			if calleeID == c.callerID {
				continue // self-edge drop, mirrors index.go's callEdges rule
			}
			edges.add([2]int64{c.callerID, calleeID}, edgeMeta{precision: "syntactic"})
		}
	}
	return edges, fanoutCapped
}
