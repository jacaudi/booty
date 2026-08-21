# Design: a transport error must neither forge nor erase a verification verdict

**Issue:** none filed — this closes final-review finding **S1** from
[jacaudi/booty#76](https://github.com/jacaudi/booty/issues/76) (shipped as PR #82, merge `c7c30db`)
**Status:** design — Gate 1 pending
**Closes:** `docs/designs/2026-08-02-tails-sha256-verification-design.md` §7.3, last bullet — the
hole that design documented rather than fixed
**Extends:** `docs/designs/2026-07-01-p3b-signature-verification-design.md` (the `--signaturePolicy`
model), the tails design's **D4b** (whose rule this applies one scope level up)

---

## 1. Goal

`reconcileTarget` currently discards **every** verification verdict for a version whenever **any**
artifact in that version returns a transport error:

```go
if vg.Wait() != nil {
	continue // transport error → whole version retried next tick (nothing recorded)
}
```

— `pkg/cache/reconcile.go:233-235`.

A checksum rejection returns `landed=false` with **no error**. So a definitive rejection co-occurring
with a sibling's transport error is thrown away with it: no `cache_entries` row is written,
`UpsertCacheEntryArchived` never runs, the four-column signature
(`size=0 AND in_window=0 AND verified=0 AND verify_err<>''`) that `db.VerifyRejectedWithin` matches
never appears, and **the retry guard cannot arm**. The next tick re-downloads 1.94 GB from zero.
Persistently — every `--cacheInterval`, for as long as both conditions hold.

This is an **incomplete new mitigation, not a regression**: without #76 there is no guard at all. But
it is precisely the hazard §7.1 of that design exists to bound.

The goal is to decide what `landed=false` together with `err != nil` means for version-level
atomicity, such that a transport error can neither **forge** a verdict nor **erase** one.

---

## 2. Evidence: what is verified, and what is assumed

### Verified — measured or read directly from the code on 2026-08-20

| Claim | How |
|---|---|
| `landArtifact` returns `(false, v, nil)` from **exactly one** place: its `reject()` closure (`verify.go:170-177`) | Read every return in `verify.go:104-205` |
| **Every** other early return in `landArtifact` carries a non-nil error — mkdir (`:109`), the `Large` `SigURL` refusal (`:123`), `downloadLargeInto` (`:128`), `hashFile` (`:149`), `DownloadStaged` (`:156`), and `land()`'s rename (`:166`) | Same read |
| The errgroup has **no context** (`new(errgroup.Group)`, `reconcile.go:220`), so no goroutine is cancelled by a sibling's error; every artifact runs to completion regardless | Read; `errgroup.Group` without `WithContext` has no cancel |
| A transport error leaves `verdicts[i]` at the zero value `classUnset`, which `aggregateVerdicts` skips entirely — neither counted verifiable nor counted failed | `verify.go:349-365`: the switch has no `classUnset` arm |
| **A truncated download is a transport error, not a short file that would read as a mismatch.** Measured: a server declaring `Content-Length: 100`, writing 50 bytes and closing, yields `copied=50 err=unexpected EOF` from `io.Copy` | Ran it — a standalone Go program against a raw `net.Listen` server, 2026-08-20 |
| **No existing test asserts the erasure behaviour.** `verify_test.go:209-213` and `:822-824` only *describe* the `vg.Wait()` routing in comments | `grep` over `pkg/cache/*_test.go` |
| Tails registers 4 files (`vmlinuz`, `initrd.img`, `9990-misc-helpers.sh`, `tails-amd64.iso`), and `netbootxyz.go:388` sets `SHA256` on **any** file the sidecar lists — so more than one artifact in the version can carry a digest | `pkg/ostype/tools.go:92-108`, `pkg/ostype/netbootxyz.go:354-391` |
| `removeVersionDir` is `os.RemoveAll` over the whole version directory, in-progress files included | `pkg/cache/layout.go:87-91` |

### Assumed — stated, not proven

- **The co-occurrence is reachable in the field but has not been observed.** The hazard class is a
  transiently-404ing or connection-resetting allowlisted file while another artifact mismatches. It
  is not reproduced from production logs; it is derived from the code paths.
- Upstream mirrors return `Content-Length` on artifact GETs. If one ever served a close-delimited
  response with no length, a truncation would go undetected by `io.Copy` and *would* surface as a
  checksum mismatch — a transport fault wearing a verdict's clothes. See §8.

### Corrected from the handoff — do not repeat this claim

The tails design §7.3 cites `pkg/ostype/netbootxyz.go`'s memtest86plus note (endpoints.yml lists 7
files, the asset mirror serves 3) as live evidence that a listed file can 404. **That specific
instance is already mitigated**: the `files` allowlist filters unserved manifest entries out *before*
any fetch. The hazard class — any allowlisted file that 404s transiently — is real; that example is
not current evidence for it.

