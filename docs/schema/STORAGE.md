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
│   ├── machineconfig.yaml        # Talos machine-config template (--talosConfigFile)
│   └── preseed.cfg               # Debian preseed template (--preseedFile)
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

## Boot config storage (P4)

Boot-config **source is DB-authoritative** as of P4: `POST`/`PUT /configs` writes are stored as
immutable revisions in `config_revisions.source_b64` (see [DATABASE.md](DATABASE.md)), not as
files under `--dataDir`. The `config/` templates on disk (`ignition.yaml`, `machineconfig.yaml`,
`preseed.cfg`) remain the **terminal fallback** — precedence rung 4, see
[CONFIGURATION.md](../CONFIGURATION.md) — served only when a host has no DB-resolved config (no
explicit `config_id`, and no role default whose kind matches the host's OS family).

- **No file→row migration.** P4 ships with an empty `configs` table; nothing on disk is read into
  the DB automatically. Operators create configs explicitly via the API — a host with no explicit
  binding and no matching role default just keeps using the file fallback indefinitely.
- **Unbound hosts are byte-unchanged.** A host with no `config_id` and no matching role default
  renders from the same on-disk template, through the same file-read + `text/template` path, as
  before P4. The DB-resolve attempt (precedence rungs 1–2) is tried first and short-circuits only
  on a hit; any miss or family/kind mismatch falls straight into the pre-P4 file-serving code,
  verbatim.

## Cluster secrets & node-config storage (P6)

Cluster state that must never leave `--dataDir` in plaintext is stored **encrypted, in the
database** — not as files:

- **`clusters.secrets_enc`** — the cluster's secrets bundle (PKI, tokens, cluster ID/secret),
  age-encrypted under the identity at `--secretsKey`.
- **`cluster_node_configs.config_enc`** — each member's frozen, per-host machineconfig revision,
  age-encrypted the same way. See [DATABASE.md](DATABASE.md#cluster_node_configs).

Nothing under `--dataDir/config/` is used for cluster members — the on-disk template
(`machineconfig.yaml`) remains the P4 fallback for **non-member** Talos hosts only (see
[Boot config storage](#boot-config-storage-p4) above).

### The DR coupling — back up the DB and the key, separately

Recovering a cluster after loss requires **both**:

1. **The backed-up `booty.db`** (or its WAL-checkpointed equivalent) — holds the ciphertext.
2. **The on-box age identity file at `--secretsKey`** — the only thing that can decrypt it.

A database backup without the key is cryptographically useless (undecryptable ciphertext); the key
without the database backup has nothing to decrypt. **Back these up separately**, with the same
durability guarantees given any other secret material — losing either one is equivalent to losing
cluster secrets recovery entirely.

## Artifact cache layout

Cached kernel/initramfs (and rootfs, for CoreOS) are stored under a uniform, segment-based path:

```
<dataDir>/cache/<os>/<segment>/<arch>/<version>/
```

`<segment>` is the single path-discriminating value carried in a target's params, chosen by fixed
precedence: the Talos Image Factory **schematic** ID for Talos, else the release **channel** for
Flatcar / Fedora CoreOS (and Debian), else a literal `-` if the target has neither. No OS carries
both keys today, so the precedence is theoretical but fixed. The **same segments** drive both the
on-disk path and the HTTP base URL clients fetch from, so disk and URL cannot drift.

**Examples:**

```
cache/flatcar/stable/amd64/4230.2.2/
    flatcar_production_pxe.vmlinuz
    flatcar_production_pxe_image.cpio.gz

cache/coreos/stable/x86_64/44.20260607.3.1/
    fedora-coreos-<version>-live-kernel.x86_64
    fedora-coreos-<version>-live-rootfs.x86_64.img
    fedora-coreos-<version>-live-initramfs.x86_64.img

cache/talos/376567988ad3…b4ba/amd64/v1.10.1/
    kernel-<arch>
    initramfs-<arch>.xz
```

> **Migration (#48):** on the first startup after upgrading past #48, a pre-existing
> `<os>/-` directory (Flatcar / Fedora CoreOS) is renamed to `<os>/<flag-channel>` — the channel
> read from `--flatcarChannel` / `--coreOSChannel` at that startup — provided the destination
> doesn't already exist. If both `<os>/-` and `<os>/<flag-channel>` exist, the `-` directory is left
> in place; its versions surface as orphans via `POST /api/v1/cache/scan`, and nothing is deleted
> automatically. If the operator changes the channel flag between the pre-#48 run and the migrated
> run, the renamed artifacts are mislabeled under the wrong channel for one cycle: the reconciler
> discovers the real newest version for the (now-correct) channel and the mislabeled version simply
> ages out as an archived entry once it rotates out of the retention window — bounded and
> self-correcting, no manual cleanup required. Debian targets are NOT covered by this migration
> (placeholder status, no predefined seeding): a pre-#48 operator-created debian target re-caches
> under its channel segment on next reconcile, and the old `debian/-` directory is reported as
> orphans by `POST /api/v1/cache/scan` like any other stale dir.

### Staged downloads (`.partial`) — P3b

Every artifact download (all OSes) is **staged** before it becomes a cached file. The body streams
to `<artifact>.partial` in the version directory (SHA-256 computed while streaming), then the
verification verdict decides its fate:

- **Land:** on a pass (or a `warn` corruption failure that lands), the `.partial` is `os.Rename`d
  onto the final `<artifact>` name. The rename is atomic because source and destination share the
  same filesystem — a boot never sees a half-written file at the final name.
- **Reject:** on a refused failure, the `.partial` is removed and the whole version directory is
  wiped (version-level atomicity), so `NewestCached` falls back to the prior cached version.

`.partial` files are never part of the boot/menu/TFTP path (which references only exact artifact
filenames), are **swept at the start of every reconcile pass** (a crash mid-download self-heals),
are **excluded from `POST /cache/scan` size sums**, and are **404'd by the `/data/` file server**
(case-insensitive) so a direct browse can't fetch an in-flight file.

**Failure-visibility on disk.** A version rejected by verification leaves **no bytes on disk** (its
directory is removed) but keeps a `cache_entries` row with `size=0`, `in_window=0`, `verified=0`,
and `verify_err` set — visible in the Cache view, invisible to the byte budget and eviction sweep.
See [DATABASE.md](DATABASE.md) and [CONFIGURATION.md](../CONFIGURATION.md).

## How the cache is populated and pruned

- A single **cache reconciler** (`--cacheInterval`, default every 5 minutes; bounded by
  `--cacheConcurrency`) caches each declared target's artifacts eagerly — on startup and on each
  tick, never on boot. Which targets exist is resolved from the declarative catalog
  (`catalog.yaml`, or a flag-derived default when no file is present — see
  [CATALOG.md](CATALOG.md)), plus one Talos target per distinct registered-host schematic.
- **Flatcar / CoreOS:** the newest `retainN` versions are kept per channel (default `1`, the
  historic "single current version" behavior). Discovery only ever returns the channel's current
  build, so a window over `retainN > 1` accumulates history one release at a time as new versions
  are discovered — it does not backfill versions upstream no longer advertises (see
  [CONFIGURATION.md](../CONFIGURATION.md)). Versions that rotate out of the window are archived,
  not deleted (see below).
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
