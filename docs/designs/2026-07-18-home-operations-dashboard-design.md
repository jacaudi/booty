# Home Operations Dashboard (P7)

**Type:** Design
**Date:** 2026-07-18
**Feature:** home-operations-dashboard

**Goal:** Replace the placeholder `HomeView` with the P7 operations dashboard — an at-a-glance landing page showing the state an operator cares about (hosts needing attention, clusters, cached OS images, cache health) and the quick actions to act on it.

---

## 1. Context & motivation

`web/src/views/HomeView.tsx` is a 15-line placeholder: a title, one sentence, and a link to Hosts, with the comment *"The full operations dashboard is a later slice (P7)."* The landing page is therefore effectively blank. This design builds that P7 dashboard.

Booty already exposes everything a first-class dashboard needs via existing list endpoints — so the core is **frontend-only aggregation**, consistent with the prior UI slices (P2 hosts view, P3 cache view, the OS-Images/Boot-Configs restructure), which were all frontend-only.

## 2. Goals / Non-goals

**Goals**
- A useful landing page: summary tiles, the operator's pending work, cache/cluster health, and quick actions.
- **Frontend-only for the core** — aggregate existing endpoints client-side; no new backend for v1.
- Honor UI conventions: **Ant Design** with design **tokens (not hex)**, **no emoji** (AntD icons / `Tag` / `Statistic`), no dark-mode-specific work (token-based, dark-ready), no `Drawer`.
- First-class **empty states** for a fresh install (guide to the first action, not a wall of zeros).

**Non-goals**
- No new operational data pipeline / metrics store — this reflects current state from existing reads, not time-series.
- No auto-refresh/websockets in v1 (a manual refresh + on-mount fetch; polling is a later nicety).
- No changes to the underlying Hosts/Clusters/Cache/Configs views — the dashboard links into them.

## 3. Data sources (existing endpoints)

| Dashboard element | Source | Backend work? |
|---|---|---|
| Hosts count + **pending approvals** (`approved=false`) + booted count + by-OS | `GET /api/v1/hosts` (`Host.Approved`, `Booted`, `OS`, `ClusterID`/`MachineType`) | none (client-side filter/count) |
| Clusters count + per-cluster CP/worker member counts + endpoint | `GET /api/v1/clusters` (+ `GET /clusters/{id}` for members) | none |
| OS Images: cached versions + **total cache bytes** (vs `cacheMaxBytes` if set) + **failed** entries | `GET /api/v1/cache` (entries carry size + state incl. the existing "Failed" filter) | none |
| Targets: managed targets, enabled/disabled, **not-yet-cached (pending)** | `GET /api/v1/targets` | none |
| Boot Configs count | `GET /api/v1/configs` | none |
| **System status** (signature policy, `--secretsKey` set → cluster ops enabled, build/version) | *not currently exposed* — server flags | **small read-only `GET /api/v1/status`** (see §5) |

## 4. Design

### 4.1 Layout (top → bottom)
1. **Stat tile row** — AntD `Statistic` cards: **Hosts** (total; a highlighted "*N pending approval*" when >0), **Clusters** (count; sub-text CP/worker totals), **OS Images** (cached versions; sub-text total bytes, and "of `cacheMaxBytes`" when a budget is set), **Boot Configs** (count). Each tile links to its view.
2. **Needs attention** (actionable, only rendered when non-empty):
   - **Pending host approvals** — compact list of `approved=false` hosts (MAC/hostname/OS) with an **Approve** affordance inline (reuses the Hosts approve mutation), plus a link to Hosts.
   - **Cache problems** — any `Failed` cache entries and any enabled target with no cached version (surfaces stuck/failed downloads at a glance — the exact class that hid a 54-minute silent DVD re-download loop during testing), linking to the Cache view filtered to Failed.
