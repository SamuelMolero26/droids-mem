#!/usr/bin/env bash
# gen_fixtures.sh — regenerates the golden per-version schema fixtures
# (schema_v0.sql … schema_v8.sql) in this directory.
#
# Each fixture is the sqlite_master dump of a database replayed through the
# first N ladder rungs, plus a trailing `PRAGMA user_version = N;` stamp so a
# test that loads it starts the ladder at exactly rung N. schema_v0.sql is the
# pre-v1.0 shape itself; schema_v1.sql … schema_v8.sql are the successive
# rung-replay states. The v7 fixture is the pre-flip shipped shape: the FTS
# tokenizer stays trigram because the porter flip lands only at rung 7→8.
# schema_v8.sql is the post-flip shape (porter FTS, no authored_at) — the
# version real installs actually sit at, and therefore the starting point of
# the only upgrade path most users will ever run, rung 8→9.
# (The 4-column recency indexes with id DESC land at rung 6→7, ADR-0033, so
# schema_v7.sql carries trigram FTS + the 4-column indexes.)
#
# The inline rung SQL below duplicates the Go consts in ../migrations.go.
# This duplication is deliberate and guarded: TestInit_FreshMatchesMigratedShape
# replays every fixture through the GO ladder and byte-compares the result
# against a fresh DB, so any drift between this script and the Go consts fails
# that test. Keep the two in sync.
#
# Idempotent: every run builds from a fresh temp DB, so rerunning produces
# byte-identical fixtures.
set -euo pipefail

cd "$(dirname "$0")"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
db="$work/gen.db"

apply_preV1() {
    sqlite3 "$db" <<'SQL'
CREATE TABLE memories (
    id          TEXT    PRIMARY KEY,
    session_id  TEXT    NOT NULL,
    task_type   TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK(kind IN ('error_resolution','task_pattern','user_rule','session_summary')),
    title       TEXT    NOT NULL,
    what        TEXT    NOT NULL,
    learned     TEXT    NOT NULL,
    tags        TEXT    NOT NULL DEFAULT '',
    fingerprint TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    CHECK(updated_at >= created_at)
);
CREATE UNIQUE INDEX idx_memories_fingerprint ON memories(fingerprint);
CREATE INDEX idx_memories_task_type ON memories(task_type);
CREATE INDEX idx_memories_kind ON memories(kind);
CREATE INDEX idx_memories_task_kind_created ON memories(task_type, kind, created_at DESC);
CREATE INDEX idx_memories_created_at ON memories(created_at DESC);
CREATE VIRTUAL TABLE memories_fts USING fts5(
    title, what, learned, tags,
    content='memories', content_rowid='rowid', tokenize='trigram'
);
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
END;
CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;
SQL
}

apply_rung01() {
    sqlite3 "$db" <<'SQL'
ALTER TABLE memories ADD COLUMN scope TEXT NOT NULL DEFAULT 'personal'
    CHECK(scope IN ('personal','shared'));
ALTER TABLE memories ADD COLUMN scrub_pattern_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE memories ADD COLUMN scrub_counts TEXT;

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
SQL
}

apply_rung12() {
    sqlite3 "$db" <<'SQL'
ALTER TABLE memories ADD COLUMN expand_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memories ADD COLUMN last_expanded_at INTEGER;

DROP TRIGGER IF EXISTS memories_au;
CREATE TRIGGER memories_au
AFTER UPDATE OF title, what, learned, tags ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;
SQL
}

apply_rung23() {
    sqlite3 "$db" <<'SQL'
ALTER TABLE memories ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual'
    CHECK(origin IN ('manual','auto'));
CREATE INDEX IF NOT EXISTS idx_memories_origin_created ON memories(origin, created_at DESC);
SQL
}

apply_rung34() {
    sqlite3 "$db" <<'SQL'
CREATE TABLE IF NOT EXISTS memory_files (
    session_id TEXT    NOT NULL,
    file_path  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (session_id, file_path)
);
SQL
}

apply_rung45() {
    sqlite3 "$db" <<'SQL'
UPDATE memories SET scope = 'personal';
SQL
}

