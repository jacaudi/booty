-- Catalog config: targets gain a `source` discriminator replacing `predefined`.
--   source ∈ {catalog, api, host}
--     catalog — declared/managed by catalog.yaml (was predefined=1)
--     host    — host-derived Talos schematic rows (predefined=0, os=talos,
--               params.schematic matches a registered Talos host)
--     api     — everything else (ad-hoc API/UI rows; migrate.go collision rows)
-- Table is rebuilt (copy -> drop -> rename) per lang_altertable §7; the runner
-- runs migrations with foreign_keys=OFF so target_versions' ON DELETE CASCADE
-- does not fire on DROP TABLE, and its FK re-points to the renamed table.
CREATE TABLE targets_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    os          TEXT    NOT NULL,
    arch        TEXT    NOT NULL,
    params      TEXT    NOT NULL DEFAULT '{}',
    mode        TEXT    NOT NULL CHECK (mode IN ('discovery','manual')),
    retain_n    INTEGER NOT NULL DEFAULT 0,
    source      TEXT    NOT NULL DEFAULT 'api' CHECK (source IN ('catalog','api','host')),
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE (os, arch, params)
);
INSERT INTO targets_new (id, os, arch, params, mode, retain_n, source, enabled, created_at, updated_at)
    SELECT id, os, arch, params, mode, retain_n,
        CASE
            WHEN predefined = 1 THEN 'catalog'
            WHEN os = 'talos'
                 AND json_extract(params, '$.schematic') IN
                     (SELECT schematic FROM hosts WHERE os = 'talos' AND schematic <> '')
                THEN 'host'
            ELSE 'api'
        END,
        enabled, created_at, updated_at
    FROM targets;
DROP TABLE targets;
ALTER TABLE targets_new RENAME TO targets;
