# Declarative Catalog Config (`catalog.yaml`)

**Type:** Design
**Date:** 2026-07-16
**Status:** Revised after SGE design review (verdict AMEND-BEFORE-PLANNING → all findings folded; see §14). Pending user written-spec review.
**Feature:** declarative-catalog-config

> **Terminology.** The user-facing declarative file is the **catalog**
> (`catalog.yaml`, a `catalog:` list). Each catalog **entry** declares one cache
> **target** — the existing internal DB concept (`targets` table). "Catalog" is
> chosen over "targets" (internal jargon) and "config" (which already means
> boot-configs in booty: the `configs` table, `debianconfig`, `--talosConfigFile`).

---

## 1. Context & motivation

Booty's cache targets (which OS images/versions it downloads and serves) are
**hardcoded in `pkg/cache/seed.go`** — Flatcar/FCOS/Talos, parameterized by CLI
flags (`--flatcarChannel`, `--talosSchematic`, …), created create-if-absent each
reconcile tick — and then managed at runtime via the `/api/v1/targets` API. There
is **no config file**: `pkg/config/config.go` never calls `viper.ReadInConfig`;
everything is flags + a few `BindEnv` env vars.

Operators want to **declare the image set they want** — "pre-seed" exactly which OS
images/versions booty holds — without editing code or driving the API by hand, and
have booty **reconcile to that declaration**. A restart with an unchanged
declaration must **not re-download** anything already cached.

This feature adds a declarative `catalog.yaml` that becomes the source of truth for
booty's *managed* cache targets, replacing the hardcoded `seed.go` list with a
shipped default file.

## 2. Goals

- A declarative YAML (`catalog.yaml`) listing the desired cache targets.
- Booty reconciles **catalog-declared targets** to match the file: create, update
  to the declared fields, and **disable** ones whose entry is removed.
- Targets created ad-hoc via the **API/UI are left untouched** (not pruned).
- The catalog **replaces** the hardcoded `seed.go` defaults; a **shipped default
  `catalog.yaml`** preserves today's out-of-box behavior when the operator provides
  none.
- **Restart idempotency:** an unchanged catalog re-derives the identical desired set
  and downloads nothing already cached — verified, not assumed.
- Documented schema with worked examples.

## 3. Non-goals

- Live file-watch / hot-reload. The catalog is read **at startup**; a change takes
  effect on restart. (Deferred — YAGNI; the reconciler already re-runs each tick,
  so hot-reload is an additive later change.)
- Declaring anything other than cache targets (global settings stay flags/env).
- The per-OS *capabilities* themselves (Debian `dvd` mode, Flatcar/FCOS arm64
  threading, etc.) — those live in their own specs (see §10). This feature is the
  **declaration + reconcile mechanism**; it exposes whatever target fields exist.
- Multi-tenant / per-user catalogs. One server-side file per booty instance.

## 4. Decisions (from brainstorming)

1. **Authoritative for catalog-declared, leave API-created alone.** Booty marks
   which targets it manages from the catalog and prunes only those.
2. **Catalog replaces code-seeding, shipped default.** Today's defaults move into a
   shipped default `catalog.yaml`; a user-provided catalog fully replaces it.
3. **Removing an entry disables the target, keeps cached bytes.** Typo-safe; disk is
   reclaimed only by the existing eviction/budget sweep or an explicit API delete.
4. **Schema shape:** `os` selector + common fields (`arch`/`retain`/`enabled`) +
   a nested **`spec`** map holding the OS-specific knobs.
5. **Load at startup, fail-fast validation.** A malformed/invalid catalog aborts
   startup rather than silently falling back (a silent fallback to defaults could
   mass-download or mass-disable unexpectedly).

## 5. Catalog file: location, loading, precedence

- **Path:** `--catalogFile`, default `<dataDir>/catalog.yaml`.
- **Name rationale:** `catalog`, not `targets` (internal jargon) or `config`
  ("config" already means boot-configs in booty — the `configs` table,
  `debianconfig`, `--talosConfigFile`; reusing it would collide).
- **Absent file:** load the **embedded shipped default**, which is **exactly today's
  Flatcar/FCOS/Talos predefined set** — no Debian (B1: Debian target support does not
  exist yet; embedding it would make every existing deployment start downloading
  Debian on upgrade, a new behavior, not a preserved one). Out-of-box behavior is
  byte-for-byte unchanged.
- **Loading:** parsed once at startup into a validated in-memory desired set; the
  reconciler consumes it each tick. Invalid catalog → startup aborts with a clear
  error (§4.5).
- **Precedence:** the catalog is authoritative for the **fields it declares** on
  managed targets. Global flags/env are unchanged for non-target settings; existing
  `--flatcarChannel`-style flags become **fallback defaults** for the shipped
  default only (a user catalog states values explicitly).

