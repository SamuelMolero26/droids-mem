package graph

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDiscoverNextRoutes_AppRouterInventory(t *testing.T) {
	repo := t.TempDir()
	files := map[string]string{
		"app/page.tsx":                            "export default function Home() { return null }\n",
		"app/(marketing)/about/page.tsx":          "export default function About() { return null }\n",
		"app/blog/[slug]/page.tsx":                "export default function Post() { return null }\n",
		"app/docs/[...parts]/page.jsx":            "export default function Docs() { return null }\n",
		"app/shop/[[...parts]]/page.js":           "export default function Shop() { return null }\n",
		"app/api/users/route.ts":                  "export function GET() {}\n",
		"app/@modal/photo/page.tsx":               "export default function ModalPhoto() { return null }\n",
		"app/photo/page.tsx":                      "export default function Photo() { return null }\n",
		"app/_private/page.tsx":                   "export default function Private() { return null }\n",
		"app/feed/(.)photo/page.tsx":              "export default function Intercepted() { return null }\n",
		"src/app/ignored/page.tsx":                "export default function Ignored() { return null }\n",
		"app/not-a-page/component.tsx":            "export function Component() { return null }\n",
		"app/api/not-a-route/route.tsx":           "export function GET() {}\n",
		"app/(admin)/@drawer/users/[id]/page.tsx": "export default function User() { return null }\n",
	}
	for name, content := range files {
		writeFile(t, repo, name, content)
	}

	mapperFiles, _, err := mapperFiles(repo)
	if err != nil {
		t.Fatal(err)
	}
	scan := scanMapperFiles(mapperFiles)
	for i := range scan.syms {
		scan.syms[i].row.id = int64(i + 1)
	}
	routes := discoverNextRoutes(repo, mapperFiles, scan.syms, scan.defaultExportNames)

	got := make([]string, 0, len(routes))
	for _, route := range routes {
		got = append(got, strings.Join([]string{route.pattern, route.kind, route.file, route.targetQName}, "|"))
	}
	want := []string{
		"/|page|app/page.tsx|app/page:Home",
		"/about|page|app/(marketing)/about/page.tsx|app/(marketing)/about/page:About",
		"/api/users|route|app/api/users/route.ts|app/api/users/route:GET",
		"/blog/[slug]|page|app/blog/[slug]/page.tsx|app/blog/[slug]/page:Post",
		"/docs/[...parts]|page|app/docs/[...parts]/page.jsx|app/docs/[...parts]/page:Docs",
		"/photo|page|app/@modal/photo/page.tsx|app/@modal/photo/page:ModalPhoto",
		"/photo|page|app/photo/page.tsx|app/photo/page:Photo",
		"/shop/[[...parts]]|page|app/shop/[[...parts]]/page.js|app/shop/[[...parts]]/page:Shop",
		"/users/[id]|page|app/(admin)/@drawer/users/[id]/page.tsx|app/(admin)/@drawer/users/[id]/page:User",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("routes =\n%v\nwant\n%v", got, want)
	}
}

