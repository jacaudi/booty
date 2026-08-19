# Design: verify the Tails ISO against its published sha256

**Issue:** [jacaudi/booty#76](https://github.com/jacaudi/booty/issues/76)
**Status:** design — Gate 1 (5 blocking, 9 significant) and a scoped re-review (2 blocking, 4 new
defects) have both returned; all findings addressed below
**Extends:** `docs/designs/2026-07-29-tool-rescue-os-support-design.md` §8.5 (the recorded deferral),
`docs/designs/2026-07-01-p3b-signature-verification-design.md` (the `--signaturePolicy` model this
extends rather than reinvents)

---

## 1. Goal

Make booty verify `tails-amd64.iso` — a 1.94 GB anonymity distro executed with full hardware
privilege on bare metal — against the sha256 its own release publishes, instead of caching it
entirely unverified.

The blocker is structural. The Tails ISO is `Large: true` (tool/rescue OS design **D13**, the
resumable downloader), and `pkg/cache/verify.go`'s `landArtifact` *hard-fails* a `Large` artifact
that declares any verification material:

```go
if a.Large {
    if a.SHA256 != "" || a.SigURL != "" {
        return false, artifactVerdict{}, fmt.Errorf(
            "cache: %s: Large artifacts carry no verification path, but sha256/sig was declared", a.Filename)
    }
```

Setting `Artifact.SHA256` today would **break caching, not verify it**. So the work is: give
`downloadLargeFile` a verification path, source the digest, and narrow — never delete — that
fail-closed guard.

## 2. Evidence: what is verified, and what is assumed

Checked against primary sources on 2026-08-02, then **independently re-verified by the Gate 1
reviewer**, which re-ran the release sweep (1295 releases), re-fetched five sidecars, and re-hashed
the cached ISO. Every claim in this table survived that second pass.

### Verified

| Claim | How | Result |
|---|---|---|
| Every Tails release publishes `sha256-checksums.txt` | `gh api repos/netbootxyz/asset-mirror/releases --paginate`, filtered to releases carrying a `tails*` asset, then to those *lacking* the sidecar | **76 of 76.** Zero exceptions. |
| No other release in that repo publishes one | Same sweep, inverted | Exactly 76 releases in the repo carry the sidecar; all are Tails. |
| The other two tool sources publish none | Independent sweep of `netbootxyz/debian-squash` (1267 releases), which serves `clonezilla-debian-stable-amd64` and `rescatux` | Only `filesystem.squashfs*`, `initrd`, `vmlinuz`. No checksums. |
| Sidecar format | `curl` + `sed -n l` on `7.10-17629562`, `7.9.1-17629562`, `7.3.1-00388326`, `6.6-cfd50f75`, `4.22-28906ec3` | One LF-terminated line, `<64 hex>` + **two spaces** + `<filename>`. No `./`, no binary-mode `*`, no trailing blank. 82 bytes for `tails-amd64.iso`. |
| **The ISO filename has changed across releases** | Same fetches | `tails-amd64.iso` now; `tails-amd64-6.6.iso` and `tails-amd64-4.22.iso` historically. The sidecar key always matches that release's asset name. Caught by the manifest-membership check when the manifest has tracked the rename, and by **D2a** when the manifest lags it — see D2a. |
| The published digest matches the bytes booty caches | `shasum -a 256` on the slice-2 lab's cached ISO | `6dab23b2…d1743` — **byte-identical**. Validated end-to-end against real cached bytes. |
| Cost of hashing 1.94 GB | `time shasum -a 256` | **3.55 s** (macOS/SSD). Disk-throughput-bound; ~19.4 s at 100 MB/s. Paid once per version download. |
| Debian's `SHA256SUMS` is the same format | Read `debiandvd.go:481-499` + live `cdimage.debian.org/.../SHA256SUMS` | Identical `<hex>␣␣<name>` shape, no `./`, no `*`. The "one format" premise for D-parser holds. |
| `pkg/ostype` can import only `pkg/config` | grep of ostype imports; **9** `pkg/cache` files import ostype | Dependency runs cache → ostype. A shared helper cannot live in cache. |
| Default signature policy | `pkg/config/config.go:91` | `warn`. |
| Eviction cannot rotate out a guard row | `pkg/db/cache.go:198-201`, `evict.go:42` | `ListArchivedUnpinned` filters `size > 0`, so a `size=0` rejection row is never an eviction candidate. A hypothesis that eviction defeats §7's guard was tested and **falsified**. |

### Assumed (stated, not provable)

- **That netboot.xyz keeps publishing the sidecar.** 76/76 is a fact about the past. This is exactly
  why **D2** fails loud rather than silently degrading to unverified.
- **That the sidecar and the ISO share a trust boundary.** See §8 — integrity, not provenance.

### Could not be verified, and stated as such

- Whether the manifest and the sidecar have ever actually desynced. Both filename conventions exist
  across releases and track the release asset in all five sampled, but historical `endpoints.yml`
  states are not retrievable. **D2a is designed for this case regardless.**
- Actual hash throughput on the NAS. Only measured on macOS; the ~20 s figure is arithmetic.
- That `Artifacts()` issues exactly one sidecar request per pass (§4.4's premise). Reasoned from
  `reconcile.go:131-137` and the single-arch, `UNIQUE(os,arch,params)`-constrained `tails`
  registration; not instrumented against a live pass.

---

## 3. Decisions

### D1 — the digest is resolved in `pkg/ostype` into `Artifact.SHA256`

`netbootxyzOS` gains sidecar registration data; `Artifacts()` fetches, parses, and populates
`Artifact.SHA256`. A new `Artifact.SumsURL` field parallel to `SigURL` was rejected: `SigURL`
declares a *different mechanism* that composes with a digest, whereas `SumsURL` and `SHA256` would
be two representations of one fact, forcing a precedence rule into `verifyArtifact`. Resolving into
`SHA256` keeps one representation and matches the FCOS precedent (streams JSON → `SHA256`).

**This is why the design is small:** `verifyArtifact`, `aggregateVerdicts`, the `verified`/
`verify_err` columns, and the API's verdict semantics all work unchanged.

### D2 — a missing or unusable sidecar fails loud

Sidecar unfetchable or malformed ⇒ `Artifacts()` errors. `reconcile.go:138-143` already turns that
into *log a warning and skip this version this tick*, deliberately not touching the row — so
fail-loud costs the availability of **updates**, never of the **existing cache**.

Falling back to `classNotVerifiable` was rejected as a *silent security downgrade*.

### D2a — the registration declares which files the sidecar must cover

**Added in response to Gate 1 B1; its justification corrected after the scoped re-review (BL-2).**

D2 alone is ambiguous: the Tails sidecar lists only the ISO, so
`vmlinuz`/`initrd.img`/`9990-misc-helpers.sh` are legitimately absent, but the *ISO* being absent
must be an error. Without an explicit rule, the ISO silently lands as `classNotVerifiable` — the
precise failure mode D2 exists to forbid. So the registration names the covered files, and a
**declared-covered file missing from the sidecar is a D2 error**; any other file's absence is not.

**The rename framing was wrong and is withdrawn.** An earlier revision justified this field with the
upstream ISO rename §2 documents. Neither rename branch can silently land unverified bytes — but
they are caught by *different* checks, so "D2a is the rename guard" is not the right statement:

- **Manifest tracks the rename** (the normal case) — `netbootxyz.go:250-252` already errors
  (`allowlisted file %q is not in the manifest entry … upstream may have renamed or dropped it`)
  *before* any `Artifact` is constructed, and §4.3's own sketch keeps that check ahead of the
  `checksumCovers` branch. So the allowlist error is what surfaces, never D2a's.
- **Manifest lags the rename** — the entry's `path` has advanced to the new release while its
  `files` list still names the old ISO. Membership passes, but the sidecar fetched from the new
  `path` keys the NEW name, so the old name is uncovered and **D2a errors** — inside `Artifacts`,
  before any download, rather than the 404 this bullet previously predicted. So D2a covers this
  rename branch too; what it *uniquely* covers is still the sidecar-only desync below.

This also means an earlier revision **deleted a correct statement** when it "corrected" M4. M4's
observation is about *fetch* ordering (the ~90-byte sidecar GET precedes the file loop — true), not
about which *error* surfaces. The original text — "if upstream renames the asset, the existing
allowlist check fires first, so this degrades to a loud, already-designed failure" — was right **for
the branch where the manifest tracks the rename**, which is the normal case, and is restored in §4.3
with that scope made explicit. In the lagging branch the loud failure is D2a's instead; the
"degrades to a loud, already-designed failure" half holds either way.

**What D2a actually and uniquely covers is a sidecar-only desync:** the manifest and the release
asset still say `tails-amd64.iso`, but the sidecar drops that line or keys it differently. Nothing
else in the pipeline notices, because every other check passes — which is exactly why the silent
downgrade would occur. §2 lists this under "could not be verified": it has not been observed, and it
is not observable from outside upstream's build.

**Why the field is not derivable from `large`** (re-review S-c). Today
`checksumCovers: ["tails-amd64.iso"]` and `large: {"tails-amd64.iso": true}` hold identical content,
so the field looks redundant. They encode different knowledge and have different change-drivers:
`large` means "too big for the staged downloader's 5-minute ceiling", `checksumCovers` means
"upstream promises a digest for this". A tool could publish a sidecar covering small files only
(no `large` entry at all), or mark a file `large` that upstream never checksums. Deriving one from
the other would make "every `Large` file must appear in the sidecar" a rule, which is a coincidence
of the present registration rather than a fact about upstream. The identical content today is not an
argument to merge them — but it *is* the reason to say so here rather than leave a reader guessing.

### D3 — hash the completed file on disk, never the stream

`downloadLargeFile` resumes via `Range`, so a resumed transfer's stream carries only the *delta*.
It is wrong a second, independent way on the **416** branch, where a prior attempt wrote every byte
and this attempt streams nothing. `verifyArtifact` already hashes `filePath` when
`streamedSHA256 == ""`, so no new hashing code is needed.

Persisting incremental hash state across resumes (Go's digest does implement `BinaryMarshaler`) was
rejected under KISS/YAGNI: a second on-disk state file that must stay exactly in sync with the byte
count, to save a measured 3.55 s.

### D4 — the `Large` guard is narrowed, not deleted

```go
if a.Large && a.SigURL != "" {
    return false, artifactVerdict{}, fmt.Errorf(
        "cache: %s: detached-signature verification is unsupported for resumable (Large) downloads", a.Filename)
}
```

Still fail-closed. The sha256 half is removed *because the path now exists* — the only honest reason
to remove a fail-closed guard.

### D4a — a `Large` artifact that fails its checksum never lands, under any policy

**Added in response to Gate 1 B2, and the most consequential change in this revision.**

Under the default `warn`, `verify.go:141` lands a `classCorruption` verdict. `reconcile.go:163` then
skips any version with `cached=1` and all final files present, and its comment states that
`verified` is *intentionally* not consulted. Net effect on the design as first written: **a
truncated Tails ISO lands, is served to netboot clients, records `verified=0`, and is never
re-downloaded — not next tick, not ever, not even after switching to `strict`** (policy tightening
is non-retroactive). The headline safety property was not delivered in the default configuration,
and the corruption was permanent.

The fix is narrow and follows from what `warn` is *for*. `warn` trades strictness for availability —
it exists so an unfetchable sidecar or an expired key does not brick a boot. For a `Large` artifact
that trade has no value on either side:

- **Nothing is gained.** A digest failure on a 1.94 GB ISO is unexplained divergence from what
  upstream published. It is *usually* truncation or corruption that will not mount via `fromiso=`,
  though a flipped bit in an unused region would fail the hash and still boot — so the honest
  argument is not "these bytes cannot boot" but **"unexplained divergence must not be served,"**
  which for an anonymity distro executed at full hardware privilege is the stronger claim anyway.
- **Nothing ambiguous is being punished — once D4b is in place.** For a `Large` artifact,
  `classCorruption` at land time means a definitive hash *mismatch*, because D2 rejects an
  unfetchable or malformed sidecar before the download and D4b removes the one other route in.

So `landArtifact` rejects a `Large` artifact on `classCorruption` regardless of policy. `warn` and
`strict` converge for this one case, the version is wiped, and §7's guard rate-limits the retry.
`policy == "off"` still short-circuits before verification, unchanged (§5.2).

### D4b — a `Large` artifact's hash is computed in `landArtifact`, and an I/O failure is an error, not a verdict

**Added in response to the scoped re-review (BL-1), which found a hole in D4a as first written.**

D4a originally claimed `classCorruption` on a `Large` artifact could *only* mean a mismatch. That was
false, and the exposure was exclusive to `Large`. `verifyArtifact` hashes the file itself when
`streamedSHA256 == ""` and classifies a **read failure** as corruption
(`verify.go:56-61`, "checksum unavailable"). D3 deliberately passes an empty streamed hash for
`Large`, while `config.DownloadStaged` always returns a digest (`config.go:154`, verified) — so on
the land path `hashFile` is reached **only** for `Large` artifacts. D4a would therefore have
converted a transient local read error — a NAS blip, fd exhaustion under `CacheConcurrency`, an
operator clearing the cache dir mid-pass — into: delete the completed 1.94 GB file (§5.3), `RemoveAll`
the version directory (`reconcile.go:200`), and arm §7's guard for an hour. Under the *pre-existing*
code that same error costs nothing, because `hashFile` is never reached.

**Resolution:** the `Large` branch of `landArtifact` computes the digest itself and treats a read
failure as a transport/IO error — `return false, artifactVerdict{}, err`. That routes to
`reconcile.go:186`'s `vg.Wait() != nil → continue`, which writes no row, runs no `removeVersionDir`,
and **leaves the resumable bytes on disk** to resume next tick. The successful digest is then passed
to `verifyArtifact` as `streamedSHA256`, so `verifyArtifact` needs no change and its own `hashFile`
branch becomes reachable only from `VerifyVersion` (reverify), where classifying an unreadable final
file as corruption is existing, unchanged behaviour.

The rule this encodes is worth stating generally: **"I could not evaluate the material" is an
infrastructure failure and must be retried; "the material does not match" is a verdict.** D2 applies
it to the sidecar, D4b applies it to the local read.

**A related pre-existing gap is NOT fixed here and is not claimed to be:** the same
`reconcile.go:163` skip means *any* warn-landed failed version, on any OS, is never retried. That
predates this change, affects small artifacts where `warn`'s availability trade is genuine, and
belongs in its own issue rather than widening this one.

### D5 — reverify covers tools that declare material

Lift `VerifyVersion`'s `family == "tool"` short-circuit. See §6 for the three non-local invariants
this disturbs.

### D6 — a rejected version is not re-downloaded until a retry window elapses

Version-level, in `reconcileTarget`. See §7 — including an explicit statement of the one path it
does **not** cover.

### D7 — base on `main` after PR #75 merges

PR #75 (`worktree-tool-rescue-os-slice2` @ `4f67260`) is open and touches
`pkg/cache/verify.go`, `pkg/cache/isodownload.go`, and `pkg/ostype/netbootxyz.go`.

**Executor warning:** none of this design's premise code exists on `main` today — no `tails`
registration, no `Artifact.Large`, no `Large` guard, and `pkg/http`'s in-flight-file refusal covers
only `.partial`. Do not start against `main` and conclude the quoted guard is missing; wait for #75.

### D-parser — the sha256sums parser moves to a new `pkg/checksum` leaf

The format is one piece of knowledge (§2 confirms Debian's `SHA256SUMS` and netboot.xyz's sidecar
are byte-identical in shape), so it gets one implementation. `pkg/cache` imports `pkg/ostype`, so it
cannot host a helper ostype needs.

`pkg/config` is the existing shared leaf and was the first choice, but rejected on naming: a file-
format parser is not configuration, and `DownloadStaged`/`ValidatePathSegment` already stretch that
package. A ~15-line `pkg/checksum` has the same import-graph cost and an honest name.

---

## 4. Sourcing the digest (`pkg/ostype`)

### 4.1 Registration

```go
register(netbootxyzOS{
    name:      "tails",
    endpoints: map[string]string{"amd64": "tails"},
    files:     []string{"vmlinuz", "initrd.img", "9990-misc-helpers.sh", "tails-amd64.iso"},
    large:     map[string]bool{"tails-amd64.iso": true},
    // Every Tails release in netbootxyz/asset-mirror (76/76, checked 2026-08-02)
    // publishes this sidecar; no other tool's releases do. It is NOT in `files`:
    // it is verification material, never cached and never served.
    checksums: "sha256-checksums.txt",
    // D2a: the ISO is the file the sidecar MUST cover. Its sole UNIQUE
    // coverage is a sidecar-only desync; see the field's doc comment for how
    // the two upstream-rename branches divide between this and the
    // manifest-membership check.
    checksumCovers: []string{"tails-amd64.iso"},
})
```

Per-tool **data**, so a tool that ever gains a sidecar is a one-line registration change with no
sibling touched. Tools without these fields behave exactly as today.

`checksumCovers` entries must be a subset of `files`; a registration violating that is a bug and is
asserted by a registry test (§10.5).

### 4.2 The sidecar is deliberately outside the manifest allowlist

`Artifacts()` refuses any allowlisted file absent from the manifest entry. The sidecar **must be
exempt**, because `endpoints.yml` never lists it — that omission is the entire premise of #76.

This is the one place the design derives an artifact URL from a filename booty hardcodes rather than
one upstream declares.

**Corrected security statement (Gate 1 S6).** An earlier revision claimed `artifactURL` "already
rejects a manifest path that could relocate the request". That is **false**, and was measured:

```
path = "/asset-mirror/releases/download/../../../evil-repo/releases/download/v1/"
  →  https://github.com/netbootxyz/asset-mirror/releases/download/../../../evil-repo/releases/download/v1/sha256-checksums.txt
  err = <nil>
```

`artifactURL` (`netbootxyz.go:283-286`) rejects **authority** relocation — absolute, protocol-
relative, non-rooted — and nothing else. `url.Parse`/`u.String()` do not path-clean, so `..`
survives to the origin, which normalizes it.

The honest bound: the composed URL is **host-pinned** to `assetBase`; a hostile manifest path can
relocate *within* that host. That is the identical exposure the ISO itself already carries, since
both derive from the same `e.Path` — the sidecar adds no new attack surface, it inherits the
existing one. The fetched bytes are only ever compared against, never executed, cached, or served.

Not added to `files`, so `ToolFiles()`, the boot-script↔allowlist bidirectional test, and everything
`/data/` serves are untouched.

### 4.3 Fetch, parse, populate

```go
func (t netbootxyzOS) Artifacts(ctx, version, arch string, _ map[string]string) ([]Artifact, error) {
    // ... existing entry/tag/allowlist logic, unchanged ...
    var sums map[string]string
    if t.checksums != "" {
        u, err := artifactURL(base, e.Path, t.checksums)
        if err != nil { return nil, ... }
        body, err := fetchMetadata(ctx, u)
        if err != nil { return nil, ... }              // D2: fail loud
        sums, err = checksum.ParseSums(body)
        if err != nil { return nil, ... }              // D2: fail loud
    }
    for _, f := range names {
        // ... existing manifest-membership check + artifactURL ...
        a := Artifact{Filename: f, URL: u, Large: t.large[f]}
        if d, ok := sums[f]; ok {
            a.SHA256 = d
        } else if slices.Contains(t.checksumCovers, f) {
            // D2a: a file the registration declares the sidecar MUST cover is
            // missing from it — a sidecar-only desync. Fail loud rather than
            // silently landing 1.94 GB as not-verifiable.
            return nil, fmt.Errorf(
                "ostype: %s: %q is declared checksum-covered but absent from %s", t.name, f, t.checksums)
        }
        // Any OTHER file absent from the sidecar stays not-verifiable: the Tails
        // sidecar legitimately lists only the ISO.
        out = append(out, a)
    }
    return out, nil
}
```

**Lookup is by filename, never "it's the only line."** The sidecar has one line today; keying on
`a.Filename` is what makes a rename fail loudly instead of attaching the wrong file's digest.

**Only the ISO gets a digest,** so `aggregateVerdicts` sees one verifiable and three not-verifiable
artifacts. Its rule — verified iff every verifiable artifact passed *and* at least one was
verifiable — records `verified=1` for a good Tails version, an improvement on today's `NULL`.

**Ordering note.** Two different orderings, and an earlier revision conflated them (re-review BL-2).
*Fetch* order: the sidecar GET precedes the per-file loop, so on an upstream rename booty pays the
~90-byte fetch and then errors — that was Gate 1 M4's point, and it stands. *Error* order: the
manifest-membership check runs before the `checksumCovers` branch, so **when the manifest has
tracked the rename** — upstream's `e.Files` lists the new name while booty's own `files` allowlist
still names the old one — the allowlist error is what surfaces and D2a is never reached. **When the
manifest lags the rename** — `path` advanced, `files` unchanged — membership passes and D2a is what
errors. Which mechanism catches a rename therefore depends on the branch; neither lets unverified
bytes land, and both fire inside `Artifacts` before any download.

### 4.4 No memoization

**Changed in response to Gate 1 S8.** An earlier revision memoized the sidecar alongside the
endpoints manifest, reasoning from #73. That analogy does not transfer: #73 saved one ~35 KB fetch
per *tool target* across eight tools, whereas `tails` is amd64-only at `retain: 1` and
`UNIQUE(os, arch, params)` permits exactly one such target — so an unmemoized fetch is **one ~90-byte
GET per reconcile pass** (288/day at the 5-minute default).

The memo would have bought that back at the cost of a second cache, a reset-coupling to
`ResetNetbootxyzCache`, an unspecified failure-memoization policy, and a test. Deleted. Reuses
`fetchMetadata` (`pkg/ostype/http.go`) — same client and `discoveryTimeout` as the manifest fetch.

*Precision on the arithmetic (re-review S-f):* "one per pass" is the discovered-target case at
`retain: 1`. Each **manual version pin** adds one call per pinned version per pass, and each
`POST /cache/{id}/reverify` adds one — more reachable now that D5 lifts the tool short-circuit. The
conclusion survives comfortably; the figure is a floor, not an exact count.

### 4.5 `pkg/checksum`

```go
package checksum
// ParseSums parses `sha256sum` binary-mode output ("<hex><space><space><name>")
// into name→digest.
func ParseSums(body []byte) (map[string]string, error)
```

`pkg/cache/debiandvd.go`'s `parseSHA256SUMS` is replaced at its single call site
(`verifyDVDChecksums`, `debiandvd.go:37`) by reading the file and passing the bytes to
`checksum.ParseSums` — two statements, since `os.ReadFile` returns `([]byte, error)`.

**Correction to an earlier claim (Gate 1 S9):** that revision said the DVD caller keeps its call
"unchanged". It does not — the call becomes qualified and reads the file itself. And §12's assertion
that "existing DVD tests cover it" was half true: `parseSHA256SUMS` has **no direct unit test**; it
is covered only transitively via `verifyDVDChecksums` (`debiandvd_test.go:174/181/214`), and the
`ensureDebianDVD` state-machine tests stub it out entirely through `swapDVDSeams`. The move
therefore ships with direct unit tests for `ParseSums` (§10.17).

---

## 5. The verification path for `downloadLargeFile` (`pkg/cache`)

### 5.1 Split the rename off the download

```go
// downloadLargeInto streams url into inProgressPath, resuming from the file's
// existing size via Range. Returns with the COMPLETE bytes at inProgressPath and
// does NOT rename — the caller verifies first, then lands.
func downloadLargeInto(ctx context.Context, url, inProgressPath string) error

// downloadLargeFile is the DVD-ONLY wrapper: download + rename, no verification.
// Artifact landing does NOT go through here — it goes through downloadLargeInto +
// landArtifact, which verifies before renaming. Kept only for ensureDebianDVD's
// isoDownload seam, which verifies after landing via verifyDVDChecksums.
func downloadLargeFile(ctx context.Context, url, destPath string) error {
    inProgress := destPath + DownloadSuffix
    if err := downloadLargeInto(ctx, url, inProgress); err != nil { return err }
    return os.Rename(inProgress, destPath)
}
```

That doc comment is **required**, not polish: `downloadLargeFile` retains the download-then-rename-
without-verification shape, and is the obvious-looking function for whoever wires the next `Large`
artifact.

The **416** branch moves into `downloadLargeInto` and returns `nil` — bytes are complete; the rename
is the caller's. Gate 1 confirmed the split leaves DVD behaviour identical: the seam is a func var on
the same `(ctx, url, destPath) error` signature, the DVD state-machine tests stub it entirely, and
the **five** direct `downloadLargeFile` tests still pass through the wrapper.

### 5.2 One shared tail in `landArtifact`

```go
func landArtifact(ctx, dir, a, policy) (bool, artifactVerdict, error) {
    mkdir...
    final := filepath.Join(dir, a.Filename)
    var inProgress, streamedSHA string
    if a.Large {
        if a.SigURL != "" { return ...unsupported (D4)... }
        inProgress = final + DownloadSuffix
        if err := downloadLargeInto(ctx, a.URL, inProgress); err != nil { return false, artifactVerdict{}, err }
        if a.SHA256 != "" {
            // D3: hash the COMPLETED file — resume-via-Range means the stream
            // carried only a delta, and the 416 branch streamed nothing at all.
            // D4b: a read failure here is infrastructure, not a verdict. Returning
            // err leaves the resumable bytes on disk and retries next tick;
            // letting verifyArtifact hash it would classify the blip as corruption
            // and (under D4a) destroy a completed multi-GB download.
            h, err := hashFile(inProgress)
            if err != nil { return false, artifactVerdict{}, fmt.Errorf("cache: hash %s: %w", inProgress, err) }
            streamedSHA = h
        }
    } else {
        p, sha, err := config.DownloadStaged(ctx, dir, a.URL)
        if err != nil { return false, artifactVerdict{}, err }
        inProgress, streamedSHA = p, sha
        final = strings.TrimSuffix(p, ".partial")
    }
    // ---- ONE tail shared by both branches ----
    // land/reject closures and `policy == "off"` exactly as today, then:
    v := verifyArtifact(ctx, inProgress, streamedSHA, a)
    switch v.class {
    case classPass, classNotVerifiable: return land(v)
    case classForgery:                  return reject(v)
    case classCorruption:
        // D4a: for a Large artifact this is a definitive hash MISMATCH — D2 rejects
        // an unfetchable/malformed sidecar before the download, and D4b routes a
        // local read failure out as an error rather than a verdict. Unexplained
        // divergence from what upstream published must not be served, so `warn` has
        // no availability to trade here and this rejects under every policy.
        if policy == "warn" && !a.Large { return land(v) }
        return reject(v)
    default: return reject(v)
    }
}
```

**What this buys, stated precisely (Gate 1 S5).** One disposition path instead of two: the `Large`
branch chooses only *how bytes arrive* and *which suffix marks them*. It is **not** a package-wide
impossibility proof, and an earlier revision's "unreachable-around" / "unrepresentable" wording
overclaimed. Two paths still reach a rename without a digest check:

1. **`policy == "off"`** short-circuits to `land(classNotVerifiable)` before verification, exactly as
   it does for staged artifacts. Consistent and intentional — "off" means off — but it does mean
   unverified `Large` landing remains reachable by configuration.
2. **`downloadLargeFile`** survives in the same package for the DVD seam (§5.1), so "simplifying"
   `landArtifact` back onto it would reopen the hole in one line. Mitigated by the mandatory doc
   comment and by §10.6's mutation-checked test, not by the type system.

### 5.3 Verify before the rename

Verifying the in-progress file means an unverified 1.94 GB never occupies the final path where
`/data/` would serve it. This complements PR #75's fix refusing to serve in-flight files
(`pkg/http/http.go:100-103` on the #75 base covers both suffixes; on `main` it covers only
`.partial` — see D7).

On rejection, `reject()` removes the in-progress file, discarding the resumable bytes. Intentional:
resuming a file already known to hash wrong appends good bytes to bad ones forever. It is also what
makes §7 necessary.

### 5.4 The in-progress suffix becomes one constant

**Added in response to Gate 1 S7.** `".download"` currently lives at `isodownload.go:25` and
`pkg/http/http.go:102`; this design would add a third site (§5.2) and a fourth (§6.2). Four
hand-synced literals for one contract that already has a documented reason to differ from
`.partial`. It becomes `cache.DownloadSuffix`, exported because `pkg/http` already imports
`pkg/cache`.

### 5.5 Two adjacent `.download` defects fixed here

- **`pkg/cache/scan.go:51`** excludes only `.partial` when totalling a version's size, so a
  `.download` is counted toward `size`, inflating `SumCacheBytes` and the eviction budget. Reachable
  when `finalFilesPresent` fails for one artifact of an already-`cached=1` Tails version and an
  operator hits `POST /api/v1/cache/scan` mid-transfer. Pre-existing on #75, but this design is the
  one auditing `.partial` assumptions and §5.4 gives it the constant to use.
- **`isodownload.go:86`** logs `bytes = offset+n` on completion, which over-reports after the 200
  branch's truncate-and-restart (`offset` is stale). `isodownload_test.go:128` exercises only the
  206 path, so nothing catches it. One line, in a function this design already opens.

---

## 6. Reverify, and the three non-local invariants it disturbs

### 6.1 `ResetNetbootxyzCache`'s doc comment becomes false

It asserts, as load-bearing, that *"`VerifyVersion` short-circuits the tool family before it ever
calls `Artifacts`, the only reader of this memo, so a tool reverify never observes stale data. Do
not 'helpfully' restore a reset call there."* Lifting the short-circuit falsifies that.

**Resolution:** restore `ResetNetbootxyzCache()` on the reverify path (`pkg/http/api_cache.go`) and
rewrite the comment. Symmetric with `ResetStreamsCache`, which that path already resets, and it does
**not** reintroduce #73 — that was a *per-target* reset inside `reconcileTarget`, not a rare manual
endpoint.

**Cost, stated fully (Gate 1 S2).** Not pure upside. There is no data race — the memo is
mutex-guarded and the published map is read-only — but the reset runs on the **API goroutine** while
`reconcileAll` may be mid-pass on the coordinator goroutine. A reverify landing mid-pass forces a
re-fetch, so a later target in the *same pass* can observe a newer manifest than an earlier one:
`DiscoverVersions` returns tag T1, the subsequent `Artifacts(version=T1)` refuses with "upstream now
publishes T2", and `reconcile.go:142` logs and skips that version for one tick. Self-healing and low
severity, but it is a new behaviour this design creates and it belongs in the comment.

### 6.2 `VerifyVersion`'s in-flight check only knows `.partial`

`verify.go:365-370` treats "final absent but a `.partial` sibling exists" as *re-download in flight →
no verdict*. A `Large` artifact's in-flight sibling is `DownloadSuffix`, never `.partial`. Unchanged,
reverify during a live Tails resume returns `artifact absent` → `classCorruption` → a **false failure
verdict** on a healthy system. The check must accept either suffix.

### 6.3 Stale-tag refusal must not become a 500

`Artifacts()` refuses any version that is not the entry's current tag — the reason the short-circuit
existed. `pkg/ostype` **gains** an exported `ErrVersionSuperseded` (it does not exist on the branch today), wrapped into that refusal; `VerifyVersion` maps
`errors.Is` on it to `(nil, "", nil)`, which is what an archived tool returns today. Every other
`Artifacts` error still propagates.

**Consequence to document (Gate 1 M10):** a transient sidecar blip now surfaces as a reverify **500**
(`api_cache.go:161-163`), where before this change a tool reverify always returned a clean no-verdict.
Consistent with how FCOS already behaves, but new for tools.

---

## 7. The re-download guard

### 7.1 The hazard

Under D4a — and under `strict` for any OS — a rejected version triggers `removeVersionDir`
(`reconcile.go:200`), which `RemoveAll`s the directory including the in-progress file. Next tick the
full land path runs again, so a persistent mismatch becomes a **1.94 GB re-download every
`--cacheInterval`, indefinitely**.

### 7.2 The guard

`reconcileTarget` skips any version whose last attempt was **rejected by verification** and whose
rejection is newer than the retry window.

**Placement is specified, not left to the executor** (re-review S-e): the check goes **before**
`o.Artifacts` (`reconcile.go:137`) inside the desired-version loop, so a guarded version also skips
the sidecar GET. Neither placement oscillates, but only this one avoids the fetch.

**No migration.** `UpsertCacheEntryArchived` (`pkg/db/cache.go:86-102`) is the writer that produces
`size=0 AND in_window=0 AND verified=0 AND verify_err<>''`; every other writer
(`UpsertCacheEntry`, `SetCacheVerified`, `SetCacheInWindow`, `SetCachePinned*`, `Scan`) was checked
across both reviews and none produces it on its own — `UpsertCacheEntry` unconditionally sets
`in_window=1`, and `SetCacheInWindow` is only ever called with `false`. The predicate is **all four
columns**, not the two an earlier revision named.

*One residual, reported rather than hidden:* `SetCacheVerified` can stamp `verified=0`/`verify_err`
onto a row already sitting at `size=0, in_window=0`, so "only writer" is strictly too strong. Neither
it nor `SetCacheInWindow` touches `fetched_at`, so the recency clause bounds the exposure, and the
worst case is one guarded hour on a version whose bytes are already gone.

**Why all four (Gate 1 B3).** The earlier predicate — `cached=0` plus a failure verdict — misfires on
a reachable transport failure: a warn-landed failed version (`cached=1`, `size>0`) later loses a file
on disk → `finalFilesPresent` false → `reconcile.go:167` runs `UpsertTargetVersion{Cached: false}`,
and `versions.go:22` does `cached = excluded.cached`, so `cached` drops to 0 while the *stale*
`verified=0`/`verify_err` remain. If the re-download then hits a transport error, `vg.Wait() != nil`
returns before anything is written — leaving exactly the two-column signature, after a transport
failure that §7.3 promises to exclude. `size=0 AND in_window=0` is what makes it unambiguous.
(Under D4a this specific sequence is now unreachable for `Large` artifacts, since they no longer
warn-land — but it stays reachable for every other OS, so the predicate is pinned regardless.)

**The comparison happens in SQL, not in Go (Gate 1 B4).** `cache_entries.fetched_at` is
`TEXT NOT NULL DEFAULT (datetime('now'))` — a **UTC** `YYYY-MM-DD HH:MM:SS` string. Nothing in the
repo parses it today (`CacheEntryRow.FetchedAt` is a plain `string`), and `modernc.org/sqlite` is
used with no time-parsing DSN option. Gate 1 measured the trap: an implementer reaching for
`time.ParseInLocation(layout, s, time.Local)` gets a timestamp *in the future*, so the version wedges
for `offset + window` (~7 h in MDT, more elsewhere) and logs a nonsense next-attempt time. So the
accessor compares in SQL —

```sql
SELECT ce.verify_err FROM cache_entries ce
  JOIN target_versions tv ON tv.id = ce.target_version_id
 WHERE tv.target_id = ? AND tv.version = ?
   AND ce.size = 0 AND ce.in_window = 0 AND ce.verified = 0 AND ce.verify_err <> ''
   AND ce.fetched_at > datetime('now', ?)
```

— returning `(blocked bool, verifyErr string)`, where `blocked=false` means "not currently guarded"
and covers both "no row" and "row does not match", removing the `ok bool` ambiguity Gate 1 raised
(M8). It selects `ce.verify_err`, **not** `1`: an earlier revision's `SELECT 1` could not supply the
string its own signature promised (re-review N-1).

**The modifier's format is pinned, because the natural Go spelling silently disables the guard**
(re-review N-3). `verifyRetryAfter` is a `time.Duration`, and `fmt.Sprintf("-%v", d)` yields
`-1h0m0s`, which SQLite does not accept. Measured:

```
sqlite> SELECT quote(datetime('now','-1h0m0s')), datetime('now','-3600 seconds');
NULL|2026-08-07 15:32:08
```

`datetime()` returns **NULL**, `ce.fetched_at > NULL` is NULL, the `WHERE` never matches, and the
guard becomes a **silent no-op** — no error, no log, no test failure except §10.15's. So the
parameter is formatted as `fmt.Sprintf("-%d seconds", int64(verifyRetryAfter/time.Second))`, and
§10.15 must assert the in-window skip specifically (a test that only checks "retries after the
window" passes against a disabled guard).

**The log line reports a relative bound, not an absolute time** (re-review N-2). An earlier revision
required logging "the next attempt time", which cannot be computed without either the forbidden Go
parse of `fetched_at` or a second SQL expression — reintroducing exactly the trap B4 raised. The
guard logs the version, its `verify_err`, and `retryAfter` as a duration ("retry in ≤ 1h0m0s").

### 7.3 Scope — including what it does NOT cover

- **Applies to every OS reaching the version loop:** Flatcar, FCOS, Debian netinst, Talos, and the
  tools. Rejected versions there drop from ~5-minute retries to one attempt per window. A behaviour
  change beyond Tails, endorsed as a DRY/KISS/YAGNI win.
- **Does NOT cover Debian DVD (Gate 1 B5).** `reconcileTarget` dispatches DVD-wanted targets at
  `reconcile.go:54-72` and **returns before the version loop**, so the guard never runs for them. An
  earlier revision's "applies to every OS" was false. The DVD path has the same hazard *today* and
  worse — `ensureDebianDVD` calls `removeUnverifiedISOs` on verify failure (`debiandvd.go:430`), so a
  persistently-divergent mirror re-pulls a multi-disc set every interval, tens of GB/hour. That is
  **pre-existing, not introduced here**, and its fix lives in a different sentinel-based state
  machine. Tracked as [jacaudi/booty#77](https://github.com/jacaudi/booty/issues/77) rather than
  silently claimed as covered.
- **Transport failures are excluded** and the §7.2 predicate now genuinely excludes them: a network
  failure returns at `reconcile.go:186-188` before any `cache_entries` row is written, so it cannot forge the four-column
  signature.
- **Self-clearing, so no new API surface.** A transient corruption heals on the next attempt after
  the window. A permanent upstream mismatch stays visible via `verify_err`, which the cache API
  already exposes.

### 7.4 Configuration

**Changed in response to Gate 1 S8/M6.** A package var, not a viper key:

```go
// verifyRetryAfter bounds how often a version rejected by verification is
// re-downloaded. A package var so tests can shrink it — same pattern as
// ostype's discoveryTimeout.
var verifyRetryAfter = time.Hour
```

An earlier revision made it viper-backed, citing this repo's rule that a network dependency must be
viper-backed so tests can redirect it. That rule is about a dependency read in *another* package
(`config.NetbootxyzEndpointsURL` is read by `pkg/ostype`, set by `pkg/cache` tests). Here the
consumer and the test are both `package cache`, so the repo's own cheaper precedent applies
(`discoveryTimeout`, `pkg/ostype/http.go:14`). This deletes a config const, a viper default, a CLI
flag, a `CONFIGURATION.md` row, and the startup-validation question. Promote it to a flag when an
operator actually needs to tune it, not before.

---

## 8. Trust model — stated honestly

`sha256-checksums.txt` is hosted on the **same GitHub release** as the ISO, fetched over the same TLS
to the same origin, with no independent trust anchor.

**What this defends against, with D4a in place:** corruption in transit, a truncated transfer, a
resume that appended to the wrong bytes, and a mirror or CDN serving damaged content — under **every**
policy, including the default `warn`, because a `Large` artifact failing its digest never lands.
Before D4a this list was aspirational under the default; it is now accurate.

**This change verifies future downloads, not the ISO already on disk (re-review S-a).** The
settled-skip at `reconcile.go:163` fires on `cached=1 && finalFilesPresent` *before* any verdict is
computed, so the Tails ISO that landed unverified under #75 keeps `verified=NULL` on the land path
forever. At `retain: 1`, once a new Tails release appears that version is archived — and per the
paragraph below, reverify cannot produce a verdict for a superseded tag. So the currently-deployed
artifact's only verification window is **a manual reverify while it is still upstream's current
release**; after that it is unverifiable by any path short of evicting it. §1's "instead of caching
it entirely unverified" and §8's defends-against list describe *subsequent* downloads. This is
distinct from §9's non-retroactivity bullet, which is about policy tightening, not newly-available
material.

**Disk rot is narrower than an earlier revision claimed (Gate 1 S3).** Reverify can only produce a
verdict for a Tails version that is *still upstream's current release*, because `Artifacts` refuses a
superseded tag and §6.3 maps that to "no verdict". Tools run at `retain: 1`, so when a new Tails
release lands the previous version is archived but stays on disk and menu-bootable — and that
archived copy, the one longest exposed to bit rot, is exactly the one reverify can never check. The
honest claim is *disk rot on the currently-published version*.

**What it does not defend against:** a compromised release, a compromised netboot.xyz account, or an
attacker with write access to the mirror — any of whom replaces ISO and checksum together. This is
integrity, not provenance.

Real provenance needs `tails-amd64.iso.sig` verified against Tails' signing key. `SigURL`'s existing
path cannot express that — it expects a signature over a *checksum file* against a known embedded
keyring (the Debian cdimage / Flatcar shape), not a detached signature over a multi-GB ISO whose key
booty does not ship. **Out of scope for #76**, and D4's narrowed guard keeps that door shut loudly.

### 8.1 The `strict` behaviour change

Today every tool artifact is `classNotVerifiable`, which `strict` admits. Once Tails declares a
digest, `strict` begins enforcing it. With D4a, `warn` enforces it too for this artifact — so the
change is visible in **both** default and strict deployments, not only strict.

The honest summary: *Tails is now protected against a corrupted download, under every policy except
`off`. It is not protected against a malicious upstream.*

---

## 9. Out of scope

- **GPG verification of `tails-amd64.iso.sig`** (§8).
- **The other seven tools.** They publish no checksums — verified across both source repos (§2).
- **The Debian DVD retry loop** (§7.3) — pre-existing, tracked as
  [#77](https://github.com/jacaudi/booty/issues/77).
- **The general warn-landed-never-retried gap** for small artifacts on any OS (D4a) — pre-existing.
  **No issue filed yet**; it warrants one, but that is the user's call to make, not this design's.
- **Retroactive re-verification.** Policy tightening stays non-retroactive per P3b §5/D15.

---

## 10. Testing

Every behavioural test must be **mutation-checked** — disable the code it covers and confirm the test
fails. Slice 2 shipped a plan-mandated test that passed identically with D13's routing disabled and
survived three review rounds; that is the failure mode this requirement exists to prevent.

**`pkg/ostype`** (in-package — `ostype_test.go` is `package ostype`; qualified calls will not compile):

1. `Artifacts` populates `SHA256` on the ISO only, from an `httptest`-served sidecar, other files
   left not-verifiable. Redirect via `viper.Set(config.NetbootxyzAssetBase, …)`.
2. Sidecar 404 → `Artifacts` errors (D2).
3. Sidecar malformed → `Artifacts` errors (D2).
4. **Sidecar valid but missing a `checksumCovers` entry → `Artifacts` errors (D2a).** This is the
   a sidecar-only desync, or a rename the manifest has not yet tracked (§3, D2a); it must not
   silently downgrade.
5. Every registration's `checksumCovers` ⊆ `files`.
6. A tool with no `checksums` issues no sidecar request and behaves exactly as today.
7. The registry membership test needs no change (no OS added) — confirm.

**`pkg/cache`** (`package cache`):

8. **The load-bearing one.** A `Large` artifact with a wrong `SHA256` is **rejected under `warn` as
   well as `strict`** (D4a): no bytes at the final path, in-progress file removed. Mutation check:
   remove the `!a.Large` condition and the `warn` case must start landing, failing this test.
9. A `Large` artifact with a correct `SHA256` lands and records `verified=1`.
10. A `Large` artifact declaring `SigURL` still hard-fails (D4).
10a. **D4b:** make `hashFile` fail on a `Large` artifact (unreadable in-progress file) and assert
    `landArtifact` returns an **error** — not a corruption verdict — so no row is written, the
    version dir survives, the in-progress bytes survive, and the guard is not armed. Mutation check:
    revert to letting `verifyArtifact` hash it and this must fail. This is the re-review's BL-1.
11. Under `policy == "off"`, a `Large` artifact lands unverified — pinning §5.2's acknowledged
    reachable path so it stays deliberate.
12. **Resume correctness:** pre-seed a partial in-progress file, serve the remainder from a
    `Range`-honouring `httptest` server, assert the digest covers the **whole** file, not the delta.
13. **416:** pre-seed a full-size in-progress file, serve 416, assert verify-then-land (not an
    unverified rename).
14. `VerifyVersion` verifies a tails version; returns *no verdict* (not `artifact absent`) with a
    live in-progress sibling (§6.2); returns no verdict (not an error) on a superseded tag (§6.3).
15. The retry guard: **assert the in-window skip specifically**, not only that a retry eventually
    happens — a test that checks only the latter passes against a guard disabled by the N-3 NULL
    bug. The window is shrunk by setting `verifyRetryAfter` directly (§7.4) — no sleeps. Also assert
    a transport failure is **not** guarded, including the B3 sequence (warn-land → file loss →
    `cached=0` → transport error) for a non-`Large` artifact.
16. `scan.go` excludes in-progress files from the size total (§5.5).

**`pkg/checksum`:**

17. Direct unit tests for `ParseSums`: the Tails single-line fixture, a real multi-line Debian
    `SHA256SUMS` fixture, a malformed line, and an empty body. These did not exist for
    `parseSHA256SUMS` (§4.5) and are part of the move.

**No lab gate.** Everything here is exercisable with `httptest` and fixtures. A real-ISO confirmation
run is available if wanted — the lab's cached 1.94 GB ISO and its known-good digest are on disk — but
it is not a gate.

---

## 11. Documentation surface

Each entry checked against the file.

- `docs/schema/CATALOG.md` — **grepped: zero verification/checksum/signature content.** An earlier
  revision claimed this change narrows a "no verification material" statement there; no such
  statement exists and that claim is withdrawn. What it needs is a *new* note that Tails alone among
  the tools carries an upstream checksum. Its retain-1 caveat (lines 98-107) is real and untouched.
- `docs/CONFIGURATION.md` — the `--signaturePolicy` section (line 404) gains §8.1, **including that
  `warn` now rejects a `Large` checksum failure** (D4a). Its existing lines 434-436 currently say
  such failures land under `warn`; that becomes wrong for `Large` and must be corrected, not just
  appended to. No `verifyRetryAfter` row — §7.4 makes it a package var.
- `docs/designs/2026-07-29-tool-rescue-os-support-design.md` §8.5 — from "deferred to a later slice"
  to "resolved by #76".
- **No release notes (Gate 1 S1).** An earlier revision listed them; there is no CHANGELOG, no
  release automation, and `gh release list` is empty. The behaviour change is communicated in the PR
  body and `CONFIGURATION.md` instead.
- `docs/schema/API.md` (the reverify row at 463, the `verified`/`verifyErr` DTO rows at 480-481) / `DATABASE.md` (line 108) — **no change, verified by reading
  both.** They define the `verified` tri-state generically and never assert that tool artifacts are
  unverifiable, so Tails moving `NULL → 1` changes which rows hold which value, not what the column
  means.

---

## 12. Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Upstream stops publishing the sidecar | Medium | D2 fails loud; the cached ISO keeps booting; a warning every pass rather than a silent downgrade. |
| Upstream renames the ISO asset, manifest tracks it | Medium | The **manifest-membership check** errors (`netbootxyz.go:250`), before any artifact is built — D2a is never reached. Confirmed to have happened twice historically. |
| Upstream renames the ISO asset, manifest lags it | Medium | Membership passes; the sidecar from the new `path` keys the new name, so **D2a** errors — still inside `Artifacts`, before any download. |
| Sidecar-only desync (sidecar drops or rekeys the ISO line) | Medium | D2a errors explicitly. This is what D2a *uniquely* catches — nothing else in the pipeline notices. |
| D4a makes a boot unavailable that `warn` would have allowed | Low–Medium | Only for a `Large` artifact whose digest is definitively wrong. Where a prior version is cached it stays on disk and menu-bootable — but on a **first-ever** Tails cache there is no fallback, so the target has no bootable Tails at all where the pre-D4a default would have booted something. Accepted: serving unexplained divergence as an anonymity distro is the worse outcome. |
| The retry guard masks a real problem | Low | `verify_err` is recorded and API-exposed on every rejection; the guard suppresses only *re-downloads*, never the verdict or the log. |
| Hashing stalls a reconcile pass | Low | 3.55 s measured (~20 s on a slow spindle), once per version download, inside an errgroup slot already occupied by a multi-GB transfer. |
| The parser move breaks the DVD path | Low | Single call site; §10.17 adds the direct tests it never had; `go build ./...` catches the import change. |

---

## 13. Open questions

None blocking. #76's four open questions are decided: sidecar availability (§2 + D2/D2a), format (§2
+ D-parser), the `strict` behaviour change (§8.1, now also a `warn` change via D4a), and the trust
model (§8). The one assumption that cannot be closed by inspection — that upstream keeps publishing
the sidecar — is a fact about the future, and D2 is the design's response to it.

Two pre-existing defects were found during review and are deliberately **not** fixed here (§9): the
Debian DVD retry loop, and the general warn-landed-never-retried gap.
