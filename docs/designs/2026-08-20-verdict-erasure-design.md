# Design: a transport error must neither forge nor erase a verification verdict

**Issue:** none filed — this closes final-review finding **S1** from
[jacaudi/booty#76](https://github.com/jacaudi/booty/issues/76) (shipped as PR #82, merge `c7c30db`)
**Status:** design — Gate 1 returned (4 blocking, 7 significant, 6 minor); all folded in below
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

**The goal, stated at the scope this design can actually deliver:** make `reconcileTarget` stop
conflating *"`landArtifact` refused these bytes"* with *"`landArtifact` could not evaluate these
bytes"*, so that a **`landArtifact`-level transport error** can neither erase a refusal nor be
recorded as one.

That is deliberately narrower than "a transport error can never forge a verdict", which is **false**
at the system level and which an earlier revision of this document wrongly claimed. `verifyArtifact`
already launders some infrastructure failures into verdicts one call frame below `landArtifact` —
see §4 D-B and §8. Closing that is a different change, in P3b's fail-closed classification, and is
not attempted here.

---

## 2. Evidence: what is verified, and what is assumed

### Verified — measured or read directly from the code on 2026-08-20

| Claim | How |
|---|---|
| `landArtifact` returns `(false, v, nil)` from **exactly one** place: its `reject()` closure (`verify.go:170-177`) | Read all 13 returns in `verify.go:104-205`; independently re-enumerated by the Gate 1 reviewer |
| **Every** other early return in `landArtifact` carries a non-nil error — mkdir (`:109`), the `Large` `SigURL` refusal (`:123`), `downloadLargeInto` (`:128`), `hashFile` (`:149`), `DownloadStaged` (`:156`), and `land()`'s rename (`:166`) | Same read, both passes |
| Every error return passes `artifactVerdict{}`, so an errored artifact's slot holds `classUnset` | `verify.go:109,123,128,149,156,166` |
| The errgroup has **no context** (`new(errgroup.Group)`, `reconcile.go:220`), so no goroutine is cancelled by a sibling's error; every artifact runs to completion regardless | Read; `errgroup.Group` without `WithContext` has no cancel |
| `aggregateVerdicts` has no `classUnset` arm, so an errored slot is neither counted verifiable nor counted failed | `verify.go:349-365` |
| **A rejection can never carry an empty `verify_err` today.** `reject()` only ever carries `classCorruption`/`classForgery`; both always append to `errs`, so `verifiable>0`, `failed=true`, and the joined message is non-empty | `verify.go:354-364`, `:370-372` — traced twice |
| **A truncated download is a transport error, not a short file that would read as a mismatch.** Measured: `Content-Length: 100`, 50 bytes sent, connection closed → `copied=50 err=unexpected EOF` from `io.Copy` | Ran it against a raw `net.Listen` server; **independently reproduced by the Gate 1 reviewer**, which also measured that a truncated *chunked* response likewise yields `unexpected EOF` |
| **`Large` and `SigURL` never co-occur.** `Large` is set at exactly one site (`netbootxyz.go:379`, and only `tails` declares it); `SigURL` at exactly one site (`ignition.go:79`, Flatcar) | `grep` for both producers across `pkg/`, tests excluded |
| **Tails' sidecar covers only the ISO.** It is a single LF-terminated line, and `checksumCovers` names only `tails-amd64.iso`; the other three artifacts are always `classNotVerifiable` and can never produce a rejection | `tools.go:92-108`; sidecar format verified during #76 |
| `removeVersionDir` is `os.RemoveAll` over the whole version directory, in-progress files included | `pkg/cache/layout.go:87-91` |
| **No existing test asserts the erasure behaviour**, and no reconcile test serves a 500/404 at all | `grep` over `pkg/cache/*_test.go`, both passes |
| **Eviction cannot erase an armed guard** — `ListArchivedUnpinned` filters `size > 0`, so the `size=0` rejection row is never an eviction candidate | `db/cache.go:259-262`, `evict.go:42,75` (found by Gate 1) |
| **Test baseline is 769**, 0 failures, on a clean tree at `0342ed4` | `go test ./... -race -count=1 -v` → 769 `=== RUN` (636 top-level + 133 subtests), 0 `--- FAIL`. Measured twice, once by Gate 1 |

**On the baseline discrepancy, stated rather than reconciled:** notes carried from #76 record 740.
That number does **not** reproduce on this tree under any counting interpretation (636 top-level /
769 total / 637 `func Test` declarations). 769 is what this branch measures today; the provenance of
740 is unknown and was not chased. An executor must record its own baseline before touching code, not
inherit either number.

### Assumed — stated, not proven

- **The co-occurrence is reachable in the field but has not been observed.** The hazard class is a
  transiently-404ing or connection-resetting allowlisted file while another artifact mismatches. It
  is not reproduced from production logs; it is derived from the code paths.
- **The `classForgery`-from-mid-read path in §8 is inferred, not measured.** `openpgp.CheckDetachedSignature`
  reads the signed file to hash it; an I/O error there matches none of the `errors.Is` arms at
  `verify.go:278-289` and falls through to the `classForgery` default. Neither this author nor Gate 1
  built a failing-read reproduction — the D4b test's mode-bit trick makes `os.Open` fail, which lands
  in the *separate* `classCorruption` arm at `:268-271`. High-confidence inference; treat as such.
- Upstream mirrors frame artifact GETs with `Content-Length` or chunked encoding. Both detect
  truncation (measured). A response using neither is undetectable by any layer — see §8.

### Corrected from the handoff — do not repeat this claim

The tails design §7.3 cites `pkg/ostype/netbootxyz.go`'s memtest86plus note (endpoints.yml lists 7
files, the asset mirror serves 3) as live evidence that a listed file can 404. **That specific
instance is already mitigated**: the `files` allowlist filters unserved manifest entries out *before*
any fetch (`netbootxyz.go:142-151`, `:330-337` — confirmed by Gate 1). The hazard class — any
allowlisted file that 404s transiently — is real; that example is not current evidence for it.

