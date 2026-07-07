# P5 — Talos Image Factory Schematics — Design

**Date:** 2026-07-04 · **Slice:** P5 (v1 roadmap) · **Issues:** [#24](https://github.com/jacaudi/booty/issues/24), [#32](https://github.com/jacaudi/booty/issues/32) · **PR target:** `jacaudi/booty:main` · ships **after** P4 (merged as [#52](https://github.com/jacaudi/booty/pull/52), `82ea2cd`); rebase base = `main`.

> **Session note:** designed 2026-07-04 in a brainstorm reconciled against **current Talos v1.13** (Image Factory API, boot-assets, kernel reference) with two findings verified at the source level (see §11). All decisions **D1–D7 are USER-APPROVED** (each as recommended, walked through individually); alternatives remain on file in §12. P5 is the **foundation** [[booty-v1-design]] P6 (cluster authoring) builds on — per-node schematic binding is the seam P6 consumes.
>
> **SGE review amendment (2026-07-06):** an `sr-go-engineer` design review (verdict *AMEND-BEFORE-PLANNING*, 0 blocking) was folded. P5-side gap-closing amendments (no approved decision reversed): **I3** per-kind validation dispatch (§4), **I4** seed vanilla by known-ID not Factory POST (D7/§13), **M1** bounded Factory POST (§4), **M4** schematic-target pruning note (§5), **I2** member-bind guard (§5). The related P6 **I1** netboot-version pin was **confirmed Option A** by the user 2026-07-06 (pin a member's netboot version to `cluster.talos_version`).

---

## 1. Context & problem

A Talos schematic is the **content-addressed ID of an image customization** — principally a set of Image Factory system extensions (and, for SBCs, an overlay). The ID names *both* the boot assets booty serves (`/image/<schematic>/<version>/kernel-<arch>` + `initramfs-<arch>.xz`) *and* the installed system's `machine.install.image` (`factory.talos.dev/installer/<schematic>:<version>`). Verified: *"The schematic ID is based on the schematic contents, so uploading the same schematic will return the same ID"* (Image Factory docs).

Today in booty the schematic is an **opaque, unnamed, unvalidated free string**:

- `pkg/hardware/mac.go` — `Host.Schematic string` (no validation, no dedicated API; set only via a raw host write).
- `pkg/http/api_hosts.go:156` — at approve time, `if h.OS == "talos" && h.Schematic != "" { params["schematic"] = h.Schematic }`, so the string flows into the target params → the cache segment `<os>/<schematic>/<arch>/<version>` (`pkg/cache/layout.go`) → `pkg/ostype/talos.go` factory URL.
- `pkg/config/config.go:68` — `TalosSchematic` default = `376567988ad3…b4ba`, which is exactly the Factory **vanilla** (no-extensions) schematic. It is the fallback in `pkg/http/machineconfig.go`.

To obtain a non-vanilla schematic today, an operator leaves booty, opens factory.talos.dev, selects extensions, copies the sha256, and pastes it into a host. **Nothing records what is in it or gives it a name.** Worse, the Factory *cannot* help here: *"Image Factory does not provide a way to list all schematics, as schematics may contain sensitive information."* The Factory will never enumerate an operator's schematics — so a booty-side registry is not a convenience, it is **the only place schematics are named and listed**.

P5 makes schematics **first-class, named, buildable DB state**, reusing P4's config machinery: author an extension set once, booty resolves it to an ID against the Factory and pre-caches the image, and hosts bind by that ID — with **no change to the boot/param/cache path** that already works.

### 1.1 Operator workflows this slice must serve

1. **Author a schematic** — name it (`cp-min`, `iscsi`, `gpu`) and declare its extensions/overlay; booty builds it against the Factory and records the ID.
2. **See the catalog** — list named schematics with their IDs and extension sets (the thing the Factory itself won't show).
3. **Bind a host** — pick a named schematic; booty writes its ID into `host.Schematic` (the existing seam), so the host boots that image and installs it.
4. **Edit** — change a schematic's extensions; a new revision mints a new ID (content-addressing). Bound hosts keep their current ID until re-bound (frozen; see D3).

---

## 2. Goals / non-goals

**Goals**

1. A new config **`kind = 'schematic'`** in the existing P4 `configs`/`config_revisions` tables (D1). Source = the extension customization YAML; each revision resolves to one schematic ID.
2. **Build against the Factory** on save: `POST <factory>/schematics` (YAML body) → `{id}` stored on the revision (D2). Air-gap = point `--talosFactoryURL` at a private/self-hosted factory (existing flag; no new mechanism).
3. **Per-node binding** through the existing `host.Schematic` natural key (D3) — additive, no boot/param/cache change. Editing mints a new ID; hosts roll forward only on explicit re-bind.
4. **Pre-cache** — saving a schematic ensures a Talos cache target so the reconciler eagerly fetches its boot assets (D4).
5. **Scope = extensions + overlays** (D5) — the knobs that actually take effect on booty's `/image` + installer paths.
6. Seed the **vanilla** schematic into the registry at startup so the UI always shows a baseline (D7).

**Non-goals (YAGNI — see §12)**

- **Local schematic-ID computation** and a **bare-ID import escape hatch** — worthless without a reachable Factory to serve the image bytes; air-gap is solved by a private Factory (D2).
- **`extraKernelArgs` / `meta` in schematics** — the Factory *ignores* both on booty's initramfs + installer paths (D5); kernel args belong in the iPXE cmdline / `machine.install.extraKernelArgs`.
- **Secureboot / UKI** assets — deferred; cache layout stays forward-compatible (D6).
- **An extensions/overlays catalog picker** (proxying `GET /version/:v/extensions/official`) — nice, not needed for v1; raw-YAML authoring is the core. Deferrable follow-on.
- Vendoring `siderolabs/image-factory` client packages — a single stdlib `net/http` POST suffices (a little copy over a little dependency).
- `DELETE` enabled (wired-but-403 until P10, mirroring P4).

---

## 3. Data model — a config kind, not a new table

P5 adds **no new table**. It reuses P4's `configs` (identity: `name`, `kind`, active-revision pointer) and `config_revisions` (immutable, append-only source copies), with `kind = 'schematic'`. This gives naming, listing, and **revision history for free**, and revisions map exactly onto content-addressing: *edit the extension set → new revision → new ID.*

**Migration `0004`** (additive, the only schema change):

```sql
ALTER TABLE config_revisions ADD COLUMN derived_schematic_id TEXT;  -- NULL except for kind='schematic'
```

- `source_b64` — the authored customization YAML (as for every kind).
- `derived_schematic_id` — the sha256 the Factory returned for *that* revision's source. NULL for non-schematic kinds. Populated on build.
- A schematic config's **current ID** = its active revision's `derived_schematic_id`. Rollback (P4's active-pointer move) re-points to an older revision → its already-stored ID.

The customization YAML booty submits (D5 scope):

```yaml
customization:
  systemExtensions:
    officialExtensions:
      - siderolabs/iscsi-tools
      - siderolabs/util-linux-tools
  # overlay (SBCs) permitted:
  # overlay: { name: rpi_generic, image: siderolabs/sbc-raspberrypi }
```

---

## 4. Build flow (create / update)

Schematic-kind configs reuse P4's storage and list/get, but their **create/update path branches** from the template-render validation used by butane/preseed/machineconfig:

```
POST /configs { kind: "schematic", name, source }   (or PUT /configs/{id})
  1. buildSchematic(source):
       POST <talosFactoryURL>/schematics   body = source (YAML)
       → 200 { "id": "<sha256>", "schematic": "<canonical yaml>" }
       (Factory error → 422 "schematic build failed: <detail>")
  2. append revision (source_b64), set derived_schematic_id = <id>, advance active pointer   [P4 machinery]
  3. ensureSchematicTarget(<id>)  → cache reconciler pre-fetches boot assets   (§5)
```

- The Factory **owns the build**; booty submits the extension set via one stdlib `net/http` POST and records the ID it assigns. No compilation, no vendored client.
- **Validation is the build** — a malformed customization is rejected by the Factory with a non-2xx, surfaced as 422 (parallels P4's render-validation-on-create).
- **Validation dispatch (SGE I3):** live `create-config`/`update-config` (`pkg/http/api_configs.go:80,147`) hardcode `renderConfig(kind, source, stubVars())` as the sole validation gate, and `Body.Kind` carries `enum:"butane,machineconfig,preseed"`. `schematic` is **non-renderable** (`renderConfig` would hit `default: unknown config kind`). P5 therefore introduces a small **per-kind validation dispatch**: renderable kinds (butane/machineconfig/preseed) → `renderConfig`; `schematic` → `buildSchematic` (the Factory POST). This is the additive seam P6's `taloscluster` (a *designed, imminent* second non-renderable kind, validated by spec/patch parse) slots into as a new arm — not an edit to P5's handler (No-Wall; present-consumer test passes because P6 is the concrete next consumer).
- **Factory POST is bounded (SGE M1):** the `POST /schematics` call carries a **short per-request context deadline** (not `config.httpClient`'s 5-minute timeout), so a slow/unreachable Factory fails the create as 422/504 rather than blocking the request for minutes.
- **Air-gap:** `--talosFactoryURL` already redirects the POST (and all `/image` fetches) to a private/self-hosted Factory. No bare-ID import path — an ID with no Factory to serve its bytes is useless (D2).
- Serving handlers (`/machineconfig`, `/ignition.json`, `/preseed`) only dispatch butane/machineconfig/preseed, so **schematic-kind configs are never served** — they resolve to an ID + a cache target, nothing more. `renderConfig` gains no `schematic` case (it is not a template).

---

## 5. Binding & pre-cache (the seams P5 touches, additively)

**Binding — unchanged boot path.** A host's schematic remains the free-string `host.Schematic` (the sha256). P5 only changes *where the value comes from*: the UI picks a schematic-kind config by name and writes its current `derived_schematic_id` into `host.Schematic`. Everything downstream (`api_hosts.go` approve → `params["schematic"]` → cache segment → `talos.go` factory URL → `.Schematic`/`.TalosVersion` TemplateVars in the machineconfig) is **untouched**. The registry is an *advisory catalog* — `host.Schematic` values not in the registry (legacy, or the raw default) still work exactly as today; the registry only adds names to known IDs.

> **SGE I2 (member-bind guard):** the raw P5 binding path (direct `host.Schematic` write) must **refuse when `host.cluster_id` is set** (the host is a P6 cluster member). A member's schematic — and its pinned netboot version (P6 I1, confirmed Option A) — is single-sourced through P6's add-member/regenerate path, which re-freezes the node config so the served machineconfig's `install.image` and the netboot assets never drift apart. Without this guard, a raw P5 re-bind would change a member's netboot image while its frozen `install.image` stays pinned (boot/install divergence).

> **No-Wall:** binding by the natural sha256 key (not a surrogate `host.schematic_id` FK) keeps P5 purely additive — new `db`/`http` files + a UI dropdown, siblings untouched. An FK would force edits to the approve path, param encoding, and the host DTO, and would break the default-schematic fallback that is not a registry member. (D3.)

**Pre-cache.** `ensureSchematicTarget(id)` creates (idempotently) a Talos cache target keyed on that schematic ID, so the existing reconciler eagerly fetches `kernel-<arch>` + `initramfs-<arch>.xz` for the schematic across the talos version window (`--talosRetainMinors`). Version-independence is verified: the schematic ID is fixed while `/image/<id>/<version>/` varies by version, so one schematic pre-caches every retained version. (Pinning a *specific* version against retention is a **P6** concern — a standalone schematic just tracks the window.)

> **SGE M4 (informational):** `ensureSchematicTarget` is a *second trigger* (schematic-save) for the same "ensure a Talos discovery target for schematic X" knowledge that `cache.seedTargets` already ensures per distinct `host.Schematic` (`pkg/cache/seed.go:62-84`). Both funnel through the single-sourced `EnsureTarget` (not a DRY violation). Schematic-derived targets inherit the existing **"host-derived rows are not pruned until the host API lands / P10"** behavior already noted in `seed.go` — a save-created schematic target likewise persists until deletion-driven pruning exists. Acceptable for v1 (it only over-caches).

---

## 6. Scope: extensions + overlays only (a verified guardrail)

Image Factory docs, verbatim: *"Installer and initramfs images support only system extensions; kernel args and META are ignored. Kernel assets are schematic-independent."*

booty netboots via `/image/<schematic>/<version>/kernel + initramfs` and installs via the `installer` image — **both paths honor only system extensions (and overlays); `extraKernelArgs` and `meta` are silently dropped.** Exposing those fields would be a footgun (a knob that does nothing). Therefore P5 schematics carry **extensions + overlays** only. Kernel args for booty's flow belong where they take effect: the iPXE kernel cmdline (boot) or `machine.install.extraKernelArgs` (installed system, set via a P4/P6 machineconfig). (D5.)

Secureboot/UKI assets (`-secureboot`, `-uki.efi`) are deferred (D6); the cache layout already isolates the schematic segment, so adding them later is additive.

---

## 7. UI — Schematics (antd v5, token-driven / v6-compatible)

Added additively through the existing Boot Configs surface (P4's `nav.tsx` seam):

- **Schematics list** — name, short ID (`a1b2…39ff`), extension set, updated-at. This is the catalog the Factory refuses to provide.
- **Author / edit** — name + a small extensions multi-select (free-form officialExtensions strings for v1; a Factory-fed picker is the deferrable follow-on) + optional overlay. Save = build + toast the resolved ID.
- **Host bind** — the Talos host assign/approve flow gains a schematic dropdown sourced from this list; selecting one writes its ID into `host.Schematic`. Free entry stays allowed (advisory registry).

---

## 8. Constraints (unchanged project invariants)

- stdlib `net/http` for the Factory POST; **no vendored image-factory client**.
- `log/slog`; host mutations through the `pkg/hardware` wrappers; reads via `deps.Store`.
- Every new knob is an explicit cobra flag (booty has no config file / `AutomaticEnv`). P5 adds **no new flag** — `--talosFactoryURL` and `--talosSchematic` already exist.
- CoreOS two-vocabulary bridge is irrelevant here (Talos name is identical in both vocabularies), but any `ostype.Lookup` on a host OS still goes through `cache.CacheNameToCanonical` per the standing rule.
- `DELETE /configs/{id}` stays wired-but-403 (P4 behavior) — covers schematic-kind too.

---

## 9. Testing (against the real harnesses)

- `buildSchematic` against an `httptest` Factory stub: valid source → ID stored on the revision + `ensureSchematicTarget` called; Factory 4xx/5xx → 422, no revision, no target.
- Edit mints a new revision with a *different* `derived_schematic_id`; rollback re-points to a prior revision's stored ID (no re-POST).
- Binding writes the active ID into `host.Schematic`; the approve path still produces the same `params["schematic"]` encoding (byte-identical target key) — a guard that P5 did not perturb the boot path.
- Vanilla seed is idempotent (create-if-absent; a second startup is a no-op).
- Docker smoke: author a real extensions schematic against factory.talos.dev, confirm the returned ID matches the Factory UI and the boot assets cache.

---

## 10. Documentation gate (slice incomplete without)

Per-slice doc gate: update `docs/schema/{API,DATABASE}.md` (the `schematic` kind, the `derived_schematic_id` column, the build endpoints) and `docs/CONFIGURATION.md` (the extensions+overlays scope + the air-gap-via-private-factory note). README verified for no-change.

---

## 11. Verified facts (Talos v1.13, this session)

- Schematic is content-addressed; the Factory returns `{id, schematic}` from `POST /schematics`. Vanilla ID = `376567988ad3…b4ba` (= booty's current default). — Image Factory docs / API.
- Same ID names boot assets **and** the installer; version supplied separately (`/image/<id>/<ver>/…`, `installer/<id>:<ver>`); upgrades keep the ID, change only the tag. — boot-assets.
- **`extraKernelArgs`/`meta` ignored on initramfs + installer; kernel is schematic-independent** (the §6 guardrail). — Image Factory learn-more.
- The Factory will not list schematics (§1 rationale). — Image Factory learn-more.
- Install-image form: standardize on `installer/<schematic>:<version>` (the v1.13 default `talosctl gen config` emits); `metal-installer/…` is an equivalent alias.

---

## 12. Explicit YAGNI / KISS cuts

- No surrogate FK (`host.schematic_id`); bind by natural sha256 (D3).
- No revisions/rollback machinery *invented* — reused from P4; content-addressing rides P4's immutable revisions.
- No local ID computation; no bare-ID import; no Factory client vendor (D2).
- No kernel-args/META fields; no secureboot/UKI; no catalog-picker in core (D5/D6).
- No new flag; no new table (one nullable column).

---

## 13. Appendix — decisions (ALL USER-APPROVED 2026-07-04, each as recommended)

- **D1 — Home:** schematic is a **new config `kind`** in P4's `configs`/`config_revisions` (reuse CRUD, list, revisions), not a dedicated table. *(Alt: dedicated `schematics` table; extend the Talos target with an extension list — rejected as more surface / cross-OS coupling.)*
- **D2 — Build-only via Factory:** save POSTs to `--talosFactoryURL`; air-gap = private/self-hosted Factory. **No bare-ID escape hatch** (an ID is useless without a Factory to serve bytes). *(Alt: bare-ID import; local ID compute — rejected.)*
- **D3 — Frozen per-node binding:** `host.Schematic` holds the sha256; editing a schematic mints a new ID, bound hosts keep their current image until re-bound; version rolls forward automatically via `NewestCached`. Confirmed version-independent against the Factory docs. *(Alt: config-reference resolved-at-boot — rejected, changes the boot path, re-images on edit.)*
- **D4 — Ensure pre-cache target on save** so the reconciler eagerly fetches boot assets.
- **D5 — Scope = extensions + overlays only:** `extraKernelArgs`/`meta` are Factory-ignored on booty's boot/install paths, so they are excluded (documented not-applicable). *(Alt: allow-all-with-caveat — rejected as a live footgun.)*
- **D6 — Defer secureboot/UKI;** keep cache layout forward-compatible.
- **D7 — Seed the vanilla schematic** (`376567…b4ba`) into the registry at startup (create-if-absent) so the UI shows a baseline. **(SGE I4):** the seed inserts the **known constant ID** (already `config.TalosSchematic`, `config.go:68`) + its `customization: {}` source directly into `configs`/`config_revisions` — it **never POSTs to the Factory during startup**. Seeding via `buildSchematic` would make booty's startup depend on Factory reachability (a disposability regression and an air-gap hazard: a private/self-hosted Factory may be down at boot). The vanilla ID is a documented constant (§11), so no build is needed to seed it.

---

## 14. Acceptance criteria

1. Migration `0004` adds `config_revisions.derived_schematic_id` (nullable); existing rows/behaviour unchanged.
2. `kind='schematic'` configs can be authored/edited/listed; save builds against the Factory and stores the returned ID on the revision; Factory errors → 422.
3. `ensureSchematicTarget` pre-caches a built schematic's boot assets via the existing reconciler.
4. Binding a schematic writes its ID into `host.Schematic`; the resulting target-param encoding is byte-identical to today (boot path unperturbed).
5. Vanilla schematic is present after first startup (seeded by its **known constant ID**, no Factory POST — SGE I4) and idempotent across restarts.
6. Schematic-kind configs are never served by `/machineconfig`/`/ignition.json`/`/preseed`.
7. Docs gate met (§10). `go test ./... -race`, `vet`, and web `tsc` clean.
