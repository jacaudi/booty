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
- Make the **interactive boot menu DB-aware** and group archived versions into a nested **"Archived OSes"** sub-menu (§4.4) — a deliberate, netboot-lab-validated boot/menu-path change.

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

1. **Write `cache_entries` at the `cached` seam.** At `reconcile.go:99` — the `if vg.Wait() == nil` block that already upserts `cached=true` — additionally stat the version's artifact files and sum their bytes, then `UpsertCacheEntry{target_version_id, size, fetched_at=now, in_window=1}`. The `cached` boolean write itself is unchanged — this **extends**, it does not reshape.
   - **File naming:** sum sizes using the **same URL-base derivation `ensureArtifact` uses** (`path.Base(a.URL)` — the file on disk is named from the URL, not from `a.Filename`; `layout.go:53` deliberately single-sources this). Factor a shared `artifactPath(dir, url)` helper so the size-sum and `ensureArtifact` can't drift for a future OS.
   - **`fetched_at` semantics:** the seam upserts on every tick a version is present, so `fetched_at` means "last confirmed present" (which gives archived rows a sane oldest-first eviction order — they freeze at their last in-window tick).
   - **`ON CONFLICT(target_version_id) DO UPDATE` set is exactly `size, fetched_at, in_window`** — it must **not** clobber `pinned` (a re-cache preserves an operator pin) nor `verified`/`verify_err` (P3b owns those). Spell these columns out in the plan.

2. **Retention = archive, not delete.** The current out-of-window prune (`reconcile.go:108-120`, `DeleteTargetVersion` + `removeVersionDir`) stops hard-deleting. A cached version that is no longer in the `retain_n` desired set gets `SetCacheInWindow(false)` (archived); its disk artifacts stay. `retain_n` still defines the in-window set.

3. **Eviction pass (new), at the end of each reconcile** (in `reconcileAll`, on the coordinator). If `cacheMaxBytes > 0` and `SumCacheBytes() > cacheMaxBytes`, delete **oldest archived-unpinned** versions (`ListArchivedUnpinnedByAge`, oldest `fetched_at` first) via the existing `DeleteTargetVersion` (cascades the `cache_entries` row) + `removeVersionDir`, until under budget or none remain. **Never** evict `in_window` or `pinned` rows; if only those remain over budget, **log a WARN and stop** (booty never deletes an in-window or pinned version). Eviction reuses the existing disk-removal helpers.
   - **No-progress guard:** eviction measures freed bytes from the DB `size` column, so a `size=0` row (e.g. a scan-repaired or pre-size row) would free nothing yet keep the loop deleting. The seam (§4.1) must always write a real summed `size`; additionally, eviction **stops when a deletion frees no measurable bytes** (re-`SumCacheBytes` shows no progress) so it can't over-evict archived rows on bad accounting.

*Known limitation (→ P3b):* `ensureArtifact` still streams directly (no `temp→rename`), so a crash mid-download can leave a truncated file that reads as "present"; P3a's `size` reflects whatever bytes are on disk. Truncation detection needs a checksum and is P3b's concern; `scan` (§6) recomputes sizes but cannot detect truncation in P3a.

### 4.4 Boot-path interaction (assigned vs menu)

**Assigned boot is unaffected** (verified): `NewestCached` selects the max version by `CompareVersions`, and an archived version is by definition strictly older than the retained (`in_window`) set, so newest-wins never picks an archived version. Manual (`source='manual'`) versions are always in the desired set → `in_window=1`, never archived.

**Interactive-menu boot changes, by design — with archived versions grouped into a nested sub-menu.** The menu is built from `cache.ListCached()` (`pkg/cache/list.go:30` → `pkg/tftp/menu.go` `renderMenu`), which walks **all** valid version dirs on disk. Under archive-not-delete, archived versions stay on disk, so they remain **bootable via menu mode — this is the rollback mechanism** (menu-boot a node back to an archived version). Rather than mix them into the main list, P3a makes the menu **DB-aware** and groups them:

