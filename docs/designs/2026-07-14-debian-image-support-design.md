# Debian Image Support: netboot path + netinst modernization + DVD archive/mirror

**Type:** Design
**Date:** 2026-07-14
**Status:** Revised twice after SGE design review (verdict READY-FOR-PLANNING once A1/A2/A3 folded — done); pending user written-spec review
**Feature:** debian-image-support

> **Revision note (post-SGE review).** This revision folds in the sr-go-engineer
> design review (verdict AMEND-BEFORE-PLANNING). Material changes: a Debian iPXE
> **boot path** is now in-scope (booty has none today — B/I3); the target model
> keeps the release-line **in `params`** with mutable `sourceMode`/`dvdCount`
> **columns** (I1); ISO download uses a **separate, un-timeout-capped** path (B1);
> `promote` runs on the **reconcile coordinator**, not inline (B3); `SHA256SUMS`
> is **GPG-verified** (I5); `dvdCount<full` is a **documented disc-1 partial
> mirror** (B2); extraction is **stage-then-swap** with its own idempotency marker
> (I7). See §13 for the full disposition of findings.

---

## 1. Context & motivation

Booty caches/serves netboot artifacts for Flatcar, Fedora CoreOS, and Talos
(predefined targets seeded in `pkg/cache/seed.go`; per-OS discovery/artifact logic
in `pkg/ostype/*`; per-OS iPXE boot scripts in `pkg/tftp/pxe_config.go`). Debian is
*registered* as an OS type (`pkg/ostype/debian.go`) and wired into the
config/preseed serving path (the `debianconfig`/`preseed` work), but **two whole
layers are missing**:

1. **No boot path.** `PXEConfig` has `flatcar`/`talos.ipxe`/`flatcar.ipxe`/
   `coreos.ipxe`/`holding.ipxe` — there is **no `debian.ipxe`**, and dispatch is
   `PXEConfig[os+".ipxe"]` (`pkg/tftp/tftp.go:159`). A Debian host cannot netboot
   today. The `debianconfig` work built *what to serve at `/preseed`*, never *how a
   Debian machine boots the installer that fetches it*.
2. **Stub image caching.** `DiscoverVersions` returns a hardcoded `{"12.5","11.9"}`
   placeholder; `Artifacts` builds only the netinst `linux`+`initrd.gz` pair for
   `stable→bookworm`/`oldstable→bullseye`; no `trixie` (13), no DVD support. Debian
   is not seeded, so a stock booty caches zero Debian.

Two operational needs motivate the work:

1. **Offline / air-gapped and LAN-fast installs.** A netinst install pulls every
   package from a network mirror. Operators want installs that need no internet and
   don't hammer `deb.debian.org` on a busy LAN.
2. **A durable local archive of specific Debian media.** Older releases move to
   `cdimage.debian.org/cdimage/archive/` as they age (Debian 11 / bullseye is
   already there). Booty should hold the exact DVD media as a checksummed,
   GPG-verified, self-contained archive before it becomes hard to fetch.

Flatcar and Fedora CoreOS are immutable, package-preinstalled images — no
package-source concern, explicitly out of scope for the mirror work.

## 2. Goals

- A **Debian iPXE boot path** (`debian.ipxe`) so Debian hosts netboot the d-i
  installer, wired to fetch booty's `/preseed`.
- Real Debian version discovery replacing the `{12.5, 11.9}` placeholder;
  support **13 (trixie)**, **12 (bookworm)**, **11 (bullseye)**.
- Two serving modes per Debian target:
  - **`netinst`** — network boot + network apt mirror (small, always-current).
  - **`dvd`** — self-contained: installer *and* packages served from a locally
    cached, GPG+checksum-verified, extracted DVD set. No dependency on Debian's
    servers at install time.
- An operator-triggered **promote** (`netinst`→`dvd`): download the latest DVD set,
  swap serving, delete the netinst artifacts.
- **Configurable DVD count** per target (1 disc for testing → full set for
  production), with documented partial-mirror semantics (§6.3).