---

## 3. The root cause, stated precisely

The bug is **not** a missing channel. `landArtifact` already produces three mutually exclusive
outcomes, and they are already distinguishable per artifact:

| outcome | signature | meaning |
|---|---|---|
| **landed** | `landed == true` | bytes sit at the final path — verdict `classPass`, `classNotVerifiable`, or `classCorruption` warn-landed |
| **rejected** | `landed == false, err == nil` | `landArtifact`'s disposition switch refused the bytes |
| **errored** | `err != nil` | `landArtifact` could not evaluate the artifact |

What `reconcileTarget` does is collapse per-artifact attribution into one scalar — `errgroup`'s
first non-nil error — and then key disposition off `landedFlags`, where **a rejection and a transport
error both read as `false`**. The information needed to tell them apart is produced, then discarded
two lines later.

This is the tails design's **D4b** rule — *"I could not evaluate the material" is infrastructure and
must be retried; "the material does not match" is a verdict* — stated and enforced at the artifact
level, and left unenforced at the version level.

### The rule is not applied consistently one frame lower, and this design does not fix that

`landArtifact:147-150` routes a `Large` artifact's `hashFile` read failure out as an **error**,
precisely so it is not mistaken for a verdict (D4b). One call frame down,
`verifyDetachedGPG:268-271` routes a *non-`Large`* artifact's `os.Open` failure into
**`classCorruption`** — the opposite rule for the same failure class. Two more sites do the same
(§8).