3. **Clusters overview** — small cards/table: each cluster's name, endpoint, and CP/worker member counts. Links to Clusters. (Directly complements the multi-control-plane import work.)
4. **System status** strip — signature policy (`strict`/`warn`/`off` as a `Tag`), cluster ops enabled/`fail-closed` (secretsKey), build/version. (Depends on §5.)
5. **Quick actions** — buttons: *Approve pending hosts*, *Add OS target*, *Import / create cluster* (route to the relevant view/modal).

### 4.2 Components & structure
- `HomeView.tsx` becomes a thin composition of small, independently-testable pieces, each owning one fetch + render: `StatTiles`, `PendingApprovals`, `CacheHealth`, `ClustersOverview`, `SystemStatus`, `QuickActions`. Each handles its own loading/empty/error state so one slow/failed endpoint doesn't blank the whole page.
- New `web/src/api/status.ts` (if §5 is included) alongside the existing api modules; everything else reuses existing api clients (`client.ts` hosts, `clusters.ts`, `cacheModel.ts`, `configs.ts`).
- Counts/filters computed client-side from the list responses (small datasets — a homelab-scale management plane).

### 4.3 Phasing (so "full dashboard" is buildable incrementally)
- **v1 (core, frontend-only):** Stat tiles, Pending approvals (actionable), Cache problems, Quick actions, empty states. Zero backend.
- **v1.1:** Clusters overview; System status strip (+ the `GET /status` endpoint from §5).
- **Later (out of this plan):** auto-refresh/polling, recent-boot-activity timeline, per-OS breakdown charts.

## 5. The one backend addition (system status, v1.1)
Signature policy, whether `--secretsKey` is configured, and build/version are server-side flags with no current API surface. Add a small read-only `GET /api/v1/status` returning `{ signaturePolicy, clusterOpsEnabled, version }` (no secrets, no host data). It's the only backend change; the v1 core does not depend on it, so it can land in v1.1 or be dropped without affecting the rest.

## 6. Error & empty states
- **Per-panel isolation:** each panel fetches independently; a failed fetch shows an inline error in that panel only, never a blank page.
- **Fresh install (all zero):** the dashboard shows a short getting-started card ("Approve your first host" / "Add an OS target") instead of empty tiles — the current placeholder's intent, kept but made actionable.
- **No pending work:** the "Needs attention" section is omitted entirely (not shown empty).

## 7. Testing
- **vitest** per panel with mocked api modules: stat counts derived correctly (incl. pending-approval highlight), pending-approvals list + approve action fires the mutation, cache-problems surfaces Failed entries + uncached enabled targets, clusters overview member counts, empty/fresh-install state, and per-panel error isolation (one endpoint 500s → only that panel shows an error).
- If §5 lands: a handler test for `GET /status` asserting it returns policy/version and **no** secret material.

## 8. Decisions
- **Frontend-only core** — aggregate existing endpoints client-side; the only backend touch is the optional `GET /status` (v1.1). Keeps v1 low-risk and consistent with prior UI slices.
- **Panel isolation** — each dashboard section is a self-contained fetch+render unit (testable, failure-isolated), not one monolithic fetch.
- **Actionable over decorative** — prioritize pending approvals + cache problems (things to *do*) over vanity charts.
- **No auto-refresh in v1** — on-mount fetch + manual refresh; polling deferred.
- Conventions: AntD tokens (no hex), no emoji, no Drawer, dark-ready via tokens.

## 9. Risks / open items
- **"Actively downloading right now"** is not a discrete API field (it's a transient `.download` on disk). v1 surfaces the actionable proxies instead — `Failed` cache entries and enabled-but-uncached targets. A true live "downloading" indicator would need a reconcile-status field and is deferred (noted so it isn't mistaken for covered).
- Client-side aggregation assumes homelab-scale list sizes (fine today); if host/target counts ever grow large, dedicated count/summary endpoints become worthwhile — not now (YAGNI).
- `GET /status` must be careful to expose only non-sensitive config (policy/version/booleans), never key paths or secrets.
