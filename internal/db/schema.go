package db

// ddl defines all tables, indexes, and triggers for a fresh v1.0 DB.
// Executed once at DB init for new databases. All statements are idempotent
// (IF NOT EXISTS). Existing databases are upgraded via the migration ladder
// in migrations.go — do NOT add ALTER TABLE here.
//
// memories uses INTEGER rowid implicitly (TEXT PRIMARY KEY id is separate).
// FTS5 external-content sync (memories_fts) keys on this rowid. Do NOT use
// INSERT OR REPLACE or REPLACE INTO on memories — those reassign rowid and
// silently desync the FTS index. Use ON CONFLICT DO UPDATE for upserts.
const ddl = `
CREATE TABLE IF NOT EXISTS memories (
    id                    TEXT    PRIMARY KEY,
    session_id            TEXT    NOT NULL,
    task_type             TEXT    NOT NULL,
    kind                  TEXT    NOT NULL CHECK(kind IN ('error_resolution','task_pattern','user_rule','session_summary')),
    title                 TEXT    NOT NULL,
    what                  TEXT    NOT NULL,
    learned               TEXT    NOT NULL,
    tags                  TEXT    NOT NULL DEFAULT '',
    fingerprint           TEXT    NOT NULL,
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    scope                 TEXT    NOT NULL DEFAULT 'shared' CHECK(scope IN ('personal','shared')),
    scrub_pattern_version INTEGER NOT NULL DEFAULT 1,
    scrub_counts          TEXT,
    CHECK(updated_at >= created_at)
);

-- meta holds singleton key/value markers (e.g. scrub_baseline_complete).
-- Used by the boot gate to refuse startup against pre-scrub databases that
-- have not yet been rescrubbed or explicitly acknowledged. Fresh v1.0 DBs
-- set scrub_baseline_complete=1 at init since there are no pre-scrub rows.
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_fingerprint ON memories(fingerprint);
CREATE INDEX IF NOT EXISTS idx_memories_task_type         ON memories(task_type);
CREATE INDEX IF NOT EXISTS idx_memories_kind              ON memories(kind);
-- idx_memories_task_kind_created composite covers leftmost-prefix (task_type)
-- and (task_type, kind) lookups AND eliminates the ORDER BY created_at DESC
-- sort step for the session_summary prune (save.go), fetchLastSession,
-- fetchAllUserRules. (Prior idx_memories_task_kind dropped in earlier
-- migration — DROP line removed v1.0 per perf-engineer rec #5.)
CREATE INDEX IF NOT EXISTS idx_memories_task_kind_created ON memories(task_type, kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_memories_created_at        ON memories(created_at DESC);

-- FTS5 tokenizer (locked decision #17): unicode61 with underscore + hyphen
-- promoted to token chars so identifiers like snake_case and kebab-case stay
-- atomic. Existing pre-v1.0 databases keep their trigram FTS until the
-- operator runs 'droids-mem migrate --rescrub', which drops + recreates this
-- table with the same DDL.
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    title,
    what,
    learned,
    tags,
    content='memories',
    content_rowid='rowid',
    tokenize='unicode61 tokenchars ''_-'''
);

-- FTS sync: INSERT
CREATE TRIGGER IF NOT EXISTS memories_ai
AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;

-- FTS sync: DELETE
CREATE TRIGGER IF NOT EXISTS memories_ad
AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
END;

-- FTS sync: UPDATE (delete old entry, insert new)
CREATE TRIGGER IF NOT EXISTS memories_au
AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;

-- Fresh DBs come up scrub-ready: there are no pre-scrub rows to rewrite, so
-- the boot gate sentinel is stamped immediately. Old databases reach this
-- key via 'droids-mem migrate --rescrub' (writes '1' after rewriting rows)
-- or '--no-rescrub' (writes '1' to acknowledge plaintext stays as-is).
INSERT INTO meta(key, value) VALUES('scrub_baseline_complete', '1')
    ON CONFLICT(key) DO NOTHING;
`
