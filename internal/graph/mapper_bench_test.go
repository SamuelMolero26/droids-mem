package graph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// benchMapperRepo writes an n-file TypeScript tree shaped to exercise the
// mapper tier the way a real repo does, not the way a minimal fixture does.
//
// Every file declares the SAME member names (apply/run/format/cleanup/
// render/update), so the ladder's byName buckets hold n candidates each and
// the walk actually has work to do — a fixture with unique names everywhere
// resolves at rung 3 immediately and measures nothing. Each file also
// imports two siblings under local aliases, so rung 2a has real bindings to
// resolve, and the call mix covers rung 1 (this.x()), rung 2a (Alias.x()),
// and the bare-name path down to rungs 3/4/5.
func benchMapperRepo(b *testing.B, n int) string {
	b.Helper()
	repo := b.TempDir()
	for i := 0; i < n; i++ {
		src := fmt.Sprintf(`import { Service as Svc } from "./mod%d";
import Helper from "./mod%d";

export class Widget%d {
  render(): void { Svc.apply(); this.update(); format(); }
  update(): void { Helper.run(); this.render(); }
  dispose(): void { cleanup(); }
}

export class Service {
  static apply(): void { format(); }
  static run(): void { cleanup(); }
}

export function format(): void { cleanup(); }
export function cleanup(): void {}
export function helper%d(): void { format(); Svc.apply(); }
`, (i+1)%n, (i+7)%n, i, i)
		path := filepath.Join(repo, fmt.Sprintf("mod%d.ts", i))
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	return repo
}

// benchMapperFiles runs discovery once, outside the timed region, for the
// per-pass benchmarks that take a file list rather than a repo path.
func benchMapperFiles(b *testing.B, repo string) []mapperFile {
	b.Helper()
	files, _, err := mapperFiles(repo)
	if err != nil {
		b.Fatal(err)
	}
	if len(files) == 0 {
		b.Fatal("discovery found no mapper files")
	}
	return files
}

// BenchmarkBuildIndex_MapperTier is the metric that actually matters: an
// agent calling graph_symbol on a cold repo waits on exactly this. Go-free
// on purpose — packages.Load would otherwise dominate and hide the mapper
// tier entirely.
func BenchmarkBuildIndex_MapperTier(b *testing.B) {
	for _, n := range []int{50, 200} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			repo := benchMapperRepo(b, n)
			dbPath := filepath.Join(b.TempDir(), "graph.db")
			st, err := stamp(repo)
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				if err := buildIndex(ctx, repo, dbPath, st); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// The four per-pass benchmarks below attribute that total. Each is one
// function over the same discovered file list, so their sum against the
// end-to-end number shows how much of a build is mapper parsing.

func BenchmarkMapperSymbols(b *testing.B) {
	files := benchMapperFiles(b, benchMapperRepo(b, 200))
	b.ReportAllocs()
	for b.Loop() {
		mapperSymbols(files)
	}
}

// BenchmarkMapperCarryScan measures mapperCarry over a tree where NO file is
// broken — the overwhelmingly common case. Nothing is carried, so every
// nanosecond here is the HasError probe re-reading and re-parsing each file
// purely to ask a yes/no question.
func BenchmarkMapperCarryScan(b *testing.B) {
	repo := benchMapperRepo(b, 200)
	files := benchMapperFiles(b, repo)
	syms, _ := mapperSymbols(files)
	dbPath := filepath.Join(b.TempDir(), "graph.db")
	b.ReportAllocs()
	for b.Loop() {
		mapperCarry(dbPath, files, syms)
	}
}

func BenchmarkMapperImports(b *testing.B) {
	files := benchMapperFiles(b, benchMapperRepo(b, 200))
	b.ReportAllocs()
	for b.Loop() {
		mapperImports(files)
	}
}

func BenchmarkCollectMapperCalls(b *testing.B) {
	files := benchMapperFiles(b, benchMapperRepo(b, 200))
	b.ReportAllocs()
	for b.Loop() {
		collectMapperCalls(files)
	}
}

// BenchmarkMapperLadderResolve isolates the ladder from every parse pass:
// collection and attribution happen in setup, so this times only the
// candidate filtering that mapperEdges does per callsite.
func BenchmarkMapperLadderResolve(b *testing.B) {
	repo := benchMapperRepo(b, 200)
	files := benchMapperFiles(b, repo)
	syms, _ := mapperSymbols(files)
	for i := range syms {
		syms[i].row.id = int64(i + 1)
	}
	fileCalls, _ := collectMapperCalls(files)
	callsites := attributeMapperCalls(syms, fileCalls)
	_, bindings, _ := mapperImports(files)
	idx := buildMapperLadderIndex(syms, resolveBindings(files, bindings))
	if len(callsites) == 0 {
		b.Fatal("no callsites attributed")
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, c := range callsites {
			idx.resolve(c)
		}
	}
}
