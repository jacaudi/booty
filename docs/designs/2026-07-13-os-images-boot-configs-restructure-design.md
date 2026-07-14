# OS Images / Boot Configs restructure — Design

**Date:** 2026-07-13
**Status:** Approved (user) · SGE-reviewed (verdict AMEND-BEFORE-PLANNING; all findings folded — see §9)
**Scope:** Frontend-only (`web/`). Folds into PR #58 (branch `worktree-ui-schematics-cache`), which is open and un-merged.
**Supersedes:** the tab/nav layout in `docs/designs/2026-07-13-ui-tabs-schematics-cache-design.md` (Schematics-as-a-Boot-Configs-tab; nav order). Everything else in that design stands.

## 1. Problem

The shipped UI files Schematics under **Boot Configs**, on the reasoning that a schematic is stored in the `configs` table with `kind='schematic'`. That is true of the *storage* and false of the *meaning*, and the backend says so plainly:

- `butane`, `machineconfig`, `preseed`, `debianconfig` flow through `renderConfig` (`pkg/http/render.go:59-84`) and are **served to the machine** — *what the machine runs*. `familyAllowsKind` (`render.go:34-43`) gates them per OS family.
- `schematic` appears in **neither** function — `renderConfig`'s `default:` arm errors on it, and `resolveConfig` gates both binding rungs through `familyAllowsKind` (`resolve.go:30,65`), so **no serving path can ever render a schematic**. It is POSTed to the Talos Image Factory (`api_configs.go:331-336`), which returns a content-addressed derived ID; it is bound via `host.schematic`. It is *which image the machine boots*.
- `taloscluster` is a third thing — a cluster spec, layered into every generated node config (`api_clusters.go:365-372`).

Corroboration: a Talos cache target is keyed **by the Factory-derived schematic ID** (`pkg/cache/schematic.go:23-38`, `params = {"schematic": …}`). The schematic *is* the image identity. Grouping it with boot configs puts two axes in one list and makes the create-kind picker ambiguous — a Talos host generally needs both a schematic (image) and a machineconfig (runtime).

## 2. Navigation

```
Home | Hosts | OS Images | Boot Configs | Clusters | About
```

`OS Images` is today's **Cache** page, renamed — which also applies the requested Cache-before-Boot-Configs reordering. `nav.tsx` stays the single source of truth:

| path | label | element |
|---|---|---|
| `/` | Home | `HomeView` |
| `/hosts` | Hosts | `HostsView` |
| `/images` | OS Images | `OSImagesView` *(new — holds the two tabs)* |
| `/boot-configs` | Boot Configs | `BootConfigsView` |
| `/clusters` | Clusters | `ClustersView` |
| `/about` | About | `AboutView` |

`/cache` → `/images` needs **no Go change**: `pkg/http/ui.go:30-32` serves any extensionless path as the SPA shell, and no Go or doc file references `/ui/cache`. The UI is unreleased, so no redirect is owed. The builder's deep-links live entirely in the query string (`useSearchParams`), so they round-trip unchanged under the new route.

## 3. OS Images page (was: Cache)

| Tab | Content |
|---|---|
| **Cached versions** | `CacheView`, with the schematic-grouping fix below. |
| **Schematics** | `SchematicsView` — list, full-page builder, Import-by-ID — moved from Boot Configs. |

`OSImagesView.tsx` is a thin router element holding the `Tabs` and rendering `<CacheView/>` / `<SchematicsView/>`; both keep their own files and responsibilities (structurally identical to what `BootConfigsView` already does).

### 3.1 Group the cache by schematic (fixes a live bug)

`cacheModel.channelOf` currently reads:

```ts
return e.params?.channel ?? e.os
```

Talos entries carry `params = {"schematic": …}` and **no `channel`**, so this falls through to `e.os` and **every schematic's images collapse into one `talos/talos` group** — visible today in the running app as a single Talos group silently merging distinct schematic targets. `params` is already on the wire (`api_cache.go:20,45`; `web/src/api/cache.ts:7`); the UI just ignores it.

Fix `channelOf` to fall back to the schematic before the OS, so each schematic becomes its own group and the Talos versions *inside* it are that image's versions:

```
▾ talos · rpi4-tailscale        3 version(s) · 3.1 GB
    schematic 43fac7…1367
▾ talos · metal-amd64-base      2 version(s) · 2.0 GB
    schematic 2d61dd…dcb8
▾ talos · 376567…b4ba           1 version(s) · 1.0 GB
    not referenced by any current schematic
▾ flatcar/stable                3 version(s) · 4.4 GB
```

A group is labelled with the **schematic config's name** when its derived ID matches a live schematic (cross-referenced from `listConfigs()` where `kind === 'schematic'`, using `derivedSchematicId`), and with the **short ID** otherwise.

