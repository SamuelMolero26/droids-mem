package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// benchMapperRepoTSX mirrors benchMapperRepo but emits .tsx files dense in
// JSX component uses — the shape jsxCallRefs exists for.
//
// benchMapperRepo writes plain .ts, so it times the JSX walk over trees that
// never match: it measures the walk's overhead and nothing the walk produces.
// This fixture is the one that measures the feature. Every file uses the SAME
// component names (Layout/Header/Button/Card/FadeIn), so the ladder's byName
// buckets hold n candidates each for JSX refs exactly as they do for calls,
// and dotted (<Svc.Panel />) plus bare (<Button />) tags cover both the
// receiver and receiver-free rungs. Lowercase html tags are included on
// purpose — jsxCallRefs skips them, and skipping is per-node work that a
// real component tree pays constantly.
func benchMapperRepoTSX(b *testing.B, n int) string {
	b.Helper()
	repo := b.TempDir()
	for i := range n {
		src := fmt.Sprintf(`import { Service as Svc } from "./mod%d";
import Helper from "./mod%d";

export function Widget%d(): JSX.Element {
  return (
    <Layout>
      <Header title="x" />
      <Svc.Panel />
      <Helper.Row />
      <Button onClick={() => format()} />
      <FadeIn>
        <Card />
        <div className="row"><span>text</span></div>
      </FadeIn>
    </Layout>
  );
}

export function Layout(): JSX.Element { return <div />; }
export function Header(): JSX.Element { return <div />; }
export function Button(): JSX.Element { return <Card />; }
export function Card(): JSX.Element { return <div />; }
export function FadeIn(): JSX.Element { return <div />; }
export function format(): void {}
`, (i+1)%n, (i+7)%n, i)
		path := filepath.Join(repo, fmt.Sprintf("mod%d.tsx", i))
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	return repo
}

func BenchmarkCollectMapperCalls_JSX(b *testing.B) {
	files := benchMapperFiles(b, benchMapperRepoTSX(b, 200))
	b.ReportAllocs()
	for b.Loop() {
		collectMapperCalls(files)
	}
}

// BenchmarkMapperLadderResolve_JSX is the one that can regress non-linearly:
// each JSX tag becomes a callsite, so a component-dense tree feeds the ladder
// several times the callsites a plain .ts tree does, against byName buckets
// of the same depth.
func BenchmarkMapperLadderResolve_JSX(b *testing.B) {
	repo := benchMapperRepoTSX(b, 200)
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
	b.Logf("callsites=%d syms=%d", len(callsites), len(syms))
	b.ReportAllocs()
	for b.Loop() {
		for _, c := range callsites {
			idx.resolve(c)
		}
	}
}

func BenchmarkBuildIndex_MapperTier_JSX(b *testing.B) {
	repo := benchMapperRepoTSX(b, 200)
	dbPath := filepath.Join(b.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		b.Fatal(err)
	}
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if err := buildIndex(ctx, repo, dbPath, st); err != nil {
			b.Fatal(err)
		}
	}
}
