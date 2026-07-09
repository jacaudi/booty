-- P5: schematics become a config kind. Two changes:
--   1. configs.kind CHECK gains 'schematic'. SQLite cannot ALTER a CHECK, so
--      the table is rebuilt (copy -> drop -> rename) per lang_altertable §7.
--      The runner executes migrations with foreign_keys=OFF (migrate.go), so
--      DROP TABLE performs no implicit DELETE (which would ON DELETE CASCADE
--      into config_revisions) and child FKs in config_revisions/roles keep
--      referencing the NAME "configs", which the final rename re-points.
--      Rows and IDs are copied verbatim; existing behavior is unchanged.
--   2. config_revisions gains derived_schematic_id: the content-addressed
--      sha256 the Image Factory returned for that revision's source.
--      NULL for every non-schematic kind. Populated at build time (revisions
--      are immutable; there is no post-insert setter).

CREATE TABLE configs_new (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL UNIQUE,
    kind               TEXT    NOT NULL CHECK (kind IN ('butane','machineconfig','preseed','schematic')),
    active_revision_id INTEGER REFERENCES config_revisions(id),
    created_at         TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT    NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO configs_new (id, name, kind, active_revision_id, created_at, updated_at)
    SELECT id, name, kind, active_revision_id, created_at, updated_at FROM configs;
DROP TABLE configs;
ALTER TABLE configs_new RENAME TO configs;

ALTER TABLE config_revisions ADD COLUMN derived_schematic_id TEXT;
