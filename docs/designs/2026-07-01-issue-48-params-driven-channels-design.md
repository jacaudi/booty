# Issue #48 — Params-driven Flatcar/CoreOS channels — Design

**Date:** 2026-07-01 · **Slice:** standalone (pre-P3b) · **Issue:** [#48](https://github.com/jacaudi/booty/issues/48) · **PR target:** `jacaudi/booty:main`, ships **before** P3b (it rewires the same `pkg/ostype/ignition.go` functions P3b touches).

> **Session note:** this design was drafted with the user away from keyboard after the
> doc-split and FCOS-404-placement questions were answered. Decisions marked
> **[provisional]** below follow the recommended option and are explicitly up for
> reversal at the design-review gate.

## 1. Context & problem

Flatcar and Fedora CoreOS can only track a **single channel process-wide**: their
drivers read `viper.GetString(config.FlatcarChannel)` / `config.CoreOSChannel`
(global flags) instead of per-target params, unlike Debian (`RequiredParams →
["channel"]`) and Talos (`→ ["schematic"]`), which already carry their variant in
target params. Consequences:

- Two Flatcar targets on different channels are impossible (the driver ignores
  `params["channel"]` even if set) — and worse, pre-#48 two such targets **collide
  on disk** under `flatcar/-/<arch>/<version>` because `paramSegment` only knows
  schematics.
- `pkg/cache/seed.go` hardcodes the predefined Flatcar/FCOS targets to `RetainN: 1`,
  and the issue's suggested workaround (`PATCH /api/v1/targets/{id} {"retainN": 2}`)
  is **silently reverted within one tick**: `seedTargets` re-upserts every tick and
  `db.UpsertTarget`'s `ON CONFLICT` clause clobbers `mode`/`retain_n`/`predefined`/`enabled`.
- Flatcar/FCOS discovery returns only the single current version, so `retainN > 1`
  never accumulates an in-window history even when it sticks.

**Live bug folded in (user-approved):** upstream FCOS renamed the live kernel from
`live-kernel-<arch>` (dash) to `live-kernel.<arch>` (dot) between FCOS 39 and 44
(verified 2026-07-01: dash-URL 404s, dot-URL 200s). booty hand-builds the dash form,
so **no current FCOS version completes caching today**. #48 fixes the pattern
(dash→dot) since it is already rewiring these URL builders; P3b then derives FCOS
artifact URLs from the streams JSON `location` fields, killing the drift class.

## 2. Goals / non-goals

**Goals**

1. `flatcar` and `fedora-coreos` become params-driven: `RequiredParams() → ["channel"]`;
   discovery/artifact URLs derive from `params["channel"]`.
2. The existing `--flatcarChannel` / `--coreOSChannel` flags become the
   **predefined-target default** — zero behavior change out of the box.
3. Multiple channels are plain data: two Flatcar targets on different channels cache
   independently under **distinct cache-layout segments** (`flatcar/stable/…`,
   `flatcar/beta/…`).
4. Per-target `retainN` is honored (seed stops clobbering it) and does something
   useful for single-version-discovery OSes (retention window over known versions).
5. FCOS live-kernel filename pattern fixed (dash→dot).

**Non-goals**

- Signature verification, atomic downloads, `verified` columns — **P3b**.
- New retention flags (`--flatcarRetainMinors` etc.) — per-target `retainN` via the
  API is the tuning surface; adding flags would duplicate it (YAGNI).
- Channel-existence validation against upstream (a typo'd channel 404s at discovery
  and logs; only path-safety is validated).
- Per-OS `channel` for Talos (its variant is the schematic) or new OSes.

## 3. Driver changes — `pkg/ostype/ignition.go`

`flatcar` and `fedoraCoreOS` mirror the `debian`/`talos` pattern:

- `RequiredParams() []string` → `["channel"]` for both.
- `flatcarBaseURL()` → `flatcarBaseURL(channel string)`; reads
  `viper.GetString(config.FlatcarURL)` (URL *template* stays a flag — it is a mirror
  selector, not a variant) and substitutes `channel` + the arch flag.
- `coreosStreamsURL()` / `coreosBuildBaseURL()` likewise take `channel`.
- `DiscoverVersions(ctx)` — **problem:** the `OS` interface passes no params to
  `DiscoverVersions`. Two options considered:
  - **(a) Widen the interface:** `DiscoverVersions(ctx, params map[string]string)`.
    Honest — discovery for these OSes *is* channel-scoped (and for Talos it is not
    params-scoped, so talos/debian ignore the argument). One mechanical change at the
    interface, three trivial implementors, one call site (`reconcileTarget`).
  - (b) Keep the signature and stash channel via viper per-call — a hidden global
    write racing the coordinator; rejected outright.

  **Decision: (a).** `DiscoverVersions(ctx context.Context, params map[string]string)`.
  Call sites: `reconcileTarget` (passes the target's decoded params). This is the
  same widening P1a already did for `Artifacts`.
- `Artifacts(version, arch, params)` — flatcar reads `params["channel"]` for the base
  URL; fedoraCoreOS reads `params["channel"]` for the build base URL **and** fixes the
  kernel filename: `fedora-coreos-%s-live-kernel.%s` (dot). Empty channel: fall back
  to the flag default (defensive parity with debian's `stable` fallback), so a
  pre-migration row can't build a `%!s(MISSING)` URL.

**Channel value validation:** a channel becomes a path segment on disk and in URLs.
Where targets enter the system (API create in `pkg/http`, `seedTargets`, and the §6
migration's flag value), the channel must match `^[a-z0-9][a-z0-9.-]*$`. Safety
comes from **single-segment anchoring** — the charset admits no `/` and no leading
dot, so the value cannot traverse regardless of interior dots (`a..b` is an odd but
harmless single segment; a literal `..` or empty string is rejected). This is the
same class of guard the menu path's `containsTraversal` already applies. Talos
schematics get the same check for free if validation lives beside `RequiredParams`
presence-checking (single knowledge site: params that become path segments must be
path-safe).

## 4. Cache layout — `pkg/cache/layout.go`

`paramSegment` becomes channel-aware; the layout invariant (design §2.3: exactly
**one** path-discriminating param per OS) is preserved — schematic for Talos,
channel for Flatcar/FCOS/Debian:

```go
// paramSegment encodes a target's params into the single path-discriminating
// cache segment: schematic (talos) > channel (flatcar/fcos/debian) > "-".
func paramSegment(params map[string]string) string {
    if s := params["schematic"]; s != "" { return s }
    if c := params["channel"]; c != "" { return c }
    return "-"
}
```

No OS carries both keys, so precedence order is theoretical; documented anyway.
`cacheSegments`/`cacheDir`/`CacheURLBase` are untouched — disk, DB and URL keep
deriving from the same helpers (No-Wall: the seam absorbs the change in one place).
`NewestCached`, `scan`, `evict`, `list` all flow the new segment automatically
because they already call `paramSegment`.

Resulting layout: `cache/flatcar/stable/amd64/4230.2.2/…`,
`cache/coreos/stable/x86_64/44.20260607.3.1/…` (menu labels become *more* readable
than the current `-`).

## 5. Seeding & the flag-as-default contract — `pkg/cache/seed.go`, `pkg/db`

**[provisional — user AFK, recommended option taken] Flag = first-boot default.**
Predefined targets are seeded **create-if-absent**: a new store method
`EnsureTarget(t)` (`INSERT … ON CONFLICT(os,arch,params) DO NOTHING`) replaces
`UpsertTarget` in `seedTargets`. Flags populate a predefined row only when it does
not exist; thereafter the API owns `retainN`/`enabled`/`mode`. Consequences, stated
honestly:

- `PATCH retainN` finally sticks (issue acceptance criterion).
- **Talos behavior change:** bumping `--talosRetainMinors` later no longer updates an
  existing predefined row (documented in CONFIGURATION.md; the API is the knob).
  Changing `--flatcarChannel` later creates a **new** predefined target for the new
  channel (params are row identity); the old channel's target remains until deleted
  (deletes are 403 until P10 — it can be `enabled=false`d via PATCH).
- Host-derived Talos schematic targets keep the same create-if-absent treatment.
- `UpsertTarget` itself remains for any caller that genuinely wants clobber
  semantics; if `seedTargets` was its only caller, it is **removed** (subtraction
  pass) — to be confirmed at plan time.

**Predefined params change:** seed now writes
`{"channel": <flag>}` for flatcar/fcos instead of `{}`.

## 6. Migration — existing rows and disk

Without migration, the old predefined rows (`params="{}"`) would remain enabled and
fail every tick post-#48 (no channel → defensive fallback hides it, but the row
duplicates the new one), and existing artifacts under `<os>/-/…` would be orphaned
and re-downloaded (gigabytes).

**[provisional] One-time Go-side migration**, run at startup **before the
reconciler loop starts** (explicitly: before `reconcileAll`, since `seedTargets`
runs inside it and the in-place rewrite must precede the create-if-absent seed to
preserve `target_versions`/`cache_entries`). Crash-consistency (SGE #3): the two
steps are **independently idempotent** — neither keys on the other having run,
so a crash between them retries the remainder on next startup:

1. **DB step** (keyed on the old shape existing): for each target
   `os IN (flatcar, fedora-coreos) AND params='{}'`: rewrite `params` to
   `{"channel": <current flag value>}` **in place** (`UPDATE targets SET
   params=?`), preserving the row's `target_versions` and `cache_entries`. If the
   destination params row already exists (operator pre-created one), the old row is
   disabled instead (`enabled=false`) and logged — never silently merged.
2. **Disk step** (keyed only on the directories, NOT on the DB shape): on every
   startup, per OS root: if `<cacheRoot>/<os>/-` exists and
   `<cacheRoot>/<os>/<flag-channel>` does not, `os.Rename` it; if both exist, WARN
   and leave `-` (scan reports orphans; reconcile re-downloads — self-healing).
   Because the disk step never consults `params`, a crash after step 1 cannot
   strand the `-` dir as a permanent, eviction-invisible disk leak.

The flag value used by both steps passes the same path-safety validation as §3
(a malformed `--flatcarChannel` fails startup rather than minting an unsafe
segment).

Rationale for rename-over-redownload: pre-#48, *all* flatcar/fcos caching was the
flag channel by construction, so the rename is semantically exact when the flag is
unchanged. If the operator changed the flag between runs, the rename mislabels old
artifacts as the new channel — the reconciler then discovers the real newest for
that channel and the mislabeled versions age out as archived entries. Bounded,
self-correcting damage; documented in STORAGE.md.

Rejected alternative — no migration, document a one-time re-download: simpler code,
but leaves live-but-broken `{}` rows in the DB, which is not self-healing.

## 7. Boot path — `pkg/tftp/tftp.go`

The **menu/selection path is untouched**: it carries the 4-segment tuple
`<cacheName>/<segment>/<arch>/<version>` generically, so channel segments flow
through (`menu/flatcar/stable/amd64/4230.2.2/boot.ipxe`).

The **assigned/legacy path** (`bootTokens`) currently hardcodes segment `"-"` and
passes `nil` params to `NewestCached`. It changes to resolve channel exactly the way
the talos arm resolves schematic — host override, else flag:

```go
case "flatcar":
    channel := viper.GetString(config.FlatcarChannel)
    if p := hostParams(host); p["channel"] != "" { channel = p["channel"] }
    arch := viper.GetString(config.FlatcarArchitecture)
    version := cache.NewestCached("flatcar", arch, map[string]string{"channel": channel})
    return bootTokensFor("flatcar", channel, arch, version, urlHost)
```

- `hostParams` decodes `host.AssignedParams` (the P1c field, canonical JSON — parsed
  with `cache.DecodeParams`). **This is not new capability:** the field exists and is
  API-populated; #48 stops the flatcar/fcos arms ignoring it. [provisional]
  *Noted asymmetry (SGE #5):* talos resolves its variant from the typed
  `host.Schematic` column, while flatcar/fcos read `AssignedParams` JSON — the
  per-host variant has two representations depending on OS. They are **not** kept
  in sync; unifying them is a separate consistency task, deliberately out of #48's
  scope.
- `bootTokensFor`'s second parameter is the generic *segment* (it already was for the
  menu path); its name/doc updates from `schematic` to `segment`.
- **`[[coreos-channel]]` is dead**: `coreos.ipxe` sets `${STREAM}` but never uses it.
  The token, its viper read, and the template line are removed (it is the very viper
  read #48 exists to eliminate).
- `TestBootTokensByteIdentical` (the #44 refactor guard) is **updated, not
  preserved**: flatcar/coreos URLs intentionally change (`/-/` → `/<channel>/`).
  The guard's purpose was "refactor must not change output"; #48 is a spec change.
  Talos output remains byte-identical.

## 8. Retention — `pkg/cache/retention.go`, `reconcile.go`

**[provisional] Retention window over known versions.** `reconcileTarget` currently
computes `retained = retentionFor(os, discovered, retainN)` — for flatcar/fcos,
`discovered` is a single version, so `retainN > 1` can never keep two versions
in-window. Change the input to the union of discovered and currently-in-window
versions:

```go
known := discovered ∪ {v.Version : v is currently in-window}   // conceptually
retained = retentionFor(t.OS, known, t.RetainN)
```

**"In-window AND cached" is load-bearing** (tightened per SGE review): the union
draws from versions whose `cache_entries` row has `in_window=1` **and** whose
`target_versions.cached=1` — *not* from all existing `target_versions` rows with
`Source="discovered"`. Two reasons:

- Archived rows keep their `target_versions` row (P3a), so a source-based union
  would resurrect archived versions into the window every tick.
- P3b introduces failure-visibility `cache_entries` rows for versions with **no
  bytes on disk** (strict-rejected). A bytes-less version must not count as
  "known" — otherwise, with `retainN=1`, a perpetually-failing new version would
  displace the last good version into archived state, where **eviction could
  delete the only servable bytes** (SGE finding #2). The `cached=1` condition is a
  no-op at #48 time (every in-window entry is cached by construction) and the
  guard P3b relies on.

The reconciler therefore needs window+cached state alongside the version list (a
joined read — e.g. extend `ListTargetVersions` or a sibling accessor; exact query
shape is a plan decision). A version mid-download when upstream moves on simply
drops out (it never completed; no resurrection).

- **Flatcar/FCOS:** after an upstream release, `retainN=2` keeps the previous
  version in-window (still served as "newest cached" fallback, not just
  menu-bootable archive). History accumulates release by release.
- **Talos:** effectively a no-op — factory `/versions` already returns the full tag
  history, so the union normally adds nothing. One deliberate edge (SGE #8): if
  upstream *yanks* a tag that is currently in-window, the union keeps it retained
  where pre-#48 it would silently drop out. Accepted as intentional resilience —
  consistent with the existing "discovery failure keeps the existing cached set"
  posture; the version ages out when newer releases push it past `retainN`.
- **Debian:** no-op (fixed 2-version discovery set).
- Evicted versions cannot resurrect: eviction deletes the `target_versions` row, so
  they are no longer "known".
- Documented interaction (issue acceptance criterion): for single-version-discovery
  OSes, `retainN` bounds the *known-version window*, which grows one release at a
  time; it does not backfill older versions that upstream no longer advertises.

## 9. Testing

- `ostype`: table-driven params-driven URL tests for both drivers (channel in
  discovery + artifact URLs; FCOS dot-form kernel filename; empty-channel flag
  fallback); httptest discovery against per-channel paths.
- `cache`: `paramSegment` precedence table; seed create-if-absent (PATCH survives a
  second seed pass); migration idempotency (fresh DB no-op / old-shape rewrite /
  destination-exists disable + disk-rename cases with `t.TempDir()`).
- `tftp`: updated `TestBootTokensByteIdentical` full-map expectations (talos
  unchanged, flatcar/coreos new URLs); host `AssignedParams` channel override;
  `[[coreos-channel]]` token absent.
- Retention: union-window table tests (single-version OS accumulating across ticks;
  talos unchanged; retainN shrink archives correctly).
- Live netboot-lab smoke is **not** required for this slice (menu path untouched;
  assigned-path change is URL-shape only, covered by unit expectations) — but the
  P3b slice that follows will exercise the lab anyway.

## 10. Documentation gate

- `docs/schema/STORAGE.md`: layout examples gain channel segments; migration note
  (`-` → `<channel>` rename, orphan behavior).
- `docs/schema/API.md`: target params for flatcar/fcos (`channel`), path-safety
  validation rule, predefined-seeding semantics (flag = first-boot default).
- `docs/CONFIGURATION.md`: `--flatcarChannel`/`--coreOSChannel`/`--talosRetainMinors`
  re-described as first-boot defaults; retention-window semantics for
  single-version-discovery OSes.
- `README.md`: only if the quick-start mentions channels (verify at plan time).

## 11. Constraints (unchanged project invariants)

Module `github.com/jeefy/booty`; PR to `jacaudi/booty`; CGO-free Go 1.26;
`log/slog`; Huma v2; mutating API open in the trust window (DELETE 403 until P10);
`target_versions.cached` stays the coarse boolean; one path-discriminating param
per OS; disk↔DB↔URL derive from `cacheSegments`/`paramSegment` only.

## 12. Acceptance criteria (from #48, refined)

1. `RequiredParams()` includes `channel` for flatcar and fedora-coreos; predefined
   targets seed `{"channel": <flag>}`; out-of-the-box behavior identical (same
   channel cached, same boot flow — URLs/dirs relocate once via migration).
2. Discovery + artifact URLs derive from `params["channel"]`; no viper channel
   reads remain in `pkg/ostype` or `pkg/tftp`.
3. Two Flatcar targets on different channels cache independently under distinct
   segments, verifiable via `GET /api/v1/cache`.
4. `PATCH /api/v1/targets/{id} {"retainN": N}` survives reconcile ticks; retention
   window accumulates across releases for single-version-discovery OSes.
5. FCOS current-build caching works again (dot-form kernel).
6. Migration: old `{}` rows rewritten/disabled, `-` dirs renamed; fresh installs
   unaffected; second startup is a no-op.
7. Docs gate (§10) complete; tests (§9) green under `go test -race`.

## Appendix — decisions taken while user AFK (review these first)

| # | Decision | Recommended-and-taken | Alternative on file |
|---|----------|----------------------|---------------------|
| D1 | Seed authority | Flag = first-boot default (create-if-absent; API owns thereafter) | Flag stays authoritative each tick (contradicts issue) |
| D2 | Migration | In-place params rewrite + disk dir rename | No migration; document one-time re-download (leaves broken `{}` rows) |
| D3 | Assigned-path channel | `AssignedParams["channel"]` override, else flag (mirrors talos schematic) | Flag only; AssignedParams stays dead for flatcar/fcos |
| D4 | Retention | Window over discovered ∪ in-window (union) | Document-only (retainN>1 stays useless for flatcar/fcos) |
| D5 | `DiscoverVersions` | Widen to `(ctx, params)` | Viper stash per call (racy — rejected) |
| D6 | `[[coreos-channel]]` | Remove dead token + template line | Keep and source from target (keeps dead code) |
