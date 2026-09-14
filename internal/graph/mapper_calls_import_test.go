package graph

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBuildIndex_ImportScopedBareNamespaceAndJSX(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "tsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./*"]}}}`)
	writeFile(t, filepath.Join(repo, "lib"), "utils.ts", "export function cn(): string { return ''; }\n")
	writeFile(t, filepath.Join(repo, "components"), "named_button.tsx", "export function NamedButton() { return null; }\n")
	writeFile(t, filepath.Join(repo, "components"), "default_button.tsx", "export default function DefaultButton() { return null; }\n")
	writeFile(t, filepath.Join(repo, "services"), "client.ts", "export class Client { static get(): void {} }\n")
	writeFile(t, repo, "distractors.ts", `
export function cn(): string { return '' }
export function merge(): string { return '' }
export function NamedButton() { return null }
export function DefaultButton() { return null }
export class utils { static cn(): string { return '' } }
export class API { static get(): void {} }
`)
	writeFile(t, filepath.Join(repo, "app"), "page.tsx", `
import { cn } from "@/lib/utils";
import { cn as merge } from "@/lib/utils";
import { NamedButton } from "@/components/named_button";
import DefaultButton from "@/components/default_button";
import PrimaryButton from "@/components/default_button";
import * as utils from "@/lib/utils";
import { Client as API } from "@/services/client";

export function NamedBareCall() { return cn(); }
export function AliasedBareCall() { return merge(); }
export function DefaultBareCall() { return DefaultButton(); }
export function RenamedDefaultBareCall() { return PrimaryButton(); }
export function NamedJSXCall() { return <NamedButton />; }
export function DefaultJSXCall() { return <DefaultButton />; }
export function RenamedDefaultJSXCall() { return <PrimaryButton />; }
export function NamespaceCall() { return utils.cn(); }
export function ClassAliasCall() { return API.get(); }
`)

	conn := buildImportTestDB(t, repo)
	cases := []struct {
		caller string
		want   []string
	}{
		{"NamedBareCall", []string{"lib/utils.ts:cn"}},
		{"AliasedBareCall", []string{"lib/utils.ts:cn"}},
		{"DefaultBareCall", []string{"components/default_button.tsx:DefaultButton"}},
		{"RenamedDefaultBareCall", []string{"components/default_button.tsx:DefaultButton"}},
		{"NamedJSXCall", []string{"components/named_button.tsx:NamedButton"}},
		{"DefaultJSXCall", []string{"components/default_button.tsx:DefaultButton"}},
		{"RenamedDefaultJSXCall", []string{"components/default_button.tsx:DefaultButton"}},
		{"NamespaceCall", []string{"lib/utils.ts:cn"}},
		{"ClassAliasCall", []string{"services/client.ts:get"}},
	}
	for _, tc := range cases {
		t.Run(tc.caller, func(t *testing.T) {
			if got := importTestEdgeTargets(t, conn, tc.caller); !slices.Equal(got, tc.want) {
				t.Errorf("edges from %s = %v, want %v", tc.caller, got, tc.want)
			}
		})
	}

	var fanoutCapped string
	if err := conn.QueryRow(`SELECT value FROM meta WHERE key = 'fanout_capped'`).Scan(&fanoutCapped); err != nil {
		t.Fatal(err)
	}
	if fanoutCapped != "0" {
		t.Errorf("fanout_capped = %q, want 0", fanoutCapped)
	}
}

func TestBuildIndex_ImportScopedBareLocalShadowAndFallback(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "tsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./*"]}}}`)
	writeFile(t, filepath.Join(repo, "lib"), "local.ts", "export function localFn(): string { return 'imported'; }\nexport function scopedFn(): string { return 'imported'; }\n")
	writeFile(t, filepath.Join(repo, "app"), "local.ts", `
import { localFn, scopedFn } from "@/lib/local";
export function LocalShadowCall() {
  function localFn(): string { return "local"; }
  return localFn();
}
export function OtherScopeCall() {
  function scopedFn(): string { return "local"; }
  return scopedFn();
}
export function ImportedOutsideShadowCall() { return scopedFn(); }
`)
	writeFile(t, filepath.Join(repo, "fallback"), "a.ts", "export function fallback(): void {}\nexport function missing(): void {}\n")
	writeFile(t, filepath.Join(repo, "fallback"), "b.ts", "export function fallback(): void {}\nexport function missing(): void {}\n")
	writeFile(t, filepath.Join(repo, "app"), "fallback.ts", `
import { missing } from "@/not-found";
export function NoImportCall() { fallback(); }
export function UnresolvedAliasCall() { missing(); }
`)

	conn := buildImportTestDB(t, repo)
	cases := []struct {
		caller string
		want   []string
	}{
		{"LocalShadowCall", []string{"app/local.ts:localFn"}},
		{"OtherScopeCall", []string{"app/local.ts:scopedFn"}},
		{"ImportedOutsideShadowCall", []string{"lib/local.ts:scopedFn"}},
		{"NoImportCall", []string{"fallback/a.ts:fallback", "fallback/b.ts:fallback"}},
		{"UnresolvedAliasCall", []string{"fallback/a.ts:missing", "fallback/b.ts:missing"}},
	}
	for _, tc := range cases {
		t.Run(tc.caller, func(t *testing.T) {
			if got := importTestEdgeTargets(t, conn, tc.caller); !slices.Equal(got, tc.want) {
				t.Errorf("edges from %s = %v, want %v", tc.caller, got, tc.want)
			}
		})
	}
}

func buildImportTestDB(t *testing.T, repo string) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st); err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func importTestEdgeTargets(t *testing.T, conn *sql.DB, caller string) []string {
	t.Helper()
	rows, err := conn.Query(`SELECT s2.file, s2.name FROM edges e
		JOIN symbols s1 ON s1.id = e.caller
		JOIN symbols s2 ON s2.id = e.callee
		WHERE s1.name = ? ORDER BY s2.file, s2.name`, caller)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var targets []string
	for rows.Next() {
		var file, name string
		if err := rows.Scan(&file, &name); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, fmt.Sprintf("%s:%s", file, name))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return targets
}
