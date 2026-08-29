# Design: bound the Debian DVD re-download, and stop reverify degrading a rejection reason

**Issues:** [jacaudi/booty#77](https://github.com/jacaudi/booty/issues/77) ·
[jacaudi/booty#83](https://github.com/jacaudi/booty/issues/83)
**Status:** design — Gate 1 returned (5 blocking, 8 significant, 7 minor); **all folded in below**,
each verified against the code before folding, and every one held
**Base:** `main` @ `69f39db` (the #84 merge). Measured baseline: **776 tests, 0 failures.**
**Extends:** the tails design's **D4b** (`docs/designs/2026-08-02-tails-sha256-verification-design.md`)
and its §7 retry guard; the verdict-erasure design's three-way disposition
(`docs/designs/2026-08-20-verdict-erasure-design.md` **D-A**)

**One design, two separable executions.** The two issues are the same distinction — *"could not
evaluate"* is infrastructure, *"does not match"* is a verdict — applied at two different scopes, so
reasoning about them together is cheap. They share no code. **Execution must split them into
separate tasks**: the DVD state-machine change (§4–§5) and the reverify vocabulary change (§6–§7)
touch disjoint files and must not be bundled into one commit.

---

## 1. Goal

**#77.** A Debian DVD target whose set fails GPG/checksum verification re-downloads the entire
multi-disc set every `--cacheInterval` — tens of GB/hour at the 5-minute default — with nothing
bounding the retry. The version-level guard #76 shipped does not reach this path: `reconcileTarget`
dispatches DVD-wanted Debian targets at `pkg/cache/reconcile.go:99` and returns at `:116`, before
the version loop where the guard sits.

**#83.** Reverifying a version that has already been rejected by verification replaces its
operator-meaningful `verify_err` (`"tails-amd64.iso: checksum mismatch"`) with `"artifact absent"`,
because D4a already removed the bytes. Safety is unaffected — `fetched_at` is untouched, so the
guard stays armed — but a durable record of *why* a multi-GB image was refused is destroyed, and it
is replaced with a claim about a **different fault with a different remedy**.

*(Gate 1 correctly objected that "the only durable record" was too strong: a second path — a reverify
during a re-download — destroys it too. §6 now closes that path as well; see D11 and residual 2.)*

Both are pre-existing and both were deliberately scoped out of #84.

---

## 2. Evidence

### Verified — read directly from the code on 2026-08-26, at `69f39db`

- **`ensureDebianDVD` records NOTHING persistent on a verification failure.** `isoVerify` fails at
  `debiandvd.go:431` → `removeUnverifiedISOs` (`:439`) → `return err` (`:440`) →
  `reconcile.go:114` logs a `slog.Warn` → `:116` returns nil. Every DB write in the function —
  `UpsertTargetVersion` (`:455`), `UpsertCacheEntry` (`:462`),
  `SetCachePinnedByTargetVersion` (`:465`), `SetTargetSourceMode` (`:468`) — sits **below** that
  return. This is the crux of #77 and §3 states its consequence. *(Independently re-verified by
  Gate 1.)*
- **The DVD state machine's only persistent signals are filesystem ones**: per-ISO presence (the
  skip at `:418`) and `.booty-dvd-complete` (the heavy-work gate at `:411`, `dvdSentinelPresent` at
  `:74`). A failure leaves *less* than the pre-attempt state, not the same: D3's deletion removes
  ISOs that may have been present before the attempt, and the `MkdirAll`'d dir is never removed.
  What matters is that nothing it leaves behind is **distinguishable from "never attempted"**, which
  is why none of it can bound a retry.
- **`debianDVDVersion` calls the same `o.DiscoverVersions` the netinst path calls.**
  `debiandvd.go:247` vs `reconcile.go:135`, with identical params. `debian.DiscoverVersions`
  (`pkg/ostype/debian.go:87-108`) returns exactly one version — the newest point release — which
  `retentionFor` always retains at `retainN >= 1`. The collision D2 reasons about is reachable end
  to end.
- **`UpsertTargetVersion` does `source = excluded.source, cached = excluded.cached`**
  (`pkg/db/versions.go:20-22`), so writing a row with `Cached:false` resets a live `cached=1` row and
  permanently converts a `discovered` row into a `manual` pin.
- **Nothing in the serving path reads those columns.** Every version resolution is a disk scan:
  `cache.NewestCached` is a pure `os.ReadDir` of the cache tree with no DB (`pkg/cache/newest.go:22-36`),
  used by `pkg/tftp/tftp.go`, `pkg/http/preseed.go`, `pkg/http/request.go` and
  `pkg/http/machineconfig.go`; `ValidCachedSelection` → `cacheDirExists` → `os.Stat`
  (`pkg/cache/list.go:94-125`); `PartitionCached`'s entry set is `ListCached()`. **D2's original
  "would break serving" claim was FALSE and Gate 1 caught it — D2 now gives the real reasons.**
- **`VerifyVersion` has exactly one production caller**, `pkg/http/api_cache.go:167`. (`git grep`
  over `*.go`, excluding `.claude/worktrees/`.) The #83 blast radius is one call site plus tests.
- **The absent branch is only reachable for an artifact that declares material** — `verify.go:426`
  short-circuits `a.SHA256 == "" && a.SigURL == ""` to `classNotVerifiable` before the `os.Stat`.
- **An ARCHIVED version never re-enters the version loop, so it never self-heals.**
  `desired = retained ∪ manual` (`reconcile.go:175`); `retained` comes from
  `discovered ∪ ListCachedInWindowVersions` (`:148-158`), and `ListCachedInWindowVersions` requires
  `ce.in_window = 1` (`pkg/db/versions.go:116`). So a rotated-out discovered version is in neither
  input: `finalFilesPresent` is never evaluated for it and `SetCacheVerified(&true, "")` never fires.
  **This falsified the first draft's D10** — see D10 for what replaced it. Flatcar makes it concrete:
  `flatcar.Artifacts` declares `SigURL` for **every** version, current or not
  (`pkg/ostype/ignition.go:73-82`), so an archived Flatcar version is verifiable by construction.
- **`VerifyVersion`'s in-flight branch is an order-dependent short-circuit.** `verify.go:440-444`
  does `return nil, "", nil` from **inside** the per-artifact loop, discarding every verdict
  accumulated so far and never examining the remaining artifacts. FCOS declares sha256 on three
  artifacts per version and Flatcar on two, so this is reachable. §6/D11 folds it in rather than
  exempting it.
- **`SetCacheVerified(tvID, nil, "")` clears `verify_err` as well as `verified`**
  (`pkg/db/cache.go:74-76`), so the in-flight branch above destroys a recorded rejection reason —
  the same harm #83 exists to prevent, by a second route.
- **`ListCached` (`pkg/cache/list.go:66-76`) keys on version *directories*, ignoring contents.** A
  new marker file adds no phantom listing; the empty dir already appears there after a failure today
  (`ensureDebianDVD` `MkdirAll`s at `:412`).
- **`Scan` (`pkg/cache/scan.go:42-45`) only walks `cached=1` rows**, and its size total is an
  `os.ReadDir` of the version dir's top level — which *would* count a marker in the shared-dir case
  of §5.4.
- **Files under `/data/cache/` are served unauthenticated.** `dataSubtrees` allows anything strictly
  below `/cache/` (`pkg/http/http.go:108`, `isAllowedDataPath` `:123-133`); `isPartialPath` filters
  only `.partial` and `DownloadSuffix` (`:174-178`); `noListingDir` blocks *listing*, not a direct
  GET (`:140-157`). **This forced D1's marker to be written EMPTY** — see D1.
- **Reverify is invoked TWO ways in the UI** — as a bulk action over every selected row
  (`web/src/views/CacheView.tsx:330`) and as a single-row button that toasts
  `Re-verified ${version}` (`:383`). The bulk path surfaces a rejected promise as an error
  (`CacheView.test.tsx:263-278`). Both matter: the first kills the HTTP-409 shape (D13), the second
  is why a silent no-op is not an acceptable answer on its own (D10).
- **Nothing deletes, resets, or ignores the marker.** `SweepPartials` removes only `*.partial`
  (`pkg/cache/partial.go:20`); `applyCatalog` never touches disk (`catalog_apply.go:25-70`);
  `MigrateChannelLayout` does a whole-dir `os.Rename` (`migrate.go:116`), carrying the marker with
  its mtime; eviction requires `size > 0` **and** a `cache_entries` row (`pkg/db/cache.go:259-262`),
  neither of which a failed DVD attempt has; `ensureDebianDVD`'s tail (`:477-484`) only removes
  *other* versions' dirs. Concurrent passes are impossible — `loop` runs `reconcileAll`
  synchronously on one goroutine (`pkg/cache/reconciler.go:76-91`). *(Gate 1 hunted this list
  independently and found no additional path.)*
- **`docs/schema/DATABASE.md:109`'s failure-class list is INCOMPLETE, and removing `artifact absent`
  does not make it complete.** Four other reachable messages are also missing from it —
  `"checksum unavailable"` (`verify.go:69`), `"signature material unavailable"` (`:273`),
  `"open for verify"` (`:277`), `"keyring parse"` (`:289`) — all produced by `verifyArtifact`, which
  `VerifyVersion` calls directly at `:448`. **The first draft claimed that doc was "already
  consistent"; that was false** and is corrected in §9.

### Assumed — stated, not proven

- **Marker mtime is trustworthy on the deployment filesystem.** booty runs on the user's NAS. A
  coarse or non-monotonic mtime (NFS/SMB) could skew the window by seconds-to-minutes against an
  hour-scale bound, which degrades gracefully. Not measured.
- **A backwards clock jump wedges the target for the size of the jump.** `time.Since(mtime)` goes
  negative, so the guard holds until wall-clock catches up, with only a per-tick `WARN`. This is not
  a new hazard class — `datetime('now')` is wall-clock too, so the existing SQL guard has the same
  exposure — but it is stated rather than left to a parenthetical. §5.4 answers "can it wedge
  forever?": no, only for the jump's duration.
- **A Debian DVD set's ISO names and the `SHA256SUMS` they are checked against are stable within a
  point release.** Inherited from the existing DVD design, not re-established here.
- **How often an operator deletes a version dir out of band.** D10's archived-version case is
  established as *mechanically* reachable; its frequency is not.

---

## 3. The root cause, stated precisely

### #77 — the guard has no signal to key on, because none is written

`db.VerifyRejectedWithin` matches a four-column signature —
`size=0 AND in_window=0 AND verified=0 AND verify_err <> ''` — with a recency clause on `fetched_at`.
That signature is produced by exactly one writer, `UpsertCacheEntryArchived`, whose justification
`pkg/db/cache.go:105-145` reasons about carefully.

The DVD path writes **no row at all** on failure. So this is not a case of a predicate needing
translation: there is no state to translate. Every candidate fix must **introduce** a signal, and
the design question is *which store it lives in*.

### #83 — `VerifyVersion` calls missing material a corruption verdict

`verify.go:445` builds `artifactVerdict{class: classCorruption, err: "%s: artifact absent"}`.
`aggregateVerdicts` folds `classCorruption` into `verified=false` with that message, and the handler
writes it unconditionally at `api_cache.go:185`.

But a missing file is not a statement about the bytes' integrity. It is the version-level twin of
the rule **D4b** already established one frame lower on the land path:

> *"I could not evaluate the material" is infrastructure and must be retried; "the material does not
> match" is a verdict.*

D4b routes a `hashFile` **read** failure out of `landArtifact` as an error rather than a corruption
verdict, precisely so a transient blip cannot destroy a completed multi-GB download. #84 applied the
same rule at the version scope, choosing to fix it at the **disposition** level (`ctx.Err()` →
defer) rather than by reclassifying inside `verifyArtifact`. This design applies it at the
**reverify** scope, the same way.

The in-flight branch two lines above (`verify.go:440-444`) is the *same* fault wearing a different
coat: it recognises that material-in-motion cannot be judged, then expresses that by discarding
every sibling's verdict and writing NULL. §6 unifies the two.

---

## 4. Decisions — #77

### D1 — the retry bound is an EMPTY marker file in the version dir, not a `cache_entries` row

`.booty-dvd-verify-failed`, written into the version dir alongside the existing
`.booty-dvd-complete` sentinel.

| The version loop's signal | The DVD path's replacement |
|---|---|
| a `cache_entries` row exists for `(target, version)` | the marker file exists in the version dir |
| `size=0 ∧ in_window=0 ∧ verified=0 ∧ verify_err<>''` — the only combination `UpsertCacheEntryArchived` produces | the marker's **existence**: it has exactly one writer (the `isoVerify`-failure branch) and no other state in the system can forge it |
| `fetched_at > datetime('now', '-N seconds')` | `time.Since(fi.ModTime()) < verifyRetryAfter` |
| the row's `verify_err`, logged as `guardReason` | **nothing — the marker is empty.** See below. |

**This is the "adapt, not copy" the issue asks for.** The four-column predicate exists because
`cache_entries` has *many* writers and the guard must not be forged by a row some other path
produced (`pkg/db/cache.go:115-122` walks that reasoning). A marker written by one branch and read
by one branch has no such problem, so the predicate's complexity has no counterpart to translate —
its *purpose* is met by the marker's exclusivity.

**The marker carries no contents, and that is a correction Gate 1 forced.** The first draft stored
`err.Error()` in it so a guarded tick could log the reason. But files under `/data/cache/` are served
unauthenticated (§2), and `verifyDVDChecksums`' errors embed **absolute server filesystem paths**
(`debiandvd.go:44`, `:48`; `verify.go:320`) or got/want digests (`debiandvd.go:62`). The existing
completion sentinel is written with `nil` contents (`debiandvd.go:219`), so a reason-bearing marker
would be the *first* cache-dir file to put server-internal diagnostics on the public read surface —
in a repo that has already shipped one incident of exactly that class (`/data/booty.db`).

Writing it empty also survives a subtraction pass on its own merits: it removes the
"unreadable marker" degenerate case from `dvdVerifyGuarded`, and it makes `markDVDVerifyFailed` a
one-liner. The reason is not lost — it is logged at the moment of failure (D6).

Two mechanical consequences worth stating, because they are the reason this is not merely "the DB
version, on disk":

- **`os.Stat` returns a real `time.Time`.** Both recorded traps are structurally absent rather than
  dodged: SQLite's `datetime('now','-1h0m0s')` → `NULL` no-op (`pkg/db/cache.go:135-145`), and the
  `fetched_at` UTC-`TEXT` parse that yields a future timestamp and wedges the version for hours.
- **`os.Chtimes` can backdate the marker in a test.** `UpsertCacheEntryArchived` hardcodes
  `datetime('now')` and `Store.db` is unexported, which is exactly what defeated the equivalent test
  on #84 and forced it onto a sentinel `verify_err` instead.

### D2 — the `cache_entries` row alternative is REJECTED, for four reasons that are NOT "it breaks serving"

Writing the guard into the DB requires a `target_versions` row (for the `tvID`), then
`UpsertCacheEntryArchived`. On the mainline promote path that collides with a live netinst row:

A discovery-mode Debian target that has cached netinst versions is promoted to `desired_mode=dvd`.
`existingDVDVersion` finds nothing (the netinst row is `source="discovered"`, not `"manual"`), so
`debianDVDVersion` resolves the version — via the same `o.DiscoverVersions` the netinst path uses —
and returns the newest point release, which `retentionFor` always retains. The code already knows
these collide: `removeStaleNetinstArtifacts` (`debiandvd.go:294`) exists for exactly the same-dir
case, and is only reachable when the version strings match.

**The first draft said this "would break serving". That was FALSE, and Gate 1 proved it:** every
version resolution on the boot path is a disk scan, not a DB read (§2). The conclusion survives, on
these grounds instead:

1. **It permanently converts a discovered row into a manual pin.** `UpsertTargetVersion` writes
   `source = excluded.source` (`pkg/db/versions.go:20-22`), and manual rows are **never pruned and
   always desired** — a one-way change to the target's retention semantics, as a side effect of a
   failed download.
2. **It under-counts `SumCacheBytes` while the bytes remain on disk.** `size=0` against a directory
   that still holds netinst artifacts corrupts the eviction budget in the *permissive* direction.
3. **It surfaces a DVD error on a netinst row.** The Cache view would show that version as
   archived-and-failed with a DVD verification message, describing a fault that did not happen to
   the artifacts the row represents.
4. **It would later suppress a netinst re-download on the strength of a DVD failure.** If the target
   is demoted back to netinst, the version-loop guard (`VerifyRejectedWithin`) reads exactly that
   four-column signature and blocks the netinst re-download for an hour — a cross-mode false
   positive the predicate's own justification never contemplated.

Guarding the write ("only if no cached row exists for this version") does not rescue it either: it
leaves the loop unbounded in precisely the colliding case, which is the mainline promote and the
case with the most disk at stake. A half-fix in the expensive half.

### D3 — `removeUnverifiedISOs` STAYS. The deletion is correct; the retry is what needed bounding

`removeUnverifiedISOs`' own comment (`debiandvd.go:303-308`, `:432-438`) is right and this design
does not reverse it. Keeping the bad ISOs would make the skip-if-present path at `:418` re-verify
the same known-bad bytes forever: on a 4-disc set that is roughly 19 GB of hashing every tick (the
measured rate is 1.94 GB in 3.55 s on SSD, disk-bound — and a NAS spindle is slower, which
strengthens this), and it **never self-heals** when mirrors converge, because the bad bytes are
never re-fetched.

Delete + bounded retry is the only shape that both stops the bleeding and still recovers. The
trade-off is explicit: each retry after the window costs a full re-download, and the bound is what
makes that acceptable.

### D4 — only a VERIFICATION failure writes the marker; a DOWNLOAD failure never does

This is D4b again, in the DVD state machine. `isoDownload` failing at `:421`/`:425`/`:428` is
"could not evaluate": `downloadLargeFile` leaves resumable `<iso>.download` bytes
(`isodownload.go:111-117`), and the next tick resumes via `Range`. Marking that would throw away
resumable progress for an hour on an ordinary network blip.

Only `isoVerify` returning non-nil (`:431`) writes the marker — the one outcome that is knowledge a
retry within the window will not change.

### D5 — `verifyRetryAfter` is REUSED, not copied

`pkg/cache/reconcile.go:29`. "How long a verification rejection suppresses a re-download" is one
piece of knowledge with one change-driver; both consumers are `package cache`. This is DRY's
extraction criterion met exactly, and it is a *reuse*, not a new abstraction.

It stays a **package var** — not a viper key, not a CLI flag. That decision is settled by #76 and its
rationale (`reconcile.go:24-28`) is unchanged by adding a second in-package consumer. The
test-swap precedent is `pkg/cache/reconcile_test.go:966-968`.

### D6 — a guarded tick returns `nil`, and the failure logs its reason at the failure site

Being guarded is not an error: the function's contract is "bring this target to `source_mode=dvd`",
and a deliberate deferral is not a failure to do so. Returning an error would make
`reconcile.go:114` log `"debian dvd ensure failed"` for a healthy rate-limit, which is a lie.

Because D1 made the marker empty, the reason lives in **two log lines, not in the marker**:

- at the moment of failure, `reconcile.go:114` already logs the full verification error;
- on each guarded tick, `ensureDebianDVD` logs a `slog.Warn` naming the target, version and window,
  and pointing at the earlier failure rather than restating it.

`ensureDebianDVD` returns `nil` in the guarded case.

### D7 — a DVD verification failure stays invisible in the Cache view

**Operator's explicit choice this session.** Today a DVD verify failure produces a `slog.Warn` and
nothing else; that is unchanged. #77 asks for a bound, not for visibility, and the safe way to add a
failure-visibility row is a *different* change that must first answer D2's four objections — most
likely a new `pkg/db` writer that refuses to touch an existing cached row. Recorded as a residual in
§10, not smuggled in here.

### D8 — no schema change, no migration, no API surface change

Stated loudly because the handoff asks: **neither half of this design touches the schema.** #77 is a
filesystem marker; #83 changes only which Go value a function returns and which `UPDATE` runs.

"No API surface change" means no route, request shape, response shape, or status code moves — §9's
edit to `docs/schema/API.md` documents a case the endpoint's existing 200 already covers.

**It is NOT an all-clear on the HTTP read surface.** The marker lands inside the tree
`/data/cache/` serves, which is why D1 writes it empty; that reasoning belongs to D1, not to this
decision.

---

## 5. The change — #77

### 5.1 The marker

```go
// dvdVerifyFailedName marks a DVD set that FAILED verification, bounding how
// often the set is re-downloaded. It is the DVD state machine's counterpart to
// the cache_entries failure row db.VerifyRejectedWithin matches on: that
// predicate needs four columns because cache_entries has many writers and none
// of the others may forge the signature (see pkg/db/cache.go), whereas this
// file has exactly ONE writer — the isoVerify-failure branch of
// ensureDebianDVD — so its bare existence is unforgeable.
//
// Unlike dvdSentinelName, whose doc comment states that PRESENCE and not mtime
// is its signal, this marker is deliberately mtime-bearing: the retry window is
// the whole point. os.Stat yields a real time.Time, so neither the SQLite
// "-N seconds" trap nor the fetched_at UTC-TEXT parse trap applies here.
//
// It is written EMPTY, like dvdSentinelName. Everything under the version dir
// is reachable over unauthenticated HTTP (pkg/http/http.go's dataSubtrees
// allows all of /data/cache/, and isPartialPath filters only the two in-flight
// suffixes), and verifyDVDChecksums' errors embed absolute server paths and
// expected digests. The reason is logged instead — see ensureDebianDVD.
const dvdVerifyFailedName = ".booty-dvd-verify-failed"
```

### 5.2 Write, read, clear

`pkg/cache/debiandvd.go` must add a `time` import; it currently has none.

```go
// markDVDVerifyFailed arms the DVD verify-retry bound for dir. Best-effort: a
// write failure is logged and non-fatal — the caller returns the verification
// error regardless, and an unwritten marker degrades to today's unbounded
// retry rather than to anything unsafe.
func markDVDVerifyFailed(dir string) { … os.WriteFile(filepath.Join(dir, dvdVerifyFailedName), nil, 0o644) … }

// dvdVerifyGuarded reports whether dir's DVD set was refused by verification
// less than verifyRetryAfter ago. A missing marker or an elapsed window both
// release the guard: every degenerate case fails OPEN, which is today's
// behaviour. A backwards clock jump holds the guard for the jump's duration
// and no longer — the same exposure the SQL guard's datetime('now') has.
func dvdVerifyGuarded(dir string) bool { … time.Since(fi.ModTime()) < verifyRetryAfter … }

// clearDVDVerifyFailed removes the marker after a successful verification.
// Not required for correctness — the guard is only read inside the
// not-yet-settled branch, and a stale marker's mtime ages out — but it keeps
// the marker out of dirSize, Scan's size walk, and the /data/ read surface.
func clearDVDVerifyFailed(dir string) { … }
```

### 5.3 The wiring in `ensureDebianDVD`

Inside the existing `if !dvdSentinelPresent(dir)` block, ahead of `os.MkdirAll` — so a guarded tick
**downloads no ISOs**, matching the version loop's reason for placing its guard before
`o.Artifacts` (`reconcile.go:182-185`). It does **not** make the tick network-free and does not claim
to: with no cached row yet, `existingDVDVersion` misses and `reconcile.go` has already resolved the
version through `debian.DiscoverVersions`, an unmemoized cdimage index GET, before calling in here.
That request is small and bounded; the multi-disc download is what #77 is about:

```go
if !dvdSentinelPresent(dir) { // heavy work only when the tree is not yet settled
	if dvdVerifyGuarded(dir) {
		// The reason is NOT repeated here: the marker is empty by design (see
		// dvdVerifyFailedName), and reconcile.go already logged the full
		// verification error when the set was refused.
		slog.Warn("cache: debian dvd set rejected by verification; not retrying yet",
			"target", t.ID, "arch", t.Arch, "version", version, "retryAfter", verifyRetryAfter)
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		…
```

and at the verify branch:

```go
	if err := isoVerify(ctx, dir, isoNames); err != nil {
		removeUnverifiedISOs(dir, isoNames)
		// Bound the retry the removal above restores (#77): the set is deleted
		// so a converged mirror can self-heal, but without this marker that
		// restores a full multi-disc re-download every --cacheInterval,
		// indefinitely. Written ONLY here: an isoDownload failure above is
		// "could not evaluate" (D4b) and leaves resumable .download bytes that
		// must retry next tick.
		markDVDVerifyFailed(dir)
		return err
	}
	clearDVDVerifyFailed(dir)
```

Returning `nil` early skips the accounting/pin/flip block below, which is correct: the tree is not
settled, so there is nothing to account for.

### 5.4 Interactions

- **`/data/` exposure** — closed by writing the marker empty (D1). An empty dot-file discloses only
  its own existence, which the version dir already discloses.
- **`dirSize` and `Scan` count the marker** — zero bytes, and removed on the DVD success path before
  any accounting runs. In the shared-dir case (a DVD version equal to a netinst-cached version —
  D2), `Scan`'s top-level `os.ReadDir` would include it at 0 bytes while it exists.
- **`ListCached`** is unaffected — it lists version *directories* regardless of contents, and the
  dir already exists after a failure today.
- **Eviction cannot release the guard.** `ListArchivedUnpinned` requires `size > 0` and a
  `cache_entries` row (`pkg/db/cache.go:259-262`); a failed DVD attempt has neither. The DVD version
  is additionally the newest discovered, hence `in_window=1` and protected by `evict.go:55`.
- **`SweepPartials`, `applyCatalog` and `MigrateChannelLayout`** cannot remove it (§2). The migration
  renames the whole dir, carrying the marker with its mtime intact.
- **`ensureDebianDVD`'s own tail** (`:477-484`) removes *other* versions' dirs. It runs on both the
  fresh-extract and sentinel-already-present paths, but never on the guarded or failed path, and it
  filters `v.Version != version` — so it can never delete the failing version's marker. A marker and
  a sentinel also cannot coexist, because `clearDVDVerifyFailed` precedes the sentinel write.
- **Concurrent passes are impossible** — `loop` runs `reconcileAll` synchronously on one goroutine
  (`pkg/cache/reconciler.go:76-91`).
- **Can it wedge a target forever?** No. The only hold longer than `verifyRetryAfter` is a backwards
  wall-clock jump, which holds for the jump's duration and then releases.

### 5.5 Next tick, per branch

| Prior tick ended in | Marker | Next tick |
|---|---|---|
| verify failure, < 1 h ago | present, fresh | guarded: downloads no ISOs (the version-resolving index GET still happens), `Warn`, `return nil` |
| verify failure, > 1 h ago | present, stale | full re-download from zero, re-verify — self-heals if the mirror converged |
| download failure | absent | resumes from `<iso>.download` via `Range`, as today |
| **extract failure** | **absent** | **re-verifies the retained ISOs, re-extracts, fails again — unbounded. Residual 7.** |
| verify success | cleared | extract + merge + accounting, as today |

---

## 6. Decisions — #83

### D9 — the fix is in `VerifyVersion`'s VOCABULARY; the call-site rule loses

**Chosen:** material that is not on disk cannot be verdicted. `VerifyVersion` stops constructing
`classCorruption{"artifact absent"}`, collects the unexaminable filenames instead, and reports
*unevaluable* — which the handler does not record as a verdict.

**The alternative — "decline to overwrite a non-empty `verify_err` when the new reason is
`artifact absent` and the row is already `size=0`" — loses on three counts:**

1. **`VerifyVersion` keeps producing a false verdict.** The special case suppresses one consumer's
   damage; the bug stays in the producer, and the next consumer inherits it.
2. **It duplicates knowledge that `pkg/db/cache.go` reasons carefully about.** "What a rejection row
   looks like" would then be represented in SQL (`VerifyRejectedWithin`) *and* as a Go condition in
   `pkg/http`. Those must change together to stay correct — DRY's extraction criterion met — and
   they cannot be single-sourced across the SQL/Go boundary.
3. **It states the rule in terms of the damage rather than the fault**, so it cannot also close the
   in-flight path D11 folds in.

### D10 — the honest argument for D9, after Gate 1 falsified the first one

**The first draft's argument was: the "file deleted out-of-band" signal is transient because the next
reconcile tick re-lands the artifacts and clears `verify_err`. That is FALSE for archived versions**
— `desired` is built from `discovered ∪ ListCachedInWindowVersions`, and the latter requires
`in_window = 1` (`pkg/db/versions.go:116`), so a rotated-out version never re-enters the loop. An
archived Flatcar version (verifiable for every version, `pkg/ostype/ignition.go:73-82`) whose files
vanish would, under a naive D9, keep a stale `verified=true` forever while reverify answered
"Re-verified ✓". That is worse than today.

The real argument for D9 has two parts, and the second is a decision, not an observation:

1. **Absence is not a verdict, and recording it as one is a category error** with a concrete cost:
   it overwrites the reason a rejected version's bytes are gone (D4a removed them by design) with a
   different fault that has a different remedy. That is #83, and it does not depend on self-healing.
2. **An unevaluable outcome must not leave a standing AFFIRMATION in place.** So D11 adds: on
   unevaluable, a `verified=true` row has its affirmation **withdrawn** (`verified → NULL`), while a
   recorded **failure** is left untouched. That closes the archived-Flatcar case — the operator sees
   "no verdict" rather than a green badge for a directory with no bytes — without reopening D9 or
   D13. It does, however, need **a new DB writer**: `db.WithdrawCacheAffirmation`
   (`pkg/db/cache.go:102-109`). The handler does hold a row from `GetCacheEntry`
   (`api_cache.go:148`), but that row cannot decide the withdrawal — it was read *before*
   `VerifyVersion`, which spends minutes fetching and re-hashing multi-GB material, and the
   reconciler goroutine writes this same `cache_entries` row via `UpsertCacheEntryArchived` inside
   that window. Branching in Go on the stale `row.Verified` would erase a rejection recorded there.
   The writer puts the test and the write in one statement instead —
   `UPDATE cache_entries SET verified = NULL, verify_err = '' WHERE target_version_id = ? AND verified = 1`
   (`pkg/db/cache.go:104`).

Withdrawal cannot release a retry guard — **and it is the `AND verified = 1` predicate that makes
that true**, not the pre-call read. `VerifyRejectedWithin`'s four-column predicate requires
`verified = 0` (`pkg/db/cache.go:182`); the predicate confines the withdrawal to rows sitting at
`true`, which leaves them at `NULL`, and neither value matches. Without it the same statement
rewrites a `verified = 0` row to `NULL` with an empty `verify_err` and releases the guard.

### D11 — collect, never short-circuit; and the in-flight branch is folded in, not exempted

Gate 1 caught a contradiction: the first draft forbade short-circuiting on the first absent artifact
(*"it would discard a real mismatch found on a sibling, depending on iteration order"*) while
declaring the in-flight branch two lines above — `return nil, "", nil` from inside the same loop
(`verify.go:440-444`) — "unchanged". That branch is the identical order-dependent short-circuit, and
its damage is worse: `SetCacheVerified(nil, "")` clears `verify_err` too (`pkg/db/cache.go:74-76`).

Reachable, and squarely inside #83's own scenario: a rejected Tails version, guard armed with
`"tails-amd64.iso: checksum mismatch"`; the window elapses; the reconciler starts the 1.94 GB
re-download, leaving `tails-amd64.iso.download`; the operator clicks reverify → in-flight branch →
`(nil, "", nil)` → **the rejection reason is wiped and the guard released.** A stalled `.download`
"can sit for hours by design" (`pkg/http/http.go:167-169`), so the window is not narrow.

**Resolution: absent and in-flight are the same thing — material that is not there to examine.**
Both are collected, neither short-circuits, and `VerifyVersion` stops distinguishing them. The
`.partial`/`DownloadSuffix` stat disappears entirely, because with both cases landing in one bucket
it has no consumer left. That is a subtraction, not an addition.

The disposition:

| present artifacts | unexaminable artifacts | result |
|---|---|---|
| at least one **failed** (corruption/forgery) | any | record that failure — **strictly better than today**, which either joins `"artifact absent"` into the message or discards the failure entirely via the in-flight short-circuit |
| all passed, or none verifiable | ≥ 1 | **unevaluable**: record no verdict, and **withdraw a standing `verified=true`** (D10) |
| any | 0 | unchanged from today |

The middle row is load-bearing: a version with missing declared material must never be affirmed
`verified=true`, and must not keep an affirmation it earned when the bytes were still there.

**This flips two existing tests' assertions** — see §8.

### D12 — the outcome is carried as a sentinel error

```go
// ErrVersionUnevaluable reports that some of a version's DECLARED material is
// not on disk to examine — deleted, evicted, or mid-re-download. It is NOT a
// verification failure: the caller must not record a verdict, because the row
// it would overwrite may hold the real reason the bytes are gone (a rejection
// under D4a removes them by design — jacaudi/booty#83).
//
// The error channel is deliberate rather than a new return type: in this
// package "could not evaluate" IS the error channel — landArtifact uses it for
// exactly this distinction (D4b) — so a sentinel keeps one vocabulary instead
// of introducing a second.
//
// Constructed at exactly one site, in VerifyVersion's tail:
//
//	fmt.Errorf("%s: %w", strings.Join(unevaluable, ", "), ErrVersionUnevaluable)
var ErrVersionUnevaluable = errors.New("cache: no material on disk to verify")
```

`VerifyVersion`'s signature `(*bool, string, error)` is unchanged; the handler discriminates with
`errors.Is`. Gate 1 verified that no genuine 500-worthy error can wrap this sentinel: it is one hop
with no intermediate wrapping, and `artifactPath`, `GetCacheEntry`, `decodeParams` and `o.Artifacts`
all return unrelated errors.

*(The first draft specified two different constructions — `a.Filename` in D12, `strings.Join` in
§7.1 — and D12's form does not compile at the tail, since `a` is out of scope. One site, quoted
above and in §7.1 identically.)*

### D13 — the handler answers 200, NOT 409

**Operator's explicit choice this session, and it is forced by the UI.** `CacheView.tsx:330` runs
reverify as a **bulk** action over every selected row, and `CacheView.test.tsx:263-278` shows that
path surfaces a rejected promise as an error. A 409 per already-rejected row would report mass
failure for a correct no-op.

So: log a `slog.Warn`, apply D10's withdrawal if one is due, reload and return the row. The operator
sees their original `"checksum mismatch"` preserved — the outcome #83 asks for — and, in the
archived case D10 covers, a row that has stopped claiming to be verified. No API-surface change, no
DTO change, no UI change.

### D14 — the remaining nil-verdict guard release is deliberately NOT fixed

`api_cache.go:171-187` documents two hazards. This design closes **#2** (reason degraded) and
**the in-flight half of #1** via D11. What remains of hazard #1 is untouched: a nil verdict from a
**superseded tool tag** (`verify.go:418-420`, which returns before the loop) or from a version whose
artifacts declare **no material at all** still writes `verified=NULL, verify_err=''`, releasing the
retry guard early.

Three reasons, and they are reasons rather than an omission:

1. It is a **settled, documented** disposition ("defensible: an operator explicitly asked").
2. `SetCacheVerified`'s nil-clearing capability exists for a real case its doc comment names — a
   version whose artifact list stopped declaring material needs its stale verdict cleared — and this
   handler is its **only** nil-writer (`reconcile.go:364` guards `verified != nil`; the only other
   callers are `pkg/db/cache_test.go`). That capability survives D9/D11: a zero-verifiable version
   with all files present has `len(unevaluable) == 0` and falls through to the normal write.
3. It is a further behaviour change in a change that already has several, and #83 does not ask for it.

Recorded as residual 2 in §10, restated to say what it actually destroys.

---

## 7. The change — #83

### 7.1 `VerifyVersion`

The per-artifact loop collects unexaminable filenames instead of verdicting them, and the
`.partial`/`DownloadSuffix` stat is **deleted** (D11):

```go
	verdicts := make([]artifactVerdict, 0, len(arts))
	var unevaluable []string // declared material that is not on disk to examine
	for _, a := range arts {
		…
		if _, serr := os.Stat(final); serr != nil {
			// D9/#83: the material is NOT HERE TO EXAMINE — deleted, evicted, or
			// mid-re-download. That is not a claim about its integrity, and
			// recording it as corruption overwrites the real reason a rejected
			// version's bytes are gone (D4a removed them by design) with a
			// different fault that has a different remedy.
			//
			// Absent and in-flight are ONE case (D11), which is why there is no
			// longer a .partial/DownloadSuffix probe here: distinguishing them
			// only ever fed a `return nil, "", nil` from inside this loop, and
			// that discarded a sibling's real mismatch, order-dependently, while
			// clearing verify_err.
			//
			// COLLECTED, never short-circuited: a failure found on a present
			// sibling is durable knowledge and must survive.
			unevaluable = append(unevaluable, a.Filename)
			continue
		}
		verdicts = append(verdicts, verifyArtifact(ctx, final, "", a))
	}
```

and the tail:

```go
	verified, verifyErr := aggregateVerdicts(verdicts)
	// D11: a real failure on a present sibling is recorded. Unexaminable material
	// only suppresses recording when there is nothing better to record — and it
	// must NEVER let a version be affirmed while declared material is missing.
	if len(unevaluable) > 0 && (verified == nil || *verified) {
		return nil, "", fmt.Errorf("%s: %w", strings.Join(unevaluable, ", "), ErrVersionUnevaluable)
	}
	return verified, verifyErr, nil
```

`verify.go` already imports `errors`, `fmt`, `os` and `strings`.

### 7.2 The reverify handler

`pkg/http/api_cache.go` must add a `log/slog` import; it currently has none.

```go
		verified, verifyErr, verr := cache.VerifyVersion(ctx, deps.Store, n)
		switch {
		case errors.Is(verr, cache.ErrVersionUnevaluable):
			// #83: there is no material to examine, so there is no verdict — and
			// recording one would overwrite the reason it is gone. NOT an HTTP
			// error: the Cache view runs reverify as a BULK action, so a 4xx here
			// would report mass failure for a correct no-op.
			slog.Warn("cache: reverify found no material to examine; verdict not recorded",
				"id", n, "os", row.OS, "version", row.Version, "err", verr)
			// D10: withdraw a standing AFFIRMATION. "verified=true" over material
			// that is gone is a claim about bytes that are not there, and an
			// ARCHIVED version never re-enters the reconcile loop to correct it
			// (db/versions.go's in_window=1 clause). A recorded FAILURE is left
			// alone — preserving it is this issue's whole point.
			//
			// Called UNCONDITIONALLY. Which rows to touch is decided in SQL, by
			// WithdrawCacheAffirmation's AND verified = 1, never from `row`: row
			// was read before VerifyVersion, and although VerifyVersion itself
			// never writes the DB, the reconciler goroutine can archive this same
			// row during the multi-GB re-hash, so branching on row.Verified would
			// erase a rejection landed in that window. The predicate is also what
			// keeps the withdrawal off the verified=0 row db.VerifyRejectedWithin
			// looks for, and so from releasing the retry guard.
			if err := deps.Store.WithdrawCacheAffirmation(row.TargetVersionID); err != nil {
				return nil, huma.Error500InternalServerError("withdraw verdict", err)
			}
		case verr != nil:
			return nil, huma.Error500InternalServerError("verify", verr)
		default:
			if err := deps.Store.SetCacheVerified(row.TargetVersionID, verified, verifyErr); err != nil {
				return nil, huma.Error500InternalServerError("record verdict", err)
			}
		}
```

The existing comment block above the call is rewritten: hazard #2 is closed, hazard #1 survives only
in its superseded/no-material form and is restated per D14.

---

## 8. Tests

Both halves are TDD. Every test carries a **mutation check**: revert the named production line and
the test must go RED on its named assertion. A mutation that does not turn its test red is a HALT
and a report, never a weakened mutation. (This project has shipped inert tests three times — the
slice-2 feature-disabled test, T4's un-wired guard, and the D4b headline test that passed in its red
phase because the code it replaced also failed closed. A green run is not evidence.)

Two exceptions to the mutation rule, named rather than left implicit: tests 4 and 10 are **trap
guards** over paths the change does not touch, so no production line exists to revert. They must
pass before and after; that is their job.

### #77 — `pkg/cache/debiandvd_test.go` (`package cache`)

1. **`TestEnsureDebianDVD_VerifyFailureBoundsTheRetry`** — `swapDVDSeams` (`:219`) with a download
   counter and an always-failing verify. Run `ensureDebianDVD` twice. Assert the download count is
   **unchanged** by the second call, and that the marker exists.
   *Mutation:* delete the `dvdVerifyGuarded` block → the count doubles.
2. **`TestEnsureDebianDVD_VerifyRetryResumesAfterTheWindow`** — same, then `os.Chtimes` the marker to
   `verifyRetryAfter + time.Minute` ago. Assert the second call **does** re-download.
   *Mutation:* drop the `time.Since(...) < verifyRetryAfter` clause (make presence alone block) → the
   re-download never happens.
   *Also assert the degenerate direction:* with `verifyRetryAfter = 0` (swap precedent
   `reconcile_test.go:966-968`) the guard must release.
3. **`TestEnsureDebianDVD_DownloadFailureDoesNotArmTheGuard`** — failing `isoDownload`, passing
   `isoVerify`. Assert **no marker** is written and the next call retries.
   *Mutation:* **add `markDVDVerifyFailed(dir)` to the `isoDownload` error branch at `debiandvd.go:421`.**
   (The first draft said "move the call above the verify branch", which is **inert**: this fixture
   returns at `:422`, before anything above `:431` executes, so the test would stay green in both
   worlds. Gate 1 caught it — exactly the pattern §8's preamble warns about.)
   This is D4's trap guard; without it, a network blip costs an hour.
4. **`TestEnsureDebianDVD_SuccessfulVerifyClearsTheMarker`** — fail once, backdate, succeed. Assert
   the marker is gone and the sentinel is present. *(Trap guard: `clearDVDVerifyFailed` is hygiene,
   not correctness — D1 — so this test is the only thing pinning it.)*

**Assert on the marker's presence, never on its mtime** — the same discipline #84's Gate 2 forced
onto `fetched_at`. With an empty marker there are no contents to assert on either.

The existing `TestEnsureDebianDVD_VerifyFailureClearsISOsForRefetch` (`:363`) must keep passing
**unchanged**: D3 does not alter the deletion.

### #83 — `pkg/cache/verify_test.go` (`package cache`) and `pkg/http/api_cache_test.go` (`package http`)

5. **`TestVerifyVersionAbsentMaterialIsUnevaluableNotAVerdict`** — a version whose declared artifact
   is missing. Assert `errors.Is(err, ErrVersionUnevaluable)` and that the message names the file.
   *Mutation:* restore the `classCorruption` construction → `err` is nil and `verified` is false.
6. **`TestVerifyVersionUnexaminableSiblingDoesNotMaskARealMismatch`** — one present artifact whose
   bytes mismatch, one missing. Assert `VerifyVersion` **returns** `verified=false` with a
   `verifyErr` naming the *mismatch* and **not** the missing file. (Returns, not records —
   `VerifyVersion` never writes the DB, `verify.go:387`.) Pins D11's first row.
   *Mutation:* return `ErrVersionUnevaluable` whenever `len(unevaluable) > 0` → the mismatch is lost.
7. **`TestVerifyVersionUnexaminableSiblingNeverAffirms`** — one present artifact that passes, one
   missing. Assert unevaluable, **not** `verified=true`. Pins D11's middle row, the load-bearing one.
   *Mutation:* drop `|| *verified` from the condition → the version is affirmed while material is
   missing.
8. **`TestReverifyLeavesAGuardedRejectionReasonIntact`** — the #83 scenario end-to-end through the
   HTTP endpoint, built on the **FCOS** fixture already in that file (`fcosStreamsHandler`,
   `api_cache_test.go:74`), **not** on `pkg/cache`'s `tailsReverifyFixture`/`seedTailsVersion`
   (`verify_test.go:504`, `:541`), which are `package cache` and unreachable from `package http`.
   Seed a rejected row via `UpsertCacheEntryArchived` with a sentinel `verify_err`, remove the dir,
   POST reverify. Assert **200**, and that `verify_err` still reads the sentinel.
   *Mutation:* drop `AND verified = 1` from `WithdrawCacheAffirmation`'s `UPDATE`
   (`pkg/db/cache.go:104`) → the unconditional withdrawal rewrites the rejected row, `verify_err`
   degrades to empty here, and `TestWithdrawCacheAffirmationLeavesARecordedRejectionIntact`
   (`pkg/db/cache_test.go:476`) reddens with it.
   **Assert on `verify_err`, never on `fetched_at`** — one-second granularity cannot discriminate a
   same-second rewrite.
9. **`TestReverifyWithdrawsAnAffirmationOverMissingMaterial`** — seed a `verified=true` row, remove
   the files, POST reverify. Assert **200** and `verified` is now NULL. Pins D10's second part and
   the archived-Flatcar case Gate 1 found.
   *Mutation:* delete the withdrawal block → the row keeps a green `verified=true` over an empty dir.
10. **`TestReverifyStillRecordsANilVerdict`** — D14's trap guard: a superseded tool tag must still
    write `verified=NULL`, proving the change did not widen into what remains of hazard #1.
    *(Trap guard — the superseded path returns at `verify.go:418-420`, before the new code, so there
    is no line to revert.)*

### Two existing tests CHANGE their assertions — this is not an additive-only change

D11 deletes the in-flight probe, so these two stop returning `(nil, "", nil)` and start returning
`ErrVersionUnevaluable`:

- `TestVerifyVersion_AbsentFinalWithPartialIsNull` (`verify_test.go:265`)
- `TestVerifyVersionTailsInFlightResumeIsNoVerdict` (`verify_test.go:621`)

Both must be **rewritten, not deleted**, and renamed to say what they now pin (that in-flight
material is unevaluable rather than a no-verdict). Flipping an existing assertion is the one move
§8's no-weakening rule cannot distinguish from weakening a test, so it is authorised **here, by
name, for these two tests only** — anything else that turns red is a defect, not a fixture to
update.

### Baseline

Measure it on the branch tip with a clean tree, as the **sole command** in its own tool call, and
cross-check with `awk`:

```
go test ./... -race -count=1 -v > /tmp/dvd-baseline.txt 2>&1
grep -c '^=== RUN' /tmp/dvd-baseline.txt
awk '/^=== RUN/{n++}END{print n+0}' /tmp/dvd-baseline.txt
```

Gate 1 measured **776 / 776, agreeing, 0 failures, 10 `ok` packages** at `b139d35`. Re-measure
anyway; never inherit a count. Chaining these into one call has produced a *plausible* wrong number
here before (298 where the truth was 769). If the two disagree, `awk` is right.

---

## 9. The claims this change falsifies

Enumerated by the claim's **MEANING**, not one phrasing, and searched across the repo root, `cmd/`,
`deploy/`, `examples/`, `web/src/` and `docs/` — not only `docs/`. On #76 a single false sentence
reached six files, four of them outside a `docs/` glob.

Gate 1 independently re-ran this enumeration: every anchor below resolves within ±2 lines,
`cmd/main.go`'s flag help (`:90`, `:111`) makes no claim this change falsifies, and `deploy/`,
`examples/` and `web/src/` are clean.

### Must be rewritten — #77 ("the DVD path is unbounded / uncovered")

- **`docs/CONFIGURATION.md:469-471`** — *"Debian **DVD** targets are the one exception — they are
  dispatched before this loop and the guard never runs for them, a pre-existing gap tracked as
  #77."* Becomes: DVD targets are bounded by the same window through a separate mechanism.
- **`pkg/cache/reconcile.go:188-190`** — *"It does NOT cover Debian DVD targets, which return above
  this loop — that hazard is pre-existing and tracked separately (#77), not fixed here."*
- **`pkg/cache/reconcile.go:18-29`** — `verifyRetryAfter`'s doc comment now has a second consumer and
  must say so, so the DVD reader finds it.
- **`pkg/cache/debiandvd.go:303-308`** — `removeUnverifiedISOs`' doc: *"so the next reconcile tick
  re-downloads clean"* → *subject to the retry bound*.
- **`pkg/cache/debiandvd.go:432-438`** — the inline comment: *"so the next tick re-downloads them
  clean and can self-heal once mirrors converge."*
- **`pkg/cache/debiandvd.go:373-391`** — `ensureDebianDVD`'s doc comment describes what gates the
  heavy work; the guard joins that description.
- **`pkg/cache/debiandvd_test.go:356-360`, `:379`** — test comments asserting the unbounded
  re-download is the intended behaviour.

### Must be rewritten — #83 ("absent material is a failure verdict"; "in-flight is a no-verdict")

- **`pkg/cache/verify.go:388-392`** — *"A verifiable artifact whose final file is absent is a failure
  (`artifact absent`) UNLESS a sibling in-progress file exists … then the whole version records
  NULL."* Both halves are falsified.
- **`pkg/cache/verify.go:435-445`** — the inline comment and both constructions.
- **`pkg/cache/isodownload.go:24-29`** — `DownloadSuffix`'s "four consumers" doc comment names
  *"pkg/cache/verify.go both writes it … and reads it (VerifyVersion's in-flight check)"*. D11
  deletes that reader, leaving three. **Easy to miss: it is in a different file from the change.**
- **`pkg/http/api_cache.go:171-187`** — hazard **#2** is closed; hazard **#1** survives only in its
  superseded/no-material form and is restated per D14.
- **`pkg/cache/verify_test.go:618`** — the same claim in a test comment (plus the two tests §8 names).
- **`docs/schema/API.md:463`** — the reverify row enumerates its outcomes. Add: a version whose
  material is not on disk leaves the recorded failure unchanged and withdraws a standing
  affirmation.
- **`docs/CONFIGURATION.md:456-461`** — the non-retroactive-policy paragraph describes reverify as
  *re-recording* `verified=0` (`:459-460`); qualify for the no-material case.

### Already inconsistent, and NOT made consistent by this change — do not claim otherwise

- **`docs/schema/DATABASE.md:109`** enumerates `verify_err`'s failure-class texts as `checksum
  mismatch` / `signature mismatch` / `unknown or expired signing key`. **The first draft claimed this
  doc "has been describing the post-fix behaviour all along" and told the executor to say so in the
  commit. That was false.** Four other reachable messages are also missing from the list —
  `"checksum unavailable"` (`verify.go:69`), `"signature material unavailable"` (`:273`),
  `"open for verify"` (`:277`), `"keyring parse"` (`:289`) — all produced by `verifyArtifact`, which
  `VerifyVersion` calls at `:448`. An unfetchable Flatcar `.sig` on reverify writes
  `"signature material unavailable"` into `verify_err` today **and after this change**.
  Removing `artifact absent` narrows the gap; it does not close it. **Do not put the "already
  consistent" claim in a commit message.** The remaining incompleteness is pre-existing and out of
  scope — residual 8.

### Unchanged and correct

- **`docs/schema/STORAGE.md:164`** and **`docs/schema/DATABASE.md:111`** describe the
  failure-visibility row. D7 adds no DVD row.
- **`docs/testing/debian-dvd-install-gate.md:182-198`** enumerates the version dir, but only on the
  success path, where the marker is cleared. Not falsified. (Its own in-tree citations are already
  stale — `debiandvd.go:60` vs the actual `:71` — which is pre-existing drift, not this change's.)

### Historical designs — annotate, do not rewrite

Add a dated pointer to this design; leave the original text intact.

- `docs/designs/2026-08-02-tails-sha256-verification-design.md` — §7.3, `:678`, `:778-779`, `:909`
  (the #77 gap) and `:566` (the absent/`.partial` split D11 deletes).
- `docs/designs/2026-08-20-verdict-erasure-design.md` — `:265` (#77 structurally out of scope) and
  `:398` (#83's degradation described).
- `docs/designs/2026-07-01-p3b-signature-verification-design.md` — `:391`, `:483` (the original
  `artifact absent` verdict).

`docs/plans/`, `docs/prompts/` and `docs/reports/` are gitignored (`.gitignore:10-12`, 0 tracked
files in each) and `.superpowers/` is a historical ledger; neither is in scope.

---

## 10. Residuals — knowingly left unfixed

1. **A Debian DVD verification failure is invisible in the Cache view** (D7). The operator sees a
   `slog.Warn` per guarded tick and nothing in the UI. Unchanged from today. Fixing it needs a
   `pkg/db` writer that refuses to touch an existing cached row, for D2's four reasons.
2. **A nil verdict from a superseded tool tag, or from a version declaring no material, still writes
   `verified=NULL` AND `verify_err=''`** — releasing the retry guard *and* destroying a recorded
   reason (`pkg/db/cache.go:74-76`). D11 closes the in-flight route into this, which was the
   reachable one; what remains is settled per D14. The first draft described this residual as only
   releasing the guard, which understated it.
3. **`verifyArtifact`'s fail-closed classification is untouched.** `verifyDetachedGPG` still launders
   three infrastructure faults into verdicts one frame lower (`verify.go:273`, `:277`, `:298`).
   P3b-settled, and #84's precedent is to mitigate at the disposition level, which is what §7 does.
4. **The marker's mtime is trusted without measurement** on the deployment filesystem (§2). A
   backwards clock jump wedges the target for the jump's duration; every other degenerate case fails
   open.
5. **Warn-landed verification failures are still never retried**, for any OS. Pre-existing, named in
   the tails design, no issue filed.
6. **A DVD set that fails verification for a reason a retry cannot fix** (a genuinely divergent
   archive) still re-downloads once per hour, forever. Bounded, not eliminated. Eliminating it needs
   a failure *count*, which needs durable state D7 declined to add.
7. **A persistent EXTRACTION failure is an unbounded loop this design does NOT bound.** `isoExtract`
   failing at `debiandvd.go:442` returns before the sentinel and **without** removing the ISOs, so
   the next tick skips every download as present (`:418`), re-hashes the whole set, and fails again —
   D3's own condemned arithmetic, ~19 GB of hashing per tick, forever. It is **disk-only, not
   network**, which is why #77's title claim ("bound the re-download") is still met. It is not
   marked because an extract failure is closer to "could not evaluate" (disk full, a transient I/O
   error) than to a verdict, and D4 says such failures must retry. Bounding it needs a different
   signal — an attempt count, or a distinct extraction marker — and that is a separate change.
   **Found by Gate 1, stated here rather than fixed.**
8. **`docs/schema/DATABASE.md:109`'s failure-class list stays incomplete** in four other ways after
   this change (§9). Pre-existing; correcting it is a doc change with its own enumeration.
9. **The DVD marker is armed by infrastructure faults, not only by verdicts.** `debiandvd.go` writes
   it on *any* non-nil `isoVerify` error, and `verifyDVDChecksums` returns a bare error for several
   "could not evaluate" faults as well as for mismatches: a `hashFile` read error part-way through a
   multi-GB ISO, an unreadable `SHA256SUMS`, `verifyDetachedGPGLocal` failing to open the signature.
   That is the same laundering D4b forbids on the land path, one frame lower, and residual 3 does not
   cover it — residual 3 names `verifyArtifact`/`verifyDetachedGPG` on the generic path, not
   `verifyDVDChecksums`. Accepted rather than narrowed: `removeUnverifiedISOs` already deletes the
   set on those same faults today, so this change's only marginal effect is the one-hour delay before
   the retry, which on a flaky filesystem is arguably the kinder outcome. Narrowing it needs the
   error classification residual 3 declines to build. **Found by the final whole-branch review.**

---

## 11. Execution shape

Two tasks, no shared code, either order:

| Task | Files | Depends on |
|---|---|---|
| **A — #77 DVD retry bound** | `pkg/cache/debiandvd.go` (+`time` import), `pkg/cache/debiandvd_test.go`, `pkg/cache/reconcile.go` (comments only), `docs/CONFIGURATION.md` (`:469-471`), `docs/designs/2026-08-02-…` + `2026-08-20-…` (annotations) | — |
| **B — #83 reverify verdict fidelity** | `pkg/cache/verify.go`, `pkg/cache/verify_test.go`, `pkg/cache/isodownload.go` (comment only), `pkg/http/api_cache.go` (+`log/slog` import), `pkg/http/api_cache_test.go`, `docs/schema/API.md`, `docs/CONFIGURATION.md` (`:456-461`), `docs/designs/2026-07-01-…` + `2026-08-02-…` (annotations) | — |

Both touch `docs/CONFIGURATION.md` in **different sections** and both annotate
`docs/designs/2026-08-02-tails-sha256-verification-design.md`; sequence them or expect a trivial
conflict.

Branch: `worktree-dvd-retry-verdict-fidelity`, cut from `main` @ `69f39db`, worked at the **repo
root** — `docs/plans/` is gitignored, so a linked worktree would not contain the plan at plan time.
PR against `--repo jacaudi/booty --base main`, merged with `--merge`.