apply_rung56() {
    sqlite3 "$db" <<'SQL'
ALTER TABLE memories ADD COLUMN review_after INTEGER;
ALTER TABLE memories ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS archived_memories (
    id                    TEXT    PRIMARY KEY,
    session_id            TEXT    NOT NULL,
    task_type             TEXT    NOT NULL,
    kind                  TEXT    NOT NULL,
    title                 TEXT    NOT NULL,
    what                  TEXT    NOT NULL,
    learned               TEXT    NOT NULL,
    tags                  TEXT    NOT NULL DEFAULT '',
    fingerprint           TEXT    NOT NULL,
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    scope                 TEXT    NOT NULL DEFAULT 'personal',
    scrub_pattern_version INTEGER NOT NULL DEFAULT 1,
    scrub_counts          TEXT,
    expand_count          INTEGER NOT NULL DEFAULT 0,
    last_expanded_at      INTEGER,
    origin                TEXT    NOT NULL DEFAULT 'manual',
    review_after          INTEGER,
    pinned                INTEGER NOT NULL DEFAULT 0,
    archived_at           INTEGER NOT NULL
);
SQL
}

apply_rung67() {
    sqlite3 "$db" <<'SQL'
DROP INDEX IF EXISTS idx_memories_task_kind_created;
CREATE INDEX idx_memories_task_kind_created ON memories(task_type, kind, created_at DESC, id DESC);

DROP INDEX IF EXISTS idx_memories_created_at;
CREATE INDEX idx_memories_created_at ON memories(created_at DESC, id DESC);

DROP INDEX IF EXISTS idx_memories_origin_created;
CREATE INDEX idx_memories_origin_created ON memories(origin, created_at DESC, id DESC);

DROP INDEX IF EXISTS idx_memories_task_type;
SQL
}

apply_rung78() {
    sqlite3 "$db" <<'SQL'
DROP TRIGGER IF EXISTS memories_ai;
DROP TRIGGER IF EXISTS memories_ad;
DROP TRIGGER IF EXISTS memories_au;
DROP TABLE IF EXISTS memories_fts;

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    title,
    what,
    learned,
    tags,
    content='memories',
    content_rowid='rowid',
    tokenize='porter unicode61 tokenchars ''_-'''
);

CREATE TRIGGER IF NOT EXISTS memories_ai
AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;

CREATE TRIGGER IF NOT EXISTS memories_ad
AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
END;

CREATE TRIGGER IF NOT EXISTS memories_au
AFTER UPDATE OF title, what, learned, tags ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;

INSERT INTO memories_fts(rowid, title, what, learned, tags)
SELECT rowid, title, what, learned, tags FROM memories;
SQL
}

# dump_fixture writes the current sqlite_master as re-executable SQL plus the
# user_version stamp. ORDER BY reproduces a loadable file: user objects first,
# then indexes, then triggers. Excluded: the FTS5 shadow tables
# (memories_fts_data/_idx/_content/_docsize/_config) — their CREATE statements
# are internal and cannot be replayed ("object name reserved for internal
# use") — and sqlite_autoindex rows (NULL sql). A replayed fixture recreates
# the shadow tables implicitly when its CREATE VIRTUAL TABLE runs.
dump_fixture() { # $1 = target file, $2 = user_version
    {
        sqlite3 "$db" "SELECT sql || ';' FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'memories_fts_%' AND name NOT LIKE 'sqlite_autoindex%' ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'index' THEN 1 ELSE 2 END, name;"
        printf 'PRAGMA user_version = %d;\n' "$2"
    } > "$1"
}

apply_preV1
dump_fixture schema_v0.sql 0
apply_rung01
dump_fixture schema_v1.sql 1
apply_rung12
dump_fixture schema_v2.sql 2
apply_rung23
dump_fixture schema_v3.sql 3
apply_rung34
dump_fixture schema_v4.sql 4
apply_rung45
dump_fixture schema_v5.sql 5
apply_rung56
dump_fixture schema_v6.sql 6
apply_rung67
dump_fixture schema_v7.sql 7
apply_rung78
dump_fixture schema_v8.sql 8