## 6. Schema

```yaml
# catalog.yaml — booty's declarative OS-image catalog.
# Booty reconciles catalog-declared targets to match this. Targets added via the
# API/UI that aren't listed here are left alone. Deleting an entry DISABLES that
# target (cached bytes are kept).
schemaVersion: 1
catalog:
  - os: flatcar | fedora-coreos | talos | debian
    arch: amd64 | arm64 | x86_64          # per-OS arch token
    enabled: true                         # optional, default true
    retain: 1                             # optional; newest-N (Talos: newest-N minor lines)
    spec:                                 # OS-specific knobs (maps into the target)
      # ── flatcar / fedora-coreos ──
      channel: stable                     # stable | beta | lts (flatcar) / stable | testing (fcos)
      # ── talos ──
      schematic: <factory-id>
      # ── debian ──
      release: "13"                       # 11 | 12 | 13  (→ params.channel)
      sourceMode: netinst                 # netinst | dvd (default netinst)
      dvdCount: 3                         # dvd mode only
```

**Field mapping to the target model.** `os`/`arch`/`enabled`/`retain` map to the
existing `targets` columns (`retain`→`retain_n`). The `spec` map is destructured
per OS: `channel`/`schematic`/`release` build the target **`params`** (the identity
discriminator); `sourceMode`/`dvdCount` map to the Debian mutable columns.
Validation is **fail-fast** and covers: unknown `spec` keys per OS, missing required
keys, bad enums, an unknown `schemaVersion`, and **per-OS arch tokens**
(`x86_64`↔fedora-coreos, `amd64`/`arm64`↔flatcar/talos) — a mismatched arch yields a
valid-looking cache segment that 404s on download, so it must be rejected up front
(M2/M3).

**Debian entries are forward-looking (B1).** `pkg/ostype/debian.go` is today a
placeholder (`DiscoverVersions` returns a fixed `{"12.5","11.9"}`, `RequiredParams`
= `["channel"]` keyed to `stable`/`oldstable` codenames), and the `sourceMode`/
`dvdCount` columns and the `release`→codename mapping **do not exist yet** — they are
introduced by the separate Debian image-support spec. Until that lands, Debian is
**not a supported catalog entry**: the shipped default omits it (§5), and the Debian
examples below (§7 ex 3) are illustrative of the *eventual* shape. The loader should
reject Debian `spec` keys (`release`/`sourceMode`/`dvdCount`) until the Debian
capability exists.

## 7. Examples

