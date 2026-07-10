# Database & persisted records

booty's persistent state is a SQLite database (`booty.db`) plus a couple of version-metadata files,
all under `--dataDir`. This documents their shape.

> **As of P1a:** control-plane and host state live in **SQLite**
> (`modernc.org/sqlite`, pure-Go, no CGO) at `<dataDir>/booty.db` (override with
> `DATABASE_PATH`). The database is authoritative. The legacy `hardware.json` is
> imported once at startup and renamed `hardware.json.migrated`. Tables below.

---

## Host database — `hosts` table

- **Location:** `hosts` table in `<dataDir>/booty.db`.
- **Format:** one row per host keyed by `mac` (lowercase, colon-delimited).
- **Durability:** WAL-journaled SQLite; writes are transactional. On first startup booty imports
  `hardware.json` into the table and renames the source file `hardware.json.migrated`.

### Host record

| Field | Type | Meaning |
|-------|------|---------|
| `MAC` | string | Canonical MAC (lowercase, colon-delimited). The map key. |
| `Hostname` | string | Hostname rendered into the boot config. |
| `IP` | string | Last-known IP (informational). |
| `Booted` | string | Last boot marker (informational). |
| `IgnitionFile` | string | Optional per-host override of the Ignition template path. |
| `OS` | string | `flatcar` \| `coreos` \| `talos` — selects the TFTP boot path. |
| `DoInstall` | bool | One-shot install flag; flipped to `false` when the host first fetches `booty.ipxe`. |
| `Schematic` | string | Talos only — per-host Image Factory schematic ID. |

A host record is created/updated via `POST /register` and removed via `POST /unregister` (see
[API.md](API.md)).

### Unknown hosts

MACs that contact booty (via TFTP or `/ignition.json`) without a matching record are tracked
**in memory only** (never persisted) and surfaced under the `unknownHosts` key of `/booty.json` so
the UI can prompt for registration. They disappear on restart or once registered.

---

## Version metadata files

booty records the currently-cached release of each channel so it can detect changes across restarts:

| File | OS | Format | Notes |
|------|----|--------|-------|
| `<dataDir>/version.txt` | Flatcar | `FLATCAR_VERSION=<v>` | Legacy — no longer read or written as of P1b; remains on disk from prior runs. |
| `<dataDir>/<channel>.json` | Fedora CoreOS | full streams JSON | Legacy — e.g. `stable.json`; no longer read or written as of P1b; remains on disk from prior runs. |

As of P1b the newest cached version is derived from the `cache/` directory for every OS (including
Talos); see [STORAGE.md](STORAGE.md).

---

## SQLite schema (`booty.db`)

Migrations are embedded (`pkg/db/migrations/`) and applied up-only under
`PRAGMA user_version`. P1a introduces four tables; P3a adds a fifth (`cache_entries`); P4 adds four
more (`configs`, `config_revisions`, `roles`, `host_roles`) plus a `hosts.config_id` column, all in
one additive migration — re-running it is a no-op. P5 (migration `0004`) extends `configs.kind` to
admit `'schematic'` and adds `config_revisions.derived_schematic_id` — see below.

### `targets`
`id`, `os`, `arch`, `params` (JSON TEXT), `mode` (`discovery`|`manual`),
`retain_n`, `predefined`, `enabled`, `created_at`, `updated_at`;
`UNIQUE(os, arch, params)`.

### `target_versions`
`id`, `target_id` → `targets(id)` `ON DELETE CASCADE`, `version`,
`source` (`discovered`|`manual`), `cached`, `created_at`;
`UNIQUE(target_id, version)`.

The **reconciler** (P1b) populates `targets` (predefined + host-derived) and `target_versions`
(`discovered` rows from upstream discovery, retained per `retain_n`; `manual` rows are never
pruned), and flips `cached` to 1 once a version's artifacts are on disk.

### `cache_entries`

Detailed cache inventory, one row per `target_version`. Added in P3a.

