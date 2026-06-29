CREATE TABLE targets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    os          TEXT    NOT NULL,
    arch        TEXT    NOT NULL,
    params      TEXT    NOT NULL DEFAULT '{}',
    mode        TEXT    NOT NULL CHECK (mode IN ('discovery','manual')),
    retain_n    INTEGER NOT NULL DEFAULT 0,
    predefined  INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE (os, arch, params)
);

CREATE TABLE target_versions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    target_id  INTEGER NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
    version    TEXT    NOT NULL,
    source     TEXT    NOT NULL CHECK (source IN ('discovered','manual')),
    cached     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE (target_id, version)
);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE hosts (
    mac             TEXT PRIMARY KEY,
    hostname        TEXT    NOT NULL DEFAULT '',
    ip              TEXT    NOT NULL DEFAULT '',
    booted          TEXT    NOT NULL DEFAULT '',
    ignition_file   TEXT    NOT NULL DEFAULT '',
    os              TEXT    NOT NULL DEFAULT '',
    do_install      INTEGER NOT NULL DEFAULT 0,
    schematic       TEXT    NOT NULL DEFAULT '',
    approved        INTEGER NOT NULL DEFAULT 0,
    boot_mode       TEXT    NOT NULL DEFAULT 'menu' CHECK (boot_mode IN ('assigned','menu')),
    assigned_os     TEXT    NOT NULL DEFAULT '',
    assigned_arch   TEXT    NOT NULL DEFAULT '',
    assigned_params TEXT    NOT NULL DEFAULT '{}',
    uuid            TEXT    NOT NULL DEFAULT '',
    serial          TEXT    NOT NULL DEFAULT '',
    first_seen      TEXT    NOT NULL DEFAULT (datetime('now')),
    last_seen       TEXT    NOT NULL DEFAULT (datetime('now'))
);
