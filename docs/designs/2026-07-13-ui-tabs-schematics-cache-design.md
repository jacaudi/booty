# Design — UI slice: Schematics (#32), Cache (#33), and #31 frontend gaps

**Type:** design
**Date:** 2026-07-13
**Issues:** #31 (partial — frontend gaps), #32 (Schematics + Talhelper), #33 (Cache view)
**Status:** approved (brainstorming), pending implementation plan

## 1. Summary

This slice advances Phase-2 (UI) of the v1 roadmap (#19) by building the web UI for
capabilities whose backends are already merged: the Talos **Schematics** builder (#32),
the rich **Cache** inventory view (#33), and the doable **frontend gaps** of the first UI
slice (#31). It is deliberately **frontend-only** — no DB/API/route changes — and every
part of #31/#32/#33 that requires backend work the API does not yet expose is **deferred
and documented on its GitHub issue**.

#34 (Dashboard + DB-admin + Replication) is out of scope: its backends (#27 ops, #28
backup/snapshots, #29 Litestream) are unbuilt.

## 2. Context (current state, audited 2026-07-13 against `main`)

**Stack:** React 18 + AntD v5 + Vite + Vitest. `web/src/api/*.ts` are thin typed wrappers
over a shared `request<T>()` (`client.ts`) — no auth header today. `nav.tsx` is the
`{path,label,element}` single source of truth consumed by `App.tsx` for both `Menu` and
`Routes`. Views follow a `load()`/`act()` + Tabs-per-view pattern. Edit UIs are **Modals**
(the only Drawer today is the Boot Configs Revisions panel).

**Already done (from earlier slices):** AntD shell, no Bootstrap; `BootConfigsView` with
Configs/Schematics/Roles tabs; `HostsView`; a minimal `CacheView`; `ClustersView`
(list/create/import/members).

**Backends ready for this slice:** `/configs` (kind=schematic; Factory ID computed
server-side in `pkg/http/schematic.go`), `/clusters` (create/import/export/update/members),
`/cache` (list with `os`/`arch`/`state`/`pinned` filters, scan, pin/unpin/reverify;
delete is 403 until P10 auth).

## 3. Locked design decisions

1. **Frontend-only.** New/updated views + `api/*.ts` wrappers + Vitest tests. No backend change.
2. **No emojis.** Use AntD icons (`CheckCircleFilled`/`WarningFilled`/`CloseCircleFilled`,
   `PushpinOutlined`, `SearchOutlined`), colored `Tag`s, or plain text. (Durable project rule.)
3. **No overlays for the two big surfaces.** Cache = grouped master list + always-visible
   detail pane. Schematics = full-page builder (list screen ⇄ builder screen via Back).
   Simple Boot Configs name/source edits stay **Modal**. The Drawer pattern is not introduced.
4. **Clusters/Talhelper stays a top-level page** (not folded into Boot Configs tabs); cluster
   lifecycle is heavier than a tab wants to be. Nav order adopts the spec's intent:
   `Home | Hosts | Boot Configs | Cache | Clusters | About`.
5. **Every deferred item is documented on its issue** — a "Remaining backend work to complete
   this issue" comment on #31/#32/#33 (see §7). This is a deliverable of the slice.
6. **Spec-vs-convention conflicts** were resolved case-by-case (validated visually); codebase
   consistency generally wins, divergences from the issue text are noted here.

## 4. Per-tab design

### 4.1 Cache view (#33) — grouped master list + detail pane

- **Summary strip:** disk used vs budget (`Progress`), and counts for in-cycle / archived /
  pinned / failed (failed styled as an alert tile).
- **Toolbar:** `Segmented` state filter (All / In cycle / Archived / Pinned / Failed) →
  maps to the `state`/`pinned` query params; OS `Select` and version `Input.Search` →
  `os`/`version`; **Scan now** button (`POST /cache/scan`, reports scanned/updated/orphans).
- **Master list (left):** collapsible section per target (`<os>/<channel>`), each with a
  rollup (version count + total size). Version rows show state `Tag`(s), a verify status icon,
  size (human-readable), and a selection checkbox. Selecting a row drives the detail pane.
- **Detail pane (right):** the selected version's header + verify status; action buttons
  **Pin/Unpin** (`/cache/{id}/pin|unpin`), **Re-verify** (`/cache/{id}/reverify`), and a
  **Delete** button disabled with a tooltip ("available after authentication (P10)").
  A **Files** section renders a documented stub for the per-file sha256 / `verify_kind`
  breakdown (deferred — see §7).
- **Bulk actions:** row selection enables Pin all / Unpin all / Re-verify all via
  **client-side fan-out** over the single-item routes (no bulk endpoint exists; documented).
- **States:** over-budget-with-nothing-evictable `Alert`; failed-verification `Alert` linking
  to the Failed filter.
- **API:** `web/src/api/cache.ts` already has list/pin/unpin/reverify/scan; add only what the
  grouping/filters need (client-side grouping; no new endpoints).

### 4.2 Schematics (#32) — full-page builder

- **List screen:** table (Name, Hardware, Talos, Arch, Ext count, Derived ID + copy) with
  **New schematic** and **Import by ID**. Names/rows navigate into the builder.
- **Builder screen (full width):** breadcrumb `‹ Schematics / <name>` (Back returns to the
  list), inner tabs **Builder / Raw YAML**. Builder form in factory-wizard order: Name →
  Hardware type (`Segmented` metal/cloud/sbc) → Talos version + Architecture (`Select`) →
  System extensions (tag multi-select) → Extra kernel args (tag mode) → Advanced (overlay,
  secureboot, collapsed). A **live YAML** pane renders the customization the client builds.
- **Generate/Save:** the client builds the customization YAML (existing
  `web/src/api/schematicYaml.ts` `buildCustomization`), `POST`/`PUT /configs` with
  `kind:"schematic"`; the **server** posts to the Factory and returns `derivedSchematicId`,
  shown in a success `Alert` with copy.
- **Import by ID:** modal to paste a sha256 → opens the builder pre-populated from the
  Factory's stored body.
- **Deep-links:** builder URL carries `?hw&arch&version&ext&kargs&secureboot` matching
  factory.talos.dev, so a schematic is shareable/bookmarkable.
- **Extension catalogue:** start with a small static list of common `siderolabs/*` extensions
  with descriptions; "fetch the live catalogue from the Factory" is a documented follow-up.
- **API:** `web/src/api/configs.ts` (schematic kind) + `schematicYaml.ts` already cover the
  build/save path; add URL-param (de)serialization helpers.

### 4.3 Talhelper / Clusters (#32, top-level) — light touch

- Keep `ClustersView` as the top-level page. Wire the **already-existing** server endpoints
  that the frontend client omits: `export-cluster-secrets` and `update-cluster` into
  `web/src/api/clusters.ts` + minimal UI (Export action, Edit form).
- **Defer:** the richer talhelper inner-tabs editor + Validate + Generate-Steps modal +
  stale detection (no `/clusters/{id}/validate` or `/generate` route exists).

### 4.4 #31 frontend gaps (only the backend-unblocked ones)

- **Boot Configs → Configs:** add a **Validate** button (surfaces the create/update
  validation errors structurally), a **template-variables cheat sheet** side panel, and a
  drag-and-drop **`Upload.Dragger`** that pre-fills the new-config form.
- **Boot Configs → Roles:** inline-editable **Default Config** `Select` in the row (replacing
  the edit-modal round-trip).
- **Hosts:** restructure the stacked Pending/Approved `<div>`s into AntD **`Tabs`** with a
  **Badge** count on Pending.
- **Nav:** reorder to `Home | Hosts | Boot Configs | Cache | Clusters | About`.

## 5. Data flow & structure

- New view components follow the established shape: `useState` data/loading/error, a
  `useCallback` `load()`, `useEffect(load,[load])`, and an `act()` wrapper that runs a
  mutation → `message` → reload. Mutations go through typed `api/*.ts` wrappers only.
- Cache grouping is a pure client-side transform over the flat `/cache` list; filters map
  1:1 to existing query params.
- Schematics list vs builder is a **local view-state switch** inside the Schematics tab
  (not a separate router route), with the builder's inputs synced to the URL **query string**
  (`?hw&arch&version&ext&kargs&secureboot`) so a builder state is deep-linkable/shareable
  without changing the tab's route.

## 6. Testing

- Vitest + Testing Library, one `*.test.tsx` sibling per new/changed view and `*.test.ts`
  per api module (matches existing convention). Assert behavior against a mocked `request`:
  filter→query-param mapping, grouping, fan-out bulk calls issue N requests, builder→YAML→ID
  round-trip, Import-by-ID pre-population, disabled-when-403 delete, no-emoji (icons/Tags used).
- `npm test` (Vitest) + `tsc` clean is the gate; no Go changes so `go test` is unaffected.

## 7. Deferred work — to be documented on the issues

Posted as a comment on each issue ("Remaining backend work to complete"):

- **#31:** Host DTO/DB enrichment (roles, config_id, first_seen, last_seen, observed_ip,
  note); bulk approve/delete routes; a host-driven resolved-config endpoint; **Token UX /
  `X-Booty-Token` — blocked on P10 auth** (no server-side token check exists).
- **#32:** `/clusters/{id}/validate` + `/generate` routes (for the talhelper Validate /
  Generate-Steps UI and stale detection); live extension-catalogue endpoint (interim: static list).
- **#33:** surface per-file sha256 + `verify_kind` on `CacheEntryDTO` (backend computes them
  internally in `pkg/cache/verify.go` but doesn't expose them); real bulk cache endpoints
  (interim: client-side fan-out); `DELETE /cache/{id}` remains 403 until P10 auth.

## 8. Non-goals

- #34 (Dashboard, DB admin, Replication) — backends unbuilt.
- Any backend/API/DB/route change.
- Auth / token UX (P10).
- Introducing the Drawer pattern.

## 9. File structure (anticipated)

- **Modify:** `web/src/views/CacheView.tsx` (rewrite to grouped+detail), `BootConfigsView.tsx`
  (Schematics tab → full-page builder; Configs validate/cheat-sheet/upload; Roles inline
  Select), `HostsView.tsx` (Tabs+Badge), `ClustersView.tsx` (Export/Edit), `nav.tsx` (order).
- **Add:** `web/src/api/schematicUrl.ts` (deep-link params) if not folded into `schematicYaml.ts`;
  extension-catalogue constant; matching `*.test.tsx`/`*.test.ts` siblings.
- **Add:** `web/src/api/clusters.ts` export/update wrappers.
