# P3a — Cache inventory + state + eviction + Cache view — Design

**Date:** 2026-07-01
**Type:** Design
**Feature:** p3a-cache-inventory-eviction-view
**Slice:** P3a of the v1 management-plane roadmap (`docs/plans/2026-06-28-v1-management-plane-design.md` §2.3, §2.9, §3, §5 P3)
**Issues:** #26, #33 (P3 split — see §1)
**Status:** Approved (design phase) — pending written-spec review + SGE design review, then `superpowers:writing-plans`

---

## 1. Context & the P3 split

The v1 roadmap's **P3** bundled three things: a `cache_entries` inventory table + state/eviction, per-OS **signature verification** (SHA256/GPG), and an Ant Design **Cache view**. Signature verification is a sizable, fairly independent subsystem (new crypto, an embedded GPG keyring, sidecar-URL derivation, a `temp→verify→rename` download-flow change, and a strict-mode boot-path fallback). Mirroring the P1a/b/c split — done for the same reviewability + risk-isolation reasons — **P3 is split**:

- **P3a (this design):** the `cache_entries` table, the reconciler writing it, **archive-not-delete retention**, **size-based eviction**, the `/api/v1/cache` API, and the **Cache view**.
- **P3b (separate later design):** signature verification, the atomic `temp→verify→rename` download flow, the `re-verify` endpoint, the Cache-view "verified" column, and strict-mode boot enforcement.

**Load-bearing foundation is merged:** P1a (`targets`/`target_versions`/`hosts` in SQLite, migrations via `//go:embed migrations/*.sql`), P1b (the `pkg/cache` reconciler that sets `target_versions.cached`), P1c (boot dispatch), and P2 (the embedded React+antd UI with a data-driven nav registry). P3a extends this; it builds `cache_entries` and signatures **from zero** (neither exists today).

## 2. Goals / non-goals

**Goals**
- A `cache_entries` table: authoritative per-version **detail** (size, state, fetched-at) that **extends** the coarse `target_versions.cached` boolean without reshaping the reconciler's `cached` write.
- Change retention from **hard-delete-beyond-`retain_n`** to **mark-archived**; reclaim disk with **size-based eviction** of oldest archived-unpinned versions over a byte budget; **pins** and in-window versions are never evicted.
- `/api/v1/cache`: list (filtered), pin/unpin, scan, and a wired-but-403 delete.
- An antd **Cache view**, slotted into the P2 nav registry as one entry.

**Non-goals**
- **→ P3b:** all signature verification (SHA256 for FCOS, GPG for Flatcar), sidecar fetching, the atomic `temp→verify→rename` download flow, the `re-verify` endpoint, the Cache-view "verified" column, and strict-mode boot fallback. `cache_entries` ships with **`verified`/`verify_err` columns present but always NULL** in P3a.
- **YAGNI:** no per-artifact `cache_entries` rows (one row per cached version); no per-target byte budgets (a single global budget); no auto-adoption of orphan disk dirs.

## 3. Data model — migration `0002_cache_entries.sql`

Migrations are numbered-file drop-ins (`//go:embed migrations/*.sql`; applied when the sorted-filename ordinal exceeds `PRAGMA user_version`, each in its own transaction). P3a adds exactly one file — no migration-runner code change. `foreign_keys` is ON in the DSN, so cascades are enforced.

```sql
CREATE TABLE cache_entries (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    target_version_id INTEGER NOT NULL REFERENCES target_versions(id) ON DELETE CASCADE,
    size              INTEGER NOT NULL DEFAULT 0,          -- sum of the version's artifact bytes on disk
    fetched_at        TEXT    NOT NULL DEFAULT (datetime('now')),
    in_window         INTEGER NOT NULL DEFAULT 1,          -- 1 = within target.retain_n; 0 = archived
    pinned            INTEGER NOT NULL DEFAULT 0,
    verified          INTEGER,                             -- NULL in P3a; P3b populates
    verify_err        TEXT,                                -- NULL in P3a; P3b populates
    UNIQUE (target_version_id)
);
```

