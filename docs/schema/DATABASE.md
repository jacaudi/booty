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
`PRAGMA user_version`. P1a introduces four tables; P3a adds a fifth (`cache_entries`).

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
| `verified` | INTEGER | **NULL in P3a — reserved for P3b** artifact-integrity verification. |
| `verify_err` | TEXT | **NULL in P3a — reserved for P3b** verification error detail. |

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
| `os` | TEXT | `flatcar` \| `coreos` \| `talos`. |
| `do_install` | INTEGER | One-shot install flag. |
| `schematic` | TEXT | Talos per-host schematic ID. |
| `approved` | INTEGER | **Active (P1c).** `1` = approved to boot; `0` = holding pattern. |
| `boot_mode` | TEXT | **Active (P1c).** `assigned` = boot the assigned target; `menu` = deferred (holds until P10). |
| `assigned_os`/`assigned_arch`/`assigned_params` | TEXT | **Active (P1c).** Target (OS, arch, params) the host boots when `boot_mode='assigned'`. |
| `uuid`/`serial` | TEXT | Scanned on every host read; not yet populated by booty (hardware identity, reserved for a future slice). |
| `first_seen`/`last_seen` | TEXT | Reserved: timestamps (not yet surfaced). |

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

Pragmas on every connection: `journal_mode=WAL`, `foreign_keys=ON`,
`busy_timeout=5000`.