---

## 3. The root cause, stated precisely

The bug is **not** a missing channel. `landArtifact` already produces three mutually exclusive
outcomes, and they are already distinguishable per artifact:

| outcome | signature | meaning |
|---|---|---|
| **landed** | `landed == true` | bytes sit at the final path — verdict `classPass`, `classNotVerifiable`, or `classCorruption` warn-landed |
| **rejected** | `landed == false, err == nil` | a verdict refused the bytes |
| **errored** | `err != nil` | the artifact could not be evaluated |

What `reconcileTarget` does is collapse per-artifact attribution into one scalar — `errgroup`'s
first non-nil error — and then key disposition off `landedFlags`, where **a rejection and a transport
error both read as `false`**. The information needed to tell them apart is produced, then discarded
two lines later.

This is the tails design's **D4b** rule — *"I could not evaluate the material" is infrastructure and
must be retried; "the material does not match" is a verdict* — stated and enforced at the artifact
level, and left unenforced at the version level.

---

## 4. Decisions

### D-A — the disposition is three-way, and a rejection wins over a co-occurring transport error

**Decided.** Per artifact, classify into landed / rejected / errored. Then:

```
any rejected  ->  REJECT   removeVersionDir, UpsertCacheEntryArchived, guard arms
else errored  ->  DEFER    continue — no row, no wipe, in-progress bytes survive (unchanged)
else          ->  CACHE    mark cached, record size + verdict (unchanged)
```

**Why rejection wins.** A rejection is knowledge a retry will not change; a transport error is
knowledge-free. Recording the rejection is what arms the guard, which is the entire point of §7 of
the tails design. Deferring instead costs a full 1.94 GB re-pull every interval for as long as the
transport fault persists.

**What it costs, stated rather than argued away.** `removeVersionDir` now also fires when a sibling
errored, so a mid-flight resumable `.download` can be destroyed by a *different* artifact's checksum
mismatch. This is not a new class of loss — today, with no transport error, a small sibling's
mismatch already wipes the whole version directory including a fully landed 1.94 GB ISO, because
version-level atomicity (§6 of the P3b design) is deliberate. What is new is only that the wipe also
fires when a sibling errored. Against that sits the unbounded re-download it replaces, and the guard
is self-clearing after one hour on versions that live for weeks.

### D-B — the discriminator is `!landed && err == nil`, NOT the verdict class

**Decided, and this is the trap.** A non-`Large` artifact failing its checksum under `warn` **lands**
— carrying `classCorruption` (`verify.go:198-199`). Keying rejection off "any failure class" would
turn every warn-landed corruption into a version rejection, silently deleting the availability
trade-off `warn` exists to provide.

`!landed && err == nil` is exactly "a verdict was reached and it refused the bytes", and §2 verifies
`reject()` is its only producer today.

**The residual is a cross-file invariant.** The predicate lives in `reconcile.go` and depends on
`landArtifact`'s return discipline in `verify.go`. If a future path ever returns `(false, v, nil)`
for a non-verdict reason, it silently routes to REJECT and forges the four-column signature. Two
mitigations, both cheap: `landArtifact`'s doc comment already states the contract
(*"err != nil is a transport/IO failure (nothing landed; retry next tick)"*), and §6's test 2 pins
the safety direction with a mutation. **Rejected alternative:** having `landArtifact` return an
explicit three-valued disposition instead of a `(bool, artifactVerdict, error)` triple the caller
re-derives. That makes the contract self-enforcing rather than documented, and is the more correct
shape in the abstract — but it changes every caller and every test of `landArtifact` for an
invariant that holds today and is mutation-tested. Recorded here so the next reader knows it was
weighed, not missed.