That asymmetry is P3b-settled and deliberately documented at `verify.go:257-262` ("unfetchable/
unparseable material … are CORRUPTION (benign / fail-closed)"). It is named here because §3's
governing principle would otherwise read as universal when it is not, and because it is the reason
§1's goal is scoped to `landArtifact` rather than to the system.

---

## 4. Decisions

### D-A — the disposition is three-way, and a rejection wins over a co-occurring transport error

**Decided.** Per artifact, classify into landed / rejected / errored. Then:

```
any rejected  ->  REJECT   removeVersionDir, UpsertCacheEntryArchived, guard arms
else errored  ->  DEFER    continue — no cache_entries row, no wipe, in-progress bytes survive (unchanged)
else          ->  CACHE    mark cached, record size + verdict (unchanged)
```

**Why rejection wins.** A refusal is knowledge a retry will not change; a transport error is
knowledge-free. Recording the refusal is what arms the guard, which is the entire point of §7 of the
tails design. Deferring instead costs a full 1.94 GB re-pull every interval for as long as the
transport fault persists.

**When `errored == 0` the new disposition is provably identical to today's**, so existing regression
coverage holds unchanged: with `vg.Wait() == nil` every `landedFlags[i]` was written, so
`slices.Contains(landedFlags, false)` ⟺ `rejected`. (Traced; independently re-derived by Gate 1.)

**What it costs — corrected, because the first revision over-stated it.** `removeVersionDir` now also
fires when a sibling errored, so in principle a mid-flight resumable `.download` could be destroyed
by a *different* artifact's refusal. **That is unreachable today**, and saying otherwise inflates the
cost: the Tails ISO is the only `Large` artifact anywhere (so the only artifact that ever writes a
`.download`), it is also the only artifact in its version that can be refused (its three siblings are
always `classNotVerifiable` — §2), and when it *is* refused, `reject()` at `verify.go:175` has
already removed its own `.download`. The cost becomes real only if a future OS pairs a `Large`
artifact with other verifiable siblings.

What is genuinely lost today is narrower: landed small files of the same version, wiped one tick
earlier than they otherwise would be. That is not a new class of loss — version-level atomicity (§6
of the P3b design) already wipes a fully landed 1.94 GB ISO for a sibling's mismatch. What is new is
only that the wipe also fires when a sibling errored.

### D-B — the discriminator is `!landed && err == nil`, NOT the verdict class

**Decided, and this is the trap.** A non-`Large` artifact failing its checksum under `warn` **lands**
— carrying `classCorruption` (`verify.go:198-199`). Keying rejection off "any failure class" would
turn every warn-landed corruption into a version rejection, silently deleting the availability
trade-off `warn` exists to provide.

**What the predicate does and does not mean.** `!landed && err == nil` means exactly *"`landArtifact`'s
disposition switch refused these bytes"*. It does **not** mean "an integrity verdict was reached" —
`verifyArtifact` can hand `landArtifact` a `classCorruption` or `classForgery` synthesized from an
infrastructure fault, and those refusals are indistinguishable here by construction (§8). §2 proves
the narrow claim; the broad one is false and is not relied on anywhere in this design.

**The residual is a cross-file invariant.** The predicate lives in `reconcile.go` and depends on
`landArtifact`'s return discipline in `verify.go`. If a future path ever returns `(false, v, nil)`
for a reason `landArtifact` itself considers infrastructural, it routes to REJECT. Two mitigations,
both cheap: `landArtifact`'s doc comment already states the contract (*"err != nil is a transport/IO
failure (nothing landed; retry next tick)"*), and §6's test 2 pins the safety direction with a
mutation.

**Rejected alternative:** having `landArtifact` return an explicit three-valued disposition instead
of a `(bool, artifactVerdict, error)` triple the caller re-derives. That makes the contract
self-enforcing rather than documented, and is the more correct shape in the abstract — but it changes
every caller and every test of `landArtifact` for an invariant that holds today and is
mutation-tested. Recorded so the next reader knows it was weighed, not missed.

### D-C — `verify_err` stays verification-only

**Decided (operator's call).** `verify_err` remains the `errors.Join` of failing **verification**
verdicts, exactly as `docs/schema/DATABASE.md:109` and `docs/schema/API.md:481` define it. A
co-occurring transport error does **not** appear in it.

The transport error is already logged per artifact by the existing
`slog.Warn("cache: artifact fetch failed", …)` inside the goroutine. The rejection's existing
`slog.Error("cache: version rejected by verification", …)` gains one field — `erroredSiblings`, a
count — so an operator reading the log sees the full picture without the column changing meaning.
Because that log field is D-C's *entire* compensating mechanism, §6 test 4 asserts it.

**Rejected alternative:** appending the transport errors to `verify_err`. It would put "could not
evaluate" text into the very field the guard predicates on, and would falsify both definitions above.

### D-D — an empty `verify_err` on the reject path is left as-is, and NO guard is added

**Decided — reversed from the first revision, which added a `rejectionReason()` helper.**

The hazard is real in shape: `UpsertCacheEntryArchived` writes `verify_err` verbatim, and
`VerifyRejectedWithin` requires `verify_err <> ''`, so a rejection carrying an empty message would
write the failure-visibility row and leave the guard **disarmed** — a silent half-fix.

It is not defended, for three reasons:

1. **The path is doubly unreachable, not merely unreachable.** It needs `verifyArtifact` to return
   `classUnset` — structurally impossible; it returns only `classPass`, `classNotVerifiable`,
   `classCorruption`, `classForgery` — *and* `aggregateVerdicts` to reach `verifiable == 0` alongside
   a rejection. §2 traces the second half closed independently.
2. **The first revision's justification was wrong.** It cited `aggregateVerdicts`' synthesis
   (`verify.go:357-364`) as an "identical fail-closed precedent". It is not identical: that guards
   *one* unreachable condition, and this would guard the conjunction of two.
3. **The empty string is currently an accidental backstop in the safety direction, and a guard would
   remove it.** `verify_err <> ''` is one of the four columns `pkg/db/cache.go:110-128` reasons
   carefully about. Guaranteeing a non-empty message from the archived-row writer would convert that
   clause into a tautology *for that writer* — visible in §6 test 2, where a mutated `rejected()`
   would then forge the **full** four-column signature instead of being refused by the empty message.

So the invariant is documented at the write site and left undefended. If it is ever violated the
failure mode is an *unarmed* guard — today's behaviour, and the safe direction — not a wrongly-armed
one.

### D-E — `errgroup` becomes a bounded waiter

**Decided.** The goroutines store their outcome and return `nil`; the transport error travels in the
outcome, not in the group's error. `vg.Wait()`'s value is then structurally always `nil` and is
discarded with a comment saying so — **and the comment must warn that reintroducing `return err` in
the goroutine would now cause that error to be silently swallowed rather than routed.**

**Rejected alternative:** `sync.WaitGroup.Go` (Go 1.25+) plus a semaphore channel. It reproduces
`errgroup.SetLimit` with more machinery and no gain; `errgroup` is the established idiom in this file
and the `concurrency` cap still comes from `SetLimit`.

**Concurrency is sound** (checked, and independently re-derived by Gate 1): each `outcomes[i]` is
written by exactly one goroutine, `errgroup.Wait` is a `sync.WaitGroup` wait and establishes
happens-before for every read after it, and nothing aliases. `go.mod` declares `go 1.26.1`, so
per-iteration loop variables make the `i`/`a` capture safe.

### D-F — scope

**In:** `pkg/cache/reconcile.go` and its tests, plus the comment corrections §7 enumerates.

**Out, deliberately:**

- **No DB change, no migration, no API surface.** `UpsertCacheEntryArchived` and
  `VerifyRejectedWithin` are unchanged.
- **`recordVersionOutcome`** — extracting the disposition tail (`reconcile.go:237-288`) is a tracked
  follow-up. This change adds a branch to that tail; extracting it is a separate change with a
  separate rationale, and bundling a refactor into a correctness fix hides the fix in the diff.
- **`verifyArtifact`'s fail-closed classification** (§3, §8) — P3b-settled, deliberately documented,
  and a different change.
- **[#77](https://github.com/jacaudi/booty/issues/77)** (Debian DVD) is structurally out of reach:
  `reconcileTarget` returns at `reconcile.go:85`, ~60 lines above the version loop.
- **[#83](https://github.com/jacaudi/booty/issues/83)** (reverify and the guard) is adjacent and
  unaffected — see §5.4.

---

## 5. The change

### 5.1 The outcome type

```go
// artifactOutcome keeps one artifact's landArtifact result intact so the
// version-level disposition can tell a REFUSAL from a FAILURE TO EVALUATE. The
// two parallel slices it replaces could not: a refusal and a transport error
// both leave landedFlags[i] false, so the errgroup's single first-error was the
// only signal separating them — and it carries no per-artifact attribution.
type artifactOutcome struct {
	landed  bool
	verdict artifactVerdict
	err     error
}

// rejected reports that landArtifact's disposition switch REFUSED these bytes,
// as opposed to landArtifact having been unable to evaluate them at all. Its
// reject() closure is the only producer of (landed=false, err=nil); every other
// early return in landArtifact carries a non-nil error.
//
// This is NOT the same as "an integrity verdict was reached": verifyArtifact
// classifies some infrastructure faults as classCorruption/classForgery before
// landArtifact ever sees them (verify.go:264-271, :290-292), and those refusals
// are indistinguishable here by construction. See the design's §8.
//
// Deliberately NOT keyed on the verdict class: a non-Large artifact failing its
// checksum under `warn` LANDS while carrying classCorruption, and a class-keyed
// predicate would reject the version and delete the availability trade-off warn
// exists for.
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
// cannot do. The group is a bounded waiter here, nothing more — so Wait's value
// is structurally always nil. REINTRODUCING `return err` above would make this
// line SWALLOW that error rather than route it; carry it in the outcome instead.
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

// A refusal WINS over a co-occurring transport error (D-A): it is knowledge a
// retry will not change, and recording it is what arms the retry guard. A
// transport error with no refusal still writes no CACHE_ENTRIES row — which is
// what keeps it from forging VerifyRejectedWithin's four-column signature, and
// what leaves a resumable <file>.download on disk to resume next tick (D4b).
// (The loop-entry UpsertTargetVersion above has already written cached=0; the
// guard reads cache_entries, not that column.)
if !rejected && errored > 0 {
	continue
}

verdicts := make([]artifactVerdict, len(outcomes))
for i, o := range outcomes {
	verdicts[i] = o.verdict
}
verified, verifyErr := aggregateVerdicts(verdicts)
```

The rest of the tail is unchanged except that its rejection branch is keyed on `rejected` instead of
`slices.Contains(landedFlags, false)`:

```go
if rejected {
	if err := removeVersionDir(cacheName, segment, t.Arch, version); err != nil { … }
	// D-D: verifyErr is non-empty for every reachable refusal (aggregateVerdicts
	// always attaches a message to classCorruption/classForgery). An empty one
	// would write this row and leave the guard DISARMED — the safe direction,
	// and deliberately undefended; see the design's D-D.
	if err := store.UpsertCacheEntryArchived(tvID, verifyErr); err != nil { … }
	slog.Error("cache: version rejected by verification",
		"os", t.OS, "version", version, "policy", policy, "err", verifyErr,
		"erroredSiblings", errored) // D-C: transport detail is logged, never folded into verify_err
	continue
}
```

### 5.4 Interactions

**`removeVersionDir`.** Runs on REJECT only — including when a sibling errored. It wipes the
in-progress files of errored siblings, which is the (currently unreachable — D-A) cost. It does
**not** run on DEFER, which is what preserves the resumable `.download` that D4b exists to protect.

**The settled-skip** (`reconcile.go:210`, `cachedByVersion[version] && finalFilesPresent(...)`)
cannot fire after a rejection: the loop-entry `UpsertTargetVersion` writes `cached=0` via
`versions.go:22`'s `cached = excluded.cached`, so the prior-tick snapshot reads false on the next
pass. The guard blocks earlier anyway — it sits before `o.Artifacts`, which is before the skip.

**The retry guard** is reached identically; nothing about `VerifyRejectedWithin` or its four-column
predicate changes. `pkg/db/cache.go:118-122`'s argument for the four-column form — that a transport
error writes nothing and therefore cannot forge the signature — **survives**, because DEFER still
writes no `cache_entries` row. What does **not** survive unqualified is
`reconcile.go:161-163`'s stronger in-tree claim that *transport failures are never guarded*; see §7.

**Eviction** cannot erase an armed guard: `ListArchivedUnpinned` filters `size > 0`, and a rejection
row is `size=0`.

**Reverify (#83)** is unaffected in mechanism and unchanged in its two known defects, both documented
at `pkg/http/api_cache.go:171-184`: it can overwrite a guarded row's `verify_err` with
`"artifact absent"` (guard stays armed, reason degraded), **and** a nil verdict writes
`verified=NULL` with an empty `verify_err`, which stops the predicate matching and **releases the
guard early**. The second is the worse of the two and the first revision of this design omitted it.
Neither is reachable for a rejected Tails version (absent artifacts yield a non-empty reason), and
neither is widened or narrowed here.

### 5.5 Next tick, per branch

| branch | what the next tick does |
|---|---|
| **REJECT** | `cached=0`, so no settled-skip. `VerifyRejectedWithin` blocks before `o.Artifacts` — no sidecar GET, no download — for up to `verifyRetryAfter` (1h). After the window, a full re-attempt from zero. |
| **DEFER** | No `cache_entries` row written, so the guard cannot arm **and no `fetched_at` is refreshed** — an older expired rejection row stays expired, so there is no wedge (§6 test 2 asserts this). `removeVersionDir` did not run, so `downloadLargeInto` resumes via `Range` from a surviving `.download`. |
| **CACHE** | Settled-skip fires, as today. |

---

## 6. Tests

Every test carries a mutation. **If a named mutation does not turn its named test red, halt and
report — do not weaken the mutation.** `pkg/cache/*_test.go` is `package cache` (in-package).

**Record the baseline first**, with two standalone commands — the piped form returns a bogus `0` in
this environment, and `-count=1` defeats the result cache:

```bash
go test ./... -race -count=1 -v > /tmp/out.txt 2>&1
grep -c '^=== RUN' /tmp/out.txt
```

It measures **769** on `0342ed4`. Record what *your* tree measures; do not inherit a number.

**The OS and `--signaturePolicy` are prescribed, not left to the executor** — they are not
interchangeable. FCOS artifacts are sha256-only and non-`Large`, so a checksum failure **lands under
`warn`** and only `strict` produces a rejection. Choosing FCOS without `strict` writes a test that
silently exercises the wrong branch. Harness precedent: `reconcile_test.go:621`
(`TestReconcileFCOSVerification`) for the FCOS shape, `:868` for the guard shape.

**1. `TestReconcileTarget_RejectionRecordedDespiteSiblingTransportError`** — the headline, and the
**only** test here that discriminates the feature.
**FCOS + `--signaturePolicy strict`.** One artifact's bytes mismatch its declared sha256; a sibling
returns 500. Asserts the **positive** outcome, not merely the absence of a crash: a `cache_entries`
row exists with `size=0, in_window=0, verified=0`, `verify_err` contains `"checksum mismatch"`, and
`store.VerifyRejectedWithin(t.ID, version, time.Hour)` returns `blocked=true`.
*Mutation:* restore **both** halves of the old shape — `return err` in the goroutine **and**
`if vg.Wait() != nil { continue }` — must go red.
*Why both:* under §5.2 the goroutine returns `nil` unconditionally, so restoring only the
`vg.Wait() != nil` branch leaves it **dead** and the test stays green. The first revision of this
document prescribed exactly that half-mutation, which would have halted an unattended executor
against its own no-weakening rule.
*Why the positive assertion is mandatory:* the mutation's effect is "the feature silently does
nothing", so a test asserting only that nothing bad happened passes in both worlds.

**2. `TestReconcileTarget_TransportErrorAloneRecordsNoRejection`** — the safety direction.
**FCOS + `strict`.** The only failure is a transport error; every other artifact verifies and lands.
Asserts no `cache_entries` row for that version, `VerifyRejectedWithin` returns `blocked=false`, and
the version directory was not removed. Additionally seeds a **stale** archived row (older than the
window) and asserts its `fetched_at` is unchanged after the DEFER — the no-wedge property §5.5
claims.
*Mutation:* change `rejected()` to `!o.landed` → must go red.

**3. `TestReconcileTarget_WarnLandedCorruptionIsNotAVersionRejection`** — the D-B trap.
**FCOS + `warn`.** A non-`Large` artifact fails its checksum while a sibling returns a transport
error. The failing artifact lands, so there is no refusal; the version DEFERs. Asserts no archived
row and that the version directory was not removed.
*Mutation:* key `rejected()` on the verdict class instead of `!o.landed` → must go red.

**4. `TestReconcileTarget_RejectionLogsErroredSiblingCount`** — D-C's compensating mechanism.
Same fixture as test 1. Captures the default `slog` handler and asserts the
`"cache: version rejected by verification"` record carries `erroredSiblings=1`. Precedent for the
capture: `reconcile_test.go`'s existing warn-log subtest.
*Mutation:* drop the `erroredSiblings` field → must go red.
*Why it exists:* D-C's entire justification for keeping transport detail out of `verify_err` is that
the operator sees it in this log line. Untested, that is a promise, not a mechanism.

**Tests 2 and 3 pass unchanged against the pre-change code — this is intended.** Both describe
behaviour today's `if vg.Wait() != nil { continue }` already produces. They are **trap guards for
their named mutations**, not feature tests. Only test 1 discriminates the feature, which is why its
mutation must be correct.

**Regression coverage already present, to verify rather than duplicate:**
`TestReconcileFCOSVerification` (`reconcile_test.go:621`) and
`TestReconcileTarget_VerificationRejectionRateLimitsRedownload` (`:868`) both exercise
rejection-with-no-sibling-error, which D-A proves is bit-identical under the new disposition. They
must still pass unchanged; do not write a third copy.

---

## 7. The claims this change falsifies

Enumerated by `grep` over **tracked** files (`docs/plans/` and `.superpowers/` are gitignored or
untracked and out of scope). The first revision's enumeration **missed five live sites and Gate 1
found them** — including `reconcile.go:161-163`, the design's own central invariant stated in the
same function, ~70 lines above the change. The likely mechanism is grep-term choice: that comment
says *"Transport **failures**"*, and the search used *"transport error"*. Search the claim's
**meaning**, not one of its phrasings.

### Must be rewritten

| Site | The claim | Why it breaks |
|---|---|---|
| **`pkg/cache/reconcile.go:161-163`** | *"Transport failures are never guarded: they return before any `cache_entries` row is written, so they cannot forge the four-column signature"* | **Wrong as stated after D-A** — a transport failure co-occurring with a refusal now gets a row and *is* guarded. The narrow true claim is that a transport failure **alone** writes no row |
| `pkg/cache/reconcile.go:234` | `// transport error → whole version retried next tick (nothing recorded)` | True only when no artifact was refused |
| `pkg/cache/reconcile.go:34-35` | *"a per-artifact download error is logged and retried next tick"* | Now conditional on no sibling refusal |
| `pkg/cache/reconcile.go:42-46` | *"A FRESH errgroup.Group is created per version (errgroup's error is set once and never reset…)"* | Under D-E no goroutine ever returns an error, so the stated rationale for the fresh group evaporates. A fossil justifying a decision its own premise no longer supports — restate the reason (per-version lifetime and `SetLimit` scoping) or drop it |
| `pkg/cache/verify.go:32-33` | *"reconcile.go's `vg.Wait() != nil -> continue` still guarantees no partially-filled verdict slice reaches aggregation"* | **Becomes false.** Partially-filled slices now reach `aggregateVerdicts` by design; `classUnset`'s fail-closed handling goes from defensive to load-bearing |
| `pkg/cache/verify.go:143-144` | D4b: *"Returning err routes to reconcile.go's `vg.Wait() != nil -> continue`: no row written, no removeVersionDir"* | Narrow — still true when no sibling was refused, which is D4b's actual case; say so |
| `pkg/cache/verify_test.go:203-207` | *"reconcile.go pre-allocates `verdicts := …` alongside `landedFlags := …` — two parallel slices"* | §5.1 **deletes both slices**. The test itself stays valid and becomes *more* load-bearing |
| `pkg/cache/verify_test.go:209-213` | *"No partially-filled slice reaches aggregateVerdicts today, because `vg.Wait() != nil` abandons the whole version first"* | Same falsification |
| `pkg/cache/verify_test.go:822-825` | The `vg.Wait()` routing description (the sentence runs to **825**, not 824 — rewriting 822-824 leaves a dangling clause) | Same |

### Historical designs — annotate, do not rewrite

They are records of their moment. Add a one-line pointer to this design; leave the analysis intact.

- `docs/designs/2026-08-02-…-design.md` **§7.3 last bullet** (*"documented here, not fixed"*) and
  **§7.3 `:676-678`** — *"Transport failures are excluded … it cannot forge the four-column
  signature"*, the bullet **immediately above** the one the first revision cited, in the same
  subsection, carrying the same now-narrowed claim. Also `:212` and `:616`.
- `docs/designs/2026-07-29-tool-rescue-os-support-design.md:70` — D13's *"abandons the whole version
  with nothing kept"*.
- `docs/designs/2026-07-01-p3a-cache-inventory-eviction-view-design.md:62` — *"the `if vg.Wait() == nil`
  block"*.

### Checked and still true — do not touch

- `docs/schema/DATABASE.md:109` (the `verify_err` definition, preserved by D-C) and `:111` (the
  failure-visibility row); `docs/schema/API.md:481` (the same definition).
- `docs/schema/STORAGE.md:162-164` — no bytes on disk for a rejected version.
- `pkg/db/cache.go:118-122` — the four-column justification; DEFER still writes no `cache_entries` row.
- `pkg/http/api_cache.go:171-184` — reasons about the four-column predicate and stays accurate.

### Repaired by this change — worth recording

`docs/CONFIGURATION.md:463-471` promises an operator that *"a persistently divergent upstream cannot
re-pull its artifacts every `--cacheInterval`"*. That is **currently an over-claim** — the guard
demonstrably fails to arm in the co-occurrence case. This change makes the sentence true. No edit
needed; note it in the PR body.

### A `gofmt` trap applies to several of these edits

Go 1.19+ doc-comment canonicalization rewrites an apostrophe pair to a Unicode right-quote, `go vet`
misses it, and CI here does not run `gofmt`. Any rewritten comment quoting `verify_err <> ''` must
put the literal in a **tab-indented code block**. Verify with a byte check (`27 27`, not
`e2 80 9d`). Reproduced during Gate 1: gofmt rewrites it in prose doc lines and leaves it intact in
a tab-indented block, and `go vet` passes both ways.

---

## 8. Residual risks, stated

- **`verifyArtifact` launders infrastructure failures into verdicts, one frame below the fix.** Three
  sites in `verifyDetachedGPG` return a refusal for what is really a transport or I/O fault:

  | site | failure | class | refused under |
  |---|---|---|---|
  | `verify.go:264-267` | `fetchBytes(a.SigURL)` — sidecar HTTP failure / 5xx | `classCorruption` | `strict` |
  | `verify.go:268-271` | `os.Open(filePath)` — local I/O | `classCorruption` | `strict` |
  | `verify.go:290-292` | I/O error while `CheckDetachedSignature` reads the signed file — falls to `default:` | `classForgery` | **`warn` AND `strict`** |

  These reach `landArtifact`'s `reject()` and therefore satisfy `!landed && err == nil`, so under D-A
  they now REJECT — arming the guard for an hour — where today a co-occurring sibling error would
  DEFER. **`SigURL` is Flatcar-only** (`ignition.go:79`, the sole producer), so the blast radius is
  Flatcar, and only the third row fires under the default `warn`. It is **pre-existing in kind**:
  today the same faults already arm the guard whenever no sibling errors. This design widens *when*,
  not *whether*. Not fixed here — `verifyArtifact`'s fail-closed classification is P3b-settled and
  deliberately documented at `verify.go:257-262`. Row 3 is inferred, not measured (§2).
- **A close-delimited truncation could still masquerade as a verdict.** §2 measured that both
  `Content-Length`-delimited and chunked truncations surface as `unexpected EOF`. A response using
  **neither** framing truncates undetectably and would arrive as a checksum mismatch → refusal →
  guard armed for an hour. Pre-existing and unchanged by this design; the residual is narrower than
  an earlier revision implied (it needs an essentially HTTP/1.0-era server). Bounded by the
  self-clearing window. No issue filed — that is the operator's call.
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
deletion), case-sensitive digest comparison, `verifyArtifact`'s fail-closed classification, and the
guard being version-level and OS-agnostic. All survived adversarial review on #76; this design builds
on them and changes none of them.