func TestDiscoverNextRoutes_UsesSrcAppWhenRootAppIsAbsent(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/app/page.tsx", "export default function Home() { return null }\n")
	writeFile(t, repo, "src/app/api/route.js", "export function GET() {}\n")

	files, _, err := mapperFiles(repo)
	if err != nil {
		t.Fatal(err)
	}
	scan := scanMapperFiles(files)
	for i := range scan.syms {
		scan.syms[i].row.id = int64(i + 1)
	}
	routes := discoverNextRoutes(repo, files, scan.syms, scan.defaultExportNames)
	got := make([]string, 0, len(routes))
	for _, route := range routes {
		got = append(got, route.pattern+"|"+route.file)
	}
	want := []string{"/|src/app/page.tsx", "/api|src/app/api/route.js"}
	if !slices.Equal(got, want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
}

func TestNextNavigation_ProvenanceAliasesAndShadowing(t *testing.T) {
	repo := t.TempDir()
	writeNextPages(t, repo, "/about")
	writeFile(t, repo, "distractors.tsx", `
export function NavLink() { return null }
export function go() {}
export function push() {}
`)
	writeFile(t, repo, "app/source.tsx", `
import NavLink from "next/link";
import { redirect as go, useRouter as makeRouter } from "next/navigation";

export function Imported() {
  const router = makeRouter();
  go("/about");
  router.push("/about");
  return <NavLink href="/about" />;
}

export function Shadowed(NavLink: unknown) {
  function go() {}
  const makeRouter = () => ({ push() {} });
  const router = makeRouter();
  go("/about");
  router.push("/about");
  return <NavLink href="/about" />;
}
`)

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("index: %v", err)
	}

	imported := symbolResponse(t, m, repo, "Imported")
	if len(imported.Destinations) != 3 || len(imported.UnresolvedDestinations) != 0 {
		t.Fatalf("Imported navigation = resolved %v unresolved %v", imported.Destinations, imported.UnresolvedDestinations)
	}
	operations := []string{
		imported.Destinations[0].Operation,
		imported.Destinations[1].Operation,
		imported.Destinations[2].Operation,
	}
	if !slices.Equal(operations, []string{"redirect", "push", "link"}) {
		t.Errorf("operations = %v, want redirect, push, link", operations)
	}
	if got := []string{
		imported.Destinations[0].Evidence,
		imported.Destinations[1].Evidence,
		imported.Destinations[2].Evidence,
	}; !slices.Equal(got, []string{"direct", "direct", "inferred"}) {
		t.Errorf("evidence = %v", got)
	}
	if len(imported.Callees) != 0 {
		t.Errorf("proven Next references leaked into call edges: %v", imported.Callees)
	}

	shadowed := symbolResponse(t, m, repo, "Shadowed")
	if len(shadowed.Destinations) != 0 || len(shadowed.UnresolvedDestinations) != 0 {
		t.Errorf("shadowed local names were treated as Next provenance: %+v", shadowed)
	}
}

// A local binding of any form shadows an outer const of the same name: the
// evaluator must not substitute the outer value for a runtime one.
func TestNextNavigation_PatternBindingsShadowOuterConst(t *testing.T) {
	repo := t.TempDir()
	writeNextPages(t, repo, "/home")
	writeFile(t, repo, "app/source.tsx", `
import { redirect } from "next/navigation";
const href = "/home";

export function Outer() { redirect(href); }
export function ObjectDestructured(props: { href: string }) { const { href } = props; redirect(href); }
export function ArrayDestructured(pair: string[]) { const [href] = pair; redirect(href); }
export function ForOf(list: string[]) { for (const href of list) { redirect(href); } }
export function ForIn(obj: object) { for (const href in obj) { redirect(href); } }
export function ArrowParam() { return (href => redirect(href)); }
export function CatchParam() { try {} catch (href) { redirect(href); } }
`)

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("index: %v", err)
	}

	if outer := symbolResponse(t, m, repo, "Outer"); len(outer.Destinations) != 1 || outer.Destinations[0].Route != "/home" {
		t.Fatalf("control: Outer destinations = %+v, want one /home", outer.Destinations)
	}
	for _, name := range []string{"ObjectDestructured", "ArrayDestructured", "ForOf", "ForIn", "ArrowParam", "CatchParam"} {
		t.Run(name, func(t *testing.T) {
			resp := symbolResponse(t, m, repo, name)
			if len(resp.Destinations) != 0 {
				t.Errorf("shadowed href resolved to outer const: %+v", resp.Destinations)
			}
			if len(resp.UnresolvedDestinations) != 1 || resp.UnresolvedDestinations[0].Reason != "non_literal" {
				t.Errorf("unresolved = %+v, want one non_literal", resp.UnresolvedDestinations)
			}
		})
	}
}