| Column | Type | Meaning |
|--------|------|---------|
| `id` | INTEGER PK AUTOINCREMENT | Stable row ID used by the API (`/cache/{id}`). |
| `target_version_id` | INTEGER NOT NULL → `target_versions(id)` **ON DELETE CASCADE** | The version this row describes. `UNIQUE` — one cache row per version. Deleted automatically when its `target_version` is removed. |
| `size` | INTEGER NOT NULL DEFAULT 0 | Total bytes of all cached artifacts for this version (summed from disk at upsert time). |
| `fetched_at` | TEXT NOT NULL DEFAULT `datetime('now')` | ISO-8601 timestamp of the last successful cache or size update. Used as the eviction ordering key (oldest first). |
| `in_window` | INTEGER NOT NULL DEFAULT 1 | `1` = currently in the retention window (in-cycle); `0` = rotated out (archived). Flipped by the reconciler; never by the API. |
| `pinned` | INTEGER NOT NULL DEFAULT 0 | `1` = operator-pinned; exempt from eviction. Set/cleared via `POST /cache/{id}/pin` and `/unpin`. A pin survives re-caching (upsert never clobbers `pinned`). |
| `verified` | INTEGER (nullable) | Tri-state artifact-integrity verdict, **populated by P3b**. `NULL` = no verdict (no verification mechanism declared — Talos/Debian, FCOS pattern-fallback pins — or not attempted under `--signaturePolicy off`); `1` = every verifiable artifact of the version passed; `0` = at least one verifiable artifact failed. Written by the reconcile land-path and by `POST /cache/{id}/reverify`; `UpsertCacheEntry` never clobbers it (P3a contract preserved). |
| `verify_err` | TEXT | Failure detail when `verified=0`, else empty. Defined as the `errors.Join` of every failing artifact's message across the version, each carrying its failure-class text (`checksum mismatch` / `signature mismatch` / `unknown or expired signing key`). |

**Failure-visibility rows (P3b).** When a version is **rejected** by verification (a failure the policy refuses to land — see [CONFIGURATION.md](../CONFIGURATION.md)), its bytes never land (or are removed) but a row is still written so the Cache view can show *why* it won't cache: `size=0`, `in_window=0`, `verified=0`, and `verify_err` set. The `size=0` keeps the row out of the eviction candidate set and the byte budget (it frees nothing). No migration is involved — `verified`/`verify_err` shipped in P3a's `0002_cache_entries.sql` (as `NULL`); P3b only writes them.

**Derived state model.** The wire API derives a human-readable `state` string from `(in_window, pinned)`:

| `in_window` | `pinned` | `state` string |
|-------------|----------|----------------|
| 1 | 0 | `in-cycle` |
| 1 | 1 | `in-cycle-pinned` |
| 0 | 0 | `archived` |
| 0 | 1 | `archived-pinned` |

The `state` string is computed on read; it is not stored. Eviction (`--cacheMaxBytes`) only considers `archived` (unpinned) rows — `in-cycle` and `archived-pinned` rows are never evicted. See [STORAGE.md](STORAGE.md) for eviction semantics.

### `meta`
`key` PRIMARY KEY, `value`.

| Key | Value | Set when |
|-----|-------|---------|
| `hardware_import_done` | `"1"` | The one-time `hardware.json` import has completed (or reached a no-file steady state). Gates re-import so a stale file cannot resurrect a deleted host. |
| `host_boot_preserved` | `"1"` | The P1c upgrade backfill has run. Gating prevents the backfill from re-running on restart. See the upgrade-backfill note in the `hosts` section below. |

### `hosts`
The host record (P1a populates the legacy columns; the approval/assignment columns are activated in
P1c; remaining columns keep their defaults):

| Column | Type | Meaning |
|--------|------|---------|
| `mac` | TEXT PK | Canonical MAC (lowercase, colon-delimited). |
| `hostname` | TEXT | Hostname rendered into the boot config. |
| `ip` | TEXT | Last-known IP. |
| `booted` | TEXT | Last boot marker. |
| `ignition_file` | TEXT | Optional per-host Ignition template override. |
| `os` | TEXT | `flatcar` \| `coreos` \| `talos`. For config-kind family matching (P4), `coreos` maps to the ostype canonical family `fedora-coreos`; `flatcar`/`talos` are identity — see `cache.CacheNameToCanonical`. |
| `do_install` | INTEGER | One-shot install flag. |
| `schematic` | TEXT | Talos per-host schematic ID. |
| `approved` | INTEGER | **Active (P1c).** `1` = approved to boot; `0` = holding pattern. |
| `boot_mode` | TEXT | **Active (P1c).** `assigned` = boot the assigned target; `menu` = deferred (holds until P10). |
| `assigned_os`/`assigned_arch`/`assigned_params` | TEXT | **Active (P1c).** Target (OS, arch, params) the host boots when `boot_mode='assigned'`. |
| `uuid`/`serial` | TEXT | Scanned on every host read; not yet populated by booty (hardware identity, reserved for a future slice). |
| `first_seen`/`last_seen` | TEXT | Reserved: timestamps (not yet surfaced). |
| `config_id` | INTEGER (nullable) | **P4.** Explicit per-host config binding — precedence rung 1 (see [CONFIGURATION.md](../CONFIGURATION.md)). `NULL` = no explicit binding. Plain nullable column, not a DB-level foreign key (SQLite's `ALTER TABLE ADD COLUMN` can't portably carry one); referential cleanup lands with P10 — `DELETE /configs` is `403` until then, so no dangling `config_id` can be created. Set via `POST /hosts/{mac}/approve` or `/bind`. |

