CREATE TABLE cache_entries (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    target_version_id INTEGER NOT NULL REFERENCES target_versions(id) ON DELETE CASCADE,
    size              INTEGER NOT NULL DEFAULT 0,
    fetched_at        TEXT    NOT NULL DEFAULT (datetime('now')),
    in_window         INTEGER NOT NULL DEFAULT 1,
    pinned            INTEGER NOT NULL DEFAULT 0,
    verified          INTEGER,
    verify_err        TEXT,
    UNIQUE (target_version_id)
);