func TestNextNavigation_BoundedDestinationEvaluation(t *testing.T) {
	repo := t.TempDir()
	writeNextPages(t, repo, "/about", "/blog/[slug]", "/products/[id]", "/search")
	writeFile(t, repo, "app/source.tsx", `
import Link from "next/link";
import { permanentRedirect as move, redirect, useRouter } from "next/navigation";

const section = "/blog";
const about = "/about";

export function Send({ slug, choose }: { slug: string, choose: boolean }) {
  const router = useRouter();
  const target = about;
  const product = choose ? "/products/a" : "/products/b";
  redirect(section + "/fixed");
  move(`+"`/blog/fixed`"+`);
  router.push(`+"`/blog/${slug}`"+`);
  router.replace(product);
  return <>
    <Link href={target} />
    <Link href={{ pathname: "/search", query: { q: slug }, hash: "results" }} />
  </>;
}
`)

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("index: %v", err)
	}
	resp := symbolResponse(t, m, repo, "Send")
	if len(resp.UnresolvedDestinations) != 0 {
		t.Fatalf("unexpected unresolved destinations: %v", resp.UnresolvedDestinations)
	}
	if len(resp.Destinations) != 7 {
		t.Fatalf("destinations = %d, want 7: %v", len(resp.Destinations), resp.Destinations)
	}

	type projected struct {
		op, evidence, certainty, destination, route string
	}
	got := make([]projected, 0, len(resp.Destinations))
	for _, destination := range resp.Destinations {
		got = append(got, projected{
			destination.Operation,
			destination.Evidence,
			destination.Certainty,
			destination.Destination,
			destination.Route,
		})
	}
	want := []projected{
		{"redirect", "direct", "matched", "/blog/fixed", "/blog/[slug]"},
		{"permanentRedirect", "direct", "matched", "/blog/fixed", "/blog/[slug]"},
		{"push", "direct", "conditional", "/blog/${slug}", "/blog/[slug]"},
		{"replace", "direct", "matched", "/products/a", "/products/[id]"},
		{"replace", "direct", "matched", "/products/b", "/products/[id]"},
		{"link", "inferred", "matched", "/about", "/about"},
		{"link", "inferred", "matched", "/search", "/search"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("destinations =\n%+v\nwant\n%+v", got, want)
	}
	if !strings.Contains(resp.Destinations[6].RawDestination, "query") ||
		!strings.Contains(resp.Destinations[6].RawDestination, "hash") {
		t.Errorf("href object lost query/hash evidence: %q", resp.Destinations[6].RawDestination)
	}
}

func TestNextNavigation_ClassificationsAndAmbiguity(t *testing.T) {
	repo := t.TempDir()
	writeNextPages(t, repo, "/about")
	writeFile(t, repo, "app/(one)/duplicate/page.tsx", "export default function One() { return null }\n")
	writeFile(t, repo, "app/(two)/duplicate/page.tsx", "export default function Two() { return null }\n")
	writeFile(t, repo, "app/source.tsx", `
import { redirect } from "next/navigation";

export function Classify() {
  redirect("https://example.com/a");
  redirect("//example.com/a");
  redirect("?tab=one");
  redirect("#top");
  redirect("/missing");
  redirect("/duplicate");
  redirect(makeDestination());
}
`)

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("index: %v", err)
	}
	resp := symbolResponse(t, m, repo, "Classify")
	if len(resp.Destinations) != 0 {
		t.Fatalf("unexpected resolved destinations: %v", resp.Destinations)
	}
	reasons := make([]string, 0, len(resp.UnresolvedDestinations))
	for _, destination := range resp.UnresolvedDestinations {
		reasons = append(reasons, destination.Reason)
	}
	want := []string{
		"external",
		"external",
		"same_route",
		"same_route",
		"no_matching_route",
		"ambiguous_route",
		"unsupported_expression",
	}
	if !slices.Equal(reasons, want) {
		t.Fatalf("reasons = %v, want %v", reasons, want)
	}
}

func TestNextNavigation_RebuildDropsResolvedTargets(t *testing.T) {
	repo := t.TempDir()
	writeNextPages(t, repo, "/about")
	writeFile(t, repo, "app/source.tsx", `
import { redirect } from "next/navigation";
export function Send() { redirect("/about"); }
`)

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	if got := symbolResponse(t, m, repo, "Send"); len(got.Destinations) != 1 {
		t.Fatalf("initial destinations = %v", got.Destinations)
	}

	writeFile(t, repo, "app/source.tsx", `
import { redirect } from "next/navigation";
export function Send() { redirect(makeDestination()); }
`)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("unresolved rebuild: %v", err)
	}
	got := symbolResponse(t, m, repo, "Send")
	if len(got.Destinations) != 0 || len(got.UnresolvedDestinations) != 1 ||
		got.UnresolvedDestinations[0].Reason != "unsupported_expression" {
		t.Fatalf("resolved target survived unresolved rebuild: %+v", got)
	}

	writeFile(t, repo, "app/source.tsx", `
import { redirect } from "next/navigation";
export function Send() { redirect("/about"); }
`)
	if err := os.Remove(filepath.Join(repo, "app", "about", "page.tsx")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("route deletion rebuild: %v", err)
	}
	got = symbolResponse(t, m, repo, "Send")
	if len(got.Destinations) != 0 || len(got.UnresolvedDestinations) != 1 ||
		got.UnresolvedDestinations[0].Reason != "no_matching_route" {
		t.Fatalf("deleted route survived rebuild: %+v", got)
	}
}