> **As of P1c:** `approved`, `boot_mode`, `assigned_os`, `assigned_arch`, and `assigned_params`
> are the columns booty now actively reads and writes. Migration `0001` (P1a) created all of these;
> P1c is the first slice to use them. `uuid` and `serial` are included in every host SELECT
> (scanned into the `Host` struct) but are not yet populated by any code path.
>
> **Upgrade backfill — `meta.host_boot_preserved`:** on the first startup after upgrading to P1c,
> booty runs a one-time backfill gated by the `meta` key `host_boot_preserved`. It marks every
> already-registered host whose `os` column is non-empty as `approved=1`,
> `boot_mode='assigned'`, `assigned_os=os` — so those hosts continue booting identically across
> the upgrade (no outage for hosts that were actively booting a configured OS). Registered hosts
> with an empty `os` column and all unknown hosts move to the holding pattern by design; they must
> be approved via `POST /api/v1/hosts/{mac}/approve` before they will boot again. Once the
> backfill runs, `host_boot_preserved` is set to `"1"` in the `meta` table and the backfill is
> skipped on all subsequent restarts.

### `configs`

Boot-config identities (P4). The live source lives in the revision pointed at by
`active_revision_id`; the row itself never carries source bytes.

| Column | Type | Meaning |
|--------|------|---------|
| `id` | INTEGER PK AUTOINCREMENT | Stable row ID used by the API (`/configs/{id}`). |
| `name` | TEXT NOT NULL UNIQUE | Operator-chosen config name. |
| `kind` | TEXT NOT NULL CHECK (`butane`\|`machineconfig`\|`preseed`\|`schematic`) | The config source dialect the operator authors (`schematic` added in P5). See "`kind` vs family `ConfigKind`" below. |
| `active_revision_id` | INTEGER → `config_revisions(id)` | The currently-live revision. `NULL` until the first revision is added. |
| `created_at`/`updated_at` | TEXT | Timestamps; `updated_at` bumps on every active-pointer move (create, edit, or rollback). |

> **P5 — migration `0004` rebuilds this table.** SQLite cannot `ALTER` a
> `CHECK` constraint, so extending `kind` to admit `'schematic'` required a
> full copy → drop → rename rebuild (per SQLite's documented table-rebuild
> procedure) rather than an additive column change. Rows and IDs are copied
> verbatim; existing behavior for `butane`/`machineconfig`/`preseed` configs
> is unchanged. Because `config_revisions` and `roles` hold foreign keys into
> `configs`, the migration runner (`pkg/db/migrate.go`) now executes with
> `PRAGMA foreign_keys = OFF` on a dedicated connection whenever at least one
> migration is pending, so the rebuild's `DROP TABLE` does not fire an
> implicit `ON DELETE CASCADE` into `config_revisions`. After the migration
> batch it runs `PRAGMA foreign_key_check` — aborting with an error if
> anything was left dangling — before re-enabling `foreign_keys`. A
> steady-state reopen with no pending migrations skips this bracket entirely,
> so it is byte-identical to the pre-P5 runner. This FK-off / rebuild /
> foreign-key-check pattern is now the standing approach for any future
> migration that needs to rebuild a table with dependents (e.g. to change
> another `CHECK` constraint).

### `config_revisions`

Immutable, append-only full copies of a config's source (P4). `PUT /configs/{id}` never mutates a
row — it inserts a new one (`revision` = max+1 for the config) and repoints
`configs.active_revision_id`.

| Column | Type | Meaning |
|--------|------|---------|
| `id` | INTEGER PK AUTOINCREMENT | Row ID; referenced by `configs.active_revision_id`. |
| `config_id` | INTEGER NOT NULL → `configs(id)` **ON DELETE CASCADE** | Owning config. |
| `revision` | INTEGER NOT NULL | Per-config sequence number (1, 2, 3, …); `UNIQUE(config_id, revision)`. |
| `source_b64` | TEXT NOT NULL | Base64-encoded config source (opaque to the DB; decoded and rendered by the HTTP layer). |
| `source_sha256` | TEXT NOT NULL | Hex SHA-256 of the raw source, computed at write time. |
| `derived_schematic_id` | TEXT (nullable) | **P5.** For `kind='schematic'` revisions, the Image Factory-returned content-addressed sha256 for this revision's source. `NULL` for every other kind. Written at INSERT time — revisions are immutable, so there is no post-insert setter. A schematic config's *current* ID is its active revision's value (see [API.md](API.md#configs) for how `POST`/`PUT /configs` build and store it). |
| `created_at` | TEXT | Revision creation timestamp. |

