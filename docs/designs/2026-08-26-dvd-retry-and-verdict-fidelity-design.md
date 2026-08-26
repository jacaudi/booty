# Design: bound the Debian DVD re-download, and stop reverify degrading a rejection reason

**Issues:** [jacaudi/booty#77](https://github.com/jacaudi/booty/issues/77) ·
[jacaudi/booty#83](https://github.com/jacaudi/booty/issues/83)
**Status:** design — Gate 1 pending
**Base:** `main` @ `69f39db` (the #84 merge). 776 tests, 0 failures.
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
guard stays armed — but the only durable record of *why* a multi-GB image was refused is destroyed,
and it is replaced with a claim about a **different fault with a different remedy**.

Both are pre-existing and both were deliberately scoped out of #84.

---

## 2. Evidence

### Verified — read directly from the code on 2026-08-26, at `69f39db`

- **`ensureDebianDVD` records NOTHING persistent on a verification failure.** `isoVerify` fails at
  `debiandvd.go:431` → `removeUnverifiedISOs` (`:439`) → `return err` → `reconcile.go:114` logs a
  `slog.Warn` → `return nil`. Every DB write in the function — `UpsertTargetVersion`,
  `UpsertCacheEntry`, `SetCachePinnedByTargetVersion`, `SetTargetSourceMode` — sits **below** that
  return, at `:455–468`. This is the crux of #77 and §3 states its consequence.
- **The DVD state machine's only persistent signals are filesystem ones**: per-ISO presence (the
  skip at `:418`) and `.booty-dvd-complete` (the heavy-work gate at `:411`, `dvdSentinelPresent` at
  `:74`). The failure path restores both to their pre-attempt state.
- **`debianDVDVersion` calls the same `o.DiscoverVersions` the netinst path calls.**
  `debiandvd.go:246-255` vs `reconcile.go:135`. The DVD version is `versions[0]` from the identical
  namespace the netinst path caches from. §4.2 turns this into a rejection.
- **`UpsertTargetVersion` does `source = excluded.source, cached = excluded.cached`**
  (`pkg/db/versions.go:20-22`), so writing a row with `Cached:false` resets a live `cached=1` row.
- **`VerifyVersion` has exactly one production caller**, `pkg/http/api_cache.go:167`. (`git grep`
  over `*.go`, excluding `.claude/worktrees/`.) The #83 blast radius is one call site plus tests.
- **`VerifyVersion`'s absent branch is only reachable for an artifact that declares material** —
  `verify.go:426` short-circuits `a.SHA256 == "" && a.SigURL == ""` to `classNotVerifiable` before
  the `os.Stat`. §6.2 turns this into the decisive argument.
- **`ListCached` (`pkg/cache/list.go:66-76`) keys on version *directories*, ignoring contents.** A
  new marker file adds no phantom listing; the empty dir already appears there after a failure today
  (`ensureDebianDVD` `MkdirAll`s at `:412`).
- **`Scan` (`pkg/cache/scan.go:42-45`) only walks `cached=1` rows.** A failed DVD attempt has no such
  row, so the marker is invisible to it — except in the shared-dir case of §5.4.
- **Reverify is a BULK action in the UI** — `web/src/views/CacheView.tsx:330` maps it over every
  selected row. §7.2 turns this into a rejection of the HTTP-409 shape.
- **`docs/schema/DATABASE.md:109` enumerates `verify_err`'s failure-class texts** as
  `checksum mismatch` / `signature mismatch` / `unknown or expired signing key`. `"artifact absent"`
  is **not** among them — the schema doc is already inconsistent with today's code, and §8 records
  that this design makes it true rather than changing it.

### Assumed — stated, not proven

- **Marker mtime is trustworthy on the deployment filesystem.** booty runs on the user's NAS. A
  coarse or non-monotonic mtime (NFS/SMB) could skew the window by seconds-to-minutes against an
  hour-scale bound, which degrades gracefully. Not measured. A clock jump **backwards** lengthens
  the bound (fails safe); a jump **forwards** shortens it, at worst restoring today's behaviour.
- **A Debian DVD set's ISO names and the `SHA256SUMS` they are checked against are stable within a
  point release.** Inherited from the existing DVD design, not re-established here.

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

### #83 — `VerifyVersion` calls a missing file a corruption verdict

`verify.go:445` builds `artifactVerdict{class: classCorruption, err: "%s: artifact absent"}`.
`aggregateVerdicts` folds `classCorruption` into `verified=false` with that message, and the handler
writes it unconditionally at `api_cache.go:185`.

But an absent file is not a statement about the bytes' integrity. It is the version-level twin of
the rule **D4b** already established one frame lower on the land path:

> *"I could not evaluate the material" is infrastructure and must be retried; "the material does not
> match" is a verdict.*

D4b routes a `hashFile` **read** failure out of `landArtifact` as an error rather than a corruption
verdict, precisely so a transient blip cannot destroy a completed multi-GB download. #84 applied the
same rule at the version scope, choosing to fix it at the **disposition** level (`ctx.Err()` →
defer) rather than by reclassifying inside `verifyArtifact`. This design applies it at the
**reverify** scope, the same way.

---

## 4. Decisions — #77

### D1 — the retry bound is a marker file in the version dir, not a `cache_entries` row

`.booty-dvd-verify-failed`, written into the version dir alongside the existing
`.booty-dvd-complete` sentinel, containing the verification error text.

| The version loop's signal | The DVD path's replacement |
|---|---|
| a `cache_entries` row exists for `(target, version)` | the marker file exists in the version dir |
| `size=0 ∧ in_window=0 ∧ verified=0 ∧ verify_err<>''` — the only combination `UpsertCacheEntryArchived` produces | the marker's **existence**: it has exactly one writer (the `isoVerify`-failure branch) and no other state in the system can forge it |
| `fetched_at > datetime('now', '-N seconds')` | `time.Since(fi.ModTime()) < verifyRetryAfter` |
| the row's `verify_err`, logged as `guardReason` | the marker's contents |

**This is the "adapt, not copy" the issue asks for.** The four-column predicate exists because
`cache_entries` has *many* writers and the guard must not be forged by a row some other path
produced (`pkg/db/cache.go:115-122` walks that reasoning). A marker written by one branch and read
by one branch has no such problem, so the predicate's complexity has no counterpart to translate —
its *purpose* is met by the marker's exclusivity.

Two mechanical consequences worth stating, because they are the reason this is not merely "the DB
version, on disk":

- **`os.Stat` returns a real `time.Time`.** Both recorded traps are structurally absent rather than
  dodged: SQLite's `datetime('now','-1h0m0s')` → `NULL` no-op (`pkg/db/cache.go:135-145`), and the
  `fetched_at` UTC-`TEXT` parse that yields a future timestamp and wedges the version for hours.
- **`os.Chtimes` can backdate the marker in a test.** `UpsertCacheEntryArchived` hardcodes
  `datetime('now')` and `Store.db` is unexported, which is exactly what defeated the equivalent test
  on #84 and forced it onto a sentinel `verify_err` instead.

### D2 — the `cache_entries` row alternative is REJECTED on evidence, not on taste

Writing the guard into the DB requires a `target_versions` row (for the `tvID`), then
`UpsertCacheEntryArchived`. On the mainline promote path that clobbers a live row:

A discovery-mode Debian target that has cached netinst versions is promoted to `desired_mode=dvd`.
`existingDVDVersion` finds nothing (the netinst row is `source="discovered"`, not `"manual"`), so
`debianDVDVersion` resolves the version — **via the same `o.DiscoverVersions` the netinst path uses**
— and returns `versions[0]`, the newest discovered version, i.e. the one most likely already cached.
The code already knows these collide: `removeStaleNetinstArtifacts` (`debiandvd.go:294`) exists for
exactly the same-dir case, and is only reachable when the version strings match.

Then, on a verify failure:

- `UpsertTargetVersion({Source:"manual", Cached:false})` flips that live row from
  `discovered`/`cached=1` to `manual`/`cached=0`, and
- `UpsertCacheEntryArchived` zeroes its `size` and sets `in_window=0`,

while the netinst files are still on disk and `source_mode` is still `netinst` — i.e. while the
target is still *serving* them. The fix would break serving on a promote that has not yet succeeded.

Guarding the write ("only if no cached row exists for this version") does not rescue it: it leaves
the loop unbounded in precisely the colliding case, which is the mainline promote and the case with
the most disk at stake. A half-fix in the expensive half.

### D3 — `removeUnverifiedISOs` STAYS. The deletion is correct; the retry is what needed bounding

`removeUnverifiedISOs`' own comment (`debiandvd.go:303-308`, `:432-438`) is right and this design
does not reverse it. Keeping the bad ISOs would make the skip-if-present path at `:418` re-verify
the same known-bad bytes forever: on a 4-disc set that is roughly 19 GB of hashing every tick (the
measured rate is 1.94 GB in 3.55 s on SSD, disk-bound), and it **never self-heals** when mirrors
converge, because the bad bytes are never re-fetched.

Delete + bounded retry is the only shape that both stops the bleeding and still recovers. The
trade-off is explicit: each retry after the window costs a full re-download, and the bound is what
makes that acceptable.

### D4 — only a VERIFICATION failure writes the marker; a DOWNLOAD failure never does

This is D4b again, in the DVD state machine. `isoDownload` failing at `:421`/`:425`/`:428` is
"could not evaluate": `downloadLargeFile` leaves resumable `<iso>.download` bytes
(`isodownload.go:111-118`), and the next tick resumes via `Range`. Marking that would throw away
resumable progress for an hour on an ordinary network blip.

Only `isoVerify` returning non-nil (`:431`) writes the marker — the one outcome that is knowledge a
retry within the window will not change.

### D5 — `verifyRetryAfter` is REUSED, not copied

`pkg/cache/reconcile.go:29`. "How long a verification rejection suppresses a re-download" is one
piece of knowledge with one change-driver; both consumers are `package cache`. This is DRY's
extraction criterion met exactly, and it is a *reuse*, not a new abstraction.

It stays a **package var** — not a viper key, not a CLI flag. That decision is settled by #76 and its
rationale (`reconcile.go:24-28`) is unchanged by adding a second in-package consumer.

### D6 — a guarded tick returns `nil`, and logs the reason

Being guarded is not an error: the function's contract is "bring this target to `source_mode=dvd`",
and a deliberate deferral is not a failure to do so. Returning an error would make
`reconcile.go:114` log `"debian dvd ensure failed"` for a healthy rate-limit, which is a lie.

`ensureDebianDVD` logs its own `slog.Warn` naming the version and the marker's recorded reason —
mirroring the version loop's `reconcile.go:211-213` — and returns `nil`.

### D7 — a DVD verification failure stays invisible in the Cache view

**Operator's explicit choice this session.** Today a DVD verify failure produces a `slog.Warn` and
nothing else; that is unchanged. #77 asks for a bound, not for visibility, and the safe way to add a
failure-visibility row is a *different* change that must first answer D2's clobber — most likely a
new `pkg/db` writer that refuses to touch an existing cached row. Recorded as a residual in §9, not
smuggled in here.

### D8 — no schema change, no migration, no API surface change

Stated loudly because the handoff asks: **neither half of this design touches the schema.** #77 is a
filesystem marker; #83 changes only which Go value a function returns and whether one `UPDATE` runs.

"No API surface change" means no route, request shape, response shape, or status code moves — §9's
edit to `docs/schema/API.md` documents a case the endpoint's existing 200 already covers.

The one shape that *would* have needed new `pkg/db` surface is the visibility row D7 sets aside —
and even that needs no migration, only a new writer. If a future change adds it, that is a bigger
decision than #77 implies, for the reason D2 gives.

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
// Its contents are the verification error, so a guarded tick can log WHY
// without a second lookup.
const dvdVerifyFailedName = ".booty-dvd-verify-failed"
```

### 5.2 Write, read, clear

```go
// markDVDVerifyFailed records reason as the DVD verify-retry bound for dir.
// Best-effort: a write failure is logged and non-fatal — the caller returns the
// verification error regardless, and an unwritten marker degrades to today's
// unbounded retry rather than to anything unsafe.
func markDVDVerifyFailed(dir, reason string) { … }

// dvdVerifyGuarded reports whether dir's DVD set was refused by verification
// less than verifyRetryAfter ago, and returns the recorded reason for the log.
// A missing marker, an unreadable one, or an elapsed window all release the
// guard: every degenerate case fails OPEN, which is today's behaviour.
func dvdVerifyGuarded(dir string) (reason string, guarded bool) { … }

// clearDVDVerifyFailed removes the marker after a successful verification.
func clearDVDVerifyFailed(dir string) { … }
```

### 5.3 The wiring in `ensureDebianDVD`

Inside the existing `if !dvdSentinelPresent(dir)` block, ahead of `os.MkdirAll` — so a guarded tick
does **no network at all**, matching the version loop's reason for placing its guard before
`o.Artifacts` (`reconcile.go:182-185`):

```go
if !dvdSentinelPresent(dir) { // heavy work only when the tree is not yet settled
	if reason, guarded := dvdVerifyGuarded(dir); guarded {
		slog.Warn("cache: debian dvd set rejected by verification; not retrying yet",
			"target", t.ID, "arch", t.Arch, "version", version,
			"verifyErr", reason, "retryAfter", verifyRetryAfter)
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
		markDVDVerifyFailed(dir, err.Error())
		return err
	}
	clearDVDVerifyFailed(dir)
```

Returning `nil` early skips the accounting/pin/flip block below, which is correct: the tree is not
settled, so there is nothing to account for.

### 5.4 Interactions

- **`dirSize` and `Scan` count the marker.** It is a regular file of ~100 bytes in the version dir.
  On the DVD success path it is removed before any accounting runs. In the shared-dir case (a DVD
  version equal to a netinst-cached version — §4.2), `Scan` would add those bytes to the netinst
  row's size while the marker exists. Stated rather than defended away: it is noise against a
  multi-GB budget, and it is the price of keeping the marker inside the dir it describes, where
  `removeVersionDir` collects it.
- **`ListCached`** is unaffected — it lists version *directories* regardless of contents, and the
  dir already exists after a failure today.
- **Eviction cannot release the guard.** `ListArchivedUnpinned` requires `size > 0` and a
  `cache_entries` row; a failed DVD attempt has neither.
- **`ensureDebianDVD`'s own tail** (`:477-484`) removes *other* versions' dirs and only runs on the
  success path, so it can never delete the failing version's marker.
- **`fullyDVDSettled` / `dvdSentinelPresent`** are untouched; the guard is strictly inside the
  not-settled branch.

### 5.5 Next tick, per branch

| Prior tick ended in | Marker | Next tick |
|---|---|---|
| verify failure, < 1 h ago | present, fresh | guarded: no network, `Warn` with the recorded reason, `return nil` |
| verify failure, > 1 h ago | present, stale | full re-download from zero, re-verify — self-heals if the mirror converged |
| download failure | absent | resumes from `<iso>.download` via `Range`, as today |
| verify success | cleared | extract + merge + accounting, as today |

---

## 6. Decisions — #83

### D9 — the fix is in `VerifyVersion`'s VOCABULARY; the call-site rule loses

**Chosen:** absence of the material is not a verification verdict. `VerifyVersion` stops
constructing `classCorruption{"artifact absent"}` and gains a third outcome, *unevaluable*, which
the handler declines to record.

**The alternative — "decline to overwrite a non-empty `verify_err` when the new reason is
`artifact absent` and the row is already `size=0`" — loses on three counts:**

1. **`VerifyVersion` keeps producing a false verdict.** The special case suppresses one consumer's
   damage; the bug stays in the producer, and the next consumer inherits it. Today there is exactly
   one caller, which makes the narrow rule *sufficient* — and makes it a trap the moment there are
   two.
2. **It duplicates knowledge that `pkg/db/cache.go` reasons carefully about.** "What a rejection row
   looks like" would then be represented in SQL (`VerifyRejectedWithin`) *and* as a Go condition in
   `pkg/http`. Those must change together to stay correct, which is DRY's extraction criterion met —
   and they cannot be single-sourced across the SQL/Go boundary.
3. **It states the rule in terms of the damage rather than the fault.** "Don't overwrite when the row
   is already size=0" describes a symptom; "absence is not a verdict" describes the fault, and is the
   rule D4b already established.

### D10 — the objection to D9 dissolves on tracing, so it is a recommendation and not a coin-flip

The honest cost of D9 *appears* to be: an artifact deleted out-of-band from a version claiming
`cached=1`/`size>0` is recorded today as `verified=false, "artifact absent"`, and would record
nothing under D9.

That signal is **transient and self-healing**, not durable:

- The absent branch is only reachable for an artifact that **declares** material (`verify.go:426`
  short-circuits the rest to `classNotVerifiable`), so the version is verifiable by construction.
- The next reconcile tick fails `finalFilesPresent(dir, arts)` (`reconcile.go:244`), runs the full
  land path, and a successful re-land writes `SetCacheVerified(&true, "")` (`reconcile.go:364-367`),
  clearing the column.

So the **only** case in which `"artifact absent"` persists in `verify_err` is exactly #83's — where
it is wrong. D9 discards a self-healing signal and preserves a durable, correct one.

### D11 — a mixed version records the real failure; absence never affirms

`VerifyVersion` collects verdicts for present artifacts and counts absent ones. Then:

| present artifacts | absent artifacts | result |
|---|---|---|
| at least one **failed** (corruption/forgery) | any | record that failure — **strictly better than today**, which joins `"artifact absent"` into the message |
| all passed, or none verifiable | ≥ 1 | **unevaluable** — record nothing |
| any | 0 | unchanged from today |

The middle row is load-bearing: a version with missing declared material must never be affirmed
`verified=true`. Short-circuiting on the first absent artifact would be simpler and **wrong** — it
would discard a real mismatch found on a sibling, depending on iteration order.

### D12 — the outcome is carried as a sentinel error, wrapped with the artifact name

```go
// ErrVersionUnevaluable reports that a version's DECLARED material is not on
// disk to examine, so there is no verdict to compute. It is NOT a verification
// failure: the caller must not record a verdict, because the row it would
// overwrite may hold the real reason the bytes are gone (a rejection under D4a
// removes them by design — jacaudi/booty#83).
//
// The error channel is deliberate rather than a new return type: in this
// package "could not evaluate" IS the error channel — landArtifact uses it for
// exactly this distinction (D4b) — so a sentinel keeps one vocabulary instead
// of introducing a second.
var ErrVersionUnevaluable = errors.New("cache: no material on disk to verify")
```

Returned as `fmt.Errorf("%s: %w", a.Filename, ErrVersionUnevaluable)` so the log names the file.
`VerifyVersion`'s signature `(*bool, string, error)` is unchanged; the handler discriminates with
`errors.Is`.

### D13 — the handler answers 200 with the row unchanged, NOT 409

**Operator's explicit choice this session, and it is forced by the UI.** `CacheView.tsx:330` runs
reverify as a **bulk** action over every selected row. A 409 per already-rejected row would report
mass failure for what is a correct no-op, and `CacheView.test.tsx:263-297` shows the bulk path
surfaces a rejected promise as an error.

So: log a `slog.Warn`, skip `SetCacheVerified`, reload and return the row exactly as the success path
does. The operator sees their original `"checksum mismatch"` preserved, which is the outcome #83
asks for. No API-surface change, no DTO change, no UI change.

### D14 — the NIL-VERDICT hazard is deliberately NOT fixed here

`api_cache.go:171-187` documents two hazards. This design closes **#2** (reason degraded) and leaves
**#1** untouched: a nil verdict — a superseded tool tag, a tool declaring no material, a re-download
in flight — still writes `verified=NULL, verify_err=''`, which releases the retry guard early.

Three reasons, and they are reasons rather than an omission:

1. It is a **settled, documented** disposition ("defensible: an operator explicitly asked").
2. `SetCacheVerified`'s nil-clearing capability exists for a real case its doc comment names — a
   version whose artifact list stopped declaring material needs its stale verdict cleared — and this
   handler is its **only** nil-writer (`reconcile.go:364` guards `verified != nil`). Folding hazard
   #1 in would make that capability dead code.
3. It is a third behaviour change in a change that already has two, and #83 does not ask for it.

Recorded as a residual in §9.

---

## 7. The change — #83

### 7.1 `VerifyVersion`

The `os.Stat` failure branch (`verify.go:434-447`) stops appending a verdict and instead collects the
absent filenames (`var absent []string`, declared beside `verdicts` at `:424`); the in-flight
`.partial`/`DownloadSuffix` check above it is **unchanged**:

```go
		if _, serr := os.Stat(final); serr != nil {
			for _, suffix := range []string{".partial", DownloadSuffix} {
				if _, perr := os.Stat(final + suffix); perr == nil {
					return nil, "", nil // re-download in flight → no verdict
				}
			}
			// D9/#83: the material is NOT HERE TO EXAMINE, which is not a claim
			// about its integrity. Recording it as corruption overwrites the
			// real reason a rejected version's bytes are gone (D4a removed them
			// by design) with a different fault that has a different remedy.
			// Counted, not verdicted — see the disposition below (D11).
			absent = append(absent, a.Filename)
			continue
		}
```

and the tail:

```go
	verified, verifyErr := aggregateVerdicts(verdicts)
	// D11: a real failure on a present sibling is durable knowledge and is
	// recorded. Absence only suppresses recording when there is nothing better
	// to record — and it must NEVER let a version be affirmed while declared
	// material is missing.
	if len(absent) > 0 && (verified == nil || *verified) {
		return nil, "", fmt.Errorf("%s: %w", strings.Join(absent, ", "), ErrVersionUnevaluable)
	}
	return verified, verifyErr, nil
```

### 7.2 The reverify handler

```go
		verified, verifyErr, verr := cache.VerifyVersion(ctx, deps.Store, n)
		switch {
		case errors.Is(verr, cache.ErrVersionUnevaluable):
			// #83: there are no bytes to examine, so there is no verdict — and
			// recording one would overwrite the reason they are gone. Leave the
			// row untouched and answer with it. NOT an HTTP error: the Cache
			// view runs reverify as a BULK action, so a 4xx here would report
			// mass failure for a correct no-op.
			slog.Warn("cache: reverify found no material to examine; verdict left unchanged",
				"id", n, "os", row.OS, "version", row.Version, "err", verr)
		case verr != nil:
			return nil, huma.Error500InternalServerError("verify", verr)
		default:
			if err := deps.Store.SetCacheVerified(row.TargetVersionID, verified, verifyErr); err != nil {
				return nil, huma.Error500InternalServerError("record verdict", err)
			}
		}
```

The existing comment block above the call is rewritten: hazard #2 is closed, hazard #1 stays and is
restated as the residual D14 makes it.

---

## 8. Tests

Both halves are TDD. Every test carries a **mutation check**: revert the named production line and
the test must go RED on its named assertion. A mutation that does not turn its test red is a HALT
and a report, never a weakened mutation. (This project has shipped inert tests three times — the
slice-2 feature-disabled test, T4's un-wired guard, and the D4b headline test that passed in its red
phase because the code it replaced also failed closed. A green run is not evidence.)

### #77 — `pkg/cache/debiandvd_test.go` (`package cache`)

1. **`TestEnsureDebianDVD_VerifyFailureBoundsTheRetry`** — `swapDVDSeams` with a download counter and
   an always-failing verify. Run `ensureDebianDVD` twice. Assert the download count is **unchanged**
   by the second call, and that the marker exists.
   *Mutation:* delete the `dvdVerifyGuarded` block → the count doubles.
2. **`TestEnsureDebianDVD_VerifyRetryResumesAfterTheWindow`** — same, then `os.Chtimes` the marker to
   `verifyRetryAfter + time.Minute` ago. Assert the second call **does** re-download.
   *Mutation:* drop the `time.Since(...) < verifyRetryAfter` clause (make presence alone block) → the
   re-download never happens.
   *Guards the degenerate direction too:* with `verifyRetryAfter = 0` the guard must release.
3. **`TestEnsureDebianDVD_DownloadFailureDoesNotArmTheGuard`** — failing `isoDownload`, passing
   `isoVerify`. Assert **no marker** is written and the next call retries.
   *Mutation:* move `markDVDVerifyFailed` above the verify branch → the marker appears.
   This is D4's trap guard; without it, a network blip costs an hour.
4. **`TestEnsureDebianDVD_SuccessfulVerifyClearsTheMarker`** — fail once, backdate, succeed. Assert
   the marker is gone and the sentinel is present.
   *Mutation:* delete `clearDVDVerifyFailed` → the marker survives a success.

**Assert on the marker's presence and contents, never on its mtime** — the same discipline #84's
Gate 2 forced onto `fetched_at`.

The existing `TestEnsureDebianDVD_VerifyFailureClearsISOsForRefetch` (`:363`) must keep passing
**unchanged**: D3 does not alter the deletion.

### #83 — `pkg/cache/verify_test.go` and `pkg/http/api_cache_test.go`

5. **`TestVerifyVersionAbsentMaterialIsUnevaluableNotAVerdict`** — a version whose declared artifact
   is missing with no in-flight sibling. Assert `errors.Is(err, ErrVersionUnevaluable)` and that the
   error names the file.
   *Mutation:* restore the `classCorruption` construction → `err` is nil and `verified` is false.
6. **`TestVerifyVersionAbsentSiblingDoesNotMaskARealMismatch`** — one present artifact whose bytes
   mismatch, one absent. Assert a **recorded** `verified=false` whose `verify_err` names the
   *mismatch* and **not** the absent file. This pins D11's first row.
   *Mutation:* return `ErrVersionUnevaluable` whenever `len(absent) > 0` → the mismatch is lost.
7. **`TestVerifyVersionAbsentSiblingNeverAffirms`** — one present artifact that passes, one absent.
   Assert unevaluable, **not** `verified=true`. This pins D11's middle row, the load-bearing one.
   *Mutation:* drop `|| *verified` from the condition → the version is affirmed while material is
   missing.
8. **`TestReverifyLeavesAGuardedRejectionReasonIntact`** — the #83 scenario end-to-end through the
   HTTP endpoint: seed a rejected row via `UpsertCacheEntryArchived` with
   `verify_err = "<sentinel>: checksum mismatch"`, remove the dir, POST reverify. Assert **200**, and
   that `verify_err` still reads `checksum mismatch`.
   *Mutation:* make the handler call `SetCacheVerified` unconditionally → `verify_err` degrades.
   **Assert on `verify_err`, never on `fetched_at`** — one-second granularity cannot discriminate a
   same-second rewrite.
9. **`TestReverifyStillRecordsANilVerdict`** — the D14 trap guard: a superseded tool tag must still
   write `verified=NULL`, proving the change did not widen into hazard #1.
   *Mutation:* extend the unevaluable branch to cover `verified == nil` → this test goes red.

The existing `TestVerifyVersion_AbsentFinalWithPartialIsNull` (`:265`) and
`TestVerifyVersionTailsInFlightResumeIsNoVerdict` (`:621`) must keep passing **unchanged**: the
in-flight branch is untouched.

**Baseline:** measure it on the branch tip with a clean tree, as the **sole command** in its own tool
call, and cross-check with `awk`:

```
go test ./... -race -count=1 -v > /tmp/dvd-baseline.txt 2>&1
grep -c '^=== RUN' /tmp/dvd-baseline.txt
awk '/^=== RUN/{n++}END{print n+0}' /tmp/dvd-baseline.txt
```

Never inherit 776 or any other number; chaining these into one call has produced a *plausible* wrong
count here before. If the two disagree, `awk` is right.

---

## 9. The claims this change falsifies

Enumerated by the claim's **MEANING**, not one phrasing, and searched across the repo root, `cmd/`,
`deploy/`, `examples/`, `web/src/` and `docs/` — not only `docs/`. On #76 a single false sentence
reached six files, four of them outside a `docs/` glob.

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

### Must be rewritten — #83 ("artifact absent is a failure verdict")

- **`pkg/cache/verify.go:389-392`** — *"A verifiable artifact whose final file is absent is a failure
  (`artifact absent`)."*
- **`pkg/cache/verify.go:436-445`** — the inline comment and the construction itself.
- **`pkg/http/api_cache.go:171-187`** — hazard **#2** is closed; hazard **#1** stays and is restated
  per D14.
- **`pkg/cache/verify_test.go:618`** — the same claim in a test comment.
- **`docs/schema/API.md:463`** — the reverify row enumerates its outcomes (500 on transient upstream
  failure, clean no-verdict on a superseded tag). Add: a version with no material on disk leaves the
  recorded verdict unchanged.
- **`docs/CONFIGURATION.md:455-460`** — the non-retroactive-policy paragraph describes reverify as
  *re-recording* `verified=0`; qualify for the no-material case.

### Already consistent — do not "fix"

- **`docs/schema/DATABASE.md:109`** enumerates `verify_err`'s failure-class texts as `checksum
  mismatch` / `signature mismatch` / `unknown or expired signing key`. `"artifact absent"` was never
  in that list, so the schema doc has been describing the post-fix behaviour all along. This design
  makes it true rather than changing it. **Worth stating in the commit** — it is evidence, not a
  coincidence.
- **`docs/schema/STORAGE.md:164`** and **`docs/schema/DATABASE.md:111`** describe the
  failure-visibility row. Unchanged: D7 adds no DVD row.

### Historical designs — annotate, do not rewrite

Add a dated pointer to this design; leave the original text intact.

- `docs/designs/2026-08-02-tails-sha256-verification-design.md` — §7.3, `:678`, `:778-779`, `:909`
  (the #77 gap) and `:566` (the absent/`.partial` split).
- `docs/designs/2026-08-20-verdict-erasure-design.md` — `:265` (#77 structurally out of scope) and
  `:398` (#83's degradation described).
- `docs/designs/2026-07-01-p3b-signature-verification-design.md` — `:391`, `:483` (the original
  `artifact absent` verdict).

`docs/plans/` and `docs/prompts/` are gitignored and `.superpowers/` is a historical ledger; neither
is in scope.

---

## 10. Residuals — knowingly left unfixed

1. **A Debian DVD verification failure is invisible in the Cache view** (D7). The operator sees a
   `slog.Warn` per guarded tick and nothing in the UI. Unchanged from today. Fixing it needs a
   `pkg/db` writer that refuses to touch an existing cached row, for D2's reason.
2. **A nil verdict from reverify still releases the retry guard** (D14) — hazard #1 of
   `api_cache.go:171`. Settled and documented; deliberately not widened into.
3. **`verifyArtifact`'s fail-closed classification is untouched.** `verifyDetachedGPG` still launders
   three infrastructure faults into verdicts one frame lower (`verify.go:273`, `:277`, `:298`). P3b-
   settled, and #84's precedent is to mitigate at the disposition level, which is what §7 does.
4. **The marker's mtime is trusted without measurement** on the deployment filesystem (§2). Both
   degenerate directions degrade to at-worst today's behaviour.
5. **Warn-landed verification failures are still never retried**, for any OS. Pre-existing, named in
   the tails design, no issue filed.
6. **A DVD set that fails verification for a reason a retry cannot fix** (a genuinely divergent
   archive) still re-downloads once per hour, forever. Bounded, not eliminated. Eliminating it needs
   a failure *count*, which needs durable state D7 declined to add.

---

## 11. Execution shape

Two tasks, no shared code, either order:

| Task | Files | Depends on |
|---|---|---|
| **A — #77 DVD retry bound** | `pkg/cache/debiandvd.go`, `pkg/cache/debiandvd_test.go`, `pkg/cache/reconcile.go` (comments only), `docs/CONFIGURATION.md` | — |
| **B — #83 reverify verdict fidelity** | `pkg/cache/verify.go`, `pkg/cache/verify_test.go`, `pkg/http/api_cache.go`, `pkg/http/api_cache_test.go`, `docs/schema/API.md`, `docs/CONFIGURATION.md` | — |

Both touch `docs/CONFIGURATION.md` in **different sections** (`:469-471` vs `:455-460`); sequence
them or expect a trivial conflict.

Branch: `worktree-dvd-retry-verdict-fidelity`, cut from `main` @ `69f39db`, worked at the **repo
root** — `docs/plans/` is gitignored, so a linked worktree would not contain the plan at plan time.
PR against `--repo jacaudi/booty --base main`, merged with `--merge`.