- **One row per cached `target_version`.** A `target_version` belongs to one `target`, which fixes a single `arch` and `params` — so a version is single-arch and `cache_entries` is 1:1 with `target_version` (no `arch` column needed; `os/arch/params/version` come from a join to `target_versions`→`targets`, keeping `arch`'s authoritative home in `targets` — DRY).
- **`ON DELETE CASCADE`:** pruning/evicting a `target_version` (the existing `DeleteTargetVersion`) auto-removes its `cache_entries` row.
- **State is derived**, not stored: `(in_window, pinned)` → `in-cycle` (1,0) · `in-cycle-pinned` (1,1) · `archived` (0,0) · `archived-pinned` (0,1).

New `pkg/db` store methods (sibling of `pkg/db/versions.go`): `UpsertCacheEntry`, `SetCachePinned(targetVersionID, bool)`, `SetCacheInWindow(targetVersionID, bool)`, `ListCacheEntries(filter)` (joined DTO rows), `SumCacheBytes()`, `ListArchivedUnpinnedByAge()`.

## 4. Reconciler changes (`pkg/cache`)

Three changes, all on the single coordinator goroutine (preserving the existing single-writer invariant):

1. **Write `cache_entries` at the `cached` seam.** At `reconcile.go:99` — the `if vg.Wait() == nil` block that already upserts `cached=true` — additionally stat the version's artifact files (`o.Artifacts(...)` → `filepath.Join(dir, a.Filename)`), sum their bytes, and `UpsertCacheEntry{target_version_id, size, fetched_at=now, in_window=1}` (preserving an existing `pinned`; leaving `verified`/`verify_err` NULL). The `cached` boolean write itself is unchanged — this **extends**, it does not reshape.

2. **Retention = archive, not delete.** The current out-of-window prune (`reconcile.go:108-120`, `DeleteTargetVersion` + `removeVersionDir`) stops hard-deleting. A cached version that is no longer in the `retain_n` desired set gets `SetCacheInWindow(false)` (archived); its disk artifacts stay. `retain_n` still defines the in-window set.

3. **Eviction pass (new), at the end of each reconcile.** If `cacheMaxBytes > 0` and `SumCacheBytes() > cacheMaxBytes`, delete **oldest archived-unpinned** versions (`ListArchivedUnpinnedByAge`, oldest `fetched_at` first) via the existing `DeleteTargetVersion` (cascades the `cache_entries` row) + `removeVersionDir`, until under budget or none remain. **Never** evict `in_window` or `pinned` rows; if only those remain over budget, **log a WARN and stop** (booty never deletes an in-window or pinned version). Eviction reuses the existing disk-removal helpers.

*Known limitation (→ P3b):* `ensureArtifact` still streams directly (no `temp→rename`), so a crash mid-download can leave a truncated file that reads as "present"; P3a's `size` reflects whatever bytes are on disk. Truncation detection needs a checksum and is P3b's concern; `scan` (§6) recomputes sizes but cannot detect truncation in P3a.

## 5. Config

New key `cacheMaxBytes` (default **`0` = unlimited** — eviction is opt-in; when `0` the eviction pass is a no-op and archived versions accumulate). Follows the existing `Cache*` naming (`cacheInterval`, `cacheConcurrency`) with `viper.SetDefault`.

## 6. API — `pkg/http/api_cache.go` (+ one `registerCache(grp, deps)` line)

Mirrors `api_targets.go` (Huma v2 group, hand-mapped DTO, `path:`/`query:` tags, `strconv.ParseBool` for bool query params). DTO:

```
CacheEntryDTO { id, os, arch, params, version, size, state, pinned, in_window, fetched_at }
```
(`state` is the derived label; no `verified` field in P3a.)

- **`GET /api/v1/cache`** — filters `?os=&arch=&state=&pinned=`. **Open.**
- **`POST /api/v1/cache/{id}/pin`** and **`/unpin`** — idempotent; set `pinned`. **Open.** Return the updated entry.
- **`POST /api/v1/cache/scan`** — **Open** (see §6.1).
- **`DELETE /api/v1/cache/{id}`** — **wired-but-403** (`huma.Error403Forbidden("destructive endpoints are disabled until authentication lands (P10)")`), per the trust-window convention (it removes artifacts from disk).

`{id}` is the `cache_entries.id`. Non-destructive mutations don't call `deps.Trigger()` (pin/unpin/scan don't fetch); unpin does **not** trigger immediate eviction — the next reconcile pass reclaims (keeps eviction on the coordinator, single-writer).

### 6.1 `POST /cache/scan` — disk ↔ DB reconciliation
Runs on the coordinator. For each `target_version` with `cached=1`: stat its version dir, **recompute `size`**, and **repair** a missing `cache_entries` row (upsert with `in_window` reflecting current `retain_n` membership). Version dirs on disk with **no** matching `target_version` are **reported as orphans, not auto-adopted** (adopting would require inventing a target). Returns `{scanned, updated, orphans}`. This is the operator's "resync the inventory to disk truth" button.

## 7. Cache view — `web/src/views/CacheView.tsx` (+ `nav.tsx` entry)

Reuses the P2 No-Wall seam: one new view file + one `navEntries` entry → nav becomes **Home · Hosts · Cache · About**. An antd `Table`:

- **Columns:** OS · Version · Arch · Size (human-readable) · State (`in-cycle`/`archived` [+`-pinned`], as a tag) · Pinned · Fetched.
- **Row actions:** **Pin/Unpin** (toggle → `POST …/pin|unpin`); **Delete** (rendered **disabled** with a tooltip "available after authentication (P10)" — surfaces the 403 path without calling it).
- **Toolbar:** a **Scan** button (`POST /cache/scan`) that shows the returned `{scanned, updated, orphans}` summary via `message`.
- One `GET /cache` load, partitioned/sorted client-side; **re-fetch** after each mutation (matching the Hosts view pattern). No "verified" column in P3a.
- Extends the `web/src/api/` client seam with `listCache/pinCache/unpinCache/scanCache` and a `CacheEntry` type.

