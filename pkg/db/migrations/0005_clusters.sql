-- P6: Talos cluster authoring. Four changes:
--   1. clusters: the cluster entity — pinned versions, endpoint, spec binding,
--      and the age-encrypted secrets bundle (P6 design §3/§7).
--   2. cluster_node_configs: the frozen, age-encrypted per-host machineconfig
--      revisions (materialize-and-freeze, D3/D10). sha256 hashes the PLAINTEXT
--      bytes. Deliberately UNSHARED with P4's plaintext config_revisions (M5).
--   3. hosts gains three nullable membership columns (a host is in <=1
--      cluster; columns, not a members table — KISS).
--   4. configs.kind CHECK gains 'taloscluster' (the cluster-spec config kind,
--      cluster-wide + role patches only). SQLite cannot ALTER a CHECK, so the
--      table is rebuilt (copy -> drop -> rename) per lang_altertable §7 —
--      exactly as 0004 did. The migration runner already executes with
--      foreign_keys=OFF on a dedicated connection (P5), so DROP TABLE performs
--      no implicit cascade DELETE and child FKs re-point via the rename.

CREATE TABLE clusters (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT    NOT NULL UNIQUE,
    endpoint        TEXT    NOT NULL,
    talos_version   TEXT    NOT NULL,
    k8s_version     TEXT    NOT NULL,
    spec_config_id  INTEGER REFERENCES configs(id),
    secrets_enc     BLOB    NOT NULL,
    created_at      TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE cluster_node_configs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    mac          TEXT    NOT NULL,
    cluster_id   INTEGER NOT NULL REFERENCES clusters(id),
    revision     INTEGER NOT NULL,
    config_enc   BLOB    NOT NULL,
    sha256       TEXT    NOT NULL,
    source       TEXT    NOT NULL CHECK (source IN ('generated','imported')),
    host_patch   TEXT,   -- the per-host strategic-merge patch that produced these
                         -- bytes; a durable generation input co-located with its
                         -- frozen output. NULL = no per-host patch (or imported).
                         -- Reused on a re-bind that omits a patch (§6.3/§9).
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE(mac, revision)
);

ALTER TABLE hosts ADD COLUMN cluster_id     INTEGER REFERENCES clusters(id);
ALTER TABLE hosts ADD COLUMN machine_type   TEXT;
ALTER TABLE hosts ADD COLUMN node_config_id INTEGER REFERENCES cluster_node_configs(id);

CREATE TABLE configs_new (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL UNIQUE,
    kind               TEXT    NOT NULL CHECK (kind IN ('butane','machineconfig','preseed','schematic','taloscluster')),
    active_revision_id INTEGER REFERENCES config_revisions(id),
    created_at         TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT    NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO configs_new (id, name, kind, active_revision_id, created_at, updated_at)
    SELECT id, name, kind, active_revision_id, created_at, updated_at FROM configs;
DROP TABLE configs;
ALTER TABLE configs_new RENAME TO configs;
