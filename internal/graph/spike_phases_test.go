//go:build spike

package graph

// Measurement spike: wall-clock per phase of buildIndex, replicated call by
// call (same functions, same order) so no production code needs timers.
// callEdges is the one function split open: its body is copied below so SSA
// build, CHA, and edge extraction can be timed apart; the copy's edge count is
// cross-checked against the real callEdges.
//
//   SPIKE_REPO=/abs/path SPIKE_ITERS=3 go test -tags spike -run TestSpikePhases -v -count=1 ./internal/graph/

import (
	"context"
	"database/sql"
	"fmt"
	"go/token"
	"go/types"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

type spikePhase struct {
	name string
	d    time.Duration
}

type spikeRun struct {
	phases           []spikePhase
	counts           map[string]int
	peakHeapInuse    uint64
	totalAllocDelta  uint64
	fullBuild        time.Duration
	fullPeakInuse    uint64
	fullTotalAllocMB uint64
}

// heapSampler polls HeapInuse every 5ms and keeps the max.
func heapSampler() (stop func() uint64) {
	var peak atomic.Uint64
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var ms runtime.MemStats
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > peak.Load() {
				peak.Store(ms.HeapInuse)
			}
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	return func() uint64 { close(done); wg.Wait(); return peak.Load() }
}

func spikeRunPhases(t *testing.T, repo, dbPath string) spikeRun {
	t.Helper()
	ctx := context.Background()
	r := spikeRun{counts: map[string]int{}}
	timeIt := func(name string, f func()) {
		s := time.Now()
		f()
		r.phases = append(r.phases, spikePhase{name, time.Since(s)})
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	stopSampler := heapSampler()

	var snap snapshot
	timeIt("1 census(stamp walk)", func() {
		var err error
		snap, err = census(repo)
		if err != nil {
			t.Fatal(err)
		}
	})
	r.counts["census_files"] = len(snap.files)

	// The `go list` subprocess alone, same flags go/packages passes, so
	// packages.Load can be split into "go list" vs in-process parse+typecheck.
	timeIt("aux 2a: go list subprocess only", func() {
		cmd := exec.CommandContext(ctx, "go", "list", "-e", "-json", "-compiled=true", "-test=true", "-export=false", "-deps=true", "-find=false", "-pgo=off", "--", "./...")
		cmd.Dir = repo
		cmd.Stdout = io.Discard
		_ = cmd.Run()
	})
	var rawPkgs []*packages.Package
	var loadErr error
	timeIt("2 packages.Load", func() {
		cfg := &packages.Config{
			Context: ctx, Dir: repo, Env: goToolEnv(), Tests: true,
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
				packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
				packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
		}
		rawPkgs, loadErr = packages.Load(cfg, "./...")
	})
	pkgs := usableGoPackages(rawPkgs, loadErr)
	var broken []*packages.Package
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			broken = append(broken, p)
		}
	}
	r.counts["go_pkgs_roots"] = len(pkgs)
	r.counts["go_pkgs_broken"] = len(broken)
	allPkgs := 0
	packages.Visit(pkgs, nil, func(*packages.Package) { allPkgs++ })
	r.counts["go_pkgs_incl_deps"] = allPkgs

	var mFiles []mapperFile
	var mStats mapperStats
	var mErr error
	timeIt("6a mapperFiles(walk)", func() { mFiles, mStats, mErr = mapperFiles(repo) })
	var scan mapperScanResult
	var mapperSyms []mapperSym
	var carriedUnitsM []string
	var carriedDirectives map[string]string
	testsSkipped := 0
	if mErr == nil {
		testsSkipped = mStats.skippedTest
		timeIt("6b scanMapperFiles(parse+syms+calls+imports)", func() { scan = scanMapperFiles(mFiles) })
		timeIt("6c mapperCarry", func() {
			mapperSyms, carriedUnitsM, carriedDirectives = mapperCarry(dbPath, mFiles, scan.syms, scan.hasError)
		})
	}
	r.counts["mapper_files"] = len(mFiles)
	r.counts["mapper_syms"] = len(mapperSyms)
	directives := scan.fileDirectives
	for f, d := range carriedDirectives {
		delete(directives, f)
		if d != "" {
			directives[f] = d
		}
	}

	if len(broken)*2 > len(pkgs) {
		if len(mapperSyms) == 0 {
			t.Fatalf("majority broken: %d/%d", len(broken), len(pkgs))
		}
		pkgs, broken = nil, nil
	}
	module := ""
	var fset *token.FileSet
	if len(pkgs) > 0 {
		fset = pkgs[0].Fset
		if pkgs[0].Module != nil {
			module = pkgs[0].Module.Path
		}
	}
	files := map[string][]byte{}
	readFile := func(name string) []byte {
		if b, ok := files[name]; ok {
			return b
		}
		b, err := os.ReadFile(name) // #nosec G304 -- spike
		if err != nil {
			b = nil
		}
		files[name] = b
		return b
	}

	var symbols []*symRow
	var byPos map[string]*symRow
	timeIt("5 goSymbols(decl extraction)", func() { symbols, byPos = goSymbols(pkgs, module, fset, readFile, repo) })
	r.counts["go_syms"] = len(symbols)
	var nav nextNavigationGraph
	var byQName map[string]int64
	var collided map[string]bool
	timeIt("5b merge+ids+nav+byQName", func() {
		seen := map[*symRow]bool{}
		for _, ms := range mapperSyms {
			if !seen[ms.row] {
				seen[ms.row] = true
				symbols = append(symbols, ms.row)
			}
		}
		for i, s := range symbols {
			s.id = int64(i + 1)
		}
		nav = buildNextNavigationGraph(repo, mFiles, nextFreshMapperSymbols(scan.syms, scan.hasError), scan.navigationSites, scan.defaultExportNames)
		byQName, collided = buildByQName(symbols)
	})

	edges := edgeSet{}
	var impls map[[2]int64]bool
	if len(pkgs) > 0 {
		brokenSet := map[*packages.Package]bool{}
		for _, p := range broken {
			brokenSet[p] = true
		}
		var prog *ssa.Program
		timeIt("3a ssa.NewProgram+CreatePackage", func() {
			prog = ssa.NewProgram(pkgs[0].Fset, ssa.InstantiateGenerics)
			packages.Visit(pkgs, nil, func(p *packages.Package) {
				if p.Types == nil {
					return
				}
				if brokenSet[p] {
					prog.CreatePackage(p.Types, nil, nil, true)
				} else {
					prog.CreatePackage(p.Types, p.Syntax, p.TypesInfo, true)
				}
			})
		})
		timeIt("3b prog.Build", func() { prog.Build() })
		var cg *callgraph.Graph
		timeIt("4a cha.CallGraph", func() { cg = cha.CallGraph(prog) })
		r.counts["cha_nodes"] = len(cg.Nodes)
		timeIt("4b edge extraction(GraphVisitEdges)", func() {
			resolve := func(fn *ssa.Function) (*symRow, bool) {
				for fn.Parent() != nil {
					fn = fn.Parent()
				}
				if o := fn.Origin(); o != nil {
					fn = o
				}
				if !fn.Pos().IsValid() {
					return nil, false
				}
				p := prog.Fset.Position(fn.Pos())
				row, ok := byPos[fmt.Sprintf("%s:%d", p.Filename, p.Line)]
				return row, ok
			}
			raw := 0
			_ = callgraph.GraphVisitEdges(cg, func(e *callgraph.Edge) error {
				raw++
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
				d := "static"
				if e.Site != nil && e.Site.Common().IsInvoke() {
					d = "interface"
				}
				edges.add([2]int64{caller.id, callee.id}, edgeMeta{dispatch: d, precision: "resolved"})
				return nil
			})
			r.counts["cha_raw_edges"] = raw
		})
		r.counts["go_edges"] = len(edges)
		goOnly := maps.Clone(edges)

		// EXPERIMENT (not production behavior): dependency packages as
		// types-only stubs, only repo roots get bodies. resolve() drops every
		// edge whose endpoint is not a repo decl, so dep bodies may be wasted.
		roots := map[*packages.Package]bool{}
		for _, p := range pkgs {
			roots[p] = true
		}
		var xEdges edgeSet
		timeIt("aux X: ssa+cha+walk, deps types-only", func() {
			xp := ssa.NewProgram(pkgs[0].Fset, ssa.InstantiateGenerics)
			packages.Visit(pkgs, nil, func(p *packages.Package) {
				if p.Types == nil {
					return
				}
				if brokenSet[p] || !roots[p] {
					xp.CreatePackage(p.Types, nil, nil, true)
				} else {
					xp.CreatePackage(p.Types, p.Syntax, p.TypesInfo, true)
				}
			})
			xp.Build()
			xg := cha.CallGraph(xp)
			xEdges = edgeSet{}
			_ = callgraph.GraphVisitEdges(xg, func(e *callgraph.Edge) error {
				if e.Callee.Func.Parent() != nil {
					return nil
				}
				res := func(fn *ssa.Function) (*symRow, bool) {
					for fn.Parent() != nil {
						fn = fn.Parent()
					}
					if o := fn.Origin(); o != nil {
						fn = o
					}
					if !fn.Pos().IsValid() {
						return nil, false
					}
					p := xp.Fset.Position(fn.Pos())
					row, ok := byPos[fmt.Sprintf("%s:%d", p.Filename, p.Line)]
					return row, ok
				}
				c, ok := res(e.Caller.Func)
				if !ok {
					return nil
				}
				d, ok := res(e.Callee.Func)
				if !ok || c == d {
					return nil
				}
				xEdges.add([2]int64{c.id, d.id}, edgeMeta{})
				return nil
			})
		})
		missing, extra := 0, 0
		for k := range goOnly {
			if _, ok := xEdges[k]; !ok {
				missing++
			}
		}
		for k := range xEdges {
			if _, ok := goOnly[k]; !ok {
				extra++
			}
		}
		r.counts["X_edges"], r.counts["X_missing_vs_full"], r.counts["X_extra_vs_full"] = len(xEdges), missing, extra

		// EXPERIMENT Y: packages.Load WITHOUT NeedDeps (deps' types from
		// export data, roots from source), then SSA with roots bodied and every
		// transitively imported *types.Package as a stub. byPos keys are
		// file:line strings, so they match across the two loads.
		var yEdges edgeSet
		var yLoadErr error
		timeIt("aux Y1: packages.Load no NeedDeps", func() {
			cfg := &packages.Config{
				Context: ctx, Dir: repo, Env: goToolEnv(), Tests: true,
				Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
					packages.NeedImports | packages.NeedTypes |
					packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
			}
			var yRaw []*packages.Package
			yRaw, yLoadErr = packages.Load(cfg, "./...")
			ypkgs := usableGoPackages(yRaw, yLoadErr)
			if len(ypkgs) == 0 {
				return
			}
			yp := ssa.NewProgram(ypkgs[0].Fset, ssa.InstantiateGenerics)
			created := map[*types.Package]bool{}
			for _, p := range ypkgs {
				if p.Types != nil && !created[p.Types] {
					created[p.Types] = true
					if len(p.Errors) > 0 {
						yp.CreatePackage(p.Types, nil, nil, true)
					} else {
						yp.CreatePackage(p.Types, p.Syntax, p.TypesInfo, true)
					}
				}
			}
			var walk func(tp *types.Package)
			walk = func(tp *types.Package) {
				for _, imp := range tp.Imports() {
					if !created[imp] {
						created[imp] = true
						yp.CreatePackage(imp, nil, nil, true)
						walk(imp)
					}
				}
			}
			for _, p := range ypkgs {
				if p.Types != nil {
					walk(p.Types)
				}
			}
			r.counts["Y_ssa_pkgs"] = len(created)
			timeIt("aux Y2: (inside Y1) ssa+cha+walk", func() {
				yp.Build()
				yg := cha.CallGraph(yp)
				yEdges = edgeSet{}
				res := func(fn *ssa.Function) (*symRow, bool) {
					for fn.Parent() != nil {
						fn = fn.Parent()
					}
					if o := fn.Origin(); o != nil {
						fn = o
					}
					if !fn.Pos().IsValid() {
						return nil, false
					}
					p := yp.Fset.Position(fn.Pos())
					row, ok := byPos[fmt.Sprintf("%s:%d", p.Filename, p.Line)]
					return row, ok
				}
				_ = callgraph.GraphVisitEdges(yg, func(e *callgraph.Edge) error {
					if e.Callee.Func.Parent() != nil {
						return nil
					}
					c, ok := res(e.Caller.Func)
					if !ok {
						return nil
					}
					d, ok := res(e.Callee.Func)
					if !ok || c == d {
						return nil
					}
					yEdges.add([2]int64{c.id, d.id}, edgeMeta{})
					return nil
				})
			})
		})
		ym, yx := 0, 0
		for k := range goOnly {
			if _, ok := yEdges[k]; !ok {
				ym++
			}
		}
		for k := range yEdges {
			if _, ok := goOnly[k]; !ok {
				yx++
			}
		}
		if yLoadErr != nil {
			t.Logf("Y load error: %v", yLoadErr)
		}
		r.counts["Y_edges"], r.counts["Y_missing_vs_full"], r.counts["Y_extra_vs_full"] = len(yEdges), ym, yx

		timeIt("4c implementsEdges", func() { impls = implementsEdges(pkgs, byPos) })
		r.counts["impl_edges"] = len(impls)
	}

	var fanout int
	timeIt("6d mapperEdges(resolution)", func() {
		alias := loadAliasConfig(repo)
		var me edgeSet
		me, fanout = mapperEdges(mFiles, mapperSyms, scan.bindings, scan.fileCalls, alias)
		r.counts["mapper_edges"] = len(me)
		for k, m := range me {
			edges.add(k, m)
		}
	})

	emptyReason := ""
	if len(symbols) == 0 {
		emptyReason = "no_indexable_symbols"
	}
	var carried []string
	timeIt("7a carriedEdges", func() {
		if len(broken) > 0 {
			names := map[string]bool{}
			for _, p := range broken {
				names[shortPkgPath(p.PkgPath, module)] = true
			}
			carried = slices.Sorted(maps.Keys(names))
			for k, m := range carriedEdges(dbPath, names, byQName) {
				edges.add(k, m)
			}
		}
		if len(carriedUnitsM) > 0 {
			mods := map[string]bool{}
			for _, mp := range carriedUnitsM {
				mods[mp] = true
			}
			for k, m := range mapperCarriedEdges(dbPath, mods, byQName, collided) {
				edges.add(k, m)
			}
			carried = append(carried, carriedUnitsM...)
		}
	})
	timeIt("7 writeGraphDB", func() {
		if err := writeGraphDB(ctx, dbPath, repo, module, snap, symbols, edges, impls, carried, emptyReason, fanout, testsSkipped, scan.importRows, directives, nav); err != nil {
			t.Fatal(err)
		}
	})
	r.peakHeapInuse = stopSampler()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	r.totalAllocDelta = after.TotalAlloc - before.TotalAlloc

	// Row counts actually written.
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, tbl := range []string{"symbols", "edges", "implements", "imports", "file_snapshot"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + tbl).Scan(&n); err == nil {
			r.counts["db_"+tbl] = n
		}
	}
	return r
}