## 8. Testing

- **Go (TDD):** `cache_entries` store methods (upsert/list-joined/pin/in-window/sum/oldest-archived); the reconciler archive-vs-delete transition (a rotated-out version becomes `in_window=0`, stays on disk); the eviction pass (oldest-archived-unpinned first; pins + in-window protected; budget honored; `0`=no-op); `scan` (size recompute, row repair, orphan reporting); `api_cache` handlers including the 403 delete and filter parsing.
- **React (Vitest+RTL):** the Cache view renders rows/state tags from mocked data; pin/unpin invoke the right client fn + re-fetch; scan shows the summary; delete is disabled.

## 9. Principles

- **No-Wall:** `verified`/`verify_err` columns, the `re-verify` endpoint, and the Cache-view "verified" column are the **P3b seam** — added additively later, P3a siblings untouched. The Cache view is one nav-registry entry (the P2 seam paying off exactly as designed).
- **DRY:** eviction/prune reuse the existing `removeVersionDir`/`DeleteTargetVersion` helpers; disk↔URL layout stays single-sourced through `cacheSegments`; `arch` is joined from `targets`, not duplicated.
- **KISS/YAGNI:** one row per version (not per artifact); a single global budget; `scan` reports orphans rather than building adoption machinery; eviction on the reconcile tick rather than a new scheduler.
- **Extend, don't reshape:** the `cached` boolean write and the reconciler's coordinator model are preserved; `cache_entries` is written adjacent to `cached`, on the same goroutine.

## 10. Documentation gate (self-documenting repo)

- `docs/schema/DATABASE.md` — the `cache_entries` table + state model.
- `docs/schema/API.md` — the `/api/v1/cache` endpoints + trust-window note.
- `docs/schema/STORAGE.md` — archive-vs-delete retention, size-based eviction, `cacheMaxBytes`.
- README/`docs/` — a Cache-view note.

## 11. Constraints

- Module path `github.com/jeefy/booty` (unchanged). **PR to `jacaudi/booty`.**
- Go 1.26, CGO-free (`modernc.org/sqlite`); `log/slog`; Huma v2 for `/api/v1`.
- Mutating API stays **open** in the trust window; `DELETE /cache/{id}` is **403 until P10**.
- The reconciler's single-writer (coordinator goroutine) invariant is preserved — all `cache_entries` writes and eviction happen there.

## 12. Acceptance criteria

1. Migration `0002_cache_entries.sql` creates `cache_entries`; `go build`/CI green.
2. The reconciler writes a `cache_entries` row (size, `in_window=1`) when it caches a version, on the coordinator goroutine, without changing the `cached` boolean write.
3. A version rotated out of `retain_n` becomes `in_window=0` (**archived**) and stays on disk — no hard delete.
4. Eviction (when `cacheMaxBytes>0` and over budget) removes oldest archived-unpinned first; **never** touches in-window or pinned; `0` = no-op.
5. `/api/v1/cache` list (filtered) / pin / unpin / scan work; `DELETE` returns 403.
6. The Cache view shows inventory + derived state with working pin/unpin/scan and a disabled delete; nav is Home · Hosts · Cache · About.
7. `verified`/`verify_err` remain NULL throughout P3a.
8. Docs updated per §10; Go `-race` + frontend suites green.

---

## Appendix — reference (current backend, verified)

- No `cache_entries` and **no signature/checksum code** exist today — both greenfield (only forward-ref comment at `pkg/cache/reconcile.go:97`).
- `cached=1` write: `pkg/cache/reconcile.go:100`; out-of-window prune: `reconcile.go:108-120`; artifact download: `ensureArtifact` (`layout.go:48`) → `config.DownloadFile` (direct stream, **no** temp/atomic-rename).
- Layout helpers: `cacheDir`/`cacheSegments`/`CacheURLBase`/`removeVersionDir` (`pkg/cache/layout.go`); `NewestCached` reads disk.
- `Artifact` descriptor is `{Filename, URL}` only (frozen P1a `OS` interface) — no checksum/sidecar; P3b must widen it or derive sidecars out-of-band.
- Migrations: `pkg/db/migrations/0001_init.sql` only; P3a = `0002_*.sql`.
- API wiring: `registerOperations` in `pkg/http/api.go:33-38` (`registerCatalog/Targets/Hosts`) — P3a adds `registerCache`. `APIDeps{Store, Trigger}`.
- Config: `cacheInterval` (5m), `cacheConcurrency` (4), `dataDir`; **no** `cacheMaxBytes` yet.
