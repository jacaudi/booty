# P3b — Signature verification — Design

**Date:** 2026-07-01 · **Slice:** P3b (deferred half of roadmap P3; P3a merged as PR #49) ·
**Depends on:** issue #48's params-driven drivers (separate PR, lands first — see
`2026-07-01-issue-48-params-driven-channels-design.md`) · **Spec:** canonical v1 design §2.9.

> **Session note:** drafted with the user away from keyboard. Decisions marked
> **[provisional]** follow the recommended option and are up for reversal at the
> design-review gate.

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
4. `--signaturePolicy strict|warn|off`, default **warn** (a verification regression
   logs; it must not cause a boot outage).
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
  *Fetch dedupe (SGE #6):* the streams document is channel-scoped and identical for
  every version of a target, so it is fetched **at most once per channel per
  reconcile pass**, not once per version — a short-TTL memoization inside the FCOS
  driver (mutex-guarded: reverify calls arrive on the API goroutine, not only the
  coordinator). Mechanism detail is a plan decision; the at-most-once requirement
  is design.
- **flatcar.Artifacts** stays offline: same two artifact URLs, plus
  `SigURL: url + ".sig"`, `GPGKey: flatcarKeyring` — an `//go:embed`ed armored
  public key (`pkg/ostype/keys/flatcar.asc`), provenance-commented with the key
  fingerprint (exact key pinned at plan time from flatcar.org's published signing key).
- **talos / debian**: unchanged shape; empty verification fields.

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

`cache.ensureArtifact`/`landArtifact` evaluate the `Artifact` + policy:

- `SHA256` set → compare against the streamed hash (constant-time not required;
  equality on hex strings).
- `SigURL` set → fetch the sidecar (`fetchMetadata`-style, small file), verify the
  detached signature over the `.partial` file with
  `openpgp.CheckDetachedSignature` (ProtonMail/go-crypto, CGO-free) against
  `GPGKey`.
- Verdict and disposition are computed separately: verify yields pass/fail/not-verifiable;
  the policy (§5) decides land vs reject; the recording (§5 aggregation) always
  happens. No entanglement.

Housekeeping:

- Stale `*.partial` files (crash mid-download) are swept at reconciler startup and
  ignored by size accounting: `reconcileTarget` sums via `artifactPath` (exact
  filenames — already immune); `Scan`'s directory walk **must skip `*.partial`**
  (today it sums every file — small P3b fix).
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

Per (verifiable) artifact:

| Policy | Verify runs | Pass | Fail |
|--------|-------------|------|------|
| `off`  | no          | rename; `verified` untouched (NULL) | — |
| `warn` | yes         | rename | **rename anyway**; WARN log; `verified=0`, `verify_err` |
| `strict` | yes       | rename | **delete `.partial`, artifact never lands** — and the whole version dir is removed (§6, version atomicity); ERROR log; `verified=0`, `verify_err` |

Non-verifiable artifacts (talos, debian, FCOS pattern-fallback versions): always
rename (atomicity still applies); `verified` stays NULL = "no verdict". NULL is
deliberately one state with two readings (no mechanism / not attempted);
`verify_err` and the OS distinguish them where it matters (UI tooltip).

**Version-level aggregation** on `cache_entries`: `verified=1` iff *every*
verifiable artifact of the version passed and at least one was verifiable;
`verified=0` if *any* failed (`verify_err` = first failure, `errors.Join`-style
message); NULL otherwise. New store method
`SetCacheVerified(targetVersionID int64, verified *bool, verifyErr string)` —
**NULL-able** (`nil` clears to "no verdict"), because a reverify of a version with
zero verifiable artifacts (e.g. an FCOS pattern-fallback pin) must be able to
express NULL, not a false pass/fail. `UpsertCacheEntry` remains
verification-agnostic (P3a contract: it never clobbers `verified`/`verify_err` —
preserved).

**Failure visibility in strict mode:** when artifacts are rejected the version never
becomes `cached=1`, but the operator must see *why* FCOS "won't cache". The reconcile
failure path writes a `cache_entries` row with **`in_window=0`** (size 0 after the
§6 dir removal) and sets `verified=0`/`verify_err`. `in_window=0` is deliberate
(SGE #2): a bytes-less rejected version is not a servable window member, must not
join #48's retention union (which counts only in-window **cached** versions —
belt-and-braces on both sides), and must not shelter behind window-protection while
eviction pressure deletes real archived bytes. Note `UpsertCacheEntry` hardcodes
`in_window=1`, so the failure path needs its own write (e.g.
`UpsertCacheEntryArchived` or upsert + `SetCacheInWindow(false)` — plan decides).
The Cache view shows an archived, failed row with the error tooltip instead of
silence.

## 6. Serving semantics — the only boot-adjacent piece

**[provisional] Strict gates admission, not serving.** In `strict`, a rejected
version's artifacts never land on disk, so `NewestCached` (disk-scan) naturally
selects the prior cached version — §2.9's "fallback to prior cached version, or
refuse if none" falls out with **zero boot-path changes** (an absent dir already
reproduces the pre-first-sync 404 for "refuse if none").

**Version-level atomicity in strict (SGE #1):** per-file staging alone is not
enough — FCOS has three artifacts; if the rootfs fails after kernel+initramfs
already renamed into place, the version *directory* exists and `NewestCached`
(which keys on dir presence, not completeness) would select the broken newer
version and 404 the boot. Therefore in `strict`, when **any** artifact of a
version is rejected, the reconciler removes the whole version directory
(`removeVersionDir` — already exists) after the errgroup settles. The dir is
absent → fallback is clean. `warn`/`off` are unaffected (everything lands).

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
2. Call `Artifacts(ctx, version, arch, params)`; for each verifiable artifact,
   recompute sha256 from the **on-disk file** / re-fetch and re-check the `.sig`.
   Missing file → failure (`verify_err: "artifact absent"`).
3. `SetCacheVerified(...)`, return the refreshed `CacheEntryDTO`.

Reverify **ignores `--signaturePolicy off`** — an explicit operator ask always
verifies [provisional]. It never deletes or moves files regardless of policy
(strict's admission gate is download-time only, keeping §6's contract).

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

## 10. Testing

- `ostype`: FCOS `Artifacts` against an httptest streams JSON (current version →
  locations+sha256; non-current → pattern fallback, empty SHA256; missing arch →
  error); flatcar artifacts carry `SigURL`/keyring; talos/debian unchanged shape.
- `config`/`cache` download pipeline (`t.TempDir()`, httptest): truncated body →
  no final file, `.partial` gone; sha256 mismatch under each policy (off/warn/strict
  land-vs-reject table); GPG happy-path + bad-sig + missing-sidecar using a
  test-generated keypair (not the real flatcar key); crash-sim stale `.partial`
  swept; `Scan` skips `.partial`.
- `db`: `SetCacheVerified` round-trip; `UpsertCacheEntry` still never clobbers
  verified (P3a regression guard extended).
- `http`: reverify happy/404/absent-file paths on the real-fixture harness
  (`newTestAPI` + httptest talos factory — note talos is NULL-verified, so the FCOS
  httptest fixture from `ostype` tests is reused here).
- Frontend: Vitest — verified column three states + reverify action wiring.
- **Netboot-lab smoke** (pre-merge, like P3a/#44): full cycle with `warn` default —
  fresh FCOS + Flatcar cache with verification passing (`verified=1` in Cache view),
  then a forced mismatch (tampered local file + reverify → ✗) — validating the
  operator loop live.

## 11. Documentation gate

- `docs/schema/DATABASE.md`: `verified`/`verify_err` now populated; NULL semantics.
- `docs/schema/API.md`: reverify endpoint; DTO fields.
- `docs/schema/STORAGE.md`: `.partial` staging, atomicity, sweep.
- `docs/CONFIGURATION.md`: `--signaturePolicy` (values, default `warn`, strict
  semantics + admission-only limitation, Talos/Debian NULL, FCOS provenance note).
  **Prominent expectation-setting (SGE #7):** `strict` means "verifiable artifacts
  that fail verification do not land" — it does **not** refuse OSes or versions
  that have no verification mechanism (Talos, Debian, FCOS pattern-fallback pins);
  those land with `verified=NULL` under every policy.
- `README.md`: one line in the feature list (verification exists, default warn).

## 12. Constraints (unchanged project invariants)

Module `github.com/jeefy/booty`; PR to `jacaudi/booty` (after #48); CGO-free Go
1.26; `log/slog`; Huma v2; trust window (mutating open, DELETE 403 until P10);
`target_versions.cached` stays coarse — `cache_entries` is the authoritative
detail; P1b/P3a write paths not reshaped (P3b adds `SetCacheVerified` + the
failure-path upsert, nothing else); layout helpers not forked.

## 13. Acceptance criteria

1. Every download (all OSes) is staged `.partial` → rename; a killed download never
   leaves a final-named file; stale `.partial` swept and excluded from sizes.
2. FCOS current-version artifacts resolve from streams JSON (URL + sha256) and
   verify; Flatcar artifacts GPG-verify against the embedded key; Talos/Debian
   remain NULL-verified.
3. `--signaturePolicy` behaves per the §5 table; default `warn`; invalid value
   fails startup.
4. `strict`: a tampered/failed version never lands, prior version keeps serving,
   failure visible in Cache view; `warn`: lands + logs + `verified=0`.
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
