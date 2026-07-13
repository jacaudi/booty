-- Debian structured authoring: configs.kind CHECK gains 'debianconfig' — the
-- curated YAML kind booty translates into a flat d-i preseed (design §3/§9).
-- SQLite cannot ALTER a CHECK, so the table is rebuilt (copy -> drop ->
-- rename) per lang_altertable §7 — exactly as 0004 and 0005 did. The
-- migration runner already executes with foreign_keys=OFF on a dedicated
-- connection (P5), so DROP TABLE performs no implicit cascade DELETE and
-- child FKs re-point via the rename.

CREATE TABLE configs_new (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL UNIQUE,
    kind               TEXT    NOT NULL CHECK (kind IN ('butane','machineconfig','preseed','schematic','taloscluster','debianconfig')),
    active_revision_id INTEGER REFERENCES config_revisions(id),
    created_at         TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT    NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO configs_new (id, name, kind, active_revision_id, created_at, updated_at)
    SELECT id, name, kind, active_revision_id, created_at, updated_at FROM configs;
DROP TABLE configs;
ALTER TABLE configs_new RENAME TO configs;
