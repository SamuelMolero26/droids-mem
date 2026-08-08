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
    updated_at  INTEGER NOT NULL, scope TEXT NOT NULL DEFAULT 'personal'
    CHECK(scope IN ('personal','shared')), scrub_pattern_version INTEGER NOT NULL DEFAULT 1, scrub_counts TEXT, expand_count INTEGER NOT NULL DEFAULT 0, last_expanded_at INTEGER, origin TEXT NOT NULL DEFAULT 'manual'
    CHECK(origin IN ('manual','auto')),
    CHECK(updated_at >= created_at)
);
CREATE VIRTUAL TABLE memories_fts USING fts5(
    title, what, learned, tags,
    content='memories', content_rowid='rowid', tokenize='trigram'
);
CREATE TABLE memory_files (
    session_id TEXT    NOT NULL,
    file_path  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (session_id, file_path)
);
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE INDEX idx_memories_created_at ON memories(created_at DESC);
CREATE UNIQUE INDEX idx_memories_fingerprint ON memories(fingerprint);
CREATE INDEX idx_memories_kind ON memories(kind);
CREATE INDEX idx_memories_origin_created ON memories(origin, created_at DESC);
CREATE INDEX idx_memories_task_kind_created ON memories(task_type, kind, created_at DESC);
CREATE INDEX idx_memories_task_type ON memories(task_type);
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
END;
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;
CREATE TRIGGER memories_au
AFTER UPDATE OF title, what, learned, tags ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;
PRAGMA user_version = 5;
