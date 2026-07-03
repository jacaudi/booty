# P3b — Signature verification — Design

**Date:** 2026-07-01 · **Slice:** P3b (deferred half of roadmap P3; P3a merged as PR #49) ·
**Depends on:** issue #48's params-driven drivers (separate PR, lands first — see
`2026-07-01-issue-48-params-driven-channels-design.md`) · **Spec:** canonical v1 design §2.9.

> **Session note:** drafted with the user away from keyboard. Decisions marked
> **[provisional]** follow the recommended option and are up for reversal at the
> design-review gate.

> **SGE adversarial re-review (2026-07-02):** an independent three-lens re-review
> (`docs/reports/2026-07-02-p3b-design-rereview.md`) returned verdict
> **AMEND-BEFORE-PLANNING** — core architecture sound (D7/D8/D9/D12 confirmed
> against merged reality), **0 blocking · 8 important · 3 minor**. Its confirmed
> findings are folded into this revision: two real P3b×retention/eviction defects
> (strict/warn boot-404 via eviction displacement, and a bytes-less eviction stall),
> a strict-mode downgrade vector (fail-closed on unobtainable verify material), the
> default-`warn`-boots-forgeries provenance gap, Flatcar key rotation/expiry, verify
> single-sourcing, FCOS pass-scoped memoization, and assorted semantics pins. The
> resulting new decisions **D13–D17 are **USER-APPROVED (2026-07-02, all as recommended)**** — the
> user has approved none of them; each carries its alternative on file. A real-signature
> go-crypto spike (finding #7) is recorded below as a **plan prerequisite**.

## 1. Context & problem

P3a shipped the `cache_entries` inventory with `verified`/`verify_err` columns
deliberately NULL, a Cache view, and archive/eviction retention. What is missing —
and what §2.9 specifies — is any integrity check on the bytes booty serves to
booting machines:

- `config.DownloadFile` streams **directly to the final path**; combined with
  `ensureArtifact`'s bare `os.Stat` existence check, a truncated or corrupted
  download is treated as complete **forever** (the file exists, so it is never
  re-fetched, and the boot path serves it).
- No artifact is checksum- or signature-verified. Upstream provides the material:
  FCOS streams JSON carries per-artifact `sha256` (and `.sig` URLs); Flatcar
  publishes detached GPG `.sig` sidecars (verified live 2026-07-01: 622-byte
  signatures next to both PXE artifacts).

## 2. Goals / non-goals

**Goals**

1. **Atomic downloads for every OS** (talos/debian included): temp file → verify →
   rename; a partial download can never be mistaken for a cached artifact.
2. **FCOS: SHA256 verification** from the streams JSON — the same fetch that supplies
   artifact `location` URLs (also fixing the URL-drift class the dash/dot 404 exposed;
   #48 ships the one-line pattern fix, P3b ships the cure).
3. **Flatcar: GPG verification** of `.sig` sidecars against an embedded keyring,
   pure-Go via `github.com/ProtonMail/go-crypto/openpgp`.
4. `--signaturePolicy strict|warn|off`, default **warn**. `warn` exists so a
   *corruption* regression (checksum mismatch) logs without causing a boot outage —
   but it must not silently boot a *forgery*. Per D15 [user-approved 2026-07-02], `warn`
   therefore refuses to land a GPG **signature mismatch** (the tamper signal) while
   still landing a checksum mismatch (the corruption signal); `strict` refuses both.
   The default is not "provenance is advisory" (see §5, §6, §11).
5. Populate `cache_entries.verified`/`verify_err`; expose them in the Cache view and
   a `POST /api/v1/cache/{id}/reverify` endpoint (both P3a No-Wall seams).
6. **Talos/Debian explicitly unverified** (`verified` stays NULL): Cosign/sigstore is
   day-two (§7 of the canonical design).

**Non-goals**

- Cosign/sigstore (Talos) — day-two.
- FCOS GPG (`.sig` against the Fedora key) — §2.9 pins SHA256 for FCOS in v1.
  *Security note recorded:* sha256-from-the-same-HTTPS-source proves transfer
  integrity, not provenance; provenance for FCOS arrives with day-two GPG/Cosign work.
- Per-OS policy granularity — one global flag in v1 (§2.9's "per-OS policies" reduces
  to: the mechanism is per-OS, the policy knob is global).
- Verifying booty's *own* rendered configs (ignition/machineconfig) — out of scope.
- Retroactive un-serving of already-cached unverified bytes in `strict`
  (see §6 — documented limitation).

## 3. The verification seam — `pkg/ostype`

**[user-approved 2026-07-01] Decision: widen `Artifacts` (networked, fallible) and
the `Artifact` struct, rather than adding a parallel `Verifier` interface.**

```go
// Artifact is one downloadable boot file, its upstream URL, and optional
// verification material. Exactly one of SHA256 / SigURL is set when the OS
// supports verification; both empty means "no mechanism" (talos, debian).
type Artifact struct {
    Filename string
    URL      string
    SHA256   string // hex; FCOS (from streams JSON)
    SigURL   string // detached GPG signature sidecar; Flatcar (URL + ".sig")
    GPGKey   []byte // armored public keyring for SigURL (flatcar's embedded key)
}

type OS interface {
    // ...
    Artifacts(ctx context.Context, version, arch string, params map[string]string) ([]Artifact, error)
}
```

Rationale:

- A parallel `VerifySpecs()` method would produce artifact URLs and verify metadata
  from **two code paths that must agree on filenames** — the exact drift class that
  just broke FCOS caching. One networked `Artifacts` call is the single source of
  artifact truth (DRY: the shared knowledge is "what exactly is this version made
  of", and it changes when upstream metadata changes).
- The signature change is contained: `reconcileTarget` is the **only** caller
  (grep-verified; the boot path never calls `Artifacts`). Talos/debian/flatcar
  remain pure functions that ignore `ctx` and return `nil` error.
- `GPGKey` on the artifact keeps flatcar's key inside `pkg/ostype` (the "new OS = one
  new file" seam) instead of leaking OS knowledge into `pkg/cache`. It is a shared
  package-level slice, not a per-artifact copy.

Per-OS behavior:

- **fedoraCoreOS.Artifacts** fetches the channel streams JSON (`fetchMetadata`, same
  30s-bounded helper discovery uses). If `version` equals the stream's current metal
  release: return `location` + `sha256` (+ nothing else; `.sig` unused in v1) for
  kernel/initramfs/rootfs from `architectures.<arch>.artifacts.metal.formats.pxe`.
  Otherwise (manually pinned older version): **pattern fallback** (dot-form
  filenames, no SHA256 → verified NULL for that version) — recorded as a documented
  limitation rather than chasing per-build `meta.json` in v1.
  *Fetch dedupe (origin SGE #6; mechanism refined by re-review #11 → D17 [user-approved 2026-07-02]):*
  the streams document is channel-scoped
  and identical for every version of a target, so it is fetched **at most once per
  channel per reconcile pass**, not once per version. The mechanism is
  **pass-scoped memoization**, not a wall-clock short-TTL: a short TTL does not align
  to reconcile-pass boundaries and is flaky to table-test. Concretely the channel
  stream is fetched once per `reconcileTarget` pass and threaded to every version in
  that pass; reverify's independent single-version call (which does not run inside a
  reconcile pass) takes either a per-call fetch or a small keyed cache. Because
  `fedoraCoreOS{}` is a stateless value receiver and reverify calls arrive on the API
  goroutine (not only the coordinator), any package-level cache must be race-clean and
  carry a **test-reset seam** (a reset hook, or move to pointer-receiver fields) so the
  mandated §10 FCOS table tests stay deterministic and hermetic under `-race`. The
  reset seam is design, not just plan mechanics.
- **flatcar.Artifacts** stays offline: same two artifact URLs, plus
  `SigURL: url + ".sig"`, `GPGKey: flatcarKeyring` — an `//go:embed`ed armored
  public key (`pkg/ostype/keys/flatcar.asc`), provenance-commented with the key
  fingerprint (exact key pinned at plan time from flatcar.org's published signing key).
- **talos / debian**: unchanged shape; empty verification fields.

**Flatcar signing-key rotation & expiry (re-review #5).** The keyring is a compile-time
artifact: rotation or expiry is fixed by a **booty release bump**, not runtime
config. `openpgp.CheckDetachedSignature` rejects an expired or rotated key, so the
design pins the operational story rather than leaving it implicit:

1. **Plan-time expiry check.** Before committing the embedded key, verify it carries
   no near-term expiry and record the expiry date (if any) and fingerprint in the
   plan alongside the fingerprint comment. A key expiring inside the support horizon
   is a plan-time red flag.
2. **Rotation runbook.** Document in `docs/CONFIGURATION.md` that a Flatcar key
   rotation requires a booty release that re-embeds the new key — there is no hot
   reload. This is the accepted cost of the embedded-key seam (D12).
3. **Distinguishable verdicts.** Surface a **distinct `verify_err`** for
   "unknown/expired signing key" (benign rotation/expiry) versus "signature mismatch"
   (tamper) — the two must not read identically, since only the latter is a forgery
   signal (this feeds D15's failure-class split and §5 aggregation).
4. **Strict outage mode.** Note in `docs/CONFIGURATION.md` that under `strict` a
   rotated/expired embedded key halts *all* new Flatcar caching until a booty
   rebuild+redeploy — a provisioning outage fixable only by a code release. Under
   `warn`, an unknown/expired-key verdict is treated as a non-forgery failure
   (lands + records; see §5/D15), so rotation does not cause an outage there.

## 4. Download pipeline — `pkg/config`, `pkg/cache`

`config.DownloadFile` is reworked into staged semantics — `config.DownloadStaged`
(its only caller is `cache.ensureArtifact`; whether the old name remains as a thin
wrapper or disappears is a plan-time subtraction decision):

1. Stream to `<dst>.partial` in the destination directory (same filesystem →
   `os.Rename` is atomic), computing SHA-256 **while streaming** (`io.TeeReader`
   into the hasher — no second disk read).
2. On transport error / non-2xx / short write: delete `.partial`, return error
   (behavior today, minus the poisoned final file).
3. Hand `(partialPath, gotSHA256)` back to the caller; the caller owns verdict,
   land/reject and recording. **Shape pinned per SGE #4** (a single `error`-return
   verify callback entangles verdict with disposition: `warn` must land the bytes
   *and* record the failure, which an `error` return cannot express without a side
   channel): `config.DownloadStaged(ctx, destDir, rawURL string) (partialPath,
   sha256Hex string, err error)`. `pkg/config` owns staging + transport + hashing
   mechanics only; `pkg/cache` owns everything after — one `landArtifact` helper
   verifies per policy, then `os.Rename`s or deletes the partial and records the
   verdict. The "caller could forget rename" risk (old D8 concern) is contained:
   `ensureArtifact` is the single caller and the rename lives inside the one
   helper.

`cache.ensureArtifact`/`landArtifact` evaluate the `Artifact` + policy. The
artifact-level verification and version-level aggregation are **single-sourced**
(D16 [user-approved 2026-07-02], re-review #10): one `pkg/cache` pair — `VerifyArtifact(ctx, filePath,
Artifact) (verdict, err)` and version-level `VerifyVersion(ctx, store, id)` — is
called by **both** the reconcile land-path (`landArtifact`) and the §7 reverify
handler, exactly as `POST /cache/scan` delegates to `cache.Scan`. "Verify an
artifact + aggregate verdicts" is one piece of knowledge (the same shared knowledge
D7 single-sourced for artifact *truth*); two implementations would drift on filename
and verdict details. `VerifyArtifact` does:

- `SHA256` set → compare against the streamed hash (constant-time not required;
  equality on hex strings).
- `SigURL` set → fetch the sidecar (`fetchMetadata`-style, small file), verify the
  detached signature over the `.partial` file with
  `openpgp.CheckDetachedSignature` (ProtonMail/go-crypto, CGO-free) against
  `GPGKey`.

**Fail-closed on unobtainable material (re-review #4).** Once an `Artifact` sets `SHA256`
or `SigURL`, inability to *fetch or evaluate* that material — a declared `.sig` that
404s/times out, a sidecar that won't parse, a declared `sha256` that is missing at
eval time — is a verification **failure** (`verdict = fail`), **never** a demotion to
"no verdict" (NULL). Treating unobtainable material as pass-through would let an
attacker who suppresses the `.sig` fetch demote a verifiable artifact to "always
lands," defeating `strict` without failing a check. Pass-through (NULL) is reserved
**strictly** for artifacts whose verification fields are *empty at eval time* (talos,
debian, FCOS pattern-fallback versions per §3) — i.e. "no mechanism declared," never
"declared but unreachable."

Verdict and disposition are computed separately: `VerifyArtifact` yields
pass/fail/not-verifiable; the policy plus failure class (§5) decides land vs reject;
the recording (§5 aggregation) always happens. No entanglement.

Housekeeping:

- Stale `*.partial` files (crash mid-download) are swept at reconciler startup and
  ignored by size accounting: `reconcileTarget` sums via `artifactPath` (exact
  filenames — already immune); `Scan`'s directory walk **must skip `*.partial`**
  (today it sums every file — small P3b fix).
- Scope of the "never served" guarantee (re-review #9): staging guarantees a `.partial`
  never enters the **boot/menu/TFTP path** (which references only exact artifact
  filenames, never `.partial`). It is *not* an absolute "never served" claim — the
  raw `/data/` `http.FileServer(http.Dir(dataDir))` lists and serves whatever is on
  disk, including an in-flight `.partial` (and pre-existing non-artifact files like
  `booty.db`). Risk is low (nothing links `.partial`), but P3b should exclude
  `*.partial` from the `/data/` FileServer while it is there; broader `/data/`
  exposure hardening is out of scope for this slice.
- `ensureArtifact`'s `os.Stat` existence check is unchanged — it becomes trustworthy
  precisely because nothing lands at the final name unverified/incomplete.
- `reconcileTarget` currently calls `o.Artifacts(...)` **twice** per version (download
  loop + size loop). Once `Artifacts` is networked (FCOS), that doubles upstream
  fetches and adds a flake point to size accounting — P3b calls it **once** per
  version and reuses the slice.

## 5. Policy — `--signaturePolicy strict|warn|off` (default `warn`)

New cobra flag + viper key `config.SignaturePolicy` in `cmd/main.go` (booty has no
`AutomaticEnv`/config file — the flag is required plumbing, per the P3a plan-review
precedent), validated at startup (unknown value → fail-fast).

**Failure classes (D15 [user-approved 2026-07-02], re-review #6).** `warn` must not treat a forgery like
corruption. A verification failure is therefore classified:

- **Signature mismatch (forgery signal)** — a GPG `.sig` that does not validate against
  `GPGKey`. This is a tamper indicator and must **never** silently boot.
- **Corruption / non-forgery failure (corruption signal)** — a SHA256 mismatch, a
  short/garbled sidecar, or an unknown/expired signing key (§3 re-review #5). These are not
  forgery evidence; landing them under `warn` is the availability trade-off `warn`
  exists for.

Per (verifiable) artifact:

| Policy | Verify runs | Pass | Fail — corruption (checksum / bad-sidecar / unknown-or-expired key) | Fail — signature mismatch (forgery) |
|--------|-------------|------|--------------------------------------------------------------------|-------------------------------------|
| `off`  | no          | rename; `verified` untouched (NULL) | — | — |
| `warn` | yes         | rename | **rename anyway**; WARN log; `verified=0`, `verify_err` | **refuse to land** — delete `.partial`, whole version dir removed (§6, version atomicity); ERROR log; `verified=0`, `verify_err` |
| `strict` | yes       | rename | **refuse to land** — delete `.partial`, version dir removed (§6); ERROR log; `verified=0`, `verify_err` | **refuse to land** — same as corruption under strict |

So `strict` refuses **both** classes; `warn` refuses **only** the signature-mismatch
(forgery) class and lands the corruption class. This is what keeps the default from
booting forged Flatcar bytes (§2 goal #4, §6, §11). The alternative — uniform `warn`
that lands every failure plus a prominent CONFIGURATION.md warning — is on file (D15).

**Policy is admission-time, never retroactive.** A policy verdict is applied when a
version is admitted; tightening the policy (e.g. `warn` → `strict`) does **not**
retroactively re-verify or evict a version the reconciler has already settled — the
idempotency skip guard leaves settled (`cached=1`, files-present) versions in place.
The operator's recourse is `POST /cache/{id}/reverify` (§7), which re-checks under the
current policy and re-records `verified=0`; removal stays a manual decision.

Non-verifiable artifacts (talos, debian, FCOS pattern-fallback versions — i.e.
artifacts whose verification fields are *empty at eval time*, per §4's fail-closed
rule): always rename (atomicity still applies); `verified` stays NULL = "no verdict".
NULL is deliberately one state with two readings (no mechanism / not attempted);
`verify_err` and the OS distinguish them where it matters (UI tooltip). "Declared but
unobtainable" verification material is **not** here — it is a failure (§4), not NULL.

**Version-level aggregation** on `cache_entries` (the `VerifyVersion` half of D16):
`verified=1` iff *every* verifiable artifact of the version passed and at least one
was verifiable; `verified=0` if *any* failed; NULL otherwise. `verify_err` is pinned
(re-review #12) to the **`errors.Join` of every failing artifact's message** — not
"first failure only" — so an operator reading the Cache-view tooltip sees *all*
failures across the version's (up to three, for FCOS) artifacts, and the failure-class
text (§3 re-review #5 / §5 re-review #6: "signature mismatch" vs "checksum mismatch"
vs "unknown/expired key") is preserved per artifact. §5, `DATABASE.md`, and `API.md`
must all state this one definition. New store method
`SetCacheVerified(targetVersionID int64, verified *bool, verifyErr string)` —
**NULL-able** (`nil` clears to "no verdict"), because a reverify of a version with
zero verifiable artifacts (e.g. an FCOS pattern-fallback pin) must be able to
express NULL, not a false pass/fail. `UpsertCacheEntry` remains
verification-agnostic (P3a contract: it never clobbers `verified`/`verify_err` —
preserved).

**Failure visibility when a version is rejected:** rejection happens under `strict`
(both failure classes) and under `warn` for the signature-mismatch class (§5 table,
D15). The version never becomes `cached=1`, but the operator must see *why* FCOS/Flatcar
"won't cache". The reconcile failure path writes a `cache_entries` row with
**`in_window=0`**, **size 0** (after the §6 dir removal), and `verified=0`/`verify_err`.
`in_window=0` is deliberate (origin SGE #2), but its rationale is narrower than the prior draft
claimed: it keeps the *failure row itself* from (a) reading as a servable window member,
and (b) joining #48's retention union as an in-window cached version. It does **not**,
by itself, protect the prior-good cached bytes from eviction — see the boot-404 fix
below, which is where that protection actually lives.

`UpsertCacheEntry` hardcodes `in_window=1`, so the failure path needs its own write
(e.g. `UpsertCacheEntryArchived` or upsert + `SetCacheInWindow(false)` — plan decides).
The Cache view shows an archived, failed row with the error tooltip instead of silence.

**Bytes-less rows must not stall eviction (D14 [user-approved 2026-07-02], re-review #2).** A `size=0`
failure row is otherwise a valid `ListArchivedUnpinned` candidate — the query has no
size filter. If its `fetched_at` is oldest it sorts to `candidates[0]`, `evictOverBudget`
deletes it, frees **0 bytes**, and the no-progress guard stops — leaving real archived
bytes behind it unreclaimed and the budget silently exceeded. Fix: **exclude `size=0`
from the eviction candidate set** (`AND ce.size > 0` in `ListArchivedUnpinned`) and from
size accounting entirely — bytes-less rows free nothing, so they are treated like
`*.partial`: visible for their `verify_err`, invisible to the byte budget and the
eviction sweep. (Alternative on file: make the no-progress guard `continue` past
zero-byte deletions rather than stop.)

**Boot-404 fix — never evict the only servable bytes (D13 [user-approved 2026-07-02], re-review #1).**
Independent of the failure row, the *discovered* version list drives retention: under
default `retainN=1` for flatcar/fcos, a rejected `failingNewest` (which is still a
discovered version) occupies the single retained slot, so the last cached `priorGood`
is archived — and once archived it becomes an eviction candidate. If eviction then
deletes it, `NewestCached` has no bytes and the boot 404s, contradicting AC#4. The fix
is a single guard in the eviction query: **never evict the newest cached version of a
target**, even when it is archived. Archived bytes still serve via `NewestCached`
(disk-scan), so preventing their eviction fully prevents the 404 — one rule closes the
whole displacement class (KISS). This holds equally for `strict` and for
`warn`+signature-mismatch, since both leave `priorGood` as the only real bytes on disk.
(Alternatives on file: (a) filter `DiscoverVersions` output through verify state before
`retentionFor` so `failingNewest` never enters the retained set; (c) hold last-known-good
in-window until a replacement verifies.)

## 6. Serving semantics — the only boot-adjacent piece

**[provisional] Admission gating, not serving.** When a version is rejected its
artifacts never land on disk, so `NewestCached` (disk-scan) naturally selects the
prior cached version — §2.9's "fallback to prior cached version, or refuse if none"
falls out with **zero boot-path changes** (an absent dir already reproduces the
pre-first-sync 404 for "refuse if none"). Rejection fires under `strict` (both
failure classes) and, per D15 [user-approved 2026-07-02], under `warn` for the **signature-mismatch
(forgery) class** — so the admission gate is no longer strict-only. A `warn` checksum
failure still lands (it is not gated). The one servable-bytes guarantee that makes the
fallback safe is D13 [user-approved 2026-07-02] (§5): eviction **never removes the newest cached
version of a target**, so the prior-good bytes `NewestCached` needs cannot be evicted
out from under the boot path.

**Version-level atomicity (origin SGE #1, now also warn+signature-mismatch):** per-file
staging alone is not enough — FCOS has three artifacts; if the rootfs fails after
kernel+initramfs already renamed into place, the version *directory* exists and
`NewestCached` (which keys on dir presence, not completeness) would select the broken
newer version and 404 the boot. Therefore whenever a version is **rejected** (any
artifact fails under `strict`, or the signature-mismatch class fails under `warn`), the
reconciler removes the whole version directory (`removeVersionDir` — already exists)
after the errgroup settles. The dir is absent → fallback is clean. `off`, and `warn`
for the checksum/corruption class, are unaffected (everything lands).

Documented limitation: bytes cached under `warn`/`off` that *later* fail (operator
flips to `strict`, or a reverify fails) remain servable — booty does not
retroactively unlink or filter them. Rationale: retroactive enforcement would need
either DB-aware version selection in the boot path (new failure modes in the most
availability-critical code, against the trust-window posture) or auto-deletion of
operator data (DELETE is 403 until P10 even for humans). The operator surface is:
reverify → see `verified=0` in the Cache view → pin/evict/delete (P10) decisions
stay human. Revisit when P10's auth lands if stricter semantics are wanted.

`bootTokens`/menu/TFTP: **no changes in this slice.** Netboot-lab smoke still runs
(P3b touches every download), but asserts the *download/verify* pipeline, not new
boot behavior.

## 7. Reverify — API + reconcile interplay

`POST /api/v1/cache/{id}/reverify` (open in the trust window — non-destructive):

1. Resolve the entry → target → OS + params (the join `GetCacheEntry` already does).
2. Delegate to the shared `cache.VerifyVersion(ctx, store, id)` (D16 [user-approved 2026-07-02],
   re-review #10) — the **same** function the reconcile land-path uses — exactly as
   `POST /cache/scan` delegates to `cache.Scan`. It calls `Artifacts(ctx, version,
   arch, params)` and, for each verifiable artifact, recomputes sha256 from the
   **on-disk final file** / re-fetches and re-checks the `.sig`, then aggregates
   (`errors.Join` per §5). This is what keeps reverify and the land-path from drifting
   on filename/verdict details, and it makes reverify's FCOS-fetch path testable in
   `pkg/cache`.
3. `SetCacheVerified(...)`, return the refreshed `CacheEntryDTO`.

**Absent-file semantics (re-review #8).** A missing **final** file is a failure
(`verify_err: "artifact absent"`) **only** when no download is in progress. If the
final file is absent but a sibling `<artifact>.partial` exists, a (re)download is in
flight — reverify records **NULL (no verdict)**, not a failure, and self-heals when the
download lands. (The window is narrow — a fresh version has no `cache_entries` row so
reverify 404s — but real for a re-download of an evicted version or a lingering crash
`.partial`.)

Reverify **ignores `--signaturePolicy off`** — an explicit operator ask always
verifies [provisional]. It never deletes or moves files regardless of policy
(the admission gate is download-time only, keeping §6's contract).

Runs on the API goroutine (like `POST /cache/scan`), read-only on disk +
single-row DB write — no coordinator hand-off needed. Reconcile ticks do not
re-verify existing files (the `os.Stat` short-circuit is untouched); verification
happens on download and on demand.

## 8. Data model & API surface

- **No migration**: `verified INTEGER NULL` / `verify_err TEXT` shipped in P3a's
  `0002_cache_entries.sql`. P3b only writes them.
- `CacheEntryDTO` gains `verified *bool` + `verifyErr string` (`omitempty`);
  `cacheState()` unchanged (verification is orthogonal to in-window/pinned).
- New endpoint: `reverify-cache` op, `POST /cache/{id}/reverify` (§7).
- `web/src/api/cache.ts`: `verified?: boolean | null`, `verifyErr?: string`,
  `reverifyCacheEntry(id)`.
- `web/src/views/CacheView.tsx`: a "Verified" column — ✓ (verified), ✗ with
  `verify_err` tooltip (failed), — (NULL); a per-row Reverify action beside Pin/Unpin.

## 9. Dependencies

One new direct dependency: `github.com/ProtonMail/go-crypto` (pure Go, maintained
OpenPGP fork; justification: stdlib has no OpenPGP — `golang.org/x/crypto/openpgp`
is frozen/deprecated; 50 lines cannot reimplement signature verification safely).
SHA-256 is stdlib `crypto/sha256`. No CGO. Binary-size impact negligible (vs the
talhelper 84MB cautionary tale on file for P6).

**Plan prerequisite — real-Flatcar-signature spike (re-review #7).** `go-crypto` has
**not** been proven against real Flatcar signatures — the only evidence so far is
"622-byte `.sig` files exist". go-crypto rejects legacy digest algorithms (e.g. SHA-1)
by default and is strict about ASCII-armored vs binary detached-signature framing and
public-key packet format; if Flatcar's production signatures trip any of these, the
mechanism fails only after implementation, and a test-generated keypair (§10) cannot
catch a real-format incompatibility. Therefore a plan session **must run this spike
before task breakdown**: fetch a real Flatcar PXE artifact + `.sig` + the published
key and confirm `openpgp.CheckDetachedSignature` returns success; record the digest
algorithm and armored-vs-binary handling in the plan. If it fails, the
dependency/verification-path choice (ProtonMail/go-crypto) reopens before any code is
written.

> **SPIKE RESULT (2026-07-02, plan session) — PASSED.** ProtonMail/go-crypto **v1.4.1**
> `openpgp.CheckDetachedSignature` verified the real
> `flatcar_production_pxe.vmlinuz` + its production 622-byte `.sig` against the
> published `Flatcar_Image_Signing_Key.asc`:
> - **Framing:** the `.sig` is a **binary** detached signature (not armored) →
>   use `CheckDetachedSignature`, not the `Armored` variant; the key file itself
>   IS armored → `ReadArmoredKeyRing`.
> - **Digest:** SHA-256 (no legacy-SHA-1 rejection issue). Signer: RSA subkey.
> - **Tamper check:** bit-flip fails with `openpgp: invalid signature: RSA
>   verification failure` — correct.
> - **Rotation reality (feeds §3's rotation subsection):** primary key
>   `F88CFEDEFF29A5B4D9523864E25D9AED0593B34A` (Flatcar Buildbot) never expires;
>   signing uses **rotating subkeys** — the bundle carries 8 expired subkeys and
>   the ACTIVE signing subkey `52F145DFD00BBDCD928CBB5A32DA80F91EF52974` expires
>   **2027-03-08**. The embedded keyring must be refreshed via a booty release
>   before that date (or when Flatcar rotates early) — pin this date in
>   CONFIGURATION.md's rotation runbook.
> The ProtonMail/go-crypto dependency choice is CONFIRMED.

## 10. Testing

- `ostype`: FCOS `Artifacts` against an httptest streams JSON (current version →
  locations+sha256; non-current → pattern fallback, empty SHA256; missing arch →
  error); flatcar artifacts carry `SigURL`/keyring; talos/debian unchanged shape.
  **FCOS pass-scoped memoization (D17):** the streams JSON is fetched once per pass
  (assert the httptest hit count across a multi-version pass), and the test-reset seam
  keeps the table tests deterministic under `-race`.
- `config`/`cache` download pipeline (`t.TempDir()`, httptest): truncated body →
  no final file, `.partial` gone; sha256 mismatch under each policy (off/warn/strict
  land-vs-reject table); GPG happy-path + bad-sig using a test-generated keypair (not
  the real flatcar key); crash-sim stale `.partial` swept; `Scan` skips `.partial`.
  **Failure-class table (D15):** under `warn`, a GPG signature mismatch **rejects** and
  removes the version dir (asserts prior version still serves) while a checksum mismatch
  **lands** with `verified=0`; under `strict`, both reject. **Fail-closed (re-review #4):**
  a declared `.sig` that 404s/errors and a declared `sha256` that is missing both yield
  a **fail** verdict (reject under strict), never NULL. **Eviction rules:** the newest
  cached version of a target is never evicted even when archived (D13); a `size=0`
  failure row is excluded from the candidate set and frees no budget (D14).
- `db`: `SetCacheVerified` round-trip; `UpsertCacheEntry` still never clobbers
  verified (P3a regression guard extended).
- `http`: reverify happy/404/absent-file paths on the real-fixture harness
  (`newTestAPI` + httptest talos factory — note talos is NULL-verified, so the FCOS
  httptest fixture from `ostype` tests is reused here). **Absent-file split (re-review #8):**
  final file absent with **no** `.partial` → failure (`verify_err: "artifact absent"`);
  final file absent **with** a sibling `.partial` → NULL (no verdict), not failure.
- Frontend: Vitest — verified column three states + reverify action wiring.
- **Netboot-lab smoke** (pre-merge, like P3a/#44): full cycle with `warn` default —
  fresh FCOS + Flatcar cache with verification passing (`verified=1` in Cache view),
  then a forced mismatch (tampered local file + reverify → ✗) — validating the
  operator loop live.

## 11. Documentation gate

- `docs/schema/DATABASE.md`: `verified`/`verify_err` now populated; NULL semantics;
  the pinned `verify_err` definition (re-review #12) — the `errors.Join` of every
  failing artifact's message, carrying the failure-class text per artifact.
- `docs/schema/API.md`: reverify endpoint; DTO fields; the same `verify_err`
  definition (must match DATABASE.md verbatim).
- `docs/schema/STORAGE.md`: `.partial` staging, atomicity, sweep.
- `docs/CONFIGURATION.md`: `--signaturePolicy` (values, default `warn`, strict
  semantics + admission-only limitation, Talos/Debian NULL, FCOS provenance note).
  **Prominent expectation-setting (origin SGE #7):** `strict` means "verifiable
  artifacts that fail verification do not land" — it does **not** refuse OSes or
  versions that have no verification mechanism (Talos, Debian, FCOS pattern-fallback
  pins); those land with `verified=NULL` under every policy.
  **Failure-class default (re-review #6, D15):** state prominently what the **default
  `warn`** does — a GPG **signature mismatch** (forgery signal) does **not** boot even
  under `warn` (it is refused, like `strict`); a **checksum mismatch** (corruption
  signal) still lands and logs. `warn` is therefore not "provenance is advisory."
  (If the D15 alternative — uniform `warn` that lands all failures — is chosen instead,
  this section must instead warn **prominently** that a FAILED Flatcar signature still
  boots under the default, and recommend `strict` as the production default.)
  **Flatcar key rotation/outage (re-review #5):** a Flatcar signing-key rotation or
  expiry requires a booty release that re-embeds the new key (no hot reload); under
  `strict` a rotated/expired embedded key halts all new Flatcar caching until that
  rebuild+redeploy (a provisioning outage fixable only by a code release), whereas
  under `warn` an unknown/expired-key verdict is a non-forgery failure that still lands.
- `README.md`: one line in the feature list (verification exists, default warn).

## 12. Constraints (unchanged project invariants)

Module `github.com/jeefy/booty`; PR to `jacaudi/booty` (after #48); CGO-free Go
1.26; `log/slog`; Huma v2; trust window (mutating open, DELETE 403 until P10);
`target_versions.cached` stays coarse — `cache_entries` is the authoritative
detail; P1b/P3a write paths not reshaped (P3b adds `SetCacheVerified` + the
failure-path upsert, nothing else); layout helpers not forked.

## 13. Acceptance criteria

1. Every download (all OSes) is staged `.partial` → rename; a killed download never
   leaves a final-named file; stale `.partial` swept and excluded from sizes; no
   `.partial` ever enters the boot/menu/TFTP path (the absolute "never served" claim
   does not cover the raw `/data/` FileServer — re-review #9).
2. FCOS current-version artifacts resolve from streams JSON (URL + sha256) and
   verify; Flatcar artifacts GPG-verify against the embedded key; Talos/Debian
   remain NULL-verified.
3. `--signaturePolicy` behaves per the §5 table; default `warn`; invalid value
   fails startup.
4. `strict`: a failed version (either failure class) never lands; `warn`: a GPG
   signature-mismatch (forgery) version never lands, a checksum-mismatch version lands
   + logs + `verified=0` (D15). In every rejected case the prior version keeps serving —
   and eviction never removes the newest cached version of a target, so the servable
   prior-good bytes cannot be evicted out from under the boot path (D13); a `size=0`
   failure row never stalls eviction (D14). Failure visible in Cache view.
5. `POST /cache/{id}/reverify` recomputes state from disk; Cache view shows the
   three-state Verified column with error tooltip and reverify action.
6. Docs gate (§11) complete; unit + race suites green; netboot-lab smoke passed.

## Appendix — decisions taken while user AFK (review these first)

| # | Decision | Recommended-and-taken | Alternative on file |
|---|----------|----------------------|---------------------|
| D7 | Verify seam | **USER-APPROVED**: Widen `Artifacts(ctx,…) ([]Artifact, error)` + Artifact fields (single source of artifact truth) | Parallel optional `Verifier` interface (two filename-agreeing code paths — drift risk) |
| D8 | Download shape | `config.DownloadStaged` returns `(partialPath, sha256Hex, err)`; `pkg/cache`'s single `landArtifact` helper owns verdict + land/reject + recording (pinned per SGE #4 — a verify-callback `error` return can't express warn's "land but record failure") | Verify callback inside `DownloadFile` (entangles verdict with disposition) |
| D9 | Strict scope | Admission-only; documented no-retroactive-unserving | DB-aware boot-path filtering (new failure modes in the availability-critical path) |
| D10 | FCOS old versions | Pattern fallback, NULL verified | Per-build `meta.json` fetch (more upstream surface for a pin-an-old-build edge) |
| D11 | Reverify vs `off` | Explicit ask always verifies | Honor `off` (makes the button a no-op) |
| D12 | Keyring placement | `GPGKey` on Artifact, key embedded in `pkg/ostype` | Keyring registry in `pkg/cache` (leaks OS knowledge across the seam) |
| D13 | Strict/warn boot-404 (re-review #1) | **[USER-APPROVED 2026-07-02]** Eviction never evicts the newest cached version of a target (one guard in the eviction query); archived bytes still serve via `NewestCached`, so preventing eviction fully prevents the 404 (§5, §6) | (a) filter `DiscoverVersions` output through verify state before `retentionFor`; (c) hold last-known-good in-window until a replacement verifies |
| D14 | Bytes-less eviction stall (re-review #2) | **[USER-APPROVED 2026-07-02]** Exclude `size=0` rows from the eviction candidate set (`AND ce.size > 0`) and from size accounting — treated like `*.partial` (visible for `verify_err`, invisible to budget/sweep) (§5) | Make the no-progress guard `continue` past zero-byte deletions instead of stopping |
| D15 | `warn` failure classes (re-review #6) | **[USER-APPROVED 2026-07-02]** Distinguish a GPG **signature mismatch** (forgery → refuse to land even under `warn`) from a **checksum/non-forgery** failure (corruption → keep warn-and-land); `strict` refuses both (§2 goal #4, §5, §6, §11) | Uniform `warn` (lands every failure) + prominent CONFIGURATION.md warning that a FAILED signature still boots under the default |
| D16 | Verify single-sourcing (re-review #10) | **[USER-APPROVED 2026-07-02]** One `pkg/cache` pair — `VerifyArtifact` + version-level `VerifyVersion` — called by **both** the reconcile land-path and the reverify handler (handler delegates as scan does to `cache.Scan`) (§4, §7) | Reverify reimplements verify+aggregate in the `pkg/http` handler (two filename/verdict paths — drift risk) |
| D17 | FCOS fetch memoization (re-review #11) | **[USER-APPROVED 2026-07-02]** Pass-scoped memoization — fetch the channel stream once per `reconcileTarget` pass and thread it; reverify's single-version call takes a per-call fetch or keyed cache; with a race-clean test-reset seam (§3) | Wall-clock short-TTL memoization (misaligns to reconcile-pass boundaries, flaky to table-test) |
