# Cache-target catalog (`catalog.yaml`)

> **Terminology.** The **catalog** is this user-facing declarative file
> (`catalog.yaml`, a `catalog:` list). Each catalog **entry** declares one cache
> **target** — the internal DB concept (the `targets` table, see
> [DATABASE.md](DATABASE.md#targets)). "Catalog" is used here rather than
> "targets" (internal jargon) or "config" (which already means boot-configs in
> booty — the `configs` table, `debianconfig`, `--talosConfigFile`).
>
> **Naming collision, unrelated feature.** booty's API already has a
> read-only `GET /api/v1/os` / `GET /api/v1/families` endpoint pair tagged
> `catalog` (see [API.md](API.md#catalog-read-only)) — a lookup of *supported OS
> types and boot-config families*. That is a **different** thing from this
> document's `catalog.yaml`: it lists what booty's code can support, not what an
> operator wants cached. The two happen to share the word "catalog"; they are
> not related.

## What it is

`catalog.yaml` declares the set of cache targets (OS/arch/version-selector
combinations) booty should discover, download, and keep cached. Booty
reconciles this declaration each cache tick: it creates missing targets,
updates the fields the catalog declares on existing ones, and **disables**
(never deletes) a target whose entry is removed. Targets created ad hoc via
the `/api/v1/targets` API or the web UI, and Talos targets derived from
registered hosts' schematics, are never touched by this pass — see
[Source semantics](#source-semantics-source) below.

This file replaces the old hardcoded `pkg/cache/seed.go` predefined-target
list. There is no migration step to run: on first startup after upgrading, a
flag-derived default catalog is applied automatically (see
[Upgrade notes](#upgrade-notes) for what changes).

## File location & precedence

Resolved once at startup (`LoadCatalog`, `pkg/cache/catalog.go`), fail-fast:

1. **`--catalogFile <path>`, set and the file exists** — parse and use it.
2. **`--catalogFile` set but the file is missing** — startup **aborts** (the
   operator asked for a specific file; a silent fallback would be surprising).
3. **`--catalogFile` unset, `<dataDir>/catalog.yaml` exists** — parse and use
   it.
4. **Neither present** — use the **flag-derived default catalog** (see
   [The default catalog](#the-default-catalog-no-file-present)). This default
   is validated exactly like a file-supplied catalog, so a bad flag value
   (e.g. an unsupported arch) also aborts startup.

A malformed or invalid catalog — from a file or from the flag-derived default —
**always aborts startup** rather than silently falling back to something else;
a silent fallback could mass-download or mass-disable targets unexpectedly.

## Schema

```yaml
schemaVersion: 1                        # must be 1 (the only version this build accepts)
catalog:
  - os: flatcar | fedora-coreos | talos  # debian: not yet supported, see below
    arch: amd64 | arm64 | x86_64         # per-OS allowed token, see table below
    enabled: true                        # optional, default true
    retain: 1                            # optional, default 1; must be >= 0
    spec:                                # OS-specific selector, maps 1:1 to the
                                          # target's identity params
      channel: stable                    # flatcar / fedora-coreos
      schematic: <factory-id>            # talos
```

| Field | Required | Meaning |
|-------|----------|---------|
| `os` | yes | One of `flatcar`, `fedora-coreos`, `talos` (see [per-OS arch](#per-os-arch-tokens)). |
| `arch` | yes | The OS's arch token — validated against the OS (below). |
| `enabled` | no, default `true` | Whether the target is active. |
| `retain` | no, default `1` | Newest-N versions to keep (Talos: newest-N **minor lines**, per [STORAGE.md](STORAGE.md)). Must be `>= 0`. |
| `spec` | yes | OS-specific map building the target's **identity params** — the same value the `/api/v1/targets` create API validates against `RequiredParams` (`ValidateTargetParams`, shared by both paths). |

**`spec` keys, per OS today:**

| OS | Required `spec` key(s) |
|----|------------------------|
| `flatcar` | `channel` |
| `fedora-coreos` | `channel` |
| `talos` | `schematic` |

`channel`/`schematic` are **not** enum-validated — any non-empty, path-safe
value is accepted (values shown above, e.g. `stable`/`beta`/`lts`, are
convention, not an enforced list). A value must be path-safe because it
becomes a cache-directory and URL segment (`ValidatePathParam`); an unsafe
value is rejected.

### Per-OS arch tokens

```
flatcar        amd64, arm64
fedora-coreos  x86_64
talos          amd64, arm64
```

A mismatched arch (e.g. `os: fedora-coreos, arch: amd64`) is rejected at load
time — an unvalidated mismatch would otherwise mint a valid-looking cache
segment that 404s on download.

### Debian — not yet supported

`os: debian` is rejected by the loader today. Debian **target** support
(`release`→codename mapping, `sourceMode`/`dvdCount` mutable columns) does not
exist yet — it lands with the separate Debian image-support feature. Once it
does, Debian becomes a supported catalog `os` like the others above; until
then, a `debian` entry in `catalog.yaml` fails validation at startup.

## Validation (fail-fast)

`LoadCatalog`/`parseCatalog` reject, with startup aborting on any failure:

- `schemaVersion` other than `1`.
- An unknown top-level or entry key (unknown-fields decoding — a typo'd key is
  a hard error, not silently ignored).
- An unsupported `os` (anything other than `flatcar`/`fedora-coreos`/`talos`;
  see [Debian](#debian--not-yet-supported)).
- An `arch` not valid for that `os` (see [table above](#per-os-arch-tokens)).
- A `spec` with a missing required key, an unexpected key, an empty value, or a
  value that isn't path-safe.
- A negative `retain`.
- A duplicate entry — two entries with the same `(os, arch, spec)` identity.

## `source` semantics (`source`)

Every target row carries a `source` discriminator (`catalog` | `api` | `host`)
recording who manages it — see [DATABASE.md](DATABASE.md#targets):

- **`catalog`** — declared in `catalog.yaml` (or the flag-derived default) and
  managed by the catalog-apply reconcile pass.
- **`api`** — created ad hoc via `POST /api/v1/targets` or the web UI. Never
  touched by the catalog pass, even if its `(os,arch,params)` happens to match
  no catalog entry.
- **`host`** — a Talos target derived from a registered host's own `schematic`
  (`EnsureSchematicTarget`, the surviving `reconcileHostSchematics` loop).
  Also never touched by the catalog pass.

**Adoption.** If an existing row's identity `(os,arch,params)` matches a
catalog entry, the catalog pass **adopts** it regardless of its prior
`source` — `api`, freshly-created, or even `host` — setting `source` to
`catalog` and reconciling its declared fields to the entry. This is a
one-way transition — once adopted, that row is catalog-managed until removed
from the catalog (at which point it is disabled, not returned to its prior
source). Adopting a `source=host` row is exactly the scenario covered in
[Known limitation](#known-limitation-a-host-managed-talos-schematic-listed-in-the-catalog)
below.

### Authoritative for declared fields only

The catalog pass is authoritative **only** for the fields the schema declares:
`enabled`, `retain` (DB column `retain_n`), and the `spec`-derived identity
params. Every other target field — notably **`mode`** (`discovery`/`manual`)
— is never touched by the catalog pass and stays whatever the API/UI last set
it to. Concretely, each reconcile tick:

1. **Entry present, target missing** → create it (`source=catalog`, `mode` set
   to `discovery`).
2. **Entry present, target exists** → update `enabled`/`retain_n` to match the
   entry; set `source=catalog`. `mode` and any other non-declared field are
   left exactly as they were.
3. **`source=catalog` target not in the desired set** (its entry was removed)
   → **disable** it (`enabled=false`). The row and its cached bytes are kept —
   disk is reclaimed only by the existing eviction/budget sweep or an explicit
   `DELETE` (currently `403` until authentication lands, P10).
4. **`source=api` / `source=host` targets** → never touched, in every case.

Identity `(os, arch, params)` is never re-keyed by any of the above, so an
unchanged catalog across a restart re-derives the identical desired set and
downloads nothing already cached.

## The default catalog (no file present)

When neither `--catalogFile` nor `<dataDir>/catalog.yaml` is present, booty
builds the desired set from flags (`defaultCatalog`, `pkg/cache/catalog.go`) —
the **curated** shipped default:

```yaml
schemaVersion: 1
catalog:
  - os: flatcar
    arch: amd64          # --flatcarArchitecture
    retain: 1
    spec: { channel: stable }   # --flatcarChannel
  - os: flatcar
    arch: amd64
    retain: 1
    spec: { channel: lts }      # always added, unless --flatcarChannel is already "lts"
  - os: talos
    arch: amd64           # --talosArchitecture
    retain: 3              # --talosRetainMinors
    spec: { schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba }  # --talosSchematic
```

**Fedora CoreOS is intentionally not in this default.** Add it via a
`catalog.yaml` (see [example 1](#example-1) below) or the API (`source=api`,
untouched by the catalog pass) if you want it. See
[Upgrade notes](#upgrade-notes) for what this means on an upgrade from a
pre-catalog booty.

A copy of this default, plus commented illustrative entries, ships at
[`docs/examples/catalog.yaml`](../examples/catalog.yaml) — copy it to
`<dataDir>/catalog.yaml` as a starting point.

## Examples

These correspond to the design's worked examples
(`docs/designs/2026-07-16-declarative-catalog-config-design.md` §7); all three
are covered by round-trip acceptance tests (`TestCatalog_RoundTripDesignExamples`).

### Example 1

An explicit Flatcar + Fedora CoreOS + Talos catalog. This is **not** what you
get with no file present (the zero-file default has no FCOS and adds
`lts` — see [above](#the-default-catalog-no-file-present)) — it's a valid
catalog you write yourself if you want FCOS alongside the others:

```yaml
schemaVersion: 1
catalog:
  - os: flatcar
    arch: amd64
    retain: 1
    spec: { channel: stable }
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: { channel: stable }
  - os: talos
    arch: amd64
    retain: 3
    spec: { schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba }
```

### Example 2

Talos-only homelab, both architectures:

```yaml
schemaVersion: 1
catalog:
  - os: talos
    arch: amd64
    retain: 3
    spec: { schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba }
  - os: talos
    arch: arm64
    retain: 3
    spec: { schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba }
```

### Example 4

Flatcar/Fedora CoreOS with multiple channels and architectures:

```yaml
schemaVersion: 1
catalog:
  - os: flatcar
    arch: amd64
    retain: 1
    spec: { channel: stable }
  - os: flatcar
    arch: amd64
    retain: 1
    spec: { channel: lts }
  - os: flatcar
    arch: arm64
    retain: 1
    spec: { channel: stable }
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: { channel: stable }
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: { channel: testing }
```

(Design example 3, a Debian catalog, is forward-looking and not yet valid —
see [Debian — not yet supported](#debian--not-yet-supported).)

## Known limitation: a host-managed Talos schematic listed in the catalog

A Talos schematic can be **both** host-derived (`source=host`, from a
registered host's own `schematic`) **and** catalog-declared (`source=catalog`)
if you happen to list the same `(os, arch, spec)` identity in both places —
there is only one target row per identity, and the outcome is deterministic,
not a race: **the catalog pass always wins** for any identity it lists. Every
reconcile tick, `reconcileAll` runs `applyCatalog` before
`reconcileHostSchematics` (`pkg/cache/reconciler.go`), and `applyCatalog`
unconditionally sets `source='catalog'` (`UpdateTargetFromCatalog`,
`pkg/db/targets.go`) for any existing row whose identity matches a catalog
entry. The host loop's writer, `EnsureSchematicTarget` (via `EnsureTarget`,
`INSERT ... ON CONFLICT DO NOTHING`), can never overwrite an existing row's
`source` — it only creates a row when none exists yet.

Concretely: if you list a schematic in `catalog.yaml` that a registered host
also uses, that target becomes (or stays) `source=catalog` — governed by the
catalog. If you later **remove** that entry from `catalog.yaml`, the catalog
pass **disables** the shared target, even though a registered host still
needs that schematic. The host loop does not undo this: `EnsureSchematicTarget`
is create-if-absent (`INSERT ... ON CONFLICT DO NOTHING`) and will not
re-enable a disabled row.

**Resolution:** either re-add the entry to `catalog.yaml`, or simply don't list
host-managed schematics in the catalog in the first place — let the host loop
manage them as `source=host`. Building cross-source arbitration (the catalog
pass checking "does any host still need this before disabling") is deliberately
not implemented — it would be speculative machinery for a case an operator can
avoid by not double-declaring a schematic. No data is ever lost either way:
disabling keeps the row and its cached bytes, and `DELETE` remains `403` until
authentication lands (P10).

## Upgrade notes

On the **first boot** after upgrading to a booty version with catalog support,
existing targets are migrated (see [DATABASE.md](DATABASE.md#targets) for the
`predefined`→`source` migration) and then reconciled against the catalog
exactly like any other tick. This has several distinct effects an operator
should know about:

- **Curated default-set change (breaking).** The shipped default **drops
  Fedora CoreOS** and **adds Flatcar `lts`** (see
  [The default catalog](#the-default-catalog-no-file-present)). If you had no
  `catalog.yaml` before upgrading, on first boot the pre-existing FCOS target
  (now `source=catalog`, no longer in the desired set) is **disabled** —
  cached bytes are kept, reclaimed only by eviction or an explicit delete —
  and a new Flatcar `lts` target is created and cached. To keep FCOS in your
  default set, add it to a `catalog.yaml` (see [example 1](#example-1)), or
  re-create it via the API (an API-created row is `source=api` and is never
  touched by the catalog pass).
- **Declared-field revert.** A pre-existing catalog-managed target's
  **declared** fields (`enabled`, `retain`) revert to the shipped default's
  values on first boot; `mode` is preserved. If you had `PATCH`ed a predefined
  target before upgrading (e.g. `enabled=false`, a custom `retain`), that
  customization is reverted — capture it in a `catalog.yaml` instead.
- **Flags still drive the default set.** `--flatcarChannel`,
  `--flatcarArchitecture`, `--talosSchematic`, `--talosArchitecture`, and
  `--talosRetainMinors` still shape the **flag-derived default** exactly as
  before — nothing changes for the retained OSes unless you add a
  `catalog.yaml`.
- **API-row adoption.** An existing `source=api` row whose identity matches a
  catalog entry (e.g. an API-created `flatcar/amd64/stable` target) is
  **adopted**: `source` becomes `catalog`, its declared fields reconcile to
  the catalog, and `mode` is preserved. This is expected — see
  [Adoption](#source-semantics-source) above.
- **New per-OS arch fail-fast.** The flag-derived default now validates arch
  against OS the same way a file-supplied catalog does (see
  [Per-OS arch tokens](#per-os-arch-tokens)) — the retired `seedTargets` did
  not. A previously-tolerated mistyped arch flag on a retained default OS —
  e.g. `--talosArchitecture=aarch64` (valid tokens: `amd64`/`arm64`) or
  `--flatcarArchitecture=x86_64` (valid tokens: `amd64`/`arm64`) — used to boot
  and mint a target that silently 404'd on every download; it now **aborts
  startup** with a clear error instead. This is an improvement (the target
  never worked), but it is a **new startup failure mode** for a previously
  "working" (if broken) flag value. `--coreOSArchitecture` no longer affects
  the default catalog at all, since Fedora CoreOS was dropped from it.