### D-C — `verify_err` stays verification-only

**Decided (operator's call).** `verify_err` remains the `errors.Join` of failing **verification**
verdicts, exactly as `docs/schema/DATABASE.md:109` defines it. A co-occurring transport error does
**not** appear in it.

The transport error is already logged per artifact by the existing
`slog.Warn("cache: artifact fetch failed", …)` inside the goroutine. The rejection's existing
`slog.Error("cache: version rejected by verification", …)` gains one field — a count of errored
siblings — so an operator reading the log sees the full picture without the column changing meaning.

**Rejected alternative:** appending the transport errors to `verify_err`. It would put "could not
evaluate" text into the very field the guard predicates on, and would falsify the definition in
`DATABASE.md:109` and `docs/schema/API.md`.

### D-D — a rejection with no message fails closed

**Decided.** `landArtifact` has a defensive `default:` arm that rejects a `classUnset` verdict
(`verify.go:202-203`). `aggregateVerdicts` skips `classUnset` entirely, so that path yields
`verify_err = ""` — and `UpsertCacheEntryArchived` would write a row that **fails**
`VerifyRejectedWithin`'s `verify_err <> ''` clause. The row lands; the guard never arms; the
re-download loop continues. A silent half-fix.

Unreachable today — no producer returns `classUnset`. It is closed anyway because this design's whole
purpose is making the guard arm reliably, and because the repo already has the identical fail-closed
precedent one function away: `aggregateVerdicts` synthesizes a message for a failure class carrying a
nil `err` (`verify.go:359-364`).

**It is a function, not an inline `if`, specifically so the defensive path is testable.** A mechanism
that cannot go red is an inert mechanism, which is the defect class that recurred four separate times
during #76. See §6, test 4.

### D-E — `errgroup` becomes a bounded waiter

**Decided.** The goroutines store their outcome and return `nil`; the transport error travels in the
outcome, not in the group's error. `vg.Wait()`'s value is then structurally always `nil` and is
discarded with a comment saying so.

**Rejected alternative:** `sync.WaitGroup.Go` (Go 1.25+) plus a semaphore channel. It reproduces
`errgroup.SetLimit` with more machinery and no gain; `errgroup` is the established idiom in this file
and the `concurrency` cap still comes from `SetLimit`.

### D-F — scope

**In:** `pkg/cache/reconcile.go` and its tests, plus the comment corrections §7 enumerates.

**Out, deliberately:**

- **No DB change, no migration, no API surface.** `UpsertCacheEntryArchived` and
  `VerifyRejectedWithin` are unchanged; `docs/schema/DATABASE.md:111` and `STORAGE.md:162-164`
  already describe the failure-visibility row correctly and stay true.
- **`recordVersionOutcome`** — extracting the disposition tail (`reconcile.go:235-286`) is a tracked
  follow-up. This change adds a branch to that tail; extracting it is a separate change with a
  separate rationale, and bundling a refactor into a correctness fix hides the fix in the diff.
- **[#77](https://github.com/jacaudi/booty/issues/77)** (Debian DVD) is structurally out of reach:
  `reconcileTarget` returns at `reconcile.go:85`, ~60 lines above the version loop, so DVD targets
  never see this code.
- **[#83](https://github.com/jacaudi/booty/issues/83)** (reverify degrades `verify_err` on a guarded
  row) is adjacent and unaffected — see §5.4.

---

## 5. The change

### 5.1 The outcome type

```go
// artifactOutcome keeps one artifact's landArtifact result intact so the
// version-level disposition can tell a VERDICT from an ERROR. The two parallel
// slices it replaces could not: a rejection and a transport error both leave
// landedFlags[i] false, so the errgroup's single first-error was the only
// signal separating them — and it carries no per-artifact attribution.
type artifactOutcome struct {
	landed  bool
	verdict artifactVerdict
	err     error
}

// rejected reports that a VERDICT refused these bytes, as opposed to the
// artifact not having been evaluated at all. landArtifact's reject() closure is
// the only producer of (landed=false, err=nil); every other early return in it
// carries a non-nil error. Deliberately NOT keyed on the verdict class: a
// non-Large artifact failing its checksum under `warn` LANDS while carrying
// classCorruption, and a class-keyed predicate would reject the version and
// delete the availability trade-off warn exists for.
func (o artifactOutcome) rejected() bool { return o.err == nil && !o.landed }
```

### 5.2 The land loop

```go
outcomes := make([]artifactOutcome, len(arts))
vg := new(errgroup.Group)
vg.SetLimit(max(concurrency, 1))
for i, a := range arts {
	vg.Go(func() error {
		landed, v, err := landArtifact(ctx, dir, a, policy)
		if err != nil {
			slog.Warn("cache: artifact fetch failed", "os", t.OS, "version", version, "file", a.Filename, "err", err)
		}
		outcomes[i] = artifactOutcome{landed: landed, verdict: v, err: err}
		return nil
	})
}
// Every goroutine returns nil: a transport error is carried per-artifact in
// outcomes[i].err so it can be ATTRIBUTED, which the group's single first-error
// cannot do. The group is a bounded waiter here, nothing more.
_ = vg.Wait()
```

### 5.3 The disposition

```go
rejected := slices.ContainsFunc(outcomes, artifactOutcome.rejected)
errored := 0
for _, o := range outcomes {
	if o.err != nil {
		errored++
	}
}

// A rejection WINS over a co-occurring transport error (D-A): it is knowledge a
// retry will not change, and recording it is what arms the retry guard. A
// transport error with no rejection still records NOTHING — which is what keeps
// it from forging VerifyRejectedWithin's four-column signature, and what leaves
// a resumable <file>.download on disk to resume next tick (D4b).
if !rejected && errored > 0 {
	continue
}
```

The rest of the tail is unchanged except that its rejection branch is keyed on `rejected` instead of
`slices.Contains(landedFlags, false)`, the `verdicts` slice it aggregates is projected out of
`outcomes`, and the recorded reason passes through `rejectionReason` (D-D):

```go
if rejected {
	if err := removeVersionDir(cacheName, segment, t.Arch, version); err != nil { … }
	if err := store.UpsertCacheEntryArchived(tvID, rejectionReason(verifyErr)); err != nil { … }
	slog.Error("cache: version rejected by verification",
		"os", t.OS, "version", version, "policy", policy, "err", verifyErr,
		"erroredSiblings", errored) // D-C: transport detail is logged, never folded into verify_err
	continue
}
```

```go
// rejectionReason guarantees a non-empty verify_err for a rejected version.
// UpsertCacheEntryArchived writes the string verbatim, and
// VerifyRejectedWithin's predicate requires verify_err <> '' — so an empty
// reason writes the failure-visibility row but leaves the retry guard DISARMED,
// which is the exact loop this design exists to stop. Unreachable today (every
// rejection carries classCorruption or classForgery, and aggregateVerdicts
// synthesizes a message for a failure class with a nil err); it fails closed
// anyway, matching that same precedent.
func rejectionReason(verifyErr string) string {
	if verifyErr == "" {
		return "version rejected by verification (no detail)"
	}
	return verifyErr
}
```

### 5.4 Interactions

**`removeVersionDir`.** Runs on REJECT only — including when a sibling errored. It wipes the
in-progress files of errored siblings, which is the accepted cost named in D-A. It does **not** run
on DEFER, which is what preserves the resumable `.download` that D4b exists to protect.

**The settled-skip** (`reconcile.go:210`, `cachedByVersion[version] && finalFilesPresent(...)`)
cannot fire after a rejection: the loop-entry `UpsertTargetVersion` writes `cached=0` via
`versions.go:22`'s `cached = excluded.cached`, so the prior-tick snapshot reads false on the next
pass. The guard blocks earlier anyway — it sits before `o.Artifacts`, which is before the skip.

**The retry guard** is reached identically; nothing about `VerifyRejectedWithin` or its four-column
predicate changes. `pkg/db/cache.go:118-122`'s argument for the four-column form — that a transport
error writes nothing and therefore cannot forge the signature — **survives verbatim**, because DEFER
still writes nothing.

**Reverify (#83)** is unaffected in mechanism and unchanged in its known defect: it can still
overwrite a guarded row's `verify_err` with `"artifact absent"`, degrading the reason while the guard
stays armed. This design does not widen or narrow that.

### 5.5 Next tick, per branch

| branch | what the next tick does |
|---|---|
| **REJECT** | `cached=0`, so no settled-skip. `VerifyRejectedWithin` blocks before `o.Artifacts` — no sidecar GET, no download — for up to `verifyRetryAfter` (1h). After the window, a full re-attempt from zero. |
| **DEFER** | No row written, so the guard cannot arm and no `fetched_at` is refreshed — an older expired rejection row stays expired, so there is no wedge. `removeVersionDir` did not run, so `downloadLargeInto` resumes via `Range` from the surviving `.download`. |
| **CACHE** | Settled-skip fires, as today. |

---

## 6. Tests

Every test carries a mutation. **If a named mutation does not turn its named test red, halt and
report — do not weaken the mutation.** Tests 1–3 drive the real `reconcileTarget` through `httptest`
servers; `pkg/cache/*_test.go` is `package cache`.

**1. `TestReconcileTarget_RejectionRecordedDespiteSiblingTransportError`** — the headline.
A version where one artifact's bytes mismatch its declared sha256 **and** a sibling returns 500.
Asserts the **positive** outcome, not merely the absence of a crash: a `cache_entries` row exists
with `size=0, in_window=0, verified=0`, `verify_err` contains `"checksum mismatch"`, **and**
`store.VerifyRejectedWithin(t.ID, version, time.Hour)` returns `blocked=true`.
*Mutation:* restore `if vg.Wait() != nil { continue }` → must go red.
*Why the positive assertion is mandatory:* the mutation's effect is "the feature silently does
nothing", so a test asserting only that nothing bad happened passes in both worlds.

**2. `TestReconcileTarget_TransportErrorAloneRecordsNoRejection`** — the safety direction.
A version where the only failure is a transport error; every other artifact verifies and lands.
Asserts no `cache_entries` row for that version, `VerifyRejectedWithin` returns `blocked=false`, and
the version directory plus any `<file>.download` still exist.
*Mutation:* change `rejected()` to `!o.landed` → must go red (a row appears and the guard arms after
a pure transport failure).

**3. `TestReconcileTarget_WarnLandedCorruptionIsNotAVersionRejection`** — the D-B trap.
A **non-`Large`** artifact fails its checksum under `--signaturePolicy warn` while a sibling returns
a transport error. The failing artifact lands, so there is no rejection; the version DEFERs.
Asserts no archived row, and that the version directory was not removed.
*Mutation:* key `rejected()` on the verdict class instead of `!o.landed` → must go red.

**4. `TestRejectionReasonNeverEmpty`** — the D-D fail-closed, unit level.
Table: `("", …)` → the synthesized reason; `("x: checksum mismatch", …)` → passthrough.
*Mutation:* make `rejectionReason` the identity function → must go red on the empty case.
*Exempt from the reconcile-level drive:* the `classUnset` reject arm is unreachable through the real
seams, which is why the guard is a function — so it has a seam of its own rather than being inert.

**Regression coverage already present, to extend rather than duplicate:**
`TestReconcileFCOSVerification` (`reconcile_test.go:621`) and
`TestReconcileTarget_VerificationRejectionRateLimitsRedownload` (`:868`) both exercise
rejection-with-no-sibling-error. Verify they still pass unchanged; do not write a fourth copy.

**Test-count baseline.** Current is **740**. Record it with two standalone commands — the piped form
returns a bogus `0` in this environment:

```bash
go test ./... -race -v > /tmp/out.txt 2>&1
grep -c '^=== RUN' /tmp/out.txt
```

---

## 7. The claims this change falsifies

Enumerated by `grep` across `*.go`, `*.md`, `*.yml`, `*.ts`, `*.tsx` — not from a list — because a
duplicated *claim* behaves like duplicated knowledge but cannot be single-sourced, and on #76 one
false sentence reached six files with four of them outside the plan's `docs/` glob. Excluded:
`web/node_modules/`, `.claude/worktrees/`, and `docs/plans/` (gitignored, historical).

| Site | The claim | Action |
|---|---|---|
| `pkg/cache/reconcile.go:234` | `// transport error → whole version retried next tick (nothing recorded)` | Rewrite — true only when no artifact was rejected |
| `pkg/cache/verify.go:32-33` | *"reconcile.go's `vg.Wait() != nil -> continue` still guarantees no partially-filled verdict slice reaches aggregation"* | **Becomes false.** Partially-filled slices now reach `aggregateVerdicts` by design; `classUnset`'s fail-closed handling goes from defensive to load-bearing |
| `pkg/cache/verify.go:143-144` | D4b: *"Returning err routes to reconcile.go's `vg.Wait() != nil -> continue`: no row written, no removeVersionDir"* | Narrow — still true when no sibling was rejected, which is D4b's actual case; say so |
| `pkg/cache/verify_test.go:209-213` | *"No partially-filled slice reaches aggregateVerdicts today, because `vg.Wait() != nil` abandons the whole version first"* | Rewrite. The test itself (`TestArtifactVerdictZeroValueIsNotAPass`) stays valid and becomes **more** load-bearing |
| `pkg/cache/verify_test.go:822-824` | Same `vg.Wait()` routing description | Rewrite |
| `docs/designs/2026-08-02-…-design.md` §7.3, last bullet | *"documented here, not fixed"* | Add a pointer to this design; leave the analysis intact as the record of when it was found |

**Checked and still true — do not touch:** `docs/schema/DATABASE.md:109` (the `verify_err`
definition, preserved by D-C) and `:111` (the failure-visibility row); `docs/schema/STORAGE.md:162-164`
(no bytes on disk for a rejected version); `pkg/db/cache.go:118-122` (the four-column justification —
DEFER still writes nothing).

**A `gofmt` trap applies to two of these edits.** Go 1.19+ doc-comment canonicalization rewrites an
apostrophe pair to a Unicode right-quote, `go vet` misses it, and CI here does not run `gofmt`. Any
rewritten comment quoting `verify_err <> ''` must put the literal in a **tab-indented code block**.
Verify with a byte check (`27 27`, not `e2 80 9d`).

---

## 8. Residual risks, stated

- **A close-delimited truncation could still masquerade as a verdict.** §2 measured that a
  `Content-Length`-delimited truncation surfaces as `unexpected EOF` — a transport error. A response
  with no `Content-Length` that closes early is undetectable by any layer, and would arrive as a
  checksum mismatch → rejection → guard armed for an hour. **This is pre-existing and unchanged by
  this design** (the same truncation with no sibling error already rejects today). Not fixed here;
  bounded by the self-clearing window. No issue filed — filing one is the operator's call.
- **The cross-file invariant in D-B.** Mitigated by `landArtifact`'s doc comment and test 2's
  mutation, not enforced by the type system. The self-enforcing alternative is recorded in D-B.
- **A genuinely transient co-occurrence now costs an hour.** If upstream is mid-publish — new sidecar,
  old bytes, a sibling not yet uploaded — rejection-wins guards a *new* version for up to an hour
  where today it would retry in minutes. Accepted: versions live for weeks, and the guard clears
  itself.
- **`verifyRetryAfter` remains a package var**, not a flag (tails design §7.4). Unchanged here.

---

## 9. What is settled and must not be relitigated

**D4a** (a `Large` checksum failure never lands, under any policy), **D4b** (a hash *read* failure is
a transport error, not a corruption verdict), the `Large` guard's narrowing to `SigURL` (never its
deletion), case-sensitive digest comparison, and the guard being version-level and OS-agnostic. All
survived adversarial review on #76; this design builds on them and changes none of them.