func TestNextNavigation_DoesNotChangeCallGraphSemantics(t *testing.T) {
	repo := t.TempDir()
	writeNextPages(t, repo, "/about")
	writeFile(t, repo, "distractors.tsx", `
export function Link() { return null }
export function redirect() {}
export function push() {}
`)
	writeFile(t, repo, "helper.ts", "export function helper() {}\n")
	writeFile(t, repo, "app/source.tsx", `
import Link from "next/link";
import { redirect, useRouter } from "next/navigation";
import { helper } from "../helper";

export function Send() {
  const router = useRouter();
  helper();
  redirect("/about");
  router.push("/about");
  return <Link href="/about" />;
}
`)

	conn := buildImportTestDB(t, repo)
	if got := importTestEdgeTargets(t, conn, "Send"); !slices.Equal(got, []string{"helper.ts:helper"}) {
		t.Fatalf("Send call edges = %v, want only helper", got)
	}
	var edgeCount, navigationCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM edges`).Scan(&edgeCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM navigations`).Scan(&navigationCount); err != nil {
		t.Fatal(err)
	}
	if edgeCount != 1 || navigationCount != 3 {
		t.Fatalf("edges=%d navigations=%d, want 1 and 3", edgeCount, navigationCount)
	}

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("manager index: %v", err)
	}
	helper := symbolResponse(t, m, repo, "helper")
	if helper.TransitiveCallers == nil || *helper.TransitiveCallers != 1 {
		t.Fatalf("helper transitive_callers = %v, want 1", helper.TransitiveCallers)
	}
	for _, name := range []string{"Link", "redirect", "push"} {
		resp := symbolResponse(t, m, repo, "distractors:"+name)
		if len(resp.Callers) != 0 || resp.TransitiveCallers == nil || *resp.TransitiveCallers != 0 {
			t.Errorf("%s call graph changed by navigation: callers=%v transitive=%v", name, resp.Callers, resp.TransitiveCallers)
		}
	}

	pathResp, err := m.Symbol(context.Background(), SymbolRequest{
		Repo: repo, Symbol: "Send", To: "helper",
	})
	if err != nil || len(pathResp.Path) != 2 {
		t.Fatalf("existing call path changed: path=%v err=%v", pathResp.Path, err)
	}
	_, err = m.Symbol(context.Background(), SymbolRequest{
		Repo: repo, Symbol: "Send", To: "distractors:redirect",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("navigation created a call path: %v", err)
	}
}

func TestNextNavigation_AnonymousPageStillResolvesByRouteAndFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app/about/page.tsx", "export default function() { return null }\n")
	writeFile(t, repo, "app/source.tsx", `
import { redirect } from "next/navigation";
export function Send() { redirect("/about"); }
`)

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("index: %v", err)
	}
	resp := symbolResponse(t, m, repo, "Send")
	if len(resp.Destinations) != 1 {
		t.Fatalf("destinations = %v", resp.Destinations)
	}
	destination := resp.Destinations[0]
	if destination.Route != "/about" || destination.TargetFile != "app/about/page.tsx" || destination.TargetQName != "" {
		t.Errorf("anonymous target = %+v", destination)
	}
}

func TestNavigationRows_OldSchemaReturnsNoRows(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE symbols (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	resolved, unresolved, truncated, err := navigationRows(context.Background(), db, 1)
	if err != nil || len(resolved) != 0 || len(unresolved) != 0 || truncated {
		t.Fatalf("old schema navigationRows = %v %v %v, err %v", resolved, unresolved, truncated, err)
	}
}

func writeNextPages(t *testing.T, repo string, patterns ...string) {
	t.Helper()
	for i, pattern := range patterns {
		dir := strings.TrimPrefix(pattern, "/")
		if dir == "" {
			dir = "."
		}
		name := "Page" + strings.Repeat("X", i)
		writeFile(t, filepath.Join(repo, "app", filepath.FromSlash(dir)), "page.tsx",
			"export default function "+name+"() { return null }\n")
	}
}

func symbolResponse(t *testing.T, m *Manager, repo, symbol string) *SymbolResponse {
	t.Helper()
	resp, err := m.Symbol(context.Background(), SymbolRequest{
		Repo: repo, Symbol: symbol, Direction: "both",
	})
	if err != nil {
		t.Fatalf("Symbol(%s): %v", symbol, err)
	}
	return resp
}