func maxrssMB(who int) float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(who, &ru)
	return float64(ru.Maxrss) / (1 << 20) // darwin: bytes
}

func TestSpikePhases(t *testing.T) {
	repo := os.Getenv("SPIKE_REPO")
	if repo == "" {
		t.Skip("set SPIKE_REPO")
	}
	repo, _ = filepath.Abs(repo)
	iters := 3
	if v, err := strconv.Atoi(os.Getenv("SPIKE_ITERS")); err == nil && v > 0 {
		iters = v
	}
	t.Logf("repo=%s GOMAXPROCS=%d iters=%d", repo, runtime.GOMAXPROCS(0), iters)

	var runs []spikeRun
	for i := range iters {
		dir := t.TempDir()
		r := spikeRunPhases(t, repo, filepath.Join(dir, "phased.db"))

		// End-to-end buildIndex on a fresh db path (runs after the phased
		// pass, so its packages.Load is never the process-first one).
		runtime.GC()
		var b runtime.MemStats
		runtime.ReadMemStats(&b)
		stop := heapSampler()
		s := time.Now()
		snap, err := census(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := buildIndex(context.Background(), repo, filepath.Join(dir, "full.db"), snap); err != nil {
			t.Fatalf("buildIndex: %v", err)
		}
		r.fullBuild = time.Since(s)
		r.fullPeakInuse = stop()
		if fdb, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "full.db")+"?mode=ro"); err == nil {
			var ns, ne int
			_ = fdb.QueryRow("SELECT count(*) FROM symbols").Scan(&ns)
			_ = fdb.QueryRow("SELECT count(*) FROM edges").Scan(&ne)
			_ = fdb.Close()
			r.counts["full_db_symbols"], r.counts["full_db_edges"] = ns, ne
		}
		var a runtime.MemStats
		runtime.ReadMemStats(&a)
		r.fullTotalAllocMB = (a.TotalAlloc - b.TotalAlloc) >> 20
		runs = append(runs, r)
		t.Logf("iter %d done: phased sum=%v full(census+buildIndex)=%v", i, sumPhases(r), r.fullBuild)
	}

	cold := runs[0]
	keys := slices.Sorted(maps.Keys(cold.counts))
	for _, k := range keys {
		t.Logf("count %-20s %d", k, cold.counts[k])
	}
	warmMed := func(idx int) time.Duration {
		if len(runs) < 2 {
			return runs[0].phases[idx].d
		}
		var ds []time.Duration
		for _, r := range runs[1:] {
			ds = append(ds, r.phases[idx].d)
		}
		sort.Slice(ds, func(a, b int) bool { return ds[a] < ds[b] })
		return ds[len(ds)/2]
	}
	var warmTotal time.Duration
	for i, p := range cold.phases {
		if !strings.HasPrefix(p.name, "aux") {
			warmTotal += warmMed(i)
		}
	}
	t.Logf("%-46s %10s %10s %7s", "phase", "cold_ms", "warm_ms", "warm%")
	for i, p := range cold.phases {
		w := warmMed(i)
		t.Logf("%-46s %10.1f %10.1f %6.1f%%", p.name, ms(p.d), ms(w), 100*float64(w)/float64(warmTotal))
	}
	t.Logf("%-46s %10.1f %10.1f", "SUM(phases)", ms(sumPhases(cold)), ms(warmTotal))
	var fulls []time.Duration
	for _, r := range runs {
		fulls = append(fulls, r.fullBuild)
	}
	t.Logf("full census+buildIndex per iter: %v", fulls)
	t.Logf("mem phased iter0: peakHeapInuse=%dMB totalAlloc=%dMB | full iter0: peakHeapInuse=%dMB totalAlloc=%dMB",
		cold.peakHeapInuse>>20, cold.totalAllocDelta>>20, cold.fullPeakInuse>>20, cold.fullTotalAllocMB)
	t.Logf("maxrss self=%.0fMB children(go list)=%.0fMB", maxrssMB(syscall.RUSAGE_SELF), maxrssMB(syscall.RUSAGE_CHILDREN))
}

func sumPhases(r spikeRun) (d time.Duration) {
	for _, p := range r.phases {
		if !strings.HasPrefix(p.name, "aux") {
			d += p.d
		}
	}
	return d
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
