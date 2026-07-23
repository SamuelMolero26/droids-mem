package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/db"
	"github.com/samuelmolero26/droids-mem/internal/store"
	_ "modernc.org/sqlite"
)

func newHomeTestStore(t *testing.T) *store.Store {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Init(conn); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return store.New(conn)
}

func TestHomeView_EmptyCorpusIsDefinitive(t *testing.T) {
	s := newHomeTestStore(t)
	v, err := homeView(context.Background(), s)
	if err != nil {
		t.Fatalf("homeView: %v", err)
	}
	if v.Total != 0 || len(v.TaskTypes) != 0 {
		t.Fatalf("want empty corpus, got total=%d types=%d", v.Total, len(v.TaskTypes))
	}
	if len(v.Help) != 1 || !strings.Contains(v.Help[0], "No memories yet") {
		t.Fatalf("empty state must say the zero and point at save, got %v", v.Help)
	}
	if v.Description == "" || v.Bin == "" {
		t.Fatalf("home view must self-identify (bin+description), got %+v", v)
	}
}

func TestHomeView_ShowsLiveContent(t *testing.T) {
	s := newHomeTestStore(t)
	_, err := s.Save(context.Background(), store.SaveRequest{
		TaskType: "myproj", Kind: "task_pattern",
		Title: "Alpha lesson on caching", What: "did a thing", Learned: "cache the fetcher",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	v, err := homeView(context.Background(), s)
	if err != nil {
		t.Fatalf("homeView: %v", err)
	}
	if v.Total != 1 || len(v.TaskTypes) != 1 || v.TaskTypes[0].TaskType != "myproj" {
		t.Fatalf("want live census with myproj, got %+v", v)
	}
	if len(v.Help) == 0 {
		t.Fatalf("non-empty corpus should still offer next steps")
	}
}

func TestGraphTarget(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		flag    string
		want    string
		wantErr bool
	}{
		{"positional", []string{"internal/store"}, "", "internal/store", false},
		{"flag", nil, "internal/store", "internal/store", false},
		{"both conflict", []string{"a"}, "b", "", true},
		{"neither", nil, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := graphTarget(c.args, c.flag, "package")
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestCollapseHome(t *testing.T) {
	if got := collapseHome("/nowhere/at/all"); got != "/nowhere/at/all" {
		t.Fatalf("non-home path must pass through, got %q", got)
	}
}
