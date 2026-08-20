// Mapper-tier call collection, containment attribution, and the resolution
// ladder: FactCalls -> which symbol the call happens inside -> which
// symbol(s) it could target -> a syntactic edgeSet entry. Ported from
// spike/mapper/cmd/qname/main.go's resolve() (design D4), rung 2a (receiver
// names a class imported into THIS FILE) deferred to PR-G2 — every other
// rung lands here.
package graph

import (
	"os"
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

		parser := gts.NewParser(eng.lang) // fresh per file: Parser is not concurrency-safe
		tree, err := parser.Parse(src)
		if err != nil {
			stats.parseErr++
			continue // unparsable file is skip-and-continue, not fatal
		}

		if eng.calls == nil {
			stats.outlineDecline++ // FactProgram failed to compile for this language
			continue
		}
		facts := eng.calls.Extract(tree)
		if len(facts.Calls) == 0 {
			continue
		}
		out = append(out, mapperFileCalls{file: f.rel, lang: f.entry.Name, refs: facts.Calls})
	}
	return out, stats
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

// mapperLadderIndex is the repo-wide lookup the resolution ladder searches:
// every mapper symbol from this build, grouped by bare name, plus the set of
// names seen as class-like definitions (design D4).
type mapperLadderIndex struct {
	syms    []mapperSym
	byName  map[string][]int
	classes map[string]bool
}

// buildMapperLadderIndex indexes syms for the ladder. classes mirrors the
// spike's ix.classes: any symbol whose kind is class-like makes its NAME
// resolvable as a receiver at rung 2b (and rung 2a in PR-G2).
func buildMapperLadderIndex(syms []mapperSym) *mapperLadderIndex {
	idx := &mapperLadderIndex{syms: syms, byName: map[string][]int{}, classes: map[string]bool{}}
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
// every other rung returns immediately on a hit. Rung 2a (receiver names a
// class imported into the caller's own file) is deferred to PR-G2 — it slots
// in above rung 2b without changing anything here.
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
	// 2b. receiver names a known class, repo-wide (2a — import-scoped — is PR-G2)
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
func mapperEdges(files []mapperFile, mapperSyms []mapperSym) (edgeSet, int) {
	fileCalls, _ := collectMapperCalls(files)
	callsites := attributeMapperCalls(mapperSyms, fileCalls)
	idx := buildMapperLadderIndex(mapperSyms)

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