- DVD-mode targets are a **pinned archive**: verbatim ISOs retained and excluded
  from eviction; ISOs GPG+checksum verified.

## 3. Non-goals

- Flatcar / FCOS package handling (immutable images — N/A).
- The **multi-arch + expanded channels/streams** work (Flatcar beta/lts, FCOS
  `testing`, arm64 threading fixes). Separate second spec, agreed to follow this.
- A full rolling apt mirror (debmirror/rsync of the whole suite). The DVD set is a
  *frozen snapshot* — what the archive goal wants.
- Automatic/scheduled promotion (operator-initiated only).
- Regenerating apt indices for partial DVD subsets (§6.3 documents the disc-1
  limitation instead).
- arm64 DVD (DVDs are amd64-only; arm64 Debian is netinst-only).

## 4. Confirmed requirements

| Release | Mode | Arch | Notes |
|---|---|---|---|
| Debian 13 (trixie) | `netinst`, promotable → `dvd` | amd64 + arm64 (netinst); amd64 (after promote) | current stable; boot+mirror from network until promoted |
| Debian 12 (bookworm) | `dvd` | amd64 | full DVD set, pinned archive |
| Debian 11 (bullseye) | `dvd` | amd64 | full DVD set, pinned archive; source in `cdimage/archive/` |

- DVD count configurable: **1 for testing, up to full set for production.**
  `dvdCount < full` is a **partial mirror** (§6.3).
- DVD serve mechanism: **keep verbatim ISO(s) AND extract a merged tree** (§6).
- Promote is **manual / operator-triggered**.

## 5. cdimage source layout (verified 2026-07-14)

- **Stable (13) DVD:** `https://cdimage.debian.org/debian-cd/current/<arch>/iso-dvd/debian-<point>-<arch>-DVD-<n>.iso`
  (currently `debian-13.6.0-amd64-DVD-1.iso`), with `SHA256SUMS` + `SHA256SUMS.sign`
  in the same dir.
- **Oldstable & older (12, 11) DVD:** `https://cdimage.debian.org/cdimage/archive/<point>/<arch>/iso-dvd/…`
  (e.g. `12.15.0`, `11.11.0`), with `SHA256SUMS` + `.sign`. `debian-cd/current/`
  holds **only** the newest stable, so oldstable/older resolve via the archive tree.