*1 — the shipped default (EXACTLY today's out-of-box set; no Debian — B1):*
```yaml
schemaVersion: 1
catalog:
  - os: flatcar
    arch: amd64
    retain: 1
    spec: {channel: stable}
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: {channel: stable}
  - os: talos
    arch: amd64
    retain: 3
    spec: {schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba}
```

*2 — Talos-only homelab, both arches:*
```yaml
schemaVersion: 1
catalog:
  - os: talos
    arch: amd64
    retain: 3
    spec: {schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba}
  - os: talos
    arch: arm64
    retain: 3
    spec: {schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba}
```

*3 — Debian shop with an offline archive (FORWARD-LOOKING — requires Debian target
support from the separate Debian spec; not valid until then, B1):*
```yaml
schemaVersion: 1
catalog:
  - os: debian
    arch: amd64
    spec: {release: "13", sourceMode: netinst}
  - os: debian
    arch: amd64
    spec: {release: "12", sourceMode: dvd, dvdCount: 3}
  - os: debian
    arch: amd64
    enabled: false
    spec: {release: "11", sourceMode: dvd, dvdCount: 3}
```

*4 — Flatcar/FCOS multiple channels + arches (dovetails with the multi-arch spec):*
```yaml
schemaVersion: 1
catalog:
  - os: flatcar
    arch: amd64
    retain: 1
    spec: {channel: stable}
  - os: flatcar
    arch: amd64
    retain: 1
    spec: {channel: lts}
  - os: flatcar
    arch: arm64
    retain: 1
    spec: {channel: stable}
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: {channel: stable}
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: {channel: testing}
```

## 8. Reconcile behavior & data model

### 8.1 Source marker (I1)

Targets gain a **`source`** discriminator generalizing the current `predefined`
bool, with **three** values:

- **`catalog`** — declared and managed by `catalog.yaml`.
- **`api`** — created ad-hoc via the API/UI.
- **`host`** — dynamically host-derived Talos schematic targets
  (`EnsureSchematicTarget`, `pkg/cache/schematic.go`; the `hostTalosSchematics` loop
  in `seed.go`), reconciled from registered hosts. A binary `{catalog,api}` has no
  home for these, so they get their own value; they are managed by the surviving
  host loop (§8.3/§10), never by the catalog pass.

`predefined` has more consumers than a bare bool rename accounts for; the plan must
handle all of them:

- **`pkg/cache/migrate.go`** uses `Predefined` as a **load-bearing one-time
  collision marker** (#48): a colliding pre-#48 row is set `Enabled=false` *and*
  `Predefined=false` so later startups skip it even after an operator re-enables it;
  `!Predefined` means "operator row, leave untouched." This must be re-expressed in
  `source` terms (collision-disabled/operator rows → `source=api`), not silently
  dropped.
- **`pkg/http/api_targets.go`** publishes `predefined` as a **wire field** on
  `TargetDTO` (OpenAPI). Decide: rename to `source` (contract change; the React UI
  does not render `predefined`, so frontend risk is low) vs. keep `predefined`
  derived from `source` for back-compat. Plan decides.

Migration maps existing rows: `predefined=true` → `source=catalog`; host-derived
schematic rows → `source=host`; everything else (incl. migrate.go's
collision-disabled rows) → `source=api`.

### 8.2 Reconcile pass (per tick, on the single-writer coordinator)

**Placement (M4):** this pass **replaces `seedTargets`' position** in the reconcile
tick (`reconciler.go`: `SweepPartials → [catalog-apply] → ListTargets → per-target
loop → evict`). It must run **before** `ListTargets`/the per-target loop so a
just-disabled row is skipped the same tick. Single-goroutine sequential execution on
the coordinator means no in-flight-download race.

The catalog is **authoritative only for the fields it declares** — `enabled`,
`retain` (`retain_n`), and the `spec`-derived params/columns. Fields the schema does
**not** carry (notably `mode`, which stays discovery/API-owned) are never touched by
the catalog pass. Against the loaded desired set:

1. For each **declared** entry: ensure the target row exists (`EnsureTarget`,
   create-if-absent on identity `(os,arch,params)`), then **update its declared
   fields** to match the catalog, and set `source=catalog`. Identity is unchanged,
   so no re-keying and no re-download.
2. For each `source=catalog` row **not** in the desired set: **disable** it
   (`enabled=false`), keep the row + cached bytes (§4.3).
3. `source=api` and `source=host` rows: **untouched** (host-derived Talos schematics
   are managed by the surviving host loop, §8.3/§10).

This changes the prior "API owns fields after seed" behavior (`seed.go` #48 D1) for
`source=catalog` targets only: their **declared** fields are reconciled to the
catalog each tick (`mode` and any non-declared field are not). API-created targets
keep API ownership. **UX consequence:** editing a managed target's *declared* field
via the UI/API reverts on the next tick — the catalog is the place to change it. The
UI should mark `source=catalog` targets as catalog-managed (edit disabled / "managed
by catalog.yaml"); a small follow-on (§10, §12.2).

### 8.3 Existing artifact/discovery flow unchanged

Discovery → `retentionFor` → `ensureArtifact` are untouched; this feature only
changes **how the target set is populated/pruned**, not how a target caches.

## 9. Restart idempotency (explicit requirement)

- Declared targets map to the same `(os,arch,params)` identity as before, so
  `EnsureTarget` is create-if-absent and `ensureArtifact` only downloads when the
  on-disk file is **absent** (`pkg/cache/reconcile.go`, `verify.go`). A restart with
  an unchanged `catalog.yaml` re-derives the identical desired set and downloads
  nothing.
- Field updates (retain/enabled/mode) mutate the row **in place** — never re-key
  identity, never evict, never re-download.
- **Verification (acceptance):** boot with a catalog → let it cache → restart with
  the same catalog → assert **zero** `"downloading (staged)"` log lines on the
  second boot. Lab-checkable and unit-checkable (reconcile against a pre-populated
  store yields no artifact fetches).

**Upgrade transition (I3).** The steady-state idempotency above is the *unchanged
catalog* case. On the **first boot after upgrading** to this feature, existing
`predefined=true` rows become `source=catalog` and are subjected to catalog
authority: their **declared** fields (`enabled`, `retain_n`) revert to the shipped
default's values. So an operator who had PATCHed a predefined target (e.g.
`enabled=false`, a custom `retain_n`) sees that reverted on first post-upgrade boot;
a re-enabled target may then re-download. `mode` is **not** declared, so a row
PATCHed to `mode=manual` keeps its mode while `enabled`/`retain_n` revert — this is
intentional (authoritative-for-declared-fields, §8.2), not an inconsistency. The
release notes must call this out; operators who customized predefined targets should
capture those customizations in their `catalog.yaml`.

## 10. Interaction with other work

- **Debian image support spec** (`2026-07-14-debian-image-support-design.md`): this
  feature can land **first, independently** — its shipped default is Debian-free (B1),
  so it needs nothing from the Debian spec. Once Debian *target* support lands
  (`release`→codename mapping, `sourceMode`/`dvdCount` columns), Debian becomes a
  supported catalog entry and the Debian spec's seeding open item (§11.1 there) is
  answered by the catalog (operators add Debian entries; a later default bump may add
  Debian 13 once it actually caches).
- **Multi-arch + channels spec** (deferred): its new rows (Flatcar beta/lts, FCOS
  testing, arm64) become `catalog.yaml` entries; its arm64 **URL-threading code
  fixes** remain separate.
- **`seed.go` (I2):** only the **static `predefined []db.Target` slice** is replaced
  by the catalog. The **dynamic host-derived Talos schematic loop**
  (`hostTalosSchematics` → `EnsureSchematicTarget`, marked `source=host`) is **not
  catalog-expressible and MUST survive** — it keeps reconciling schematic targets
  from registered hosts, coexisting with the catalog-apply pass (which never touches
  `source=host` rows). `SeedVanillaSchematic` (a boot *config*, not a target) is
  unaffected.
- **API/UI:** unchanged for `source=api`/`source=host` targets; add a
  `source=catalog` read-only/"managed" affordance (small UI follow-on).

## 11. Testing

- **Unit:** YAML parse + schema validation (unknown os/spec keys, missing required
  spec per OS, bad enums) fail-fast; desired-set → reconcile actions (create,
  update-declared-fields, disable-removed, leave-api-alone); source-marker
  migration; embedded-default load when catalog absent.
- **Idempotency:** reconcile against a pre-populated store performs **no** artifact
  fetches; restart-with-same-catalog → zero downloads (§9).
- **Round-trip:** each §7 example parses, validates, and produces the expected
  target rows.
- **Back-compat:** absent catalog reproduces today's exact predefined set.

## 12. Open items for written-spec review

1. **`source` representation** — a new `source` enum column (`catalog`/`api`/`host`)
   vs. keeping `predefined` and deriving; + the `TargetDTO` wire-field decision and
   `migrate.go` re-expression (§8.1). Plan decides the concrete shape; design fixes
   the three-value taxonomy.
2. **Managed-target UI affordance** — mark `source=catalog` as read-only now, or
   defer to a UI follow-on. (Design leans defer; note it.)
3. **Shipped-default delivery** — Go `embed` of a default `catalog.yaml` vs.
   building the default set in code. (Design leans `embed` for a single visible
   source of truth; plan confirms.)

## 13. Rough work breakdown (for the implementation plan)

1. Schema types + YAML loader (reuse the existing **`go.yaml.in/yaml/v4`** direct
   dep — `go.mod`, already used in `clustergen.go`/`debiangen.go`; do **not** add a
   new YAML lib, M1) + validation (fail-fast: unknown keys, enums, `schemaVersion`,
   per-OS arch) + `--catalogFile` flag.
2. Embedded default `catalog.yaml` via Go `embed` (established pattern) —
   **exactly today's Flatcar/FCOS/Talos set, no Debian** (B1).
3. `source` marker (`catalog`/`api`/`host`) + migration; re-express `migrate.go`'s
   collision marker; decide `TargetDTO` field (I1).
4. Reconcile: desired-set apply (create/update-declared-fields/disable-removed;
   leave `api` and `host` alone), placed before the per-target loop (M4),
   single-writer.
5. Replace **only the static predefined slice** in `seed.go`; **preserve the
   host-derived schematic loop** (`source=host`, I2).
6. Idempotency test (zero-download-on-restart) + upgrade-transition behavior +
   validation + round-trip tests.
7. (Follow-on, optional) UI "managed by catalog.yaml" affordance.

## 14. Disposition of SGE design-review findings

- **B1** (shipped default's Debian entry modeled against nonexistent Debian target
  support; would download Debian on upgrade) — adopted: default is Debian-free
  (§5/§7 ex 1); Debian entries forward-looking + loader rejects them until the Debian
  capability lands (§6); catalog lands independently (§10).
- **I1** (`predefined`→`source` under-enumerated) — adopted: `source ∈
  {catalog,api,host}`; `migrate.go` marker + `TargetDTO` wire field enumerated (§8.1).
- **I2** (retiring `seed.go` must not drop host loop) — adopted: only the static
  slice is replaced; host-schematic loop survives as `source=host` (§8.2/§10/§13.5).
- **I3** (upgrade reverts operator edits; `mode` inconsistency) — adopted:
  authoritative for **declared fields only** (`mode` stays API-owned); upgrade-
  transition subsection added (§8.2/§9).
- **M1** reuse `go.yaml.in/yaml/v4` (§13.1) · **M2** per-OS arch validation (§6) ·
  **M3** validate `schemaVersion` (§6) · **M4** apply-pass ordering (§8.2).
- **Verified sound by the reviewer (no change):** restart idempotency (§9),
  schema shape / `RequiredParams` fit, non-goal scoping.