That "otherwise" is load-bearing, not cosmetic: editing a schematic mints a new derived ID and therefore a **new cache target, and the old one is never pruned** (`pkg/cache/schematic.go:20-22` says so outright). Those orphans exist today and are currently invisible, padding the Talos blob. Labelling them *as* orphans is what makes the two tabs agree with each other.

## 4. Boot Configs page

Tabs stay **`Configs | Roles`**.

The Configs list excludes `kind === 'schematic'` (moved to OS Images) **and** `kind === 'taloscluster'` (now genuinely owned by Clusters — §5). Excluding `taloscluster` also retires the permanently-disabled Validate button those rows carried, since they are not renderable (`api_configs.go:211-215`).

Columns:

```
Name | Kind | Active Rev | Updated | Actions
```

There is **no OS column**. `ConfigDTO` (`api_configs.go:21-29`) carries no OS — a config binds to hosts, not to an OS — so an OS column would be a pure reverse-map of the Kind column beside it. Instead the **Kind cell leads with the OS product name and shows the literal server kind beneath it**:

| Server kind | Displayed as | Secondary |
|---|---|---|
| `machineconfig` | **Talos Linux** | `machineconfig` |
| `butane` | **Flatcar / Fedora CoreOS** | `butane` |
| `debianconfig` | **Debian** | `debianconfig` |
| `preseed` *(legacy)* | **Debian** | `preseed` |

`butane` names two OSes because the server has one `ignition` family serving both (`ostype/ignition.go:20-21`), and nothing on the config says which — so naming both is the honest reading, not a compromise.

Validate, the template cheat sheet, `Upload.Dragger`, and the Roles inline default-config `Select` are otherwise unchanged from PR #58.

### 4.1 Create flow

One choice — the OS. The kind follows from it; no format choice, no content sniffing.

```
Name  [ debian-worker ]

OS    ( ) Flatcar / Fedora CoreOS      → butane
      ( ) Talos Linux                  → machineconfig
      (•) Debian                       → debianconfig

      kind: debianconfig               ← displayed, not chosen
```

These three options cover **every OS booty supports**: `pkg/ostype` registers exactly four OSes across exactly three families (`ostype.go:58-62`, `ignition.go:20-21`, `talos.go:13`, `debian.go:10`). No family is left unable to author a config.

**Raw `preseed` is not offered.** `debianconfig` and `preseed` serve byte-identical output — `text/plain` at `/preseed` (`render.go:71-81`) — differing only in authoring format: `debianconfig` is structured YAML that `translateDebianConfig` compiles into a flat d-i preseed, exactly as `butane` → `ignition`. booty exists to make that easy, so the UI offers the structured format only. Bringing an existing preseed is a CLI concern (§8). A host with *no* config remains a valid state: it boots the installer and a human completes it.

This is a strict **authoring gain**, not a regression: today's picker is `['butane','machineconfig','preseed']` (`BootConfigsView.tsx:10`) and does not offer `debianconfig` at all.

