# On-disk storage layout

Everything booty persists lives under **`--dataDir`** (default `/data`). Mount this as a volume to
keep cache and registration across restarts.

```
<dataDir>/
├── booty.db                      # SQLite state: hosts + targets (see DATABASE.md)
├── booty.db-wal                  # SQLite write-ahead log (WAL mode)
├── booty.db-shm                  # SQLite shared-memory index
├── hardware.json.migrated        # legacy host DB, imported into booty.db at first start
├── version.txt                   # Flatcar: current cached version marker
├── <channel>.json                # CoreOS: streams metadata (e.g. stable.json)
├── config/
│   ├── ignition.yaml             # Butane template for Flatcar/CoreOS (IGNITION_FILE)
│   └── machineconfig.yaml        # Talos machine-config template (--talosConfigFile)
├── undionly.kpxe                 # proxyDHCP pass-1 BIOS iPXE binary (if proxyDHCP enabled)
├── ipxe.efi                      # proxyDHCP pass-1 UEFI iPXE binary
├── ipxe-arm64.efi               # proxyDHCP pass-1 ARM64 iPXE binary
└── cache/                        # downloaded boot artifacts (see below)
```

> **As of P1b:** `version.txt` / `<channel>.json` are no longer read for version state — the newest
> cached version is derived from the `cache/` directory for every OS (not just Talos).

> **Host DB migration (P1a):** the host store moved from `hardware.json` into the
> SQLite `hosts` table. On the first start after upgrade, an existing
> `hardware.json` is imported and renamed `hardware.json.migrated` (kept as a
> recovery artifact); the import runs exactly once. The database path defaults to
> `<dataDir>/booty.db` and is overridable with `DATABASE_PATH`.

## Artifact cache layout

Cached kernel/initramfs (and rootfs, for CoreOS) are stored under a uniform, segment-based path:

```
<dataDir>/cache/<os>/<schematic-or-dash>/<arch>/<version>/
```

The `<schematic-or-dash>` segment is the Talos Image Factory schematic ID for Talos, and a literal
`-` (no schematic) for Flatcar and CoreOS. The **same segments** drive both the on-disk path and the
HTTP base URL clients fetch from, so disk and URL cannot drift.

**Examples:**

```
cache/flatcar/-/amd64/3598.5.0/
    flatcar_production_pxe.vmlinuz
    flatcar_production_pxe_image.cpio.gz

cache/coreos/-/x86_64/39.20231101.3.0/
    fedora-coreos-<version>-live-kernel-x86_64
    fedora-coreos-<version>-live-rootfs.x86_64.img
    fedora-coreos-<version>-live-initramfs.x86_64.img

cache/talos/376567988ad3…b4ba/amd64/v1.10.1/
    kernel-<arch>
    initramfs-<arch>.xz
```

## How the cache is populated and pruned

- A single **cache reconciler** (`--cacheInterval`, default every 5 minutes; bounded by
  `--cacheConcurrency`) caches each declared target's artifacts eagerly — on startup and on each
  tick, never on boot. Predefined targets (Flatcar, Fedora CoreOS, Talos) are seeded automatically,
  plus one Talos target per distinct registered-host schematic.
- **Flatcar / CoreOS:** the previous version's directory is removed when a newer one is cached
  (single current version per channel).
- **Talos:** the newest `--talosRetainMinors` minor lines are kept (default 3), per schematic and
  arch. As of P1b the reconciler now **prunes** discovered versions outside that set — a change from
  the retired cron, which cached the same set but never pruned. Manual pins are never pruned. The
  boot path is unaffected (it serves the newest cached version, which is always retained).

## Cache retention, archiving, and eviction (P3a)

### Archive-not-delete

As of P3a, discovered versions that rotate out of the retention window are **archived** rather than
deleted. Their on-disk artifacts are kept and their `cache_entries` row is marked `in_window=0`
(state: `archived`). Archived versions remain fully bootable — the interactive boot menu surfaces
them under a nested **Archived OSes** sub-menu (see below) so operators can roll back to a prior
release without re-downloading anything. Manual rows (source `manual`) are never archived, pinned or not.

### Size-based eviction (`--cacheMaxBytes`)

When `--cacheMaxBytes` is set to a positive value (bytes), the reconciler enforces a disk-usage
ceiling after each pass. Eviction works oldest-first:

1. Sum total `cache_entries.size` across all rows.
2. If over budget, evict the oldest **archived, unpinned** row (`fetched_at ASC`): delete its
   `target_version` row (which cascades to `cache_entries`) and remove its on-disk directory.
3. Repeat until under budget or no evictable candidates remain.

**In-cycle and pinned versions are never evicted.** If the total exceeds `--cacheMaxBytes` and
only those rows remain, booty logs a warning and stops — it will not delete versions that are
either actively in the retention window or operator-pinned.

A no-progress guard halts eviction if a deletion makes no measurable change to the total (guards
against `size=0` rows causing runaway deletes).

| Flag | Default | Meaning |
|------|---------|---------|
| `--cacheMaxBytes` | `0` | Max total cache bytes before evicting oldest archived-unpinned versions. `0` = unlimited (eviction is opt-in). |

> **Recommendation:** if you rely on the Archived OSes boot sub-menu for rollback, set
> `--cacheMaxBytes` to bound how much disk the archive can consume. Without a limit, every
> rotated-out version is kept indefinitely.

### Archived OSes boot sub-menu

When any archived versions are present, the interactive iPXE boot menu (served for
`boot_mode='menu'` hosts) adds a nested **Archived OSes** entry below the main version list.
Selecting it opens a second menu page containing every archived version across all OS types.
Choosing an archived version boots it immediately — no re-download, no DB change, fully ephemeral
(the selection is not written back). This is the primary rollback path.
