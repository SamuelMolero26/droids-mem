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
const ddl = ddlTables + FTSSchema + ddlMeta

const ddlTables = `
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
    scope                 TEXT    NOT NULL DEFAULT 'personal' CHECK(scope IN ('personal','shared')),
    scrub_pattern_version INTEGER NOT NULL DEFAULT 1,
    scrub_counts          TEXT,
    expand_count          INTEGER NOT NULL DEFAULT 0,
    last_expanded_at      INTEGER,
    origin                TEXT    NOT NULL DEFAULT 'manual' CHECK(origin IN ('manual','auto')),
    review_after          INTEGER,
    pinned                INTEGER NOT NULL DEFAULT 0,
    CHECK(updated_at >= created_at)
);

-- archived_memories is the soft-delete destination for supersede (ADR-0030):
-- an explicit 1:1 column mirror of memories plus archived_at. No FTS, no
-- triggers -- archived rows are deliberately invisible to mem_context/
-- mem_search/review list. The explicit column list (not SELECT-star at
-- archive time) is checked against memories via a PRAGMA table_info parity
-- test so future ALTERs on memories fail loud here instead of silently
-- dropping a column from the archive copy.
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
-- idx_memories_origin_created serves the auto-summary recency read
-- (recent-sessions: WHERE origin='auto' ORDER BY created_at DESC LIMIT N) and
-- the origin-keyed eviction scan (ADR-0016). Never joined on FTS.
CREATE INDEX IF NOT EXISTS idx_memories_origin_created    ON memories(origin, created_at DESC);

-- memory_files is the file-provenance relation (ADR-0021 Phase 2): the files a
-- Claude Code session read or changed, keyed by the droids-mem session_id the
-- session's memories carry. Orthogonal to the Memory model — never joined to
-- memories_fts, never scrubbed, deduped, or retained; it feeds the future Graph
-- tab's file→graph-node join. Composite PK dedupes repeat touches of a file.
CREATE TABLE IF NOT EXISTS memory_files (
    session_id TEXT    NOT NULL,
    file_path  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (session_id, file_path)
);
`

// FTSSchema is the FTS5 virtual table + the three sync triggers (AI/AD/AU).
// Single source of truth: the fresh-DB ddl embeds it, and `migrate --rescrub`
// re-executes it verbatim after dropping the old index (internal/store/migrate.go),
// so a tokenizer change here propagates to migrated DBs automatically.
//
// FTS5 tokenizer (decision #17, + porter ADR-0018-era retrieval pass): the
// porter stemmer wraps unicode61, folding morphological variants (cancel /
// cancellation, panic / panicked) to a common stem at both index and query
// time so paraphrased lessons match. unicode61's underscore + hyphen token
// chars are preserved underneath, keeping snake_case and kebab-case atomic.
// porter does NOT bridge true synonyms (panic <-> nil pointer) — that gap is
// left to write-time canonical tags, not retrieval-side machinery (embeddings
// rejected: local-first, pure-Go, no CGO).
// Existing databases pick up the stemmer by running 'droids-mem migrate'
// (either mode drops + recreates this table from FTSSchema and reindexes).
const FTSSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    title,
    what,
    learned,
    tags,
    content='memories',
    content_rowid='rowid',
    tokenize='porter unicode61 tokenchars ''_-'''
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

-- FTS sync: UPDATE (delete old entry, insert new). Scoped to the indexed text
-- columns (decision: scoped trigger, ADR-0013) so metadata-only updates — the
-- Expand signal increment in particular — do NOT trigger a full FTS
-- delete+reinsert. An UPDATE that touches none of title/what/learned/tags has
-- no business re-indexing FTS.
CREATE TRIGGER IF NOT EXISTS memories_au
AFTER UPDATE OF title, what, learned, tags ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;
`

const ddlMeta = `
-- Fresh DBs come up scrub-ready: there are no pre-scrub rows to rewrite, so
-- the boot gate sentinel is stamped immediately. Old databases reach this
-- key via 'droids-mem migrate --rescrub' (writes '1' after rewriting rows)
-- or '--no-rescrub' (writes '1' to acknowledge plaintext stays as-is).
INSERT INTO meta(key, value) VALUES('scrub_baseline_complete', '1')
    ON CONFLICT(key) DO NOTHING;
`
