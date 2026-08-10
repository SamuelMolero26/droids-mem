package db

import (
	"strings"
	"testing"
)

// TestMigrations_RungsDoNotEmbedLiveSchema pins the immutability of shipped
// ladder rungs: a rung is history, so the SQL a given user_version transition
// executes must never change. Composing a rung from a live schema.go const
// (ddlTables, FTSSchema, ddlMeta) breaks that — the next edit to the live
// definition silently rewrites the rung for every database still sitting on
// the earlier version, and if that same edit also ships as a new rung, the
// older path applies it twice.
//
// TestInit_FreshMatchesMigratedShape does not cover this: it asserts
// convergence of the final shape, and a live const moves both the fresh and
// the migrated path together, so the two stay equal while the rung's history
// silently rewrites itself.
//
// Fix when this fails: copy the DDL text into migrations.go, frozen at the
// shape that rung shipped with. Do not "fix" it by editing the live const back.
func TestMigrations_RungsDoNotEmbedLiveSchema(t *testing.T) {
	live := map[string]string{
		"ddlTables": ddlTables,
		"FTSSchema": FTSSchema,
		"ddlMeta":   ddlMeta,
	}
	for _, m := range migrations {
		for name, def := range live {
			if strings.Contains(m.sql, def) {
				t.Errorf("rung %d→%d embeds the live schema.go const %s; freeze the DDL text in migrations.go instead",
					m.from, m.to, name)
			}
		}
	}
}