An existing `preseed` row still lists, edits (PUT re-validates against the row's *stored* kind, `api_configs.go:167`), and validates correctly; it simply displays as **Debian · `preseed`**.

## 5. Clusters page — bind the spec

`taloscluster` may only be hidden from Boot Configs if something else owns it. Today **nothing does**: `ClustersView`'s Create/Import/Edit modals carry only name/endpoint/versions, and no UI anywhere sets `specConfigId`. Hiding the kind without this section would make cluster specs invisible in the entire UI while they continue to be layered into every generated node config.

So the cluster **Edit** modal gains a **Spec config** `Select`, fed by `listConfigs().filter(kind === 'taloscluster')`, with an explicit "none" state.

- It goes on **Edit only**: `PUT /clusters/{id}` accepts `specConfigId` (`api_clusters.go:182`, validated to be `kind=taloscluster` at `:207-216`), but `POST /clusters` **does not** (`api_clusters.go:128-133`). Create a cluster, then bind its spec. Frontend-only holds.
- Omitting `specConfigId` **preserves** the existing binding (`api_clusters.go:198-206`), and the server **cannot clear** one at all — a nil pointer is indistinguishable from an explicit null. So the Select offers no clear affordance, exactly as the Roles default-config Select does (same trap, already hit and fixed once on this branch). Unbinding is backend follow-up work (§8).
- `web/src/api/clusters.ts`'s `updateCluster` had `specConfigId` **removed** during PR #58's final review as a dead parameter. It now has a real caller and comes back.

## 6. `configKinds.ts` — one place for kind knowledge

The OS-family ↔ kind mapping is knowledge the server already owns (`familyAllowsKind`, `render.go:34`) but does not expose. The frontend must keep a copy, which can drift. Contain it: **one module** exports the OS list (create picker), the kind → display-name map (§4), and — critically — the *kind sets*:

- **renderable / boot-config kinds** (`butane`, `machineconfig`, `debianconfig`, `preseed`) — what the Configs list shows.
- **bindable kinds** — what a host's config Select and a role's default-config Select may offer.

This is not bookkeeping. Four sites currently hard-code `kind === 'schematic'` and **none excludes `taloscluster`**:

- `SchematicsView.tsx:32` · `HostsView.tsx:67` · `HostsView.tsx:158` · `BootConfigsView.tsx:316`

`HostsView.tsx:158` (host config bind) and `BootConfigsView.tsx:316` (role default) will therefore happily offer a **`taloscluster`** as a boot config. Binding one is **silently useless**: `familyAllowsKind("machineconfig","taloscluster")` is false, so `resolveConfig` falls through to the server-default file with only a `slog.Warn` (`resolve.go:30,39-41`). The user sees a bound config and gets an unbound boot. **This is a pre-existing bug**, and routing all four sites through `configKinds.ts` fixes it as a side effect.

Drift fails safe: a new server kind simply isn't offered by the UI; a tightened `familyAllowsKind` yields a loud 422, not corruption. An issue (§8) proposes exposing families via the API so this copy can be deleted.

## 7. Constraints (unchanged from PR #58)

Frontend-only — no Go / DB / route / API-contract change. No emoji or status-glyph characters. Dark theme via AntD design tokens (`theme.useToken()`), never hand-picked hex. No Drawer. DELETE buttons stay disabled with the "available after authentication (P10)" tooltip. Commit messages scrub all trailers.

## 8. Follow-ups (GitHub issues to file, not built here)

1. **Remove the `preseed` kind end-to-end** — DB `CHECK`, `render.go`, `familyAllowsKind`, docs.
2. **CLI: preseed → debianconfig converter** — the migration path for a raw preseed.
3. **Expose the OS-family ↔ kind mapping via the API** — retires `configKinds.ts`'s duplicated copy.
4. **Allow clearing a cluster's spec binding** (and a role's default config) — the API cannot distinguish "absent" from "explicitly null", so neither can be unbound. Needs PATCH-with-null semantics or a dedicated unbind route.
5. **Prune orphaned schematic cache targets** — editing a schematic strands its old derived-ID target forever (`pkg/cache/schematic.go:20-22`). The UI now surfaces them; the backend should reap them.

## 9. SGE review — findings folded

| Finding | Resolution |
|---|---|
| **BLOCKING-1** — "Clusters owns `taloscluster`" was false; nothing in the UI sets `specConfigId`, so hiding the kind would orphan it | §5: cluster Edit gains a Spec config Select (Edit only — POST doesn't accept the field) |
| **BLOCKING-2** — the co-location thesis wasn't delivered: `channelOf` collapses all schematics into one `talos/talos` group, and orphaned targets are never pruned | §3.1: group by schematic, name live ones, flag orphans |
| **IMPORTANT-1** — four hard-coded `kind` filters; Host/Role Selects offer a non-bindable `taloscluster` (pre-existing bug) | §6: all four route through `configKinds.ts`, which owns the bindable set |
| **IMPORTANT-2** — the OS column had no backing field (pure reverse-map of Kind) | §4: OS column dropped; the Kind cell carries the OS name |
| **MINOR-1** — unmapped-kind fallback unspecified for the OS column | §4: `preseed` → **Debian**; column dropped anyway |
| MINOR-2 (route rename safe), MINOR-3 (`OSImagesView` earns its keep) | Confirmed; §2 |

All five backend claims the design rests on were verified against source and **CONFIRMED**.

## 10. Testing

Vitest + Testing Library, per existing conventions. The load-bearing assertions:

- The Configs list excludes `schematic` **and** `taloscluster`.
- Each OS in the create form produces the correct `kind` on the wire (`butane` / `machineconfig` / `debianconfig`) — asserted against the actual `createConfig` argument. `preseed` is offered nowhere.
- The Kind cell renders the OS name with the raw kind beneath it, including the legacy `preseed` → **Debian** fallback.
- `cacheModel.groupKey` splits two schematics of the same OS/arch into **separate** groups (this fails on today's code), and a schematic ID matching no live schematic is labelled as unreferenced.
- The host config Select and the role default-config Select offer neither `schematic` nor `taloscluster`.
- The cluster Edit modal sends `specConfigId` when a spec is picked, and **omits** it when untouched (preserve-the-binding semantics).
- The Schematics tab renders under OS Images; the builder's deep-links round-trip under `/images`.
- The nav renders in the new order.