- **The menu partitions cached versions by `cache_entries.in_window`.** A disk-present version whose `cache_entries` row has `in_window=0` → the **Archived** group; everything else (in_window=1, or no row yet — graceful default) → the **main** group. Disk stays the source of "what is bootable"; the DB adds the "archived" label.
- **Main menu:** the in-window versions, then a single **`Archived OSes ▸`** item (rendered **only when at least one archived version exists**), then the existing `retry`/hold item. Selecting `Archived OSes ▸` chains to a second iPXE menu.
- **Archived sub-menu:** the archived versions (same slash-label → `${sel}` → `.../boot.ipxe` selection mechanism as #44, so an archived pick boots that exact version), plus a **`‹ Back`** item that returns to the main menu.
- **Implementation:** extend `renderMenu` (`pkg/tftp/menu.go`) to emit two `menu`/`choose` blocks in **one served script** with `goto` between them (no new TFTP endpoint, no extra round-trip). Four control-flow details the plan must pin (SGE re-check, verified against the real #44 code) — the exact iPXE is proven **live in the netboot lab**:
  - **Conditional dispatch (not #44's uniform chain).** #44 dispatches *branchlessly*: `chain …/menu/${sel}/boot.ipxe` for every item (`menu.go:115-119`). Archived *version* items can reuse that. But `Archived OSes ▸` and `‹ Back` are **not** boot tuples — left to the uniform chain they'd hit the invalid-tuple→holding→reload path and never open the sub-menu. Each block's post-`choose` needs a guarded dispatch, e.g. `iseq ${sel} <sentinel> && goto <label> || chain …/menu/${sel}/boot.ipxe || goto retry`.
  - **Per-block `--default`/`--timeout`.** iPXE `--default <tag>` references a **menu-item tag scoped to that menu**, not a `goto` label. The archived block must emit its own default target (e.g. the `‹ Back` item) and its own `--timeout`; a dangling `--default retry` in a block with no `retry` item is a bug. Pin both blocks' timeout + default.
  - **Store wiring (riskiest change).** `renderMenu` is called from a store-less `readHandler` (`tftp.go:135`); the `tftp` package has no `*db.Store`. Reuse the existing injected-store discipline — `hardware`'s `SetStore` + `withRLockedStore` RWMutex pattern (`mac.go:64-91`) — via a `tftp.SetStore` wired in `cmd/main.go` (or route the `in_window` query through the hardware store). **Do not open a second DB handle** (breaks the single-connection discipline).
  - **Disk↔DB key mapping.** `cache_entries` keys by `target_version_id`; the disk `CacheEntry` is `(cacheName, segment "-", arch, version)` (`list.go:17-22`). Partitioning maps disk tuple → row via `cacheNameToCanonical` + params/segment normalization. A mapping **miss** falls to the main group (safe direction — never hides a bootable, never mis-archives an in-window) but silently mislabels an archived version, so it **must be unit-tested against real Talos schematic values** (the `segment "-"` vs actual schematic-hash case).
  - The `Archived OSes ▸` item's visibility (`only when archived exist`) and the archived block's contents both derive from the **same partition of one `ListCached()` call** — not a separate DB count — so a dir evicted mid-render can't produce an item that opens an empty sub-menu. Sentinels (`archived`/`back`) are single tokens, provably non-colliding with the bounded 4-segment real keys.

**Scope, sequencing & no-regression:** this boot/menu-path change is **coupled to archive-not-delete and not deferrable** — once versions archive, they immediately appear in the (disk-only) menu, so the grouping must land in the same slice to avoid a polluted-menu regression window. It is P3a's **riskiest task: sequence it last**, after the store-wiring it depends on, **gated on a live netboot-lab smoke test** (boot an in-window *and* an archived version via the sub-menu; exercise `‹ Back`; empty-archived hides the item; all-archived leaves only `Archived OSes ▸`). The in-window `item` *lines* are unchanged, but the main block's dispatch line gains the `iseq` guard, so the lab must re-confirm in-window boot (not "byte-for-byte untouched"). Assigned-boot is untouched. Archived-group growth stays bounded by `cacheMaxBytes`/pins.

## 5. Config

New key `cacheMaxBytes` — an **`int64`** (read via `viper.GetInt64`), in **bytes**, default **`0` = unlimited** (eviction is opt-in; when `0` the eviction pass is a no-op and archived versions accumulate). Add the `CacheMaxBytes = "cacheMaxBytes"` const alongside `CacheInterval`/`CacheConcurrency` with `viper.SetDefault`, following the existing `Cache*` naming.

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
Executes **synchronously via `deps.Store`** from the handler (like the existing targets/hosts mutations — write serialization is `SetMaxOpenConns(1)`, §11, not the coordinator goroutine). For each `target_version` with `cached=1`: stat its version dir and **recompute `size`**; **repair** a missing `cache_entries` row (upsert `size` + `fetched_at`, defaulting `in_window=1`). **Scan does not compute `in_window`** — window membership derives from live discovery (`retentionFor`), which is the reconciler's job, not something scan can know from disk+DB. So scan leaves an existing row's `in_window` untouched and defaults a repaired row to `1`; the next reconcile self-heals it (the prune loop sets rotated-out→archived, the seam sets desired→in-window). Version dirs on disk with **no** matching `target_version` are **reported as orphans, not auto-adopted** (adopting would require inventing a target). Returns `{scanned, updated, orphans}`. This is the operator's "resync the inventory to disk truth" button.

## 7. Cache view — `web/src/views/CacheView.tsx` (+ `nav.tsx` entry)

Reuses the P2 No-Wall seam: one new view file + one `navEntries` entry → nav becomes **Home · Hosts · Cache · About**. An antd `Table`:

- **Columns:** OS · Version · Arch · Size (human-readable) · State (`in-cycle`/`archived` [+`-pinned`], as a tag) · Pinned · Fetched.
- **Row actions:** **Pin/Unpin** (toggle → `POST …/pin|unpin`); **Delete** (rendered **disabled** with a tooltip "available after authentication (P10)" — surfaces the 403 path without calling it).
- **Toolbar:** a **Scan** button (`POST /cache/scan`) that shows the returned `{scanned, updated, orphans}` summary via `message`.
- One `GET /cache` load, partitioned/sorted client-side; **re-fetch** after each mutation (matching the Hosts view pattern). No "verified" column in P3a.
- Extends the `web/src/api/` client seam with `listCache/pinCache/unpinCache/scanCache` and a `CacheEntry` type.

## 8. Testing

- **Go (TDD):** `cache_entries` store methods (upsert/list-joined/pin/in-window/sum/oldest-archived); the reconciler archive-vs-delete transition (a rotated-out version becomes `in_window=0`, stays on disk); the eviction pass (oldest-archived-unpinned first; pins + in-window protected; budget honored; `0`=no-op; no-progress guard); `scan` (size recompute, row repair, orphan reporting, `in_window` left untouched); `api_cache` handlers including the 403 delete and filter parsing.
- **Menu rendering (`pkg/tftp/menu.go`, unit):** `renderMenu` partitions in-window vs archived; the disk↔DB tuple→`in_window` mapping is tested **against a real Talos schematic-hash value** (the `segment "-"` vs schematic case — where a mapping miss would silently leak an archived version into the main group); `Archived OSes ▸` appears only when archived exist (derived from the same partitioned `ListCached` set) and is absent otherwise; **all-archived** (main block has only `Archived OSes ▸` + retry) and **empty-archived** edges; the archived block emits its own `--default`/`--timeout` and a `‹ Back` item; the guarded `iseq … && goto … || chain …` dispatch is present in both blocks.
- **Live netboot-lab smoke test** (the un-unit-testable iPXE semantics, as #44 required) — lab checklist: boot an **in-window** version from the main menu (re-confirm #44 selection/retry survives the added `iseq` guard); boot an **archived** version through the sub-menu; exercise `‹ Back` (→ main menu); the empty-archived case hides `Archived OSes ▸`; and **confirm a second unnamed `menu` command in one script resets the item list** (does not append to the first block) — an iPXE semantic to prove live, not from docs.
- **React (Vitest+RTL):** the Cache view renders rows/state tags from mocked data; pin/unpin invoke the right client fn + re-fetch; scan shows the summary; delete is disabled.

## 9. Principles

- **No-Wall:** `verified`/`verify_err` columns, the `re-verify` endpoint, and the Cache-view "verified" column are the **P3b seam** — added additively later, P3a siblings untouched. The Cache view is one nav-registry entry (the P2 seam paying off exactly as designed).
- **DRY:** eviction/prune reuse the existing `removeVersionDir`/`DeleteTargetVersion` helpers; disk↔URL layout stays single-sourced through `cacheSegments`; `arch` is joined from `targets`, not duplicated.
- **KISS/YAGNI:** one row per version (not per artifact); a single global budget; `scan` reports orphans rather than building adoption machinery; eviction on the reconcile tick rather than a new scheduler.
- **Extend, don't reshape:** the `cached` boolean write and the reconciler's coordinator model are preserved; `cache_entries` is written adjacent to `cached`, on the same goroutine.

## 10. Documentation gate (self-documenting repo)

- `docs/schema/DATABASE.md` — the `cache_entries` table + state model.
- `docs/schema/API.md` — the `/api/v1/cache` endpoints + trust-window note.
- `docs/schema/STORAGE.md` — archive-vs-delete retention, size-based eviction, `cacheMaxBytes`, and the **menu-shows-archived rollback behavior** (§4.4) with the recommendation to set `cacheMaxBytes` when relying on the interactive menu.
- README/`docs/` — a Cache-view note.

## 11. Constraints

- Module path `github.com/jeefy/booty` (unchanged). **PR to `jacaudi/booty`.**
- Go 1.26, CGO-free (`modernc.org/sqlite`); `log/slog`; Huma v2 for `/api/v1`.
- Mutating API stays **open** in the trust window; `DELETE /cache/{id}` is **403 until P10**.
- **Write serialization is `SetMaxOpenConns(1)`** (`db.go`), not the coordinator goroutine — the coordinator exists to avoid a viper race, and the existing targets/hosts API already writes via `deps.Store` directly from the HTTP goroutine. So: the **reconcile-time writes** (the `cache_entries` seam and the eviction pass) run on the coordinator because they live in `reconcileAll`; the **API mutations** (pin/unpin/scan) write via `deps.Store` from the handler, exactly like `api_targets.go`. Both are safe under the single open connection.

## 12. Acceptance criteria

1. Migration `0002_cache_entries.sql` creates `cache_entries`; `go build`/CI green.
2. The reconciler writes a `cache_entries` row (size, `in_window=1`) when it caches a version, on the coordinator goroutine, without changing the `cached` boolean write.
3. A version rotated out of `retain_n` becomes `in_window=0` (**archived**) and stays on disk — no hard delete.
4. Eviction (when `cacheMaxBytes>0` and over budget) removes oldest archived-unpinned first; **never** touches in-window or pinned; `0` = no-op.
5. `/api/v1/cache` list (filtered) / pin / unpin / scan work; `DELETE` returns 403.
6. The Cache view shows inventory + derived state with working pin/unpin/scan and a disabled delete; nav is Home · Hosts · Cache · About.
7. The interactive boot menu groups archived versions under a nested **`Archived OSes ▸`** sub-menu (shown only when archived versions exist), with `‹ Back`; the netboot-lab smoke test boots an in-window and an archived version and passes.
8. `verified`/`verify_err` remain NULL throughout P3a.
9. Docs updated per §10; Go `-race` + frontend suites green.

---

## Appendix — reference (current backend, verified)

- No `cache_entries` and **no signature/checksum code** exist today — both greenfield (only forward-ref comment at `pkg/cache/reconcile.go:97`).
- `cached=1` write: `pkg/cache/reconcile.go:100`; out-of-window prune: `reconcile.go:108-120`; artifact download: `ensureArtifact` (`layout.go:48`) → `config.DownloadFile` (direct stream, **no** temp/atomic-rename).
- Layout helpers: `cacheDir`/`cacheSegments`/`CacheURLBase`/`removeVersionDir` (`pkg/cache/layout.go`); `NewestCached` reads disk.
- `Artifact` descriptor is `{Filename, URL}` only (frozen P1a `OS` interface) — no checksum/sidecar; P3b must widen it or derive sidecars out-of-band.
- Migrations: `pkg/db/migrations/0001_init.sql` only; P3a = `0002_*.sql`.
- API wiring: `registerOperations` in `pkg/http/api.go:33-38` (`registerCatalog/Targets/Hosts`) — P3a adds `registerCache`. `APIDeps{Store, Trigger}`.
- Config: `cacheInterval` (5m), `cacheConcurrency` (4), `dataDir`; **no** `cacheMaxBytes` yet.
