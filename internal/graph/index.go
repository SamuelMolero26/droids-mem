package graph

import (
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
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

// buildIndex loads, type-checks, and analyzes the repo, then atomically
// replaces dbPath with a fresh graph (build to .tmp, rename over). A repo that
// does not type-check returns an error and leaves any existing graph intact.
func buildIndex(ctx context.Context, repo, dbPath, stampVal string) error {
	cfg := &packages.Config{
		Context: ctx,
		Dir:     repo,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("load packages (is the Go toolchain installed?): %w", err)
	}
	if len(pkgs) == 0 {
		return fmt.Errorf("no Go packages found under %s", repo)
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return fmt.Errorf("repo does not type-check: %w", p.Errors[0])
		}
	}

	module := ""
	if pkgs[0].Module != nil {
		module = pkgs[0].Module.Path
	}

	fset := pkgs[0].Fset
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

	var symbols []*symRow
	byPos := map[string]*symRow{} // "abs-file:line" → row, for SSA function matching
	for _, p := range pkgs {
		shortPkg := shortPkgPath(p.PkgPath, module)
		for _, f := range p.Syntax {
			for _, decl := range f.Decls {
				symbols = appendDeclSymbols(symbols, byPos, fset, readFile, decl, shortPkg, repo)
			}
		}
	}
	for i, s := range symbols {
		s.id = int64(i + 1)
	}

	edges, err := callEdges(pkgs, byPos)
	if err != nil {
		return err
	}
	impls := implementsEdges(pkgs, byPos)

	return writeGraphDB(dbPath, repo, module, stampVal, symbols, edges, impls)
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

// callEdges builds SSA and a CHA call graph, then maps functions back to
// symbol rows by declaration position. CHA over-approximates interface
// dispatch — the safe direction for "what breaks if I change X" — and needs
// no main-function roots, so library repos index fully (RTA would not).
func callEdges(pkgs []*packages.Package, byPos map[string]*symRow) (map[[2]int64]bool, error) {
	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	cg := cha.CallGraph(prog)
	cg.DeleteSyntheticNodes()

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

	edges := map[[2]int64]bool{}
	err := callgraph.GraphVisitEdges(cg, func(e *callgraph.Edge) error {
		caller, ok := resolve(e.Caller.Func)
		if !ok {
			return nil
		}
		callee, ok := resolve(e.Callee.Func)
		if !ok || caller == callee {
			return nil
		}
		edges[[2]int64{caller.id, callee.id}] = true
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
// so readers never observe a half-built graph.
func writeGraphDB(dbPath, repo, module, stampVal string, symbols []*symRow, edges, impls map[[2]int64]bool) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return fmt.Errorf("create graph dir: %w", err)
	}
	removeStaleTemps(dbPath)
	// Per-process temp name: a second droids-mem (e.g. the MCP server rebuilding
	// the same repo while a CLI graph query does too) must not clobber our
	// half-written file. Each builder writes its own .tmp.<pid> and the rename is
	// atomic — last writer wins with byte-identical content. In-process, repoLock
	// already serializes same-repo builds, so pid is unique enough.
	tmp := fmt.Sprintf("%s.tmp.%d", dbPath, os.Getpid())
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", "file:"+tmp)
	if err != nil {
		return fmt.Errorf("create graph db: %w", err)
	}
	err = func() error {
		if _, err := db.Exec(schema); err != nil {
			return fmt.Errorf("apply graph schema: %w", err)
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		symIns, err := tx.Prepare(`INSERT INTO symbols
			(id, qname, name, kind, package, file, line, exported, signature, doc, source)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer symIns.Close()
		for _, s := range symbols {
			if _, err := symIns.Exec(s.id, s.qname, s.name, s.kind, s.pkg, s.file, s.line,
				s.exported, s.signature, s.doc, s.source); err != nil {
				return fmt.Errorf("insert symbol %s: %w", s.qname, err)
			}
		}
		edgeIns, err := tx.Prepare(`INSERT OR IGNORE INTO edges (caller, callee) VALUES (?,?)`)
		if err != nil {
			return err
		}
		defer edgeIns.Close()
		for e := range edges {
			if _, err := edgeIns.Exec(e[0], e[1]); err != nil {
				return err
			}
		}
		implIns, err := tx.Prepare(`INSERT OR IGNORE INTO implements (iface, impl) VALUES (?,?)`)
		if err != nil {
			return err
		}
		defer implIns.Close()
		for e := range impls {
			if _, err := implIns.Exec(e[0], e[1]); err != nil {
				return err
			}
		}
		// FTS mirror for the search fallback; rowid == symbols.id for the join back.
		if _, err := tx.Exec(`INSERT INTO symbols_fts(rowid, qname, name, doc, signature)
			SELECT id, qname, name, doc, signature FROM symbols`); err != nil {
			return fmt.Errorf("populate symbols_fts: %w", err)
		}
		for k, v := range map[string]string{
			"stamp": stampVal, "repo": repo, "module": module, "indexed_at": nowUTC(),
		} {
			if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?,?)`, k, v); err != nil {
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
	return os.Rename(tmp, dbPath)
}

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
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
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
