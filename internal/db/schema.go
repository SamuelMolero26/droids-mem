package db

// ddl defines all tables, indexes, and triggers.
// Executed once at DB init. All statements are idempotent (IF NOT EXISTS).
//
// memories uses INTEGER rowid implicitly (TEXT PRIMARY KEY id is separate).
// FTS5 external-content sync (memories_fts) keys on this rowid. Do NOT use
// INSERT OR REPLACE or REPLACE INTO on memories — those reassign rowid and
// silently desync the FTS index. Use ON CONFLICT DO UPDATE for upserts.
const ddl = `
CREATE TABLE IF NOT EXISTS memories (
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

CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_fingerprint ON memories(fingerprint);
CREATE INDEX IF NOT EXISTS idx_memories_task_type         ON memories(task_type);
CREATE INDEX IF NOT EXISTS idx_memories_kind              ON memories(kind);
CREATE INDEX IF NOT EXISTS idx_memories_task_kind         ON memories(task_type, kind);
CREATE INDEX IF NOT EXISTS idx_memories_created_at        ON memories(created_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    title,
    what,
    learned,
    tags,
    content='memories',
    content_rowid='rowid',
    tokenize='trigram'
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
`
