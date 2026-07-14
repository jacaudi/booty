# OS Images / Boot Configs restructure — Design

**Date:** 2026-07-13
**Status:** Approved (user, 2026-07-13)
**Scope:** Frontend-only (`web/`). Folds into PR #58 (branch `worktree-ui-schematics-cache`), which is open and un-merged.
**Supersedes:** the tab/nav layout in `docs/designs/2026-07-13-ui-tabs-schematics-cache-design.md` (Schematics-as-a-Boot-Configs-tab; nav order). Everything else in that design stands.

## Problem

The shipped UI files Schematics under **Boot Configs**, on the reasoning that a schematic is stored in the `configs` table with `kind='schematic'`. That is true of the *storage* and false of the *meaning*, and the backend says so plainly:

- `butane`, `machineconfig`, `preseed`, `debianconfig` flow through `renderConfig` (`pkg/http/render.go`) and are **served to the machine** — they are *what the machine runs*. `familyAllowsKind` gates them by the host's OS family.
- `schematic` appears in **neither** function. It is POSTed to the Talos Image Factory, which returns a content-addressed derived ID; it is bound via `host.schematic`, never rendered, never served as a config. It is *which image the machine boots*.
- `taloscluster` is a third thing again — a cluster spec, owned by the Clusters page.

Corroboration: a Talos cache target is keyed **by schematic ID** (`targets.params = {"schematic": "…"}`). The schematic *is* the image identity. Grouping it with boot configs puts two different axes in one list and makes the create-kind picker ambiguous — a Talos host generally needs both a schematic (image) and a machineconfig (runtime).

## Design

The organizing principle: **images** (what the machine boots) vs **configs** (what the machine runs).

### 1. Navigation

```
Home | Hosts | OS Images | Boot Configs | Clusters | About
```

`OS Images` is today's **Cache** page, renamed. This also applies the requested Cache-before-Boot-Configs reordering.

`nav.tsx` stays the single source of truth for routes and menu. The entries become:

| path | label | element |
|---|---|---|
| `/` | Home | `HomeView` |
| `/hosts` | Hosts | `HostsView` |
| `/images` | OS Images | `OSImagesView` *(new — hosts the two tabs)* |
| `/boot-configs` | Boot Configs | `BootConfigsView` |
| `/clusters` | Clusters | `ClustersView` |
| `/about` | About | `AboutView` |

The route is renamed `/cache` → `/images` to match the page. The UI is unreleased, so no redirect from `/cache` is owed. `OSImagesView` is a thin new file holding the `Tabs` and rendering `<CacheView/>` and `<SchematicsView/>`; both of those keep their own files and responsibilities.

### 2. OS Images page (was: Cache)

Two tabs:

| Tab | Content |
|---|---|
| **Cached versions** | Today's `CacheView`, unchanged. |
| **Schematics** | `SchematicsView` — the list, the full-page builder, and Import-by-ID — moved from Boot Configs. |

The two tabs are the same subject from two directions: Schematics *defines* a custom Talos image; Cached versions shows the images that were then discovered and cached for it.

The builder remains a local view-switch inside its tab (not a router route) and keeps its query-string deep-links. Those links now hang off the OS Images route rather than the Boot Configs route; the UI is unreleased, so no redirect is owed.

### 3. Boot Configs page

Tabs stay **`Configs | Roles`**.

The Configs list excludes `kind === 'schematic'` (moved to OS Images) **and** `kind === 'taloscluster'` (a cluster spec, owned by Clusters). This retires the permanently-disabled Validate button that `taloscluster` rows carried, since a taloscluster is not renderable.

Columns:

```
Name | OS | Kind | Active Rev | Updated | Actions
```

`Kind` reads as a friendly name with the literal server kind beneath it in dim secondary text — the friendly label is what you read; the real kind is never hidden.

| OS | Friendly kind | Server kind |
|---|---|---|
| Flatcar / Fedora CoreOS | Ignition (Butane) | `butane` |
| Talos | Machine config | `machineconfig` |
| Debian | Debian config | `debianconfig` |

Validate, the template cheat sheet, `Upload.Dragger`, and the Roles inline default-config `Select` are unchanged from PR #58.

### 4. Create flow

One choice — the OS. The kind follows from it; there is no format choice and no content sniffing.

```
Name  [ debian-worker ]

OS    ( ) Flatcar / Fedora CoreOS
      ( ) Talos
      (•) Debian

      kind: debianconfig          ← displayed, not chosen

┌─ source ──────────────────────┐
│  drag a file, or type…        │
└───────────────────────────────┘
```

**Raw `preseed` is not offered.** `debianconfig` and `preseed` serve byte-identical output (`text/plain` at `/preseed`); they differ only in authoring format — `debianconfig` is structured YAML that booty translates into a flat d-i preseed (`translateDebianConfig`), exactly as `butane` → `ignition`. booty exists to make that easy, so the UI offers the structured format only. Bringing an existing preseed is a CLI concern (see Follow-ups). A host with *no* config is already a valid state — it boots the installer and a human completes it.

The `preseed` kind still exists server-side, so the UI must not crash on one; but the software is alpha and no design accommodation is made for it beyond the `Kind` column falling back to the raw kind string.

### 5. `configKinds.ts` — one place for the mapping

The OS-family ↔ kind mapping is **knowledge that already exists server-side** (`familyAllowsKind`, `pkg/http/render.go:34`), but no endpoint exposes it. The frontend must therefore keep its own copy, which can silently drift.

Contain the damage: one module, `web/src/api/configKinds.ts`, is the single client-side source of truth — the create picker's OS list, the friendly `Kind` labels, and the list's OS column all read from it. Nothing else hard-codes a kind string. An issue is filed to expose families via the API so this copy can be deleted.

## Constraints (unchanged from PR #58)

Frontend-only — no Go / DB / route / API-contract change. No emoji or status-glyph characters. Dark theme via AntD design tokens (`theme.useToken()`), never hand-picked hex. No Drawer. DELETE buttons stay disabled with the "available after authentication (P10)" tooltip. Commit messages scrub all trailers.

## Follow-ups (GitHub issues to file, not built here)

1. **Remove the `preseed` kind end-to-end** — DB `CHECK` constraint, `render.go`, `familyAllowsKind`, docs.
2. **CLI: preseed → debianconfig converter** — the migration path for anyone holding a raw preseed.
3. **Expose the OS-family ↔ kind mapping via the API** — retires `configKinds.ts`'s duplicated copy of `familyAllowsKind`.

## Testing

Vitest + Testing Library, per existing conventions. The load-bearing assertions:

- The Configs list excludes `schematic` **and** `taloscluster`.
- Selecting each OS in the create form produces the correct `kind` on the wire (`butane` / `machineconfig` / `debianconfig`) — asserted against the actual `createConfig` argument.
- `preseed` is offered nowhere in the create flow.
- The Schematics tab renders under OS Images, and the builder's deep-links still round-trip under the new route.
- The nav renders in the new order.
