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
`PRAGMA user_version`. P1a introduces four tables.

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

### `meta`
`key` PRIMARY KEY, `value`.

Keys: `hardware_import_done` (`"1"` once the one-time `hardware.json` import has completed or reached
a no-file steady state — gates re-import so a stale file cannot resurrect a deleted host).

### `hosts`
The host record (P1a populates the legacy columns; the rest are reserved for
later slices and keep their defaults):

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
| `approved` | INTEGER | Reserved (P1c/P2): host approval. |
| `boot_mode` | TEXT | Reserved (P1c): `assigned`\|`menu`. |
| `assigned_os`/`assigned_arch`/`assigned_params` | TEXT | Reserved (P1c): assignment. |
| `uuid`/`serial` | TEXT | Reserved: hardware identity. |
| `first_seen`/`last_seen` | TEXT | Reserved: timestamps. |

Pragmas on every connection: `journal_mode=WAL`, `foreign_keys=ON`,
`busy_timeout=5000`.
