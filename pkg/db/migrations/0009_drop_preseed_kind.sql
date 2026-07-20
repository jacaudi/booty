-- #59: retire the raw 'preseed' config kind. SQLite cannot ALTER a CHECK, so
-- the configs table is rebuilt (copy -> drop -> rename) per lang_altertable §7,
-- exactly as 0004/0005/0006 did. A fail-fast Go pre-flight (preflightPreseedRemoval
-- in migrate.go) runs BEFORE this migration and aborts with a helpful message if
-- any kind='preseed' row survives, so the INSERT...SELECT below never has to
-- reject one. The runner already brackets the loop with foreign_keys=OFF, so the
-- DROP performs no cascade and child FKs re-point via the rename.

CREATE TABLE configs_new (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL UNIQUE,
    kind               TEXT    NOT NULL CHECK (kind IN ('butane','machineconfig','schematic','taloscluster','debianconfig')),
    active_revision_id INTEGER REFERENCES config_revisions(id),
    created_at         TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT    NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO configs_new (id, name, kind, active_revision_id, created_at, updated_at)
    SELECT id, name, kind, active_revision_id, created_at, updated_at FROM configs;
DROP TABLE configs;
ALTER TABLE configs_new RENAME TO configs;