- **netinst installer pair:** the raw netboot `linux`+`initrd.gz` under
  `deb.debian.org/.../installer-<arch>/current/images/netboot/…` (today's path).

**Discovery** resolves the newest point release for a version line and must not
depend on brittle HTML scraping where avoidable — see §8.1 / M2.

## 6. Serve mechanism (DVD → HTTP)

### 6.1 Decision: keep verbatim ISO + extract a merged tree

For each DVD-mode target, booty stores the downloaded `.iso` file(s) verbatim *and*
extracts their contents, merging DVD-1..N into a single `dists/`+`pool/` tree
served by booty's existing static file server (`dataFileHandler`, `pkg/http/http.go:72`).

Rationale — the only option satisfying all three goals at once:

- **Exact-media archive:** the verbatim `.iso`, once GPG+checksum verified (§6.2),
  is a provable, durable copy that survives the release leaving the live mirror.
- **Offline/LAN install + self-contained boot:** the merged tree is plain static
  files; installer kernel/initrd (from the DVD's `install.<arch>/` tree) *and*
  packages both come from it, so an install needs nothing from Debian's servers.
- **Operationally boring:** no container privileges; the ISO9660 reader is confined
  to the **cache-time extraction step** (§8.2), never the serve hot path.

Rejected: **loop-mount** (needs `CAP_SYS_ADMIN` in a distroless pod);
**extract-and-delete-ISO** (discards the archive); **userspace-reader-serve-on-the-fly**
(1× storage but a permanent multi-ISO union resolver — extraction does the merge
once instead).

**Accepted cost:** ~2× disk (ISO + extracted). Full amd64 sets are ~3 discs
(~11 GB) each, so 11+12 ≈ 2×(11+11) ≈ **~44 GB**. Cheapest resource for a
self-hosted netboot server.

### 6.2 Provenance: GPG-verify the checksums

Checksumming against an unauthenticated `SHA256SUMS` proves nothing against a
tampered source. Booty **GPG-verifies `SHA256SUMS` against `SHA256SUMS.sign`** using
Debian's CD-signing key, then checksums each ISO against the verified sums. This
reuses the existing detached-GPG path (`verifyDetachedGPG`, `pkg/cache/verify.go:145`;
`Artifact.SigURL`/`GPGKey`, `pkg/ostype/ostype.go:22-23`) — the Debian CD-signing
key is added to the keyring alongside `flatcarKeyring`.

### 6.3 Merge + partial-mirror semantics (disc-1 limitation)

On a real Debian DVD set, disc 1's `dists/*/*/Packages` indices reference `pool/`
files that physically live on discs 2-3. Booty's merge is a **`pool/` union** with
**disc-1's `dists/` served verbatim** (disc 1 carries the authoritative indices).
Consequences, documented and accepted (chosen over regenerating indices — KISS):

- **Full set (all discs):** complete offline mirror — every indexed package present.
- **`dvdCount < full` (e.g. 1, for testing):** a **partial mirror** — apt's index
  references packages that may be absent; only **disc-1-resident packages install
  offline**. This covers typical minimal/server installs. The offline install gate
  (§10) at `dvdCount=1` therefore uses a **disc-1-resident package set**.

Booty does **not** regenerate apt indices (non-goal §3); the operator gets a
complete mirror by caching the full set.

## 7. Target model

Debian target **identity stays `UNIQUE(os, arch, params)`** — unchanged from today
(`migrations/0001_init.sql:12`; `EnsureTarget` conflicts on that tuple,
`pkg/db/targets.go`). The release line lives **in `params` under the existing
`channel` key** — booty's Debian type already uses `channel` as its discriminator
(`RequiredParams()=["channel"]`, `debian.go:16`; today it maps `stable`/`oldstable`
→ codename). This work carries the **release identifier** in that same key
(e.g. `{"channel":"13"}`, mapped 11→bullseye / 12→bookworm / 13→trixie). Reusing
`channel` — rather than a new `suite` key (A2) — means **no change** to
`RequiredParams`, the create-API validation, or `paramSegment` (`layout.go:112-120`),
which already surfaces `channel` into the on-disk/URL segment **and into the
boot-menu selection tuple** `<cacheName>/<segment>/<arch>/<version>` (`menu.go:117`).
That last point is load-bearing: it is what lets the boot tuple carry the suite so
the §8.3 host→suite→mode resolution works off the menu path.

Two **mutable columns** are added by a simple `ALTER TABLE ADD COLUMN` migration —
*not* part of identity:

- **`sourceMode`** (`netinst`|`dvd`) — the **effective** serving mode. Serving and
  boot dispatch key off this; the reconciler flips it to `dvd` **only after** the
  extracted tree lands (§8.5), so `dvd` is never served against a missing tree.
- **`dvdCount`** (1..N) — disc count for `dvd` mode (§6.3).
- **`desired_mode`** (nullable) — the **promote intent** (A1). `Trigger()` is an
  argument-less coalesced kick (`reconciler.go:66`) carrying no payload, so the
  transition must be recorded durably: `promote` sets `desired_mode=dvd`; the
  reconciler's Debian branch acts on `desired_mode != sourceMode`, and on success
  sets `sourceMode=desired_mode` and clears `desired_mode`. This separates
  *aspirational* from *effective* mode, resolving the "flip last" requirement.

Therefore: seeding 11/12/13 as distinct rows works (params differ), no
`EnsureTarget` collision; and **promote flips `sourceMode` in place** on the same
row with no re-keying of disk layout, URLs, or eviction.

This replaces the earlier "identity must become `(os,arch,version)`" framing, which
manufactured a migration and collided with the params-blob identity and the
`CreateTarget` DTO (which has no version field).

DVD-mode targets are excluded from **eviction** via the existing per-version
`cache_entries.pinned` guard (`pkg/db/cache.go`; `evict.go`/`ListArchivedUnpinned`) —
see §9. (Retention selection, `retentionFor`, never deletes disk; the correct
mechanism is the eviction guard, not "retention exclusion.")

## 8. Components & data flow

### 8.1 `pkg/ostype/debian.go` — discovery + netinst artifacts

- **`DiscoverVersions`** — resolve the newest point release for the target's suite:
  `debian-cd/current/` for stable (13); highest `<major>.x.y` under
  `cdimage/archive/` for 12/11. Prefer a structured source over HTML autoindex
  scraping where one exists; on discovery failure the reconciler already keeps the
  existing set (`reconcile.go:65`) (M2).
- **Suite→codename** — add `trixie` (13); keep `bookworm`/`bullseye`; drive by the
  `suite` param.
- **`Artifacts`** — returns the **netinst** `linux`+`initrd.gz` pair (amd64+arm64).
  It stays within the frozen `OS` interface `Artifacts(ctx, version, arch, params)`
  and is **not** widened for DVD mode (I2). DVD handling is a **Debian-keyed branch
  in the reconciler** (§8.2), mirroring the existing Talos-keyed branch precedent
  in `pkg/cache/retention.go:44-49` — no interface change.

### 8.2 DVD caching + extraction (cache-time, reconciler branch)

A Debian-`dvd` branch in the reconcile flow (single-writer coordinator goroutine,
`reconcile.go:17-19`):

1. **Download** DVD-1..`dvdCount` ISOs + `SHA256SUMS` + `SHA256SUMS.sign` via a
   **separate ISO download path** — context-cancellation only, **no total-request
   `Timeout`** (the shared `httpClient` caps requests at 5 min, `config.go:66`,
   which no multi-GB ISO can meet — B1), streamed to disk, ideally HTTP-Range
   resumable.
2. **Verify** — GPG-verify `SHA256SUMS` (§6.2), then checksum each ISO.
3. **Extract + merge** — extract each ISO with a named Go ISO9660 reader
   (**Rock Ridge required** for deep, case-sensitive `pool/main/…/*.deb` paths;
   the library must be validated against a real Debian DVD before adoption — I6),
   merging per §6.3 into a **staging** tree, then **atomically swap** into place
   (mirror the existing `.partial`→rename land pattern, `verify.go:96-101`). The
   extraction phase has its **own idempotency marker** (a completion sentinel) so a
   settled ISO does not cause the artifact-present skip (`reconcile.go:136`) to
   bypass extraction (I7).
4. **Pin** the version (`cache_entries.pinned`) and retain the verbatim ISOs.

### 8.3 Debian boot path (`pkg/tftp`)

New `PXEConfig["debian.ipxe"]` entry, alongside the flatcar/talos/coreos entries:

- `kernel <BASEURL>/…/linux auto=true priority=critical preseed/url=http://[[server]]/preseed …`
- `initrd <BASEURL>/…/initrd.gz`
- `[[debian-baseurl]]` points at the cached tree: the **extracted DVD `install.<arch>/`**
  for `dvd` mode, or the cached **netinst** dir for `netinst` mode.

**Host→target→mirror resolution** is new plumbing (acknowledged; not "trivial"):
the boot dispatch (`bootDispatch`, assigned/menu, `tftp.go`) and the preseed
renderer must resolve the host's Debian **suite + sourceMode** to choose (a) the
boot kernel/initrd source and (b) the apt mirror. Suite selection reuses the
existing **assigned/menu** dispatch (a host is assigned a Debian suite, or the boot
menu offers cached Debian suites via `PartitionCached`); the suite rides the boot
selection tuple via the `channel` segment (§7), so the tuple can discriminate
suites. The exact host→suite binding is nailed down in the implementation plan,
consistent with existing boot-dispatch. Wiring detail: add a `"debian": "Debian"`
entry to `osTitle` (`menu.go:16`) so menu items render a proper label rather than
the raw cache name (A3).

### 8.4 Preseed mirror wiring (`pkg/http`)

For a `dvd`-mode Debian host, the rendered preseed points `d-i mirror/http/hostname`
+ `mirror/http/directory` at booty's local extracted tree. This is **new plumbing**,
not a drop-in: today the mirror directives are emitted only when an operator authors
a `mirror:` block (`debiangen.go:460-466`), and `TemplateVars` (`render.go:17-29`)
has no mirror/target fields and `handlePreseedRequest` has no target/`sourceMode`
awareness (I3). The work: host→target→mode resolution + new template vars + the
mirror template lines. `netinst` mode is unchanged (network mirror).

### 8.5 Promote (`POST /api/v1/targets/{id}/promote-dvd`)

Open until auth (consistent with the targets API). The handler **records intent —
sets `desired_mode=dvd` (+ `dvdCount`) on the row (§7) — and enqueues a reconcile
via `Trigger()`** (`api_targets.go:139,178,224`), then returns immediately. It does
**not** download or mutate cache state inline (B3: the cache is single-writer, and a
multi-GB transfer would blow the 900s HTTP `WriteTimeout`, `http.go:53`). The
handler validates first: Debian, currently `sourceMode=netinst`, **arch amd64**
(M1). The reconciler's Debian branch then, on `desired_mode != sourceMode`:

1. Download + verify + extract/merge the latest DVD set (§8.2).
2. Flip effective `sourceMode` → `dvd`, clear `desired_mode`, pin.
3. Delete the superseded netinst artifacts.

The effective `sourceMode` flip is last and single-writer, so a failed/partial
download leaves the target serving `netinst` (the `desired_mode` intent persists for
the next reconcile tick to retry). Serving/boot never observe `dvd` until the tree
exists.

### 8.6 Seeding (`pkg/cache/seed.go`)

- Seed **Debian 13 netinst** (enabled) by default — small, safe.
- **Do not** auto-enable 11/12 DVD downloads on a fresh boot (tens of GB). Operators
  opt in via the targets API / a flag; default `dvdCount` = **1** (testing), bumped
  to the full set explicitly. *(Confirm enabled-opt-in vs. seeded-but-disabled —
  §11.)*

## 9. Storage, retention, pinning

- **DVD-mode:** verbatim ISOs + extracted merged tree, both retained. The version is
  **pinned** via `cache_entries.pinned`, which excludes it from the byte-budget
  **eviction** sweep (`evict.go`/`ListArchivedUnpinned`) — the correct
  never-delete mechanism. (Alternatively `mode="manual"` already yields
  "never archived/pruned"; the plan picks one and states it in the code's terms.)
- **netinst-mode:** the installer pair for the newest point release; normal handling.
- **Promote** deletes only the netinst artifacts it supersedes.
- Debian 11: source is `cdimage/archive/`; once cached, booty is the durable copy.

## 10. Testing strategy

- **Unit:** discovery point-release resolution (current vs archive) + failure
  fallback; suite→codename incl. trixie; netinst `Artifacts`; GPG+checksum verify;
  DVD `pool/` union + disc-1 `dists/` merge; stage-then-swap + idempotency marker;
  promote state machine (netinst→dvd; failed download leaves netinst intact; arch
  reject).
- **Byte/golden:** preseed mirror directives differ correctly between `netinst`
  (network) and `dvd` (local booty tree); `debian.ipxe` token expansion.
- **Install gate (load-bearing):** the netboot-lab (QEMU on Apple-Silicon, method in
  memory `0e962ae3`) runs at least one **offline DVD-mode install** end to end —
  `debian.ipxe` boots the installer from the extracted tree, apt installs from
  booty's local mirror **with no internet**, machine reboots into working Debian.
  At `dvdCount=1` the package set is **disc-1-resident** (§6.3). Byte goldens
  validate bytes, not install-correctness; the lab install is the real check (same
  lesson as the `debianconfig` F1/F2/F3 bugs).

## 11. Open items for written-spec review

1. **DVD-mode default seeding** — proposed: 13 netinst enabled; 11/12 DVD opt-in
   (not auto-enabled), default `dvdCount=1`. Confirm vs. seeded-but-disabled.
2. **Pin vs. manual mode** for the never-evict guarantee (§9) — plan picks one.
3. **Host→suite binding** for boot dispatch (§8.3) — assigned vs. menu selection;
   the constraint (suite rides the boot tuple via the `channel` segment, §7) is
   fixed; the plan nails the exact binding mechanism.

## 12. Rough work breakdown (for the implementation plan)

1. Debian discovery: real point-release resolution (current + archive) + trixie.
2. Target model: release id under the existing `channel` param key + mutable
   `sourceMode`/`dvdCount`/`desired_mode` columns + `ALTER TABLE` migration.
3. Netinst `Artifacts` (13 amd64+arm64); Debian-keyed DVD branch in the reconciler.
4. Separate ISO download path (no total timeout, ctx-cancel, streamed, resumable).
5. GPG+checksum verification (Debian CD key in keyring; reuse `verifyDetachedGPG`).
6. ISO9660 extraction + `pool/` merge + stage-then-swap + idempotency marker (name
   & validate the library).
7. `debian.ipxe` boot entry + host→target→(kernel/initrd, mirror) resolution.
8. Preseed mirror wiring for `dvd` mode (new `TemplateVars` fields + template lines).
9. Promote endpoint → reconcile-coordinator enqueue + state machine + netinst
   cleanup + arch reject.
10. Seeding (13 netinst; 11/12 opt-in); pin/eviction wiring.
11. Tests incl. the netboot-lab offline-install gate.

## 13. Disposition of SGE design-review findings

- **B1** (5-min download ceiling) — adopted: separate ISO download path (§8.2/§12.4).
- **B2** (partial-mirror coherence) — adopted as documented disc-1 limitation
  (§6.3); `dvdCount<full` is a partial mirror, full set is complete.
- **B3** (promote inline vs single-writer) — adopted: promote enqueues to the
  reconcile coordinator (§8.5).
- **I1** (target-model reframing) — adopted: suite-in-params + mutable columns (§7).
- **I2** (`Artifacts` can't branch on mode) — adopted: DVD is a reconciler branch,
  interface unchanged (§8.1/§8.2).
- **I3** (mirror wiring + no boot path) — adopted: boot path in-scope (§8.3), preseed
  wiring called out as new plumbing (§8.4).
- **I4** (pin/retention wording) — adopted: reuse per-version `cache_entries.pinned`
  eviction guard; wording fixed (§7/§9).
- **I5** (GPG-verify SHA256SUMS) — adopted (§6.2).
- **I6** (name/validate ISO9660 lib) — adopted as a gated step (§8.2/§12.6).
- **I7** (stage-then-swap + idempotency) — adopted (§8.2).
- **M1** promote arch reject (§8.5); **M2** discovery robustness (§8.1); **M3**
  `dvdCount` semantics tied to §6.3; **M4** disk estimate corrected to ~44 GB (§6.1).

**Second-round findings (re-review):**

- **A1** (promote intent unrepresented; `Trigger()` payload-less) — adopted: a
  nullable `desired_mode` column separates intent from effective `sourceMode`; the
  reconciler flips effective mode on success (§7/§8.5).
- **A2** (`suite` param not surfaced by `paramSegment`/boot tuple) — adopted: reuse
  the existing `channel` key as the release discriminator, already surfaced into the
  boot tuple — no `RequiredParams`/API/segment churn (§7/§8.3).
- **A3** (Debian menu label) — adopted: add `osTitle["debian"]` (§8.3).
