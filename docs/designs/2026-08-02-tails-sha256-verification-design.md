# Design: verify the Tails ISO against its published sha256

**Issue:** [jacaudi/booty#76](https://github.com/jacaudi/booty/issues/76)
**Status:** design — awaiting Gate 1 review
**Supersedes/extends:** `docs/designs/2026-07-29-tool-rescue-os-support-design.md` §8.5 (the recorded
deferral), `docs/designs/2026-07-01-p3b-signature-verification-design.md` (the policy model this
extends rather than reinvents)

---

## 1. Goal

Make booty verify `tails-amd64.iso` — a 1.94 GB anonymity distro executed with full hardware
privilege on bare metal — against the sha256 its own release publishes, instead of caching it
entirely unverified.

The blocker is structural, not clerical. The Tails ISO is `Large: true` (tool/rescue OS design
**D13**, the resumable downloader), and `pkg/cache/verify.go`'s `landArtifact` *hard-fails* a
`Large` artifact that declares any verification material:

```go
if a.Large {
    if a.SHA256 != "" || a.SigURL != "" {
        return false, artifactVerdict{}, fmt.Errorf(
            "cache: %s: Large artifacts carry no verification path, but sha256/sig was declared", a.Filename)
    }
```

Setting `Artifact.SHA256` today would **break caching, not verify it**. The resumable path has no
verification path at all. So the work is: give `downloadLargeFile` a verification path, source the
digest, and narrow — never delete — that fail-closed guard.

## 2. Evidence: what is verified, and what is assumed

This section is deliberately first. Several claims in the issue were unverified when it was filed;
they were checked against primary sources on 2026-08-02 before this design was written.

### Verified (checked directly, this session)

| Claim | How it was checked | Result |
|---|---|---|
| Every Tails release publishes `sha256-checksums.txt` | `gh api repos/netbootxyz/asset-mirror/releases --paginate`, filtered to releases carrying a `tails*` asset, then filtered to those *lacking* the sidecar | **76 of 76** carry it. Zero exceptions. |
| No other tool publishes one | Same sweep, inverted: releases with `sha256-checksums.txt` and no `tails` asset | **Empty.** Only Tails releases carry it. |
| Sidecar format | `curl` on releases `7.10-17629562`, `7.3.1-00388326`, `6.6-cfd50f75`, `6.15.1-00388326`, rendered with `sed -n l` | A single LF-terminated line, `<64 hex>` + **two spaces** + `<filename>`. No `./` prefix, no binary-mode `*` marker, no trailing blank line. |
| The ISO filename in the sidecar tracks the release | Same four fetches | `tails-amd64.iso` on current releases; older ones used a version-stamped `tails-amd64-6.6.iso`. The sidecar key always matches that release's asset name. |
| The published digest matches the bytes booty actually caches | `shasum -a 256` on the slice-2 lab's cached ISO at `/tmp/booty-lab2/data/cache/tails/-/amd64/7.10-17629562/tails-amd64.iso` | `6dab23b2…d1743` — **byte-identical** to the sidecar. The mechanism is validated end-to-end against real cached bytes, not just against documentation. |
| Cost of hashing 1.94 GB | `time shasum -a 256` on that file | **3.6 s** (macOS, SSD, page cache possibly warm). Go's `crypto/sha256` with SHA-NI is faster than `shasum`'s Perl implementation, so disk throughput dominates; a slow spindle at ~100 MB/s is ~20 s. Negligible beside the 1.94 GB transfer itself, and paid once per version download. |
| The format is already parsed in this repo | Read `pkg/cache/debiandvd.go:479-499` | `parseSHA256SUMS` parses exactly this shape (`strings.Cut(line, "  ")`) for Debian's `SHA256SUMS`. |
| `pkg/ostype` cannot import `pkg/cache` | `grep` of ostype's imports; `pkg/cache/{catalog,debiandvd,layout}.go` import ostype | ostype imports **only** `pkg/config`. The dependency runs cache → ostype, so any shared helper must live in `pkg/config` or lower. |
| Default signature policy | `pkg/config/config.go:91`, `config_test.go:98` | `warn`. |

### Assumed (stated, not provable)

- **That netboot.xyz keeps publishing the sidecar.** 76/76 is strong evidence about the past and
  says nothing certain about the next release. This is precisely why **D2** fails loud instead of
  silently degrading to unverified — a future omission must be visible, not absorbed.
- **That the sidecar and the ISO share a trust boundary.** See §8: this defends against corruption,
  not against a compromised release. That is a property of the design, not a gap in verification.
- **That the sidecar continues to list the ISO under the same name the manifest declares.** If
  upstream renames the asset, the *existing* allowlist check (`allowlisted file %q is not in the
  manifest entry`) fires first, so this degrades to a loud, already-designed failure.

---

## 3. Decisions

### D1 — the digest is resolved in `pkg/ostype` into `Artifact.SHA256`

`netbootxyzOS` gains one registration field, `checksums string`, holding the sidecar's filename.
Only Tails sets it (`sha256-checksums.txt`). `Artifacts()` fetches the sidecar from the same release
path, parses it, looks the ISO up by filename, and populates `Artifact.SHA256`.

**Rejected alternative — a new `Artifact.SumsURL` field parallel to `SigURL`.** It is superficially
attractive (`SigURL` is also a URL the cache layer fetches at verify time, and it would avoid moving
the parser). It was rejected on DRY grounds: `SigURL` declares a *different mechanism* that composes
with a digest, whereas `SumsURL` and `SHA256` would be two representations of one fact — "the
expected digest of this file" — forcing an implicit precedence rule into `verifyArtifact`. Resolving
to `SHA256` keeps exactly one representation and matches the existing FCOS precedent (streams JSON →
`SHA256`) rather than inventing a third pattern.

**Consequence — this is why the design is small.** Because the digest lands in the field the system
already understands, `verifyArtifact`, `landArtifact`'s disposition logic, `aggregateVerdicts`, the
`verified`/`verify_err` columns, and the API's verdict semantics all work **unchanged**.

**Rejected alternative — fetch the sidecar in `pkg/cache` at land time.** Keeps every helper in one
package, but puts a tool-specific special case in the cache layer, breaks the "an `Artifact`
declares its own verification material" model, and leaves `VerifyVersion` structurally unable to
re-check it.

### D2 — a missing or unusable sidecar fails loud

Sidecar unfetchable, malformed, or missing an entry for the ISO ⇒ `Artifacts()` returns an error.

This is the fail-closed posture `verify.go` already states ("a DECLARED sha256/.sig that cannot be
evaluated is corruption, never NULL"), and it is cheap here because of where the error lands:
`pkg/cache/reconcile.go:138-143` already treats an `Artifacts` failure as *log a warning and skip
this version this tick*, explicitly declining to touch the row so a settled version survives a
transient upstream blip. So fail-loud costs the availability of **updates**, never the availability
of the **existing cache** — the currently-cached Tails keeps booting.

The alternative — falling back to `classNotVerifiable` when the sidecar is absent — was rejected
because it is a *silent security downgrade*: booty would revert to today's unverified behaviour with
no operator-visible signal, which is the failure mode most worth designing against.

### D3 — hash the completed file on disk, never the stream

`downloadLargeFile` resumes via an HTTP `Range` header, so a resumed transfer's stream contains only
the *delta*. Hashing what this attempt copied is therefore wrong. It is wrong a second, independent
way on the **416** branch, where a prior attempt already wrote every byte and this attempt streams
*nothing at all*.

`verifyArtifact` already hashes `filePath` when `streamedSHA256 == ""`, so this requires no new
hashing code — the Large path simply passes an empty streamed hash and `hashFile` reads the finished
file.

**Rejected alternative — persist incremental hash state across resumes.** Go's `crypto/sha256`
digest does implement `encoding.BinaryMarshaler`, so this is possible: marshal the digest beside the
`.download` file, restore on resume. It was rejected under KISS/YAGNI — it introduces a second
on-disk state file that must stay exactly in sync with the byte count (and silently produces a wrong
digest if it ever doesn't), to save a measured 3.6 s.

### D4 — the `Large` guard is narrowed, not deleted

The guard becomes:

```go
if a.Large && a.SigURL != "" {
    return false, artifactVerdict{}, fmt.Errorf(
        "cache: %s: detached-signature verification is unsupported for resumable (Large) downloads", a.Filename)
}
```

Still fail-closed, still refusing to land bytes whose declared material it cannot evaluate — now
scoped to the material that genuinely has no path (§9). The sha256 half is removed *because the path
now exists*, which is the only honest reason to remove a fail-closed guard.

### D5 — reverify covers tools that declare material

Lift `VerifyVersion`'s `family == "tool"` short-circuit (`verify.go:346`). See §6 for the two
non-local invariants this disturbs, both of which are handled.

### D6 — a rejected version is not re-downloaded until a retry window elapses

Version-level, in `reconcileTarget`, applying to **every** OS — not only Tails and not only `Large`.
See §7.

### D7 — base on `main` after PR #75 merges

PR #75 (`worktree-tool-rescue-os-slice2`, 20 commits) is open and touches `pkg/cache/verify.go`,
`pkg/cache/isodownload.go`, and `pkg/ostype/netbootxyz.go` — the exact files this work lands in.
Implementation waits for #75 to merge and branches from `main`. Branching from #75's head was
considered and rejected: it buys an earlier start at the cost of carrying 20 commits and a mandatory
rebase if #75 is amended.

---

## 4. Sourcing the digest (`pkg/ostype`)

### 4.1 Registration

```go
register(netbootxyzOS{
    name:      "tails",
    endpoints: map[string]string{"amd64": "tails"},
    files:     []string{"vmlinuz", "initrd.img", "9990-misc-helpers.sh", "tails-amd64.iso"},
    large:     map[string]bool{"tails-amd64.iso": true},
    // Every Tails release in netbootxyz/asset-mirror (76/76 checked 2026-08-02)
    // publishes this sidecar; no other tool's releases do. It is NOT in `files`:
    // it is verification material, never cached and never served.
    checksums: "sha256-checksums.txt",
})
```

Per-tool **data**, so a second tool that ever gains a sidecar is a one-line registration change with
no sibling touched (patchbay). Tools without the field behave exactly as today.

### 4.2 The sidecar is deliberately outside the manifest allowlist

`Artifacts()` refuses any allowlisted file absent from the manifest entry (`upstream may have
renamed or dropped it`). The sidecar **must be exempt from that check**, because `endpoints.yml`
never lists it — that omission is the entire premise of #76.

This is the one place the design derives an artifact URL from a filename booty hardcodes rather than
one upstream declares, and it deserves to be named rather than buried. It is bounded: the filename
is a compile-time constant in booty's own registration data, the URL is composed by the existing
`artifactURL(base, e.Path, …)` (which already rejects a manifest path that could relocate the
request), and the fetched bytes are only ever *compared against*, never executed, cached, or served.

The sidecar is **not** added to `files`, so `ToolFiles()`, the boot-script↔allowlist bidirectional
test, and everything `/data/` serves are untouched.

### 4.3 Fetch, parse, populate

```go
func (t netbootxyzOS) Artifacts(ctx, version, arch string, _ map[string]string) ([]Artifact, error) {
    // ... existing entry/tag/allowlist logic, unchanged ...
    var sums map[string]string
    if t.checksums != "" {
        u, err := artifactURL(base, e.Path, t.checksums)   // same composition as any artifact
        if err != nil { return nil, ... }
        sums, err = fetchChecksums(ctx, u)                 // memoized; see 4.4
        if err != nil { return nil, ... }                  // D2: fail loud
    }
    for _, f := range names {
        // ... existing manifest-membership check + artifactURL ...
        a := Artifact{Filename: f, URL: u, Large: t.large[f]}
        if sums != nil {
            if d, ok := sums[f]; ok {
                a.SHA256 = d
            }
            // A file absent from the sidecar stays not-verifiable: the Tails
            // sidecar lists ONLY the ISO, so vmlinuz/initrd.img/9990-misc-helpers.sh
            // are legitimately unlisted. Absence is not an error here.
        }
        out = append(out, a)
    }
    return out, nil
}
```

**Lookup is by filename, never "it's the only line."** The sidecar has exactly one line today, but
keying on `a.Filename` is what makes an upstream rename fail loudly instead of silently attaching
the wrong file's digest to the ISO.

**Only the ISO gets a digest**, so `aggregateVerdicts` sees one verifiable artifact and three
not-verifiable ones. Its rule — verified iff every verifiable artifact passed *and* at least one was
verifiable — then records `verified=1` for a good Tails version, which is a visible improvement in
the API and UI (today it is `NULL`).

### 4.4 Memoization

`fetchChecksums` memoizes by URL in the existing `netbootxyzCache` struct, cleared by the same
`ResetNetbootxyzCache()`. `reconcile.go` calls `Artifacts()` once per desired version per target
**before** the cached-skip short-circuit, so an unmemoized fetch would issue one request per tails
target per pass — the exact shape of the waste [#73](https://github.com/jacaudi/booty/issues/73)
fixed for `endpoints.yml`. Memoized, it is one ~90-byte GET per reconcile pass.

Reuses `fetchMetadata` (`pkg/ostype/http.go`) — the same client and `discoveryTimeout` the manifest
fetch already uses. No new HTTP client.

### 4.5 The parser moves to `pkg/config`

`parseSHA256SUMS` moves from `pkg/cache/debiandvd.go` to `pkg/config`, split into a bytes-taking
core plus a thin path wrapper the Debian DVD caller keeps using unchanged.

**Why a move and not a copy:** the sha256sum file format is one piece of knowledge. If it ever needs
to tolerate a binary-mode `*` marker or a `./` prefix, both call sites must change together — the
DRY criterion exactly. `pkg/cache` imports `pkg/ostype`, so the shared helper cannot live in cache;
`pkg/config` is the leaf both already import, and already hosts comparable shared machinery
(`DownloadStaged`, `ValidatePathSegment`).

---

## 5. The verification path for `downloadLargeFile` (`pkg/cache`)

This is the structural core.

### 5.1 Split the rename off the download

```go
// downloadLargeInto streams url into inProgressPath, resuming from the file's
// existing size via Range. It returns with the COMPLETE bytes at inProgressPath
// and does NOT rename — the caller verifies first, then lands. The suffix
// discipline is unchanged: callers use ".download", which survives SweepPartials.
func downloadLargeInto(ctx context.Context, url, inProgressPath string) error

// downloadLargeFile keeps its current signature and behaviour (download + rename)
// for the Debian DVD path (isoDownload), which verifies after landing via
// verifyDVDChecksums + removeUnverifiedISOs. Unchanged, out of scope.
func downloadLargeFile(ctx context.Context, url, destPath string) error {
    inProgress := destPath + ".download"
    if err := downloadLargeInto(ctx, url, inProgress); err != nil { return err }
    return os.Rename(inProgress, destPath)
}
```

The **416** branch moves into `downloadLargeInto` and simply returns `nil` — bytes are already
complete at the in-progress path; the rename is the caller's. Its existing comment ("isoVerify's
checksum step downstream is the correctness gate") becomes literally true for the tool path too,
where today it is aspirational.

### 5.2 One shared tail in `landArtifact`

```go
func landArtifact(ctx, dir, a, policy) (bool, artifactVerdict, error) {
    mkdir...
    final := filepath.Join(dir, a.Filename)
    var inProgress, streamedSHA string
    if a.Large {
        if a.SigURL != "" { return ...unsupported (D4)... }
        inProgress = final + ".download"
        if err := downloadLargeInto(ctx, a.URL, inProgress); err != nil { return false, artifactVerdict{}, err }
        // streamedSHA stays "" → verifyArtifact hashes the finished file (D3)
    } else {
        p, sha, err := config.DownloadStaged(ctx, dir, a.URL)
        if err != nil { return false, artifactVerdict{}, err }
        inProgress, streamedSHA = p, sha
        final = strings.TrimSuffix(p, ".partial")
    }
    // ---- from here, ONE tail shared by both branches ----
    // land/reject closures, `policy == "off"`, verifyArtifact, the class switch:
    // all exactly as today, operating on inProgress/final.
}
```

**This is the answer to "what stops a silent regression into *Large artifacts are never
verified*."** Not vigilance, and not a comment: after the split there is no second disposition path
to forget. The `Large` branch chooses only *how bytes arrive* and *which suffix marks them*;
verification and disposition are unreachable-around. A future contributor cannot skip verification
for `Large` without deleting the shared tail, which every staged artifact also depends on.

Backstopped by a mutation-checkable test (§10).

### 5.3 Verify before the rename

Verifying the `.download` rather than the landed file means an unverified 1.94 GB never occupies the
final path, where `/data/` would serve it. This complements — and does not rely on — PR #75's S1 fix
refusing to serve `.download` files.

On rejection, the existing `reject()` closure removes the in-progress file, discarding the resumable
bytes. That is intentional: resuming a file already known to hash wrong would append good bytes to
bad ones forever. It is also what makes §7 necessary.

---

## 6. Reverify, and the two non-local invariants it disturbs

Lifting the `tool` short-circuit is three lines, and it falsifies two invariants stated elsewhere in
the codebase. Both are recorded here because this repo's dominant review-finding pattern has been
undocumented non-local invariants rather than local bugs.

### 6.1 `ResetNetbootxyzCache`'s doc comment becomes false

It currently asserts, as load-bearing:

> *"The reverify path deliberately does NOT reset this memo … `VerifyVersion` short-circuits the
> tool family before it ever calls `Artifacts`, the only reader of this memo, so a tool reverify
> never observes stale data. Do not 'helpfully' restore a reset call there."*

Once the short-circuit is lifted, reverify **does** read the memo, and can observe data up to one
reconcile pass stale.

**Resolution:** restore `ResetNetbootxyzCache()` on the reverify path (`pkg/http/api_cache.go`) and
rewrite the comment to say why. This is symmetric with `ResetStreamsCache`, which that path already
resets, and it does **not** reintroduce #73 — that regression was a *per-target* reset inside
`reconcileTarget`, not a reset on a rare manual endpoint. Cost per reverify: one ~35 KB manifest and
one ~90-byte sidecar.

### 6.2 `VerifyVersion`'s in-flight check only knows `.partial`

`verify.go:365-370` treats "final file absent, but a `.partial` sibling exists" as *a re-download is
in flight → record no verdict*. A `Large` artifact's in-flight sibling is **`.download`**, never
`.partial` — that suffix distinction is the whole point of D13's `SweepPartials` survival.

Unchanged, reverify during a live Tails resume would return `artifact absent` → `classCorruption` →
a **false failure verdict** on a healthy system. The check must accept either suffix.

### 6.3 Stale-tag refusal must not become a 500

`Artifacts()` refuses any version that is not the entry's current tag — the reason the short-circuit
was introduced, since an archived tool version would otherwise make reverify a permanent 500.

**Resolution:** `pkg/ostype` exports a sentinel (e.g. `ErrVersionSuperseded`) wrapped into that
refusal; `VerifyVersion` maps `errors.Is(err, ostype.ErrVersionSuperseded)` to `(nil, "", nil)` —
"no verdict" — which is exactly what an archived tool returns today. Every other `Artifacts` error
still propagates.

---

## 7. The re-download guard

### 7.1 The hazard

Under `--signaturePolicy strict`, a rejected version triggers `removeVersionDir`
(`reconcile.go:200`), which `os.RemoveAll`s the version directory — including the `.download`. Next
tick, nothing is cached, so the full land path runs again. A persistent mismatch therefore becomes a
**1.94 GB re-download every `--cacheInterval`, indefinitely**. This hazard cannot occur today
because no tool artifact is verifiable; D1 creates it.

### 7.2 The guard

Before the land loop, `reconcileTarget` skips any version whose **last attempt was rejected by
verification** and whose rejection is newer than `verifyRetryAfter`, logging the version, the
recorded `verify_err`, and when it will next be attempted.

**No migration is required.** A rejected version already writes a `cache_entries` row with
`verified=0`, a non-empty `verify_err`, `size=0`, `in_window=0`, and `fetched_at = datetime('now')`,
while its `target_versions.cached` stays `0`. `cached=0` **and** a recorded failure verdict is an
unambiguous signature — a `warn`-landed failure has `verified=0` too, but `cached=1` and `size>0`,
and is already skipped by the idempotency check.

That discriminator is indirect, so it is encapsulated in **one** new `pkg/db` accessor returning
`(when time.Time, verifyErr string, ok bool)` rather than spread across a `reconcile.go` predicate.
One place to read, one place to change.

### 7.3 Scope and non-scope

- **Applies to every OS,** not just Tails and not just `Large`. Confining it would be a special case
  earning nothing: a 5-minute retry of a version that just failed verification is waste in every
  case, and the rejection is recorded identically for all of them. **This is a behaviour change to
  Flatcar, FCOS, and Debian that #76 does not mention** — rejected versions there drop from ~5-minute
  retries to hourly. Called out explicitly, and confirmed as the intended trade-off.
- **Transport failures are deliberately excluded.** `vg.Wait() != nil` returns before any row is
  written (`reconcile.go:186-188`), so a network failure records nothing and keeps retrying every
  tick, exactly as today. Only *verification* rejections latch.
- **Self-clearing, so no new API surface.** A transient corruption (a truncated transfer) heals on
  the next attempt after the window with no operator action. A permanent upstream mismatch stays
  visible via `verify_err`, which the cache API already exposes. An explicit "retry now" endpoint
  would be speculative; deleting and re-adding the target already works.

### 7.4 Configuration

`verifyRetryAfter`, viper-backed, default **1h**. Viper-backed rather than a const because tests
must control it — this repo's standing rule, with precedent in `NetbootxyzEndpointsURL` /
`NetbootxyzAssetBase` (a const there made a `pkg/cache` integration test unbuildable).

---

## 8. Trust model — stated honestly

`sha256-checksums.txt` is hosted on the **same GitHub release** as `tails-amd64.iso`, fetched over
the same TLS connection to the same origin, with no independent trust anchor.

**What this defends against:** corruption in transit, a truncated transfer, a resume that appended to
the wrong bytes, silent disk rot (via reverify, per D5), and a mirror or CDN that serves damaged
content.

**What it does not defend against:** a compromised release, a compromised netboot.xyz account, or an
attacker with write access to the asset mirror — any of whom can replace the ISO and the checksum in
the same breath. This is integrity, not provenance.

Real provenance would require verifying `tails-amd64.iso.sig` against Tails' own signing key.
`SigURL`'s existing path cannot express that: it expects a detached signature over a *checksum file*
verified against a known embedded keyring (the Debian cdimage / Flatcar shape), not a detached
signature over a multi-GB ISO whose signing key booty does not ship. **Out of scope for #76**, and
D4's narrowed guard keeps that door shut loudly rather than silently.

The honest summary for the changelog: *Tails is now protected against a corrupted download. It is
not protected against a malicious upstream.*

### 8.1 The `strict` behaviour change

Today every tool artifact is `classNotVerifiable`, which `strict` admits — `strict` genuinely has
nothing to be strict about. Once Tails declares a digest, `strict` begins **enforcing** it: a
mismatched or truncated ISO is rejected and the version wiped, blocking a boot that previously
succeeded.

Intended, and a real behaviour change. Under the default `warn` it lands with `verified=0` and the
existing `landed artifact with failed verification` warning, so default deployments see a signal
rather than a denial. Documented in `docs/schema/CATALOG.md`, `docs/CONFIGURATION.md`, and the
release notes.

---

## 9. What is explicitly out of scope

- **GPG verification of `tails-amd64.iso.sig`** (§8). Separate, larger work.
- **The other seven tools.** They publish no checksums (verified, §2); their `classNotVerifiable`
  status is correct and unchanged.
- **The Debian DVD path.** `downloadLargeFile` keeps its signature and its verify-after-rename
  behaviour. Only the split is new.
- **Retroactive re-verification.** Policy tightening remains non-retroactive per the P3b design
  (§5, D15); a version admitted under a looser policy stays until reverify re-checks it on demand.

---

## 10. Testing

Every behavioural test below must be **mutation-checked** — disable the code it covers and confirm
the test fails. Slice 2 shipped a plan-mandated test that passed identically with D13's routing
disabled and survived three review rounds; that is the failure mode this requirement exists to
prevent.

**`pkg/ostype` (in-package — `pkg/ostype/*_test.go` is `package ostype`; qualified calls will not
compile):**
1. `Artifacts` populates `SHA256` on the ISO only, from an `httptest`-served sidecar, with the other
   three files left not-verifiable. Redirect via `viper.Set(config.NetbootxyzAssetBase, …)`.
2. Sidecar 404 / malformed line / ISO absent from the sidecar → `Artifacts` errors (D2), one case
   each.
3. A tool with no `checksums` field issues **no** sidecar request and behaves exactly as today.
4. The sidecar is fetched at most once per `ResetNetbootxyzCache` cycle across repeated `Artifacts`
   calls (the #73 regression shape) — assert the request count against the `httptest` server.
5. The registry membership test in `ostype_test.go` pins exact registered names; confirm no change
   is needed (none is — no OS is added).

**`pkg/cache` (`package cache`):**
6. **The load-bearing one.** A `Large` artifact with a deliberately wrong `SHA256`: rejected under
   `strict` (no bytes at the final path, `.download` removed), landed with `verified=0` under
   `warn`. Mutation check: delete the `verifyArtifact` call from the shared tail and this must fail.
7. A `Large` artifact with a correct `SHA256` lands and records `verified=1`.
8. A `Large` artifact declaring `SigURL` still hard-fails (D4).
9. Resume correctness: pre-seed a partial `.download`, serve the remainder with a `Range`-honouring
   `httptest` server, and assert the digest is computed over the **whole** file, not the delta. This
   is the trap D3 exists for.
10. The **416** path: pre-seed a full-size `.download`, serve 416, assert the file is verified and
    landed (not renamed unverified).
11. `VerifyVersion` on a tails version verifies the ISO; on an in-flight `.download` returns *no
    verdict* rather than `artifact absent` (§6.2); on a superseded tag returns no verdict, not an
    error (§6.3).
12. The retry guard: a rejected version is skipped inside the window and re-attempted after it, with
    `verifyRetryAfter` set via `viper.Set`. A transport failure is **not** guarded.

**Not driveable by test, and stated as such:** nothing. There is no lab gate for this change — it is
fully exercisable with `httptest` and fixture files. A real-ISO run is available as confirmation if
wanted (the lab's cached 1.94 GB ISO and its known-good digest are on disk) but is not a gate.

---

## 11. Documentation surface

- `docs/schema/CATALOG.md` — Tails is now verifiable; the tool cohort's blanket "no verification
  material" statement is narrowed, and the retain-1 caveat is untouched.
- `docs/CONFIGURATION.md` — new `verifyRetryAfter`; `--signaturePolicy` gains the §8.1 note.
- `docs/designs/2026-07-29-tool-rescue-os-support-design.md` §8.5 — updated from "deferred to a later
  slice" to "resolved by #76", retaining the correction it already carries.
- Release notes — the `strict` behaviour change and the honest trust-model sentence from §8.
- `docs/schema/API.md` / `DATABASE.md` — **no change**; `verified`/`verify_err` semantics are
  untouched by design (D1).

---

## 12. Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Upstream stops publishing the sidecar | Medium | D2 fails loud; the cached ISO keeps booting; the operator sees a warning every pass rather than a silent downgrade. |
| Upstream renames the ISO asset | Low | The existing allowlist check fires first with a clear message; the sidecar lookup never silently attaches another file's digest. |
| The retry guard masks a real problem | Low | `verify_err` is recorded and exposed by the cache API on every rejection; the guard only suppresses *re-downloads*, never the verdict or the log line. |
| Hashing stalls the reconcile pass | Low | Measured 3.6 s (≈20 s on a slow spindle) once per version download, inside an errgroup slot already occupied by a multi-GB transfer. |
| The parser move breaks the Debian DVD path | Low | Pure move plus a path wrapper; existing DVD tests cover it, and `go build ./...` catches the import change. |

---

## 13. Open questions

None blocking. #76's four open questions are all decided: sidecar availability (§2 + D2), format
(§2 + D1/4.5), the `strict` behaviour change (§8.1), and the trust model (§8). The single assumption
that cannot be closed by inspection — that upstream keeps publishing the sidecar — is a fact about
the future, and D2 is the design's response to it.
