package graph

import (
	"context"
	"path/filepath"
	"testing"
)

// TestJSX_ComponentCallersAndTransitiveCallers is the ground-truth pin for
// the JSX visitor: mapper symbols are already correct (Button, FadeIn,
// Container via tags query), but before the visitor transitive_callers was
// systematically 0 because jsx_element never produced a callsite. After the
// visitor each component must have callers and a non-zero transitive count,
// and depth must be material.
func TestJSX_ComponentCallersAndTransitiveCallers(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "app/components/ui"), "button.tsx", "export function Button() { return null; }\n")
	writeFile(t, filepath.Join(repo, "app/components/ui"), "fade_in.tsx", "export function FadeIn(props: any) { return null; }\n")
	writeFile(t, filepath.Join(repo, "app/components/ui"), "container.tsx", "export function Container(props: any) { return null; }\n")
	writeFile(t, filepath.Join(repo, "app/pages"), "home.tsx", `
import { Button } from "../components/ui/button";
import { FadeIn } from "../components/ui/fade_in";
import { Container } from "../components/ui/container";

export function Home() {
  return (
    <Container>
      <FadeIn><Button /></FadeIn>
      <Button />
    </Container>
  );
}
export function Other() {
  return <Button />;
}
`)
	writeFile(t, filepath.Join(repo, "app/pages"), "about.tsx", `
import { Button } from "../components/ui/button";
export function About() { return <Button />; }
`)

	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Button is used in Home (twice, nested + sibling) + Other + About = 3 distinct callers
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Button", Direction: "up", Depth: 1})
	if err != nil {
		t.Fatalf("Symbol Button: %v", err)
	}
	if resp.Symbol == nil {
		t.Fatal("Button symbol missing")
	}
	if len(resp.Callers) == 0 {
		t.Fatalf("Button callers empty, want >=3 (Home, Other, About) — JSX visitor not wired")
	}
	if resp.TransitiveCallers == nil || *resp.TransitiveCallers == 0 {
		t.Fatalf("Button transitive_callers = %v, want >0 — JSX edges missing", resp.TransitiveCallers)
	}
	if len(resp.Callers) != 3 {
		t.Errorf("Button callers = %d, want 3 distinct callers (Home, Other, About); got %+v", len(resp.Callers), resp.Callers)
	}
	// depth=3 must still be non-empty and tc must match depth=1 for this shallow graph
	resp3, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Button", Direction: "up", Depth: 3})
	if err != nil {
		t.Fatalf("Symbol Button depth3: %v", err)
	}
	if len(resp3.Callers) == 0 {
		t.Error("Button depth=3 callers empty, want same as depth=1")
	}
	if resp3.TransitiveCallers == nil || *resp3.TransitiveCallers == 0 {
		t.Error("Button depth=3 transitive_callers 0")
	}

	// FadeIn and Container each used once in Home
	for _, name := range []string{"FadeIn", "Container"} {
		r, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: name, Direction: "up", Depth: 1})
		if err != nil {
			t.Fatalf("Symbol %s: %v", name, err)
		}
		if len(r.Callers) == 0 {
			t.Errorf("%s callers empty, want 1 (Home)", name)
		}
		if r.TransitiveCallers == nil || *r.TransitiveCallers == 0 {
			t.Errorf("%s transitive_callers = %v, want >0", name, r.TransitiveCallers)
		}
		if len(r.Callers) != 1 || r.Callers[0].QName != "app/pages/home:Home" {
			t.Errorf("%s callers = %+v, want exactly Home", name, r.Callers)
		}
	}

	// path Home -> Button must exist (BFS over mapper edges)
	pathResp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Home", To: "Button"})
	if err != nil {
		t.Fatalf("path Home->Button: %v", err)
	}
	if len(pathResp.Path) == 0 {
		t.Fatalf("path Home->Button empty, want at least [Home, Button]")
	}
}

// TestJSX_HtmlTagNotCounted ensures lowercase html tags never produce edges
// even when a same-named function exists — the visitor must filter them.
func TestJSX_HtmlTagNotCounted(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "app"), "comp.tsx", "export function div() {}\n")
	writeFile(t, filepath.Join(repo, "app/pages"), "home.tsx", `
import { div } from "../comp";
export function Home() { return <div><span>hi</span></div>; }
export function UseDiv() { return div(); }
`)
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("Index: %v", err)
	}
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "div", Direction: "up"})
	if err != nil {
		t.Fatalf("Symbol div: %v", err)
	}
	if len(resp.Callers) != 1 {
		t.Errorf("div callers = %d, want 1 (UseDiv via call_expression only, not Home via <div>); got %+v", len(resp.Callers), resp.Callers)
	} else if resp.Callers[0].QName != "app/pages/home:UseDiv" {
		t.Errorf("div caller = %q, want UseDiv", resp.Callers[0].QName)
	}
}

// TestJSX_MemberExpression captures <Foo.Bar /> as Name=Bar Receiver=Foo.
// Bar is a free function, so rung 0 falls through and repo-wide resolves it.
func TestJSX_MemberExpression(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "app/ui"), "foo.tsx", "export function Bar() {}\n")
	writeFile(t, filepath.Join(repo, "app/pages"), "use.tsx", `
import * as Foo from "../ui/foo";
export function Use() { return <Foo.Bar />; }
`)
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("Index: %v", err)
	}
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Bar", Direction: "up"})
	if err != nil {
		t.Fatalf("Bar: %v", err)
	}
	if len(resp.Callers) == 0 {
		t.Fatalf("Bar callers empty, want 1 via <Foo.Bar />")
	}
	if resp.Callers[0].QName != "app/pages/use:Use" {
		t.Errorf("Bar caller = %q, want Use", resp.Callers[0].QName)
	}
}

// TestJSX_SelfClosingAndOpeningBothCount verifies that both
// <Foo> (opening) and <Foo /> (self-closing) are counted.
func TestJSX_SelfClosingAndOpeningBothCount(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "app/components"), "button.tsx", "export function Button() {}\n")
	writeFile(t, filepath.Join(repo, "app/pages"), "home.tsx", `
import { Button } from "../components/button";
export function Home() { return <Button></Button>; }
export function Home2() { return <Button />; }
`)
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("Index: %v", err)
	}
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Button", Direction: "up"})
	if err != nil {
		t.Fatalf("Button: %v", err)
	}
	if len(resp.Callers) != 2 {
		t.Errorf("Button callers = %d, want 2 (Home via opening, Home2 via self-closing); got %+v", len(resp.Callers), resp.Callers)
	}
}

// TestJSX_JavaScriptFile ensures .jsx is handled the same as .tsx.
func TestJSX_JavaScriptFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "app/components"), "button.jsx", "export function Button() {}\n")
	writeFile(t, filepath.Join(repo, "app/pages"), "home.jsx", `
import { Button } from "../components/button";
export function Home() { return <Button />; }
`)
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("Index: %v", err)
	}
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Button", Direction: "up"})
	if err != nil {
		t.Fatalf("Button: %v", err)
	}
	if len(resp.Callers) == 0 {
		t.Fatalf("jsx Button callers empty")
	}
}
