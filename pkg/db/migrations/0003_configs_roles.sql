-- P4: boot configs become first-class DB state (configs + immutable revisions),
-- roles (fleet-wide default configs), and per-host binding. All additive.

CREATE TABLE configs (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL UNIQUE,
    kind               TEXT    NOT NULL CHECK (kind IN ('butane','machineconfig','preseed')),
    active_revision_id INTEGER REFERENCES config_revisions(id),
    created_at         TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE config_revisions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    config_id     INTEGER NOT NULL REFERENCES configs(id) ON DELETE CASCADE,
    revision      INTEGER NOT NULL,
    source_b64    TEXT    NOT NULL,
    source_sha256 TEXT    NOT NULL,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE (config_id, revision)
);

CREATE TABLE roles (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL UNIQUE,
    default_config_id INTEGER REFERENCES configs(id) ON DELETE SET NULL,
    created_at        TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE host_roles (
    host_mac TEXT    NOT NULL REFERENCES hosts(mac) ON DELETE CASCADE,
    role_id  INTEGER NOT NULL REFERENCES roles(id)  ON DELETE CASCADE,
    PRIMARY KEY (host_mac, role_id)
);

-- Plain nullable column: SQLite ALTER ADD COLUMN cannot portably carry an
-- ON DELETE foreign key, so referential cleanup lands with P10 (DELETE /configs
-- is 403 until then, so no dangling config_id can be created).
ALTER TABLE hosts ADD COLUMN config_id INTEGER DEFAULT NULL;