**Rollback** (`POST /configs/{id}/rollback`) repoints `active_revision_id` at an existing older
revision — a pointer move, not a copy; no new revision is created. For a schematic config this
re-points at that older revision's **already-stored** `derived_schematic_id` — no Factory rebuild
occurs. **Prune** (applied after every `PUT`, bounded by `--configRevisionsKeep`) deletes revisions
outside the newest-N **union** the currently-active revision: the active row is never deleted even
when it falls outside the newest-N window (e.g. after a rollback to an old revision followed by
edits to a *different* config leaves this one's old active revision untouched). See
[CONFIGURATION.md](../CONFIGURATION.md).

**Seeded `vanilla` config (P5).** At every startup, `http.SeedVanillaSchematic` create-if-absents a
config named `vanilla` (`kind='schematic'`, source `customization: {}\n`). Its revision's
`derived_schematic_id` is set directly to the known constant `config.DefaultTalosSchematic` (also
the `--talosSchematic` flag default) — **without** a Factory POST, since schematics are
content-addressed and the vanilla ID is already known. Idempotent: a config already named `vanilla`
(from a prior run, or operator-created) makes the seed a no-op.

### `roles`

Fleet-wide groupings that carry an optional default config (P4).

| Column | Type | Meaning |
|--------|------|---------|
| `id` | INTEGER PK AUTOINCREMENT | Stable row ID used by the API (`/roles/{id}`). |
| `name` | TEXT NOT NULL UNIQUE | Operator-chosen role name; also the tie-break order for precedence rung 2 — a host's roles are tried alphabetically, first match with a non-null default wins. |
| `default_config_id` | INTEGER → `configs(id)` **ON DELETE SET NULL** | Config served to hosts with this role when they have no explicit `hosts.config_id`. `NULL` = no default. |
| `created_at`/`updated_at` | TEXT | Timestamps. |

### `host_roles`

Many-to-many join between hosts and roles (P4).

| Column | Type | Meaning |
|--------|------|---------|
| `host_mac` | TEXT → `hosts(mac)` **ON DELETE CASCADE** | Part of the composite PK. |
| `role_id` | INTEGER → `roles(id)` **ON DELETE CASCADE** | Part of the composite PK. |

`PRIMARY KEY (host_mac, role_id)`. Written wholesale by `SetHostRoles` (delete-then-insert,
transactional) — a host's role set is always replaced atomically, never partially updated.

**`kind` vs family `ConfigKind` (§3.1).** `configs.kind` is the dialect an operator *authors*
(`butane`, `machineconfig`, `preseed`); each OS family separately declares a `ConfigKind` — the
boot-config-URL *mechanism* served at `/ignition.json`, `/machineconfig`, `/preseed`
(`ignition`, `machineconfig`, `preseed`). `configKindForFamily` (`pkg/http/render.go`) is the single
source of the relationship, and only the ignition family differs: the `ignition` family's
`ConfigKind` maps to the `butane` config kind (Ignition is Butane's compiled wire format — operators
author Butane YAML; booty translates it to Ignition JSON at render time), while `machineconfig` and
`preseed` map to themselves. A config bound to a host — explicitly or via a role default — must
satisfy `config.kind == configKindForFamily(hostFamily.ConfigKind)`, enforced both at bind time
(`POST /hosts/{mac}/approve` / `/bind`, `422` on mismatch) and again at resolve time
(`pkg/http/resolve.go`, which falls through to the file path on mismatch rather than erroring the
boot).

**`schematic` (P5) is not a bindable dialect.** `configKindForFamily` never returns `schematic` for
any OS family, so a `schematic`-kind config can never satisfy the family-match check above — it is
rejected with `422` at bind time (`/approve`, `/bind`) and, as defense in depth, falls through at
resolve time exactly like any other mismatch. Concretely, `schematic` configs are never served by
`/ignition.json`, `/machineconfig`, or `/preseed`. They are consumed through a separate, dedicated
endpoint instead — `POST /hosts/{mac}/schematic` (see [API.md](API.md#hosts)) — which writes the
derived ID straight into `hosts.schematic`, bypassing `hosts.config_id` and the family-match gate
entirely.

Pragmas on every connection: `journal_mode=WAL`, `foreign_keys=ON`,
`busy_timeout=5000`.
