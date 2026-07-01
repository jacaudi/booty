# Design: Interactive iPXE Boot Menu (`boot_mode='menu'`)

**Type:** Design
**Date:** 2026-06-30
**Issue:** [#44](https://github.com/jacaudi/booty/issues/44) — P1c follow-up: interactive boot-menu dispatch
**Status:** Approved; SGE design review folded in 2026-06-30 (verdict `sound-with-fixes`, all findings absorbed) — pending writing-plans
**Roadmap slice:** v1 management plane, post-P1c follow-up (see `docs/plans/2026-06-28-v1-management-plane-design.md` §2.5)

---

## 1. Problem

P1c shipped SQLite-driven boot dispatch with two live states — **holding** (unapproved/unknown
→ wait-and-retry loop) and **assigned** (approved + a pinned OS → boots that OS's newest cached
version). It defined a third state, **menu**, but deferred it: `bootDispatch` currently maps
`boot_mode='menu'` to the holding pattern (`pkg/tftp/tftp.go`), and nothing in the system ever
sets `boot_mode='menu'` (approve always assigns the host's own OS).

This design makes **menu** real: when an approved host is in menu mode, booty serves an iPXE menu
of every currently-cached `(os, version)` choice; the node selects one and boots that exact
version. The selection is **per-boot** — booty writes nothing during the boot path.

## 2. Goals / Non-goals

**Goals**
- An approved host in `boot_mode='menu'` is served an interactive iPXE menu of the cached images.
- The node's selection boots that specific `(os, schematic, arch, version)` tuple.
- A mechanism exists to put a host into menu mode.
- The `assigned` boot path is **byte-identical** to P1c (no regression for migrated hosts).

**Non-goals (YAGNI — explicitly out of scope)**
- A web UI control for menu mode (that is P2; this ships the API endpoint only).
- Persisting a menu choice across reboots / pinning an assignment to a non-newest version.
- A new `AssignedVersion` column or any schema migration.
- Per-host menu filtering, default-OS auto-boot policy, or menu theming.

## 3. Decisions

The five open questions from the design handoff, resolved (the last three via `AskUserQuestion`):

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| Q1 | What sets `boot_mode='menu'`? | **New endpoint** `POST /api/v1/hosts/{mac}/menu` (re-adds `db.SetBootMode`). | Explicit, symmetric with approve/revoke; leaves approve's auto-assign untouched. UI control is later (P2). |
| Q2 | Menu data source | **Disk** — new `cache.ListCached()`. | The boot path already treats disk as bootable truth (`NewestCached`); a disk-sourced menu cannot offer a choice that 404s. The `target_versions.cached` flag is coarse and churns on transient failure (P3 owns proper cache accounting). |
| Q3 | Boot a *specific* chosen version | New `bootTokensFor(os, schematic, arch, version, urlHost)`; `bootTokens` (assigned) delegates to it. | Single-sources token construction; assigned output stays byte-identical (guard test). No persisted version needed — the tuple is carried in-band. |
| Q4 | Selection → boot round-trip | **All-TFTP** — menu items chain `tftp://[server-ip]/menu/<os>/<seg>/<arch>/<version>/boot.ipxe`. | Keeps all boot-script rendering in `pkg/tftp` (no cross-package coupling, no new transport); consistent with P1c's deliberate `tftp://` holding re-chain. |
| Q5 | Persistence | **Ephemeral** — selection writes nothing; menu re-shown every boot. | Boot path stays read-only; no schema change. "Pick once and stick" is already the existing approve/assign flow. |

## 4. Architecture

### 4.1 Boot state machine (pkg/tftp)

`bootDispatch(host)` gains a real menu arm; holding/assigned are unchanged:

```
no host / unapproved            -> "holding", ""
approved + boot_mode=assigned   -> "assigned", assignedOS   (UNCHANGED, byte-identical)
approved + boot_mode=menu       -> "menu", ""               (NEW)
```

`readHandler` grows two TFTP branches:

1. **`booty.ipxe` + kind=menu** → serve a **generated** `menu.ipxe` (one `item` per cached entry).
   Parallel to the existing holding/assigned rendering in the same `if filename == "booty.ipxe"`
   block.
2. **`menu/<os>/<seg>/<arch>/<version>/boot.ipxe`** (NEW synthetic route, matched before
   `safeJoin`, like `pxelinux.cfg/default`) → parse the path, validate it against the cache,
   render that exact OS template via `bootTokensFor`.

### 4.2 Cache enumeration (pkg/cache)

```go
// CacheEntry is one bootable artifact set present on disk. Names are the ON-DISK
// cache names/segments (e.g. "coreos"), matching the boot path.
type CacheEntry struct {
    CacheName string // <os> disk segment: flatcar | coreos | talos
    Segment   string // schematic, or "-"
    Arch      string
    Version   string
}

// ListCached walks cache/<cacheName>/<seg>/<arch>/<version> and returns every
// directory whose version passes the corresponding ostype's ValidateVersion —
// the SAME filter NewestCached uses. Sorted by (CacheName, version desc) for a
// stable menu order.
func ListCached() []CacheEntry
```

This is the multi-version generalization of `NewestCached`'s single-newest disk scan, reusing
`cacheNameToCanonical` + `ostype.Lookup` + `ValidateVersion` + `CompareVersions`. `ListCached` and
`NewestCached` stay **two separate functions** — do not refactor `NewestCached` to
`ListCached`+max-pick, as that would perturb the byte-identical assigned path for no benefit (KISS:
the small shared shape is cheaper duplicated than unified through the regression-sensitive path).

### 4.3 Token rendering (pkg/tftp)

Extract the per-OS token-map construction out of `bootTokens` into a fully-specified primitive:

```go
// bootTokensFor builds the substitution map for one exact (os, schematic, arch,
// version) tuple. The menu-boot branch calls it with the path-supplied tuple.
func bootTokensFor(osToLoad, schematic, arch, version, urlHost string) map[string]string

// bootTokens (assigned path) resolves arch (viper), schematic (host/viper), and
// version (cache.NewestCached) exactly as today, then delegates to bootTokensFor.
// Output for assigned hosts is unchanged.
func bootTokens(osToLoad, urlHost string, host *hardware.Host) map[string]string
```

`osToLoad` here is the on-disk OS name (`flatcar`/`coreos`/`talos`) — the same value the existing
`bootTokens` switch keys on and the same value carried in the menu path's `<os>` segment, so no
canonical-name translation is needed at render time.

> Note: `bootTokensFor` is fully specified by its args for the BASEURL/version/arch/schematic, but
> not for every token — the CoreOS arm still reads `[[coreos-channel]]` from viper
> (`config.CoreOSChannel`), since channel is a stream selector that isn't part of
> `(os,schematic,arch,version)`. This is unchanged from today's `bootTokens` and is correct (the
> explicit version pins the BASEURL); a code comment should note it so the next reader doesn't
> expect channel in the path.

### 4.4 Selection safety

The `menu/…/boot.ipxe` filename is synthetic and handled before `safeJoin`. The branch:
1. Strips the `menu/` prefix and `/boot.ipxe` suffix, splitting into exactly
   `<os>/<seg>/<arch>/<version>` (reject if not exactly 4 segments).
2. **Rejects an unknown `<os>`:** `ostype.Lookup(cacheNameToCanonical(os))` must resolve, exactly as
   `ListCached`/`NewestCached` do. This bounds `<os>` to a real cache name and prevents serving an
   arbitrary existing cache subdir whose top segment isn't an OS.
3. Rejects `..`/empty segments; reconstructs `cacheDir(os, seg, arch, version)` and confirms it
   resolves under `cacheRoot` and the directory exists.
4. Runs the OS's `ValidateVersion(version)`.

Any failure → fall back to the holding render (wait-and-retry), never serve arbitrary disk content.
This re-uses the cache layout helpers as the single source of the on-disk path.

### 4.5 Menu template

Generated in Go (it is dynamic, so not a static `PXEConfig` entry). Shape:

```
#!ipxe
menu Booty — select an image to boot
item retry                            Wait / retry
item flatcar/-/amd64/3815.2.0         Flatcar 3815.2.0 (amd64)
item talos/<schematic>/amd64/1.7.0    Talos 1.7.0 (amd64) [<schematic-prefix>]
...
choose --timeout 300000 --default retry sel || goto retry
chain tftp://[[server-ip]]/menu/${sel}/boot.ipxe || goto retry
:retry
chain tftp://[[server-ip]]/booty.ipxe || shell
```

- The `item` key **is** the cache-relative path `<os>/<seg>/<arch>/<version>`, so `${sel}` maps
  directly to the selection-boot filename — self-describing, no server-side key map, fully stateless.
- An explicit `item retry` entry is always emitted **first** so `--default retry` references a real
  label (and gives the operator a manual "wait" choice). On the `retry` selection, `menu/retry/...`
  is not a valid 4-segment tuple → the selection branch's holding fallback fires (§4.4), so it loops
  exactly like `:retry`.
- `choose --timeout … --default retry` means an unattended/headless node never blocks at the
  prompt: on timeout it re-chains `booty.ipxe` (same safe degradation as holding mode).
- **Empty cache** → only the `retry` item → the menu is just the holding loop with a prompt.

> **iPXE-semantics verification gate (writing-plans must schedule this).** Three iPXE behaviors
> this template relies on are NOT verifiable from the Go source and must be confirmed against the
> netboot lab's actual iPXE build *before or during* slices 4–5: (a) an `item` **label may contain
> slashes** and `${sel}` expands them intact into the chain URL; (b) `choose --default retry` is
> honored; (c) `choose` returns nonzero so `|| goto retry` fires on timeout/cancel. The always-present
> `retry` item removes the zero-item edge case. If slashes-in-labels does NOT hold, fall back to an
> opaque per-entry index key + a server-side index→tuple map rendered into the same menu request
> (still stateless within one render) — a localized change, not a redesign.

### 4.6 Set menu mode (pkg/db + pkg/hardware + pkg/http)

- Re-add `db.SetBootMode(mac, mode string)` (removed as dead code in P1c cleanup) and the
  `hardware.SetBootMode(mac, mode)` wrapper. Because it takes a second arg, the wrapper follows
  **`SetAssignment`'s** shape (its own `withRLockedStore` call) — **not** the single-arg
  `mutateHost` helper that `Approve`/`Revoke` use.
- New endpoint `POST /api/v1/hosts/{mac}/menu`: call bare `hardware.Approve` (sets `approved=1`)
  **first**, then `hardware.SetBootMode(mac, "menu")`. It MUST NOT route through
  `hardware.SetAssignment` — the existing approve *handler* assigns via `SetAssignment`, which sets
  `boot_mode='assigned'` and would clobber menu mode. (A leftover `assigned_os` from a prior
  assignment is harmless: `bootDispatch` keys on `boot_mode`, so `menu` still wins.)
- **OPEN** in the trust window (mutating but non-destructive, exactly like `approve`/`revoke`;
  not a `PUT`/`DELETE`, so not 403). Unknown MAC → 404. Returns the updated host.

### 4.7 Out of scope: the legacy `pxelinux.cfg/default` path

`readHandler`'s `pxelinux.cfg/default` branch serves `host.OS` directly with no boot-state dispatch.
A menu-mode host that boots via legacy syslinux (rather than iPXE) will **not** get a menu — it
falls through to its OS as today. This is acceptable: the interactive menu is inherently an iPXE
feature, and booty's primary path is iPXE/proxyDHCP. The legacy branch is left untouched.

## 5. End-to-end flow

```
proxyDHCP ──next-server──▶ TFTP booty.ipxe
                              │  bootDispatch(host)
                              ▼
        approved + boot_mode=menu ──▶ serve generated menu.ipxe (cache.ListCached)
                              │
        node picks "talos/<schematic>/amd64/1.7.0"
                              ▼
        chain tftp://[server-ip]/menu/talos/<schematic>/amd64/1.7.0/boot.ipxe
                              │  parse + validate vs cache
                              ▼
        render PXEConfig["talos.ipxe"] via bootTokensFor(talos, <schematic>, amd64, 1.7.0)
                              ▼
                         node boots that exact version   (nothing written)
```

Putting a host into menu mode (operator, out-of-band): `POST /api/v1/hosts/{mac}/menu`.

## 6. Implementation slices (for writing-plans)

Each slice is an independent, TDD-sized unit.

| # | Slice | Files | Acceptance |
|---|-------|-------|-----------|
| 1 | Cache enumeration | `pkg/cache/list.go` (+test) | `ListCached` returns only `ValidateVersion`-passing entries, sorted stably; ignores stray dirs |
| 2 | Token extraction | `pkg/tftp/tftp.go` (+test) | `bootTokensFor` exists; `bootTokens` delegates; **new** `TestBootTokensByteIdentical` does a full-map `maps.Equal` of `bootTokens(...)` vs a captured legacy snapshot for **talos, flatcar, AND coreos** (seeded cache dir + viper) — see B1/§7. The existing 2-key `TestAssignedTokensMatchLegacy` is insufficient as the guard. |
| 3 | Set-mode plumbing | `pkg/db/host.go`, `pkg/hardware/mac.go` (+tests) | `SetBootMode` round-trips; `hardware.SetBootMode` follows the `SetAssignment` wrapper shape (own `withRLockedStore`), not `mutateHost` |
| 4 | Menu render + dispatch | `pkg/tftp/tftp.go`, `pkg/tftp/menu.go` (+tests) | `bootDispatch` menu→`"menu"`; `booty.ipxe` serves generated `menu.ipxe` with a leading `retry` item; empty cache → retry-only menu |
| 5 | Selection boot branch | `pkg/tftp/tftp.go` (+tests) | valid `menu/…/boot.ipxe` renders the right tuple; not-4-segments / unknown-`os` / `..` / missing-dir / bad-version → holding fallback |
| 6 | API endpoint + docs | `pkg/http/api_hosts.go` (+test), `docs/schema/API.md` | `POST /hosts/{mac}/menu` calls bare `Approve` then `SetBootMode('menu')` (NOT `SetAssignment`); unknown MAC → 404; documented |

Suggested order: 1 → 2 → 3 → 4 → 5 → 6 (4 depends on 1+2; 5 depends on 2; 6 depends on 3).

**Verification gate (not a code slice):** the iPXE-semantics check from §4.5 (slashes-in-labels,
`--default`, zero/`||`-on-cancel) must be confirmed against the netboot lab's iPXE build during
slices 4–5. If slashes-in-labels fails, apply the §4.5 opaque-index-key fallback (localized to
slices 4–5).

## 7. Testing strategy

- **State machine:** extend `TestBootDispatchStateMachine` — menu host → `("menu","")`.
- **Byte-identical guard (the core regression risk):** the existing `TestAssignedTokensMatchLegacy`
  only compares 2 flatcar keys — too weak to back the invariant. Add `TestBootTokensByteIdentical`:
  a full `maps.Equal` of the entire token map from `bootTokens(os,...)` against a captured legacy
  snapshot for **talos, flatcar, AND coreos** (CoreOS is highest-risk: most tokens, and
  `[[coreos-channel]]` comes from viper, not the tuple), with a seeded cache dir + viper config per
  OS. This must be green after the extraction.
- **Enumeration:** `ListCached` over a temp cache tree — valid versions surfaced, stray/invalid
  dirs skipped, ordering stable.
- **Menu render:** generated `menu.ipxe` contains one item per entry with the correct
  `<os>/<seg>/<arch>/<version>` key; zero entries → retry loop only.
- **Selection parse + safety:** valid tuple renders the OS template; traversal (`..`), unknown OS,
  missing dir, bad version → holding fallback (table test).
- **API:** `POST /hosts/{mac}/menu` on a known host sets `boot_mode='menu'` (and approves);
  unknown MAC → 404.

## 8. Constraints honored

- `bootTokens` byte-identical for `assigned` hosts (P1c structural guarantee).
- Menu arm **extends** `bootDispatch`; holding/assigned untouched.
- New endpoint OPEN (non-destructive) in the trust window; no new `PUT`/`DELETE`.
- No new transport, no cross-package coupling, no `AssignedVersion` field, no schema migration.
- Boot path stays read-only (only the existing `DoInstall` flip writes).
- CGO-free (`modernc.org/sqlite`), `log/slog`, Viper retained, module path `github.com/jeefy/booty`.
- Doc gate: `docs/schema/API.md` updated for the new endpoint.
- PR targets `jacaudi/booty`, not `jeefy`.
