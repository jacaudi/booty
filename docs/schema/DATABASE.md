# Database & persisted records

booty's current persistent "database" is a single JSON file plus a couple of version-metadata files,
all under `--dataDir`. This documents their shape.

> **Forward-looking:** the v1 management plane moves this state into **SQLite**
> (`modernc.org/sqlite`, pure-Go, no CGO). The host record and version state become tables; this
> page is updated as P1 (targets/versions) and P4 (host store) land. The JSON shapes below remain
> the source of truth until then.

---

## Host database — `hardware.json`

- **Location:** `<dataDir>/<HARDWARE_MAP>` (default `hardware.json`).
- **Format:** a JSON object whose keys are **canonicalized MACs** (lowercase, colon-delimited) and
  whose values are Host records.
- **Durability:** the whole map is rewritten atomically (temp file + rename) on every change; an
  in-memory copy is rolled back to match disk if a write fails. MAC keys are canonicalized on load,
  so records written by older versions are upgraded transparently.

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
| `<dataDir>/version.txt` | Flatcar | `FLATCAR_VERSION=<v>` | Seed marker read at cold start. |
| `<dataDir>/<channel>.json` | Fedora CoreOS | full streams JSON | e.g. `stable.json`; overwritten on each version check. |

Talos keeps no separate metadata file — the newest cached version is derived directly from the cache
directory (semver-sorted); see [STORAGE.md](STORAGE.md).
