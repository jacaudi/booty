# Design: Tool / rescue OS support (netboot.xyz-sourced)

**Type:** Design
**Date:** 2026-07-29
**Issue:** none yet — this design precedes the tracking issue
**Status:** Approved by user 2026-07-29 (section-by-section). Three independent cold reviews folded
(see §12): Gate 1 design review → `AMEND-BEFORE-PLANNING`; Gate 2 plan review; Gate 2 re-review of
the rewritten plan, which found a **real correctness bug in §8.4** (now fixed) by executing the
plan's code. Implementation plan written and amended. **Ready for execution** once the
`worktree-docs-boot-menu-vlan` prerequisite merges.
**Roadmap slice:** OS-support wishlist — the "7 tool OSes" cohort, plus Tails

---

## 1. Problem

booty's interactive boot menu (`boot_mode='menu'`) lists whatever is cached on disk. Today that can
only ever be Flatcar, Fedora CoreOS, Talos, or Debian: the OS registry is populated by per-file
`init()` into `osRegistry` (`pkg/ostype/ostype.go:70-73`), and `catalog.yaml` rejects any other
`os:` value at startup via the `catalogArches` table (`pkg/cache/catalog.go:23-28, 82-88`).

The operator-facing want is a rescue kit at the console: pick a machine, netboot it, and get
Memtest86+ or SystemRescue without touching its assignment or carrying USB sticks. That is the
classic netboot.xyz use case and it fits menu mode exactly — but no amount of `catalog.yaml`
editing can produce it.

This design adds a **tool OS** class sourced from netboot.xyz's `endpoints.yml`, so those images
become ordinary cache targets and therefore ordinary menu entries.

## 2. Goals / Non-goals

**Goals**

- Eight tool images become cacheable, catalog-declarable targets: Memtest86+, SystemRescue,
  Clonezilla, ShredOS, UEFI Shell, ZFSBootMenu, Rescatux, Tails. This design covers all eight;
  **the slice it specifies delivers three** (D3), with the rest landing additively per §11.
- They appear in the boot menu under their own `Tools & rescue...` submenu.
- Versions track netboot.xyz automatically, the way every other OS tracks its upstream.
- booty **hosts and caches** the artifacts itself; it does not chainload netboot.xyz mirrors at
  boot time (user decision, 2026-07-01).
- Adding tool #9 later is additive — see §11 for the honest site count.

**Non-goals (YAGNI)**

- Per-host config for tools. They take none; there is no Ignition/machineconfig/preseed analogue.
- A UI control for assigning a tool to a host. Menu mode is the intended path. (The API path still
  exists and must not misbehave — see §6.4.)
- Mirroring netboot.xyz's full endpoint catalogue. Eight curated tools, chosen 2026-07-01.
- Building booty-specific tool images, or customizing the upstream ones.
- Signature/checksum verification of tool artifacts — see §8.5, no mechanism exists upstream.

## 3. Decisions

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| D1 | How is `endpoints.yml` consumed? | **Runtime fetch**, memoized. | Consistent with how every other OS discovers versions; keeps tool versions self-updating. Accepts a runtime dependency on a third-party schema. Alternative weighed in §13.1. |
| D2 | Menu placement | **`Tools & rescue...` submenu**, parallel to `Archived OSes...`. | Keeps the installable-OS list short; reuses the nested-menu shape `renderMenu` already has; matches netboot.xyz's own organization. |
| D3 | First-slice scope | **Architecture + 3 tools**: SystemRescue, UEFI Shell, Memtest86+. | Revised post-review: these three now cover the three genuinely hard cases — multi-`initrd`, firmware-gated, and platform/bitness-branching (§6.2). Keeps the lab gate at 3 boots. |
| D4 | Taxonomy shape | **One shared `netbootxyzOS` implementation, one data row per tool.** | The netboot.xyz plumbing is one piece of knowledge that changes together — DRY. Per-tool variation is data. Precedent: `ostype.go:31` argues `Family` should be data, not an interface. |
| D5 | Where does boot data live? | Discovery data in `pkg/ostype`; boot scripts in `pkg/tftp`. | Exactly how every existing OS is already split (`ostype/debian.go` + `PXEConfig["debian.ipxe"]`, `pxe_config.go:54-58`). |
| D6 | Default catalog | Tools are **not** in the flag-derived default; opt-in via `catalog.yaml`. | Follows the Debian-DVD precedent that a fresh install downloads nothing until an operator asks. SystemRescue and Tails are ~1 GB and ~1.5 GB. |
| D7 | On-disk version identity | **The netboot.xyz release tag** (`13.01-d20a63ac`), not the pretty `version`. **The menu label shows the tag too.** | Revised twice. The tag changes exactly when the artifacts change, making version→path total; the pretty version does not (§8.1). An earlier revision promised the pretty version in the menu label — **withdrawn**, see §7. |
| D8 | Boot-script shape | **One literal iPXE script per tool**, tokenized on `[[baseurl]]` — **no form taxonomy**. | Revised post-review. The eight tools need 1–3 `initrd` lines, `${platform}` branches, and per-tool cmdlines that no three-form abstraction can express (§6.1). This is also the repo's existing idiom and strictly less code. |
| D9 | Snapshot memoization shape | **Mutex-guarded memo + explicit reset**, mirroring `pkg/ostype/streams.go`. No TTL, no single-flight. | Revised post-review. Targets reconcile **sequentially** (`pkg/cache/reconciler.go:113-120`), so single-flight guards nothing; the mutex exists for the API-goroutine reader (§8.3). |
| D10 | Which files a tool caches | **A per-tool `files []string` allowlist** on the registration row. `Artifacts` requests only those and errors if one is missing from the manifest. | **Added 2026-07-30 after the slice-1 lab gate**, reversing §6's original "cache-more-than-you-boot" stance. netboot.xyz lists files its releases do not publish (`memtest86plus`: 7 listed, 3 published), and one 404 aborts the whole version forever. Fail-loud is deliberate: a missing allowlisted file means upstream renamed the exact artifact the boot script needs, which must not degrade to a silent half-cache. |
| D11 | Clonezilla endpoint choice | **`clonezilla-debian-stable-amd64` only**, registered as `clonezilla`. | Upstream publishes four (debian/ubuntu × stable/testing). Tools are param-less (D4/§4.3), so the choice cannot be a `spec` key, and the user's dedupe is explicitly one-tool-per-function. Ubuntu-based builds (newer kernels for very new hardware) and testing builds remain additive later as separate registrations. |
| D12 | ShredOS destructiveness | **Ship it, behind an in-script iPXE confirmation gate.** Three sub-decisions settled at Gate 1 — see §6.5. | ShredOS boots into nwipe's interactive interface — upstream's README states it "does not autonuke your discs at launch", and booty does not pass `--autonuke`. But booty's `Tools & rescue...` submenu boots a selection immediately with no confirmation, and the entry leads to a disk eraser, so the gate is warranted as defense in depth ahead of nwipe's own confirmation. Upstream gates it behind a full-screen warning and a method choice; booty ports the warning into its literal script (D8 makes this natural) and hardcodes the wipe method, since tools take no params. |
| D13 | How Tails' 1.94 GB ISO is fetched | **Route it through the existing `downloadLargeFile`**, not `config.DownloadStaged`. | **Added at Gate 1 — blocking.** `config.httpClient` sets `Timeout: 5 * time.Minute` as a hard ceiling over the *entire* request, so landing 1,936,009,216 B requires **6.45 MB/s sustained, stall-free**. Below that, `io.Copy` errors → the `.partial` is removed → `reconcile.go`'s `if vg.Wait() != nil { continue }` abandons the whole version with nothing kept, and retries the full ~1.9 GB every `--cacheInterval` forever. That is D10's permanent-abandon failure arriving through a different door, and the allowlist cannot prevent it because the file is present and allowlisted. `pkg/cache/isodownload.go` already solves exactly this: `isoClient` has **no** timeout, it resumes via HTTP `Range`, and its `.download` suffix survives `SweepPartials` (which deletes `*.partial` at the top of every pass). Precedent: `debiandvd.go:221` (`isoDownload = downloadLargeFile`). Clonezilla (574 MB) and Rescatux (778 MB) need only ~1.6–2.1 MB/s and are fine on the ordinary path — this is a Tails-specific routing decision, not a general one. |
| D14 | Empty allowlist mode | **`files` is mandatory** for every tool; delete the "empty means every manifest file" fallback. | Added at Gate 1 (subtraction). All three shipped tools set `files`, all five slice-2 tools will, and `TestToolFileAllowlistTracksBootScripts` already errors on an empty allowlist — so the fallback branch is unreachable by policy. Dead flexibility; removing it deletes a code path and a mental model. |

## 4. Architecture

### 4.1 `pkg/ostype/netbootxyz.go` — shared machinery, exactly once

A memoized snapshot of `endpoints.yml`, structured exactly like the existing `streamsCache`
(`pkg/ostype/streams.go:12-16`): a package-level map behind a `sync.Mutex`, with an exported
`ResetNetbootxyzCache()` called wherever `ResetStreamsCache()` already is.

No TTL and no single-flight. Both were justified in an earlier draft by concurrent target
reconciliation, which does not exist: `reconcileAll` iterates targets in a plain sequential loop on
the coordinator goroutine (`pkg/cache/reconciler.go:113-120`), and both `reconciler.go:14-19` and
`reconcile.go:17-19` document that as a deliberate invariant. The mutex is still required, because
`VerifyVersion` → `o.Artifacts` runs on the **API goroutine** (`pkg/cache/verify.go:304-318`) —
which is precisely why `streamsCache` has one.

> **Carry into the plan:** `ResetStreamsCache()` is currently called at the top of
> **`reconcileTarget`** (`pkg/cache/reconcile.go:36`), i.e. per *target*, despite its doc comment
> saying "pass entry". Copying that idiom yields one fetch per tool target per pass. If one fetch
> per pass is wanted, the reset belongs in `reconcileAll`. Decide explicitly rather than by
> imitation.

`netbootxyzOS` implements the `ostype.OS` interface once for all tools:

- `DiscoverVersions` — returns the **release tag** derived from the snapshot entry's `path`
  (D7), after validating it (§8.2).
- `Artifacts` — reads the entry's `path` + `files`, emitting one `Artifact` per **allowlisted** file
  at `https://github.com/netbootxyz<path><file>`. Each tool registration carries a `files []string`
  allowlist (D10); `Artifacts` requests only those and **fails loudly** if an allowlisted name is
  absent from the manifest entry. An empty allowlist means every manifest file. No `SHA256`/`SigURL`
  — none are published (§8.5).
- `RequiredParams` — empty. Tools have no path-discriminating params.
- `ValidateVersion` — path-safe charset (§8.2).
- `CompareVersions` — string compare (§8.4 documents the consequences).
- `Family` — the new `tool` family (§4.3).

### 4.2 `pkg/ostype/tools.go` — the data rows

```go
register(netbootxyzOS{
    name:      "systemrescue",
    endpoints: map[string]string{"amd64": "systemrescue-amd64"},
    files:     []string{"vmlinuz", "initrd", "archiso_pxe_http", "airootfs.sfs"}, // D10, mandatory
})
// "uefishell", NOT the "uefi-shell-x64" endpoint: both ship the same
// uefi-shell-x64.efi, but netboot.xyz's own menus reference uefishell and
// nothing upstream references uefi-shell-x64 — making the latter the likelier
// to be pruned. uefishell also carries aarch64/arm for a future arm64 target.
register(netbootxyzOS{
    name:      "uefi-shell",
    endpoints: map[string]string{"amd64": "uefishell"},
})
register(netbootxyzOS{
    name:      "memtest86plus",
    endpoints: map[string]string{"amd64": "memtest86plus"},
})
```

`endpoints` is an explicit `arch → endpoint key` map rather than a `"systemrescue-{arch}"`
template. netboot.xyz's keys are irregular in several ways — `systemrescue-amd64` is per-arch,
`memtest86plus` carries no arch, `uefi-shell-x64` uses a different arch token, `shredos-x86_64`
uses yet another — and an explicit map absorbs all of them without a mini-language.

### 4.3 The `tool` family

```go
families["tool"] = {Name: "tool", ConfigKind: "", Template: ""}
```

Tools take no per-host config, so there is no authorable config kind and no config-URL directive.

This exposes a latent bug that must be fixed in the same change: `authoringKindsForFamily`
(`pkg/http/render.go:35-44`) falls through to `return []string{familyConfigKind}`, so a
config-less family would advertise `authoringKinds: [""]`. An explicit arm returning an **empty
slice** is required — and is sufficient. Verified downstream: `catalog.ts:22` flattens
`authoringKinds` into a Set, so an empty slice contributes nothing, and `osFamily["memtest86plus"]
= []` correctly yields no kinds (an empty array is not nullish, so the `??` fallback doesn't fire).
`familyAllowsKind("", anything)` is then `false`, meaning a tool host can never be bound a config —
the desired semantics. **No web code change is needed**, only a stale comment (§10).

### 4.4 Changes to `pkg/cache/catalog.go`

`catalogArches` (`pkg/cache/catalog.go:23-28`) is the gate on catalog `os:` values and per-OS arch
tokens. It needs a row per tool, and that row **repeats the arch set already declared** in the
`endpoints` map of §4.2 — two sites that must change together to stay correct, which is the DRY
criterion met exactly.

Required work:

- Single-source the arch set: derive the tools' `catalogArches` entries from the OS registry, or
  add a test asserting the two agree. Do not hand-maintain both.
- Fix the hardcoded error string at `catalog.go:84`
  (`"(supported: flatcar, fedora-coreos, talos, debian)"`), which goes stale the moment a tool
  registers.
- **Decided (user, 2026-07-29): fix it in this slice.** `POST /api/v1/targets` validates the OS via
  `ostype.Lookup` only and never consults `catalogArches`, so an API-created `memtest86plus/arm64`
  target is accepted and then fails in `Artifacts` every tick forever. Pre-existing for the four
  current OSes, but newly easy to hit because tools have narrow, irregular arch sets. The catalog
  loop and the create handler both call a new exported `cache.ValidateOSArch(os, arch)`, so the
  rule is single-sourced rather than duplicated at the API boundary.

### 4.5 What needs no change

Independently verified twice — by the author and by the cold review — against the code at
`23a8536`. Do not re-litigate:

- **`ValidateTargetParams`** (`pkg/cache/catalog.go:141-158`) derives entirely from
  `o.RequiredParams()`; an empty required set validates cleanly against a nil/empty `spec:`, and
  `encodeParams` maps that to `"{}"` (`layout.go:152-155`).
- **`ValidCachedSelection`** (`pkg/cache/list.go:111-125`) resolves via `ostype.Lookup` +
  `ValidateVersion` + `cacheDirExists`; a registered tool works immediately.
- **`paramSegment`** (`pkg/cache/layout.go:120-128`) returns `"-"` for param-less targets, and
  `menuItemText` suppresses the bracket suffix when `Segment == "-"` (`menu.go:32-38`).
- **`landArtifact`/`DownloadStaged`** are **not** a traversal vector: `config.go:107-117` takes
  `path.Base(u.Path)` and rejects `.`/`..`/`/`. Adversarial `files` values were checked and all
  reduce to a safe single segment. The only residual risk is two `files` entries colliding on the
  same basename, which none of the eight do.
- **The `tool` family breaks no other consumer.** Full sweep of `Family`/`ConfigKind`/
  `familyAllowsKind`: `api_catalog.go:50,63,69`, `api_hosts.go:38`, `resolve.go:25,30,65,89-95`,
  `preseed.go:36`. Nothing assumes a non-empty `ConfigKind`.

## 5. Data flow

```
catalog.yaml  { os: systemrescue, arch: amd64 }     (no spec — no required params)
      │  reconcile tick (sequential, one target at a time)
      ▼
DiscoverVersions ──▶ mutex-guarded endpoints.yml memo
                 ──▶ release tag "13.01-d20a63ac"   (D7; pretty "13.01" kept for the label)
      │
      ▼
Artifacts(tag, "amd64") ──▶ snapshot entry path + files
      └─▶ https://github.com/netbootxyz/asset-mirror/releases/download/13.01-d20a63ac/
              {vmlinuz, initrd, airootfs.sfs, archiso_pxe_http}
      │  landArtifact (existing staged-download path, unchanged)
      ▼
<dataDir>/cache/systemrescue/-/amd64/13.01-d20a63ac/
      │  ListCached ──▶ PartitionCached
      ▼
Tools & rescue submenu ──▶ chain tftp://<ip>/menu/systemrescue/-/amd64/13.01-d20a63ac/boot.ipxe
      │  existing selection validation + re-gate, unchanged
      ▼
PXEConfig["systemrescue.ipxe"] + [[baseurl]] token ──▶ kernel / initrd × 2 / boot
```

## 6. Boot scripts

### 6.1 One literal script per tool — no form taxonomy

An earlier draft proposed three reusable "boot forms" (`formKernelInitrd`, `formEFIChain`,
`formSanboot`) with a per-tool cmdline string. **That is abandoned.** Checked against netboot.xyz's
menu templates — the second oracle — the taxonomy is wrong for at least three of eight tools, and a
cmdline string cannot repair it because the differences are *structural*, not textual:

| Tool | What it actually needs |
|------|------------------------|
| **SystemRescue** | `kernel …vmlinuz archisobasedir=sysresccd … archiso_http_srv=…` + **two** `initrd` lines (`initrd`, and `archiso_pxe_http /hooks/archiso_pxe_http mode=755`) |
| **Tails** | kernel + **three** `initrd` lines, including the ISO itself mounted as `/tails.iso` |
| **ShredOS** | kernel-only (`console=tty3 nwipe_options=…`) — **not** `sanboot` |
| **Memtest86+** | endpoint ships **seven** files; upstream boots exactly **one** (`mt86p_x86_64`) on **both** EFI and BIOS. `imgfree` + `kernel` + `boot` |
| **UEFI Shell / ZFSBootMenu** | upstream type `direct` — `imgfree` + `kernel <…​.efi>` + `boot`, **not** `chain` |

All upstream utility entries emit a leading **`imgfree`** before `kernel`/`sanboot`; booty's scripts
do the same.

**~~Cache-more-than-you-boot is accepted.~~ REVERSED by the slice-1 lab gate (2026-07-30) — see
D10.** This section originally argued that `Artifacts` should emit one entry per `files` member and
that filtering "would add a per-tool 'which files matter' list that only this tool needs (YAGNI)."
That reasoning was wrong on a fact nobody had checked: **netboot.xyz's manifest lists files its own
asset mirror does not publish.** `memtest86plus` lists seven; only three exist. The four 404s made
`reconcileTarget` abandon the whole version every pass forever (~2,000 GitHub req/day) and left
untracked orphans on disk. The per-tool allowlist is now **mandatory**, not a nice-to-have — it is
the only thing that stops booty asking for files that do not exist. It also removes the cross-arch
waste the original text dismissed (an amd64 `uefi-shell` target was downloading arm and aarch64
EFI binaries).

So: each tool gets a literal iPXE script in `pkg/tftp`, tokenized on `[[baseurl]]`, exactly as
`PXEConfig["debian.ipxe"]`/`["talos.ipxe"]`/`["coreos.ipxe"]` already are (`pxe_config.go:19-58`).
This is less code than three form constants plus a switch plus per-tool cmdlines, lets each tool
carry however many `initrd` lines it needs, and removes the growing-switch wall that §11 of the
earlier draft worried about — there is no switch to grow.

**The per-tool cmdlines are pinned from the oracle during planning, not deferred past it.** They
determine the script's shape, so deferring them would defer a structural decision.

### 6.2 Slice-1 tool selection (revised)

- **SystemRescue** — the multi-`initrd` case (two `initrd` lines, one carrying an iPXE-side
  `/hooks/...` argument).
- **UEFI Shell** — the firmware-gated case: upstream lists it only in the EFI and ARM menus, and
  never appears in either pcbios menu, so firmware-gating is real for this tool. (An earlier
  revision cited a separate `memtest86legacy` BIOS entry as the evidence here; that is unrelated to
  UEFI Shell and was left over from the pre-Memtest86+ draft.)
- **Memtest86+** — the plain single-binary case. It was also chosen as the
  cache-more-than-you-boot case (seven files cached, one booted) — **that rationale is dead**: it
  turned out four of the seven are 404 upstream, which is what produced D10, and the shipped
  allowlist caches exactly one file. This **replaces Memtest86 (free)**, whose endpoint is
  `enabled: false` upstream and appears only in the ARM menu.

Together they cover: multiple `initrd` lines, a firmware-gated single binary, and the simple
`imgfree`/`kernel`/`boot` shape.

### 6.3 Firmware-dependent entries

netboot.xyz maintains four parallel menus (`utilitiesefi`, `utilitiespcbios32`,
`utilitiespcbios64`, `utilitiesarm`) precisely because these entries are not firmware-agnostic.
booty's `renderMenu` emits one entry per cache tuple with no firmware branch, and iPXE's
`${platform}` is not consulted anywhere in `pkg/tftp` today.

**Resolution:** where a branch is genuinely needed it lives *inside* the per-tool script, which D8
makes straightforward. Menu *entries* stay firmware-agnostic; the *script* adapts. No `renderMenu`
firmware branch is introduced.

For the slice-1 three, only **UEFI Shell** actually needs it: its script must fail loudly with a
readable message on a BIOS client rather than hanging. **Memtest86+ needs no branch at all** —
upstream boots the same `mt86p_x86_64` under both firmwares on amd64 (the 32-bit `mt86p_i586`
maps to a *different booty arch*, not a different firmware). An earlier draft claimed a
firmware+bitness branch here; that was wrong.

### 6.4 Token substitution and the assigned path

`bootTokensFor` (`pkg/tftp/tftp.go:234-257`) switches on the concrete on-disk OS name, so tools
need either a `default:` arm or an `isTool()` check via
`ostype.Lookup(cache.CacheNameToCanonical(os)).Family().Name == "tool"` — `pkg/tftp` already
imports both packages. Tools emit generic `[[baseurl]]`, `[[version]]`, `[[arch]]` tokens; one arm
serves all of them, since they share the `cache.CacheURLBase` shape.

**`bootTokens` — the *assigned* path (`tftp.go:332-386`) — must also be handled.** Once
`PXEConfig["memtest86plus.ipxe"]` exists, a host whose `assigned_os` is set to a tool would render
that template with **unsubstituted `[[baseurl]]`**, where today an unknown assigned OS yields an
empty script. §2 makes assigning a tool a non-goal, but the API path exists. Either give
`bootTokens` a tool arm or have the assigned path refuse a tool family explicitly — silently
emitting a broken script is not acceptable.

### 6.5 ShredOS's confirmation gate (D12) — the three sub-decisions

D12 says "an in-script gate defaulting to go back". Gate 1 established that the concept is sound but
that three things an implementer must decide were left undecided. All three are settled here, from
iPXE source.

**(a) The destructive block MUST be last in the file.** This is the load-bearing rule, and it is a
safety property, not style. iPXE `goto` is script-local and lines otherwise execute sequentially, so
in the obvious ordering —

```
choose --default goback ans || goto goback
iseq ${ans} proceed && goto wipe || goto goback
:goback
chain tftp://[[server-ip]]/booty.ipxe || shell
:wipe
kernel [[baseurl]]/shredos …
boot
```

— a `chain` that *succeeds and later returns* falls straight through into `:wipe` and wipes the
disks. `uefi-shell.ipxe` is safe only incidentally, because its `:notefi` block happens to be last.
For ShredOS the requirement is explicit: **either the wipe block is the final block in the script, or
the safe arm ends in `exit`.** §7 states this rule for ZFSBootMenu, where a fall-through is merely
confusing; here it destroys data.

**(b) `choose` gets an explicit `--timeout`, matching booty's other menus.** iPXE's `--timeout` is
optional and a `struct menu_ui` timeout of 0 means *indefinite* (`src/hci/tui/menu_ui.c`), so
upstream's `shredos.ipxe.j2` — which passes none — blocks forever. Every booty menu uses
`--timeout 300000` (`menu.go`). ShredOS follows booty's convention, and because timeout expiry
selects the *highlighted* item, the `--default` must be the safe arm so an unattended machine falls
back rather than wiping. Confirmed from source: `--default <name>` is honored, absent it the first
named item is highlighted, and ESC/Ctrl-C returns `-ECANCELED` so `|| goto <safe>` fires.

> **Caveat to document in the script:** `--retimeout` defaults to 0 and **any** keypress sets the
> remaining timeout to it, so a half-interacted gate parks indefinitely regardless of `--timeout`.
> That is acceptable — a parked gate is safe; only an auto-proceeding one is not.

**(c) The wipe method is `prng`.** Upstream offers six (`dodshort`, `dod522022m`, `dod3pass`,
`ops2`, `gutmann`, `prng`) differing by >30× in runtime. "Hardcodes the wipe method" is not an
executable instruction. `prng` is a single-pass cryptographic-stream wipe — the modern default for
non-classified media, and the fastest of the six, which matters because a multi-day Gutmann pass on
a misclick is its own hazard. An operator wanting a DoD or Gutmann pass boots ShredOS's own menu
from other media; booty offers one safe default, not a policy engine.

## 7. Menu integration

`renderMenu` (`pkg/tftp/menu.go:111-151`) splits in-window entries by family
(`ostype.Lookup(cache.CacheNameToCanonical(e.CacheName)).Family().Name == "tool"`):

- **Main menu:** `retry`, the non-tool in-window entries, then `Tools & rescue...` and
  `Archived OSes...` — each emitted only when its group is non-empty.
- **Tools submenu:** `Back`, the tool entries, chaining the same `menu/<tuple>/boot.ipxe` path.
  It needs **its own `choose` variable** — the archived block already uses `asel` (`menu.go:142-143`),
  so a third is required rather than reusing either.
- **Archived tools go to `Archived OSes...`**, not to the tools submenu. Archived is archived.

**Dispatch shape.** `renderMenu`'s doc comment (`menu.go:109-110` — it is a function doc, not a
package comment) names the guarded `iseq`/`goto` shape as the invariant. Each sentinel gets its own
guarded line with an explicit `|| goto <label>` fall-through. The reason is **not** operator
precedence — iPXE's `&&`/`||` are equal-precedence and left-associative, so a single chained line
would evaluate correctly. The real reason is that **iPXE aborts a script on the first failing
command unless `||` catches it**, and `iseq` returns failure on mismatch: `iseq ${sel} tools &&
goto tools` with no `||` would kill the menu on every non-`tools` selection. The existing code
obeys this at `menu.go:127` and `:143`.

**Labels.** `osTitle` (`menu.go:16-21`) gains an entry per tool, so the menu reads
`Memtest86+ 8.00-32a14678 (amd64)`.

An earlier revision of this design claimed the label would show the *pretty* version
(`Memtest86+ 8.00`) while the cache path carried the tag. **That is withdrawn.** `menuItemText`
renders `CacheEntry.Version`, which is by definition the on-disk value, and the pretty version
exists only in the upstream manifest — so honouring the claim would require fetching that manifest
at menu-render time, putting a network call on the boot path. The label shows the tag. It is
uglier and it is correct.

**Sentinel collision is a non-issue.** An earlier draft proposed guarding it. Item keys are the
full 4-segment tuple (`key := e.CacheName + "/" + e.Segment + "/" + e.Arch + "/" + e.Version`,
`menu.go:118` and `:139`), so every cache key contains three `/` and can never equal a bare
sentinel — an OS named `tools` yields `tools/-/amd64/1.0`. The guard test is dropped (YAGNI). What
*is* worth asserting, and is now in §9: every emitted `item` key is either a known sentinel or a
well-formed 4-segment tuple.

## 8. Error handling & security

### 8.1 Version identity (D7)

The on-disk version is the **release tag** — the last non-empty segment of the snapshot entry's
`path` (`13.01-d20a63ac`, `edk2-stable202002-a6917535`, `0.72-beta8-2568400c`). Every one is
path-safe under the existing charset, and the tag changes exactly when the artifacts change.

This exists to fix two concrete bugs in the pretty-version alternative:

1. **`current` would never update.** `rescatux` publishes `version: current`. The cache dir would
   be `rescatux/-/amd64/current`, and `reconcile.go:160-162` short-circuits on an already-cached
   version with all files present — permanently. booty would pin whatever bytes it fetched first
   and silently never see an upstream rebuild.
2. **Old versions would receive new bytes.** The snapshot carries one `path` per endpoint, so
   `Artifacts` cannot honour a `version` argument that differs from the current release. Under
   `retain > 1`, or on any re-land of a still-in-window version (`reconcile.go:100-109, :127`), the
   reconciler would write *current* bytes into an *old* version's directory. A dir named `13.01`
   holding `13.02` bytes is worse than a hard failure.

The tag makes version→path total, so neither can occur.

### 8.2 Version strings are a trust boundary

`ValidateVersion` runs on disk-read (`list.go:67`, `list.go:121`, `newest.go:29`) and on the
manual-pin API (`api_targets.go:203`) — but **not** on versions returned by `DiscoverVersions`
(`reconcile.go:87`). Every existing OS parses its version out of a controlled upstream format, so
nothing was ever exposed. A tool's version derives from a third party's YAML and becomes a **cache
directory name**.

The guard therefore lives in `netbootxyzOS.DiscoverVersions`. Confirmed as the right placement
rather than tightening the reconcile contract: `retentionFor` and the whole desired-set path
consume the raw slice, and every existing `ValidateVersion` is *narrower* than path-safety
(`semver.IsValid` at `talos.go:21`, `^\d+(\.\d+){0,2}$` at `debian.go:26-29`), so changing the
contract would alter behavior for four OSes to fix a boundary belonging to one.

**Import-cycle constraint.** `pkg/cache` imports `pkg/ostype` (`catalog.go:12`), so `ostype` cannot
import `cache` — meaning the path-safe check cannot call `cache.ValidatePathParam`. Naively
inlining the regex creates a second copy of a site whose doc explicitly claims to be single
("Single knowledge site for 'values that become path segments must be path-safe'",
`layout.go:136-140`). **Move `pathParamRE` to a leaf package both can import** (e.g. `pkg/config`)
and have both delegate. Verified: the existing charset accepts all eight tool tags.

### 8.3 Failure modes

- **Discovery failure degrades to stale, not broken.** `reconcile.go:87-92` logs and leaves
  `retained` empty with `pruneDiscovered=false`; cached tools stay bootable and un-archived.
- **`reverify` on the API goroutine.** `VerifyVersion` → `o.Artifacts` (`verify.go:304-318`) means
  a `reverify` on a tool row would 500 during a netboot.xyz outage. Return not-verifiable instead.
- **Sequential targets mean a hung fetch stalls the rest of the pass.** Pin an explicit timeout on
  the `endpoints.yml` fetch.
- **Host assertion applies to the constructed URL only, never the redirect target.**
  `github.com/<org>/<repo>/releases/download/...` 302s to `objects.githubusercontent.com`, and Go's
  default client follows it — asserting on the final URL would break every download.
- **The org prefix is convention, not schema — but the off-org threat is NOT expressible in the
  field booty reads.** An earlier revision cited netboot.xyz's `dts` entry as pointing at
  `boot.dasharo.com`; that was a **misread**. The host lives in
  `roles/netbootxyz/defaults/main.yml`, which booty never consumes. Every `path:` in
  `endpoints.yml` is relative (`grep "path: http"` matches nothing across 1582 lines). The guard
  therefore validates the manifest **path** — rejecting absolute, protocol-relative, or non-rooted
  values, and an unset asset base which would otherwise yield a silently *relative* URL. A
  composed-URL host check would be dead code, since the authority always comes from the base.
  *(An earlier revision trailed an instruction here to "decide whether an off-org entry is a hard
  fail or a skip". Deleted at Gate 1: it contradicted the paragraph above it and asked for a decision
  that cannot be made, since the threat is not expressible in the field booty reads. Verified across
  all 164 endpoints — `grep -c "path: http" endpoints.yml` → 0. The one absolute URL upstream ships,
  `boot.dasharo.com` for `dts`, lives in `defaults/main.yml`, not in a `path:`.)*

### 8.4 Retention and ordering

`CompareVersions` is a string compare, because netboot.xyz tags have no shared grammar.

**An earlier revision called this "harmless at the default `retain: 1`". That was wrong, and it is
a real correctness bug** — demonstrated by the Gate 2 reviewer. `reconcileTarget` retains
`retentionFor(discovered ∪ in-window-cached, retainN)` (`reconcile.go:104-110`), and
`retentionFor` sorts descending by `CompareVersions` and takes the first N
(`retention.go:58-62`). So at `retain: 1`, a **newer** upstream tag that sorts lexically **below**
the cached one loses: upstream `10.00-bbbbbbbb` vs cached `9.05-aaaaaaaa` keeps the old one
forever, logging only `artifacts unavailable; skipping version this tick`. That is the same
never-updates failure §8.1 says the release-tag identity exists to prevent — the tag made
`version → path` total but did nothing for *newest*. Memtest86+ at `8.00` hits it on the
`9.xx → 10.00` rollover.

**Resolution:** `retentionFor` gains a `tool` branch returning the discovered set verbatim, with
no sort. This is sound because netboot.xyz publishes exactly one release per endpoint and
`Artifacts` refuses any non-current tag, so a stale tag can never be re-landed regardless.
`retain` is therefore inert for tools, and the catalog rejects `retain != 1` on a tool entry so an
operator is never silently ignored.

### 8.5 No integrity verification — accepted risk

**`endpoints.yml` publishes no checksums and no signatures**, so `Artifact.SHA256`/`SigURL` stay
empty and artifacts land as `classNotVerifiable`, which is accepted under **every** policy including
`strict` (`verify.go:107-116`). `--signaturePolicy strict` genuinely does not help — there is no
mechanism *in the manifest* to be strict about. Same posture as Talos and Debian netboot artifacts
today; the trust anchor is HTTPS plus GitHub's release-asset hosting.

**Narrowed at Gate 1 — the broader claim was false.** An earlier revision (and §2's non-goals) said
"no mechanism exists upstream". That is true of the manifest but **not of every release**: the Tails
release `7.10-17629562` publishes `sha256-checksums.txt`
(`6dab23b2…  tails-amd64.iso`) **and** `tails-amd64.iso.sig` as assets the manifest simply does not
reference. The correct statement is: *the manifest publishes no verification material; some releases
publish sidecar files it does not list.*

This matters most where the design's own reasoning bites hardest — §8.5 argues the risk is recorded
because "a rescue tool is exactly the artifact an attacker would want to poison", and the 1.94 GB
anonymity distro is the sharpest instance, yet it is the one with a checksum available.
`Artifact.SHA256` already exists and `landArtifact` already consumes it, so wiring it is not
speculative machinery. **Accepting the risk remains the decision** (checked: one release; whether
every Tails release publishes these is unverified), but it is now an informed acceptance rather than
a claim that nothing is available. Deferred to a **later slice**, not a blocker — slice 2 closed
without wiring it, so "a slice-2 follow-up" would now read as work that was silently dropped. It
needs its own tracking issue when the owner decides to schedule it.

Recorded explicitly because the risk profile differs from the existing cases: these are third-party
binaries executed with full hardware privilege on bare metal, and a rescue tool is exactly the
artifact an attacker would want to poison.

## 9. Testing

**Unit**

- Snapshot parsing. Measured against the repo's actual `go.yaml.in/yaml/v4`: a plain
  `Version string` field **does** preserve the literal (`13.01` → `"13.01"`, `7.10` → `"7.10"`), so
  the hazard is real but the fix is trivial. The hazard only materializes when decoding into
  `any`/`map[string]any` (measured: `7.10` → `float64` → `"7.1"`) — **forbid that**. Also **do not**
  copy `parseCatalog`'s `yaml.WithKnownFields()` idiom (`catalog.go:67`): entries carry
  `os`/`arch`/`flavor`/`kernel` and netboot.xyz adds keys freely (measured: it errors).
- Release-tag derivation from `path`, including trailing-slash handling.
- `ValidateVersion` rejects traversal and non-path-safe strings; accepts all eight real tags.
- URL construction: that the **manifest path** is rejected when absolute, protocol-relative, or
  non-rooted, and that an empty asset base fails rather than composing a relative URL. *(An earlier
  revision asked for a test of the "constructed-URL host check" — deleted at Gate 1: §8.3 concludes
  that check is not expressible and the shipped code documents it as dead. Do not test for it.)*
- **The D10 allowlist:** that `Artifacts` returns only allowlisted files, that a missing allowlisted
  filename is a **loud error** rather than a short cache, and that every allowlisted filename is
  actually referenced as `[[baseurl]]/<file>` in that tool's script — the anti-drift guard. Slice 1
  learned the hard way that matching the bare filename against the whole source file is not enough:
  `initrd` appears ~15 times in prose, so a deleted `initrd [[baseurl]]/initrd` line still passed.
  Match the `[[baseurl]]/` prefix against the `PXEConfig` **value**, with `airootfs.sfs` the single
  documented exception (the archiso hook fetches it via `archiso_http_srv`, so it is never literal).
- Per-tool script goldens for all three slice-1 tools. Note Memtest86+ has **no** `${platform}`
  branch (§6.3) — the golden asserts its absence; UEFI Shell is the one that branches.
- `renderMenu` across all four combinations of tools present/absent × archived present/absent,
  asserting the guarded-dispatch shape, the distinct `choose` variables, and that every emitted
  `item` key is either a known sentinel or a well-formed 4-segment tuple.
- `authoringKindsForFamily` returns an empty slice for a config-less family.
- `catalogArches` and the OS registry agree on every tool's arch set.

**Integration**

- Reconcile a tool target against a fixture `endpoints.yml` served locally: target → discovery →
  artifacts → cache dir → `ListCached` → menu entry.

**Lab gate (required before merge)**

Three boots in the QEMU netboot lab, one per hard case: SystemRescue (multi-`initrd`), UEFI Shell
on a UEFI client (firmware-gated), Memtest86+ (exercised on both BIOS and UEFI).

Scope note: the two SystemRescue cmdline questions (`initrd=initrd.magic`, `BOOTIF`) were settled
from primary sources — iPXE, mkinitcpio-archiso, and klibc — and are **not** open lab questions.
See §12. The lab exists to prove these images boot from booty's own cache and URLs on real
firmware, which no source can establish.

This is not ceremony. On the debianconfig branch, byte-exact goldens and two code reviews all
passed while three real bugs sat in the output — they only manifested when debian-installer
actually executed the preseed. Tool boot correctness has the same property.

**Review**

A cold `sr-go-engineer` whole-branch review that drives the built binary, per repo convention.

**CI** is `build` / `vet` / `test -race` (`docs/standards.md` §3.4).

## 10. Documentation impact

- `docs/BOOT-MENU.md` — the tools submenu, and tool entries in the "adding more OSes" section.
  That file does **not** exist on this branch; it lands via `worktree-docs-boot-menu-vlan`, which
  is a **prerequisite** — the execution branch rebases onto it once merged.
- `docs/schema/CATALOG.md` — the `os:` value list, per-OS arch tokens, and **`spec` changes from
  required to conditional** (`CATALOG.md:75` currently documents it as `| spec | yes |`). Plus the
  `retain` caveat from §8.4 if that route is chosen.
- `docs/schema/API.md` — new OS names on `GET /api/v1/os`; the `tool` family on `GET /api/v1/families`
  with an empty `authoringKinds`.
- `deploy/catalog.yaml` — commented-out tool entries with their disk-size costs.
- `README.md` — the supported-OS table. **Separately:** `README.md:186` documents a
  `--updateSchedule` flag that **does not exist in the code**; the real knob is `--cacheInterval`
  (default 5m, `config.go:46,82`, `cmd/main.go:89-90`). Fix it while in the file.
- `web/src/api/configKinds.ts:48-50` — the comment "These three cover every OS booty supports
  (pkg/ostype registers four OSes across three families)" goes stale. Comment only; no functional
  web change (§4.3).

## 11. Slice 2 — the remaining five tools

**Verified against live netboot.xyz on 2026-07-30** (manifest entries, GitHub release assets, and
upstream menu templates all re-fetched — not carried over from the earlier draft).

Each tool is **3 code sites + 3 test sites + ~5 doc sites** — corrected at Gate 1 against the shipped
code, which found the previous "four sites" wrong in *both* directions. No change to the shared
implementation, the family, the menu, or the catalog schema.

| Kind | Site | Note |
|---|---|---|
| code | `pkg/ostype/tools.go` row | name + `endpoints` + `files` (D10/D14) |
| code | literal boot script in `pkg/tftp/tool_scripts.go` | pinned from the oracle, below |
| code | `osTitle` label in `pkg/tftp/menu.go` | |
| ~~code~~ | ~~`catalogArches`~~ | **Not a site.** `pkg/cache/catalog.go` derives tool rows from `ostype.ToolArches()`. The earlier draft listed it; nothing to edit. |
| test | `pkg/tftp/tool_scripts_test.go` | hard-codes the script-key list **and** asserts the tool count |
| test | `pkg/ostype/ostype_test.go` | hard-codes the full registry name list |
| test | `pkg/cache/reconciler_test.go` | its fixture manifest must carry each tool's allowlisted files |
| docs | `docs/schema/CATALOG.md` | names the tools in ~6 places |
| docs | `deploy/catalog.yaml`, `docs/examples/catalog.yaml` | commented opt-in blocks, both files |
| docs | `README.md`, `docs/BOOT-MENU.md`, `docs/schema/API.md` | supported-OS lists |

The two test sites that assert an exact count or list will **fail loudly** on the first new tool,
which is the intended behaviour — the same guard that caught `TestRegistry_RegistersAllFour` in
slice 1. Budget for editing them rather than treating them as breakage.

| Tool | Endpoint key | Release tag | Boot shape | Cached size |
|---|---|---|---|---|
| Clonezilla | `clonezilla-debian-stable-amd64` (D11) | `3.3.3-15-1a41a72c` | kernel + 1 `initrd`; squashfs pulled by live-boot via `fetch=` | 574 MB |
| Rescatux | `rescatux` | `0.72-beta8-2568400c` | same debian-squash live shape | 778 MB |
| ShredOS | `shredos-x86_64` | `2025.11_31_x86-64_0.42-bf7a6bdf` | **kernel-only**, no initrd; behind a confirm gate (D12) | 97 MB |
| ZFSBootMenu | `zfsbootmenu` | `3.1.0-1620b6a3` | single `.efi` chainload — **EFI-only** | 86 MB |
| Tails | `tails` | `7.10-17629562` | kernel + **three** `initrd` lines, incl. the ISO at `/tails.iso` | 2.05 GB |

**All five publish every file their manifest lists** (checked asset-by-asset), so none reproduces
the `memtest86plus` 404 defect today. The D10 allowlist is still mandatory — it is the guard against
this changing under us, which is exactly how the defect arose.

Notes that change the work:

- **Arch tokens differ upstream.** `shredos-x86_64` declares `arch: x86_64` and `zfsbootmenu` /
  `rescatux` declare no arch at all. booty's tool family uses `amd64` throughout, and the
  `endpoints` map exists precisely to absorb this (§4.2) — map booty `amd64` → the upstream key.
  Do **not** introduce an `x86_64` arch token for tools.
- **Rescatux publishes `version: current`** — the literal string, forever. This is the case D7 was
  written for; the release tag `0.72-beta8-2568400c` is the on-disk version and the code already
  does this. It is the strongest live confirmation of D7 available.
- **ZFSBootMenu is EFI-only** and needs the same `iseq ${platform} efi || goto notefi` guard as
  UEFI Shell, including the re-chain so the branch terminates (§6.3). Its file is
  `zfsbootmenu-recovery-x86_64.efi` — the *recovery* image, the only one the endpoint publishes.
- **All five scripts are pinned from an upstream oracle — none is derived.** An earlier revision of
  this section claimed ZFSBootMenu and Rescatux "have no upstream menu template" and must be derived
  from the UEFI Shell and Clonezilla precedents. That is **literally true and materially wrong**, and
  the prescribed derivation was actively harmful — corrected at Gate 1. There is no `.j2` template
  for either because both are `type: direct` entries, which upstream defines **inline in
  `roles/netbootxyz/defaults/main.yml`** — the same file this design already reads for
  `kernel_params`. Transcribe from there, verbatim:

  ```yaml
  # defaults/main.yml:885-889 (utilitiesefi)
  zfsbootmenu:
    kernel: ${live_endpoint}{{ endpoints.zfsbootmenu.path }}zfsbootmenu-recovery-x86_64.efi
    type: direct

  # defaults/main.yml:855-861 (utilitiesefi) and :987-993 (utilitiespcbios64) — identical
  rescatux:
    initrd: ${live_endpoint}{{ endpoints.rescatux.path }}initrd
    kernel: ${live_endpoint}{{ endpoints.rescatux.path }}vmlinuz boot=live
      fetch=${live_endpoint}{{ endpoints.rescatux.path }}filesystem.squashfs
      selinux=1 security=selinux enforcing=0 {{ kernel_params }}
    type: direct
  ```

  **Do not derive Rescatux from Clonezilla.** Their cmdlines are not the same shape: Clonezilla
  carries seven options Rescatux does not (`username=user union=overlay components noswap edd=on
  nomodeset ocs_live_run=… ocs_live_batch=no nosplash noprompt`), and Rescatux carries
  `selinux=1 security=selinux enforcing=0`, which the Clonezilla derivation would have **silently
  dropped**. An implementer following the old text would have produced a wrong script with no way to
  notice.

- **Platform guards, settled from the same file.** `zfsbootmenu` appears **only** under
  `utilitiesefi` — it is genuinely EFI-only and needs the `iseq ${platform} efi || goto notefi` guard
  plus the terminating re-chain, exactly as `uefi-shell.ipxe` does. `rescatux` appears under **both**
  `utilitiesefi` and `utilitiespcbios64`, so it must **not** get a platform guard.
- **Tails is 2.05 GB**, of which 1.94 GB is `tails-amd64.iso` mounted as a third initrd. It is the
  single largest artifact booty will cache. **D6 (opt-in) does NOT carry the weight here** — an
  earlier revision said it did, which was wrong: opt-in governs whether you *try*, not whether it can
  *succeed*. See D13; Tails cannot land through the ordinary tool download path at all.
- All five together are **≈3.6 GB** of booty disk if an operator enables everything.

**Transcription hazards — each of these has already bitten once, or is one keystroke from doing so:**

| Hazard | Rule |
|---|---|
| Registered booty OS names are *not* the endpoint keys in the table above | Use `clonezilla`, `rescatux`, `shredos`, `zfsbootmenu`, `tails` — **not** `shredos-x86_64` |
| Upstream templates use `${url}`/`${kernel_url}` **with** a trailing slash (`${url}vmlinuz`); booty's `[[baseurl]]` has **none** | Always `[[baseurl]]/vmlinuz` — including inside `fetch=` and `fromiso=` values |
| Tails' ISO is `tails-${os_arch}.iso` upstream | The literal must be `tails-amd64.iso` |
| ShredOS uses bare `${cmdline}`, *not* `{{ kernel_params }}` | So upstream deliberately emits **no** `initrd=initrd.magic` for this kernel-only boot — do not copy SystemRescue's preamble. Its full line is `kernel … console=tty3 loglevel=3 nwipe_options="--method=…"` — note `loglevel=3` |
| `${cmdline}` and `${ipparam}` are netboot.xyz variables, set in its own `boot.cfg` | booty must not copy them; iPXE expands an unset setting to empty, so they are harmless but dead |

**Client RAM, not just booty disk.** The table's size column is booty's cache. What decided whether
slice-1 boots worked was the *client's* RAM — SystemRescue needed ≳3 GB because archiso stages its
1.05 GB squashfs in tmpfs twice. Three of these five are RAM-dominated by construction and none has
a measured figure yet (only a lab boot settles it):

- **Tails** boots `fromiso=/tails.iso`, so the 1.936 GB ISO stays resident for the whole session on
  top of the 97 MB `initrd.img`; Tails' own documented minimum is 2 GB before that.
- **Rescatux** (639 MB) and **Clonezilla** (489 MB) pull their squashfs into tmpfs via `fetch=`.

**The initrd ceiling is real but not a blocker — upstream already boots this.** Linux sets
`initrd_addr_max = 0x7fffffff` (`arch/x86/boot/header.S`) and iPXE clamps initrd placement to it,
returning `-ENOBUFS` when the payload will not fit (`src/arch/x86/image/bzimage.c`). Tails' three
initrds total **2,033,548,455 bytes** against a 2,147,483,647 ceiling — 113.9 MB of headroom.

An earlier revision of this section treated that as an open risk requiring a BIOS-specific lab boot.
**That was overstated**: netboot.xyz PXE-boots this exact payload with these exact three `initrd`
lines, and its maintainer confirms reaching the "Welcome to Tails" screen on KVM/Proxmox and ESXi
(netbootxyz#1102, #1104). The existence proof outranks the arithmetic. What the headroom figure
*does* justify is a **watch item, not a gate**: a future upstream ISO growth beyond ~113 MB breaks
BIOS booting outright, and the failure would present as iPXE's `-ENOBUFS`, not as a booty bug.

**Three gotchas taken from netboot.xyz's issue history — all cost their users real time:**

- **Client RAM is 4–8 GB for Tails, not ~3 GB.** Upstream maintainer `antonym`: *"You'll need a lot
  of memory, probably 4GB to 8GB as it's loading the ISO into RAM and still needs space to run. (2GB
  probably wouldn't cut it)"* — **netbootxyz#1104**, not #1102 (#1102 carries only the shorter
  *"You may need to increase the memory to 8GB or so"*). A reporter in the same thread measured the
  hard floor at *"3840MB (3.75GB)"* with 4 GB+ recommended. This is the highest client requirement
  of any tool booty caches — document it next to the catalog entry, and size the lab VM accordingly.
- **`9990-misc-helpers.sh` is netboot.xyz's PATCH, not an incidental extra file.** netbootxyz#1624
  ("Tails Failing to Boot") was `mount: /run/live/fromiso: mount failed: Operation not permitted` —
  the kernel was not loading the **loop** module, so the ISO could not be mounted. The fix forces a
  `modprobe loop` and shipped in the **asset-mirror build**, i.e. in this helper. That is why the
  file exists in the manifest at all, and it means the helper is **version-coupled to the ISO**: an
  upstream bump that changes one and not the other breaks the boot silently. Both are allowlisted
  (D10) and land in the same release tag (D7), so booty's release-tag identity already keeps them in
  step — a benefit of D7 that was not anticipated when it was written.
- **Some VMs need an emulated CD-ROM drive** or Tails fails with `unable to find a medium containing
  a live filesystem` (netbootxyz#1104, reproduced on VirtualBox and KVM). The lab gate must account
  for this before concluding a Tails failure is booty's fault.

**Clonezilla / Rescatux, from the same source (netbootxyz#1254, #1626):**

- **booty is immune to upstream's open DNS bug.** netbootxyz#1254 (still open) has Debian-based live
  boots — Clonezilla included — failing because the initramfs runs `wget` for the squashfs
  immediately after writing `/etc/resolv.conf`, and the lookup loses the race. **booty's
  `[[baseurl]]` is an IP-and-port, never a hostname**, so the `fetch=` URL needs no DNS at all. Worth
  knowing so the symptom is not misdiagnosed as a booty defect, and worth *not* "fixing".
- **netbootxyz#1626 is the one that should change how we think about caching.** Clonezilla hung at
  *"Waiting for ethernet cards up"* on every machine, confirmed by the maintainer on both Debian and
  Ubuntu images, and it persisted past 8 GB of RAM. The root cause was **not** the boot script: the
  *mirrored* `initrd` had been built without network drivers. The fix was a rebuilt asset.

  booty caches exactly those mirrored assets, so a bad upstream build is served faithfully to every
  client. **This is an argument for D7, not against it** — the release tag changes when the artifacts
  are rebuilt, so a fixed asset arrives as a new tag and booty re-caches it automatically.

  **The residual risk is asset mutation at a fixed tag.** booty's skip-if-cached short-circuit
  (`cachedByVersion[version] && finalFilesPresent(...)`) means that if upstream ever *replaces* files
  under an existing tag rather than cutting a new one, booty keeps serving the broken bytes forever
  and no reconcile pass will notice — there are no checksums to compare against (§8.5). Record this
  as a known limitation; the mitigation if it ever bites is an operator-triggered re-fetch, which
  `POST /api/v1/cache/{id}/reverify` does not currently provide for tools (it short-circuits, §8.3).

**What cannot be copied from upstream: the caching.** netboot.xyz sets
`live_endpoint: https://github.com/netbootxyz` (`defaults/main.yml:182`) — its clients stream every
artifact **directly from GitHub**, and it never writes the ISO to its own disk. booty caches
locally, which is the entire point of booty. So D13's 5-minute download ceiling has no upstream
counterpart to imitate; it is a consequence of booty's architecture and must be solved in booty.

## 12. Review provenance

Independent `sr-go-engineer` review (opus, cold, no authoring context) 2026-07-29 — verdict
**AMEND-BEFORE-PLANNING**. All findings folded into this document:

- **[Important]** Boot-form taxonomy wrong for ≥3 of 8 tools; cmdline deferral hid a structural
  error → §6.1 abandons the taxonomy for literal per-tool scripts (D8); §6.2 swaps Memtest86 (free,
  `enabled: false` upstream) for Memtest86+.
- **[Important]** `version` is not a stable artifact identity; `Artifacts` cannot honour its
  `version` argument → §8.1, D7 (release tag).
- **[Important]** Single-flight/TTL justification contradicted by sequential reconciliation →
  §4.1, D9 (mutex memo mirroring `streams.go`), plus the reset-site caveat.
- **[Important]** "One row in two files" wrong; `catalogArches` is a fourth site duplicating the
  arch set → §4.4, §11.
- **[Minor]** Sentinel-collision guard defends nothing (keys are 4-segment tuples) → §7 drops it,
  adds the key-shape assertion instead.
- **[Minor]** `iseq` rationale wrong (abort-on-failure, not precedence); tools submenu needs its own
  `choose` variable → §7.
- **[Minor]** `ostype` cannot import `cache` — path-safe regex would be duplicated → §8.2.
- **[Minor]** YAML decode specifics, measured against `yaml/v4` → §9.
- **[Minor]** `bootTokensFor` needs a mechanism; the *assigned* path would emit unsubstituted
  tokens → §6.4.
- **[Minor]** String `CompareVersions` misorders at `retain > 1` → §8.4.
- **[Minor]** Runtime-fetch failure modes: API-goroutine `reverify`, sequential stall, redirect vs
  constructed-URL host check, off-org endpoints → §8.3.
- **[Minor]** Stale citations (`ostype.go:57-62` is the `families` map, not the registry;
  `--updateSchedule` does not exist; `menu.go` has no package comment; `BOOT-MENU.md` cited in the
  present tense) → fixed throughout, §10.
- **[Minor]** Missed doc sites: `CATALOG.md:75`, `configKinds.ts:48-50` → §10.

Confirmed correct and unchanged: the `ValidateTargetParams` / `ValidCachedSelection` /
`paramSegment` / `DownloadStaged` no-change list; the `authoringKindsForFamily` bug and the
sufficiency of an empty-slice arm; discovery-failure degradation; `classNotVerifiable` under
`strict`; the `https://github.com/netbootxyz` artifact prefix; the endpoint-key irregularity
justifying an explicit map; D5's split matching precedent; and the `tool` family breaking no other
`Family`/`ConfigKind` consumer.

### Gate 2 (plan review) findings that amended THIS document

The implementation plan's cold review (2026-07-29) verified by writing the plan's code into the
tree and running the suite. Three of its findings were defects in this design, not just the plan:

- **[Blocking]** D7/§7's "pretty version for the menu label" was unimplementable without a network
  call on the boot path → **withdrawn**; the label shows the release tag (§7).
- **[Significant]** §4.3's "empty slice" is only correct as `[]string{}` — `return nil` marshals to
  JSON `null`, and `catalog.ts`'s `flatMap` keeps a `null` as an element, so it *would* reach the
  UI pickers. `/os` guards this at `api_catalog.go:44-47`; `/families` does not.
- **[Significant]** §8.3's reverify requirement collides with §8.1's fail-loud `Artifacts`:
  reverify on any *archived* tool version would be a permanent 500, not an outage-only one. The
  fix is to short-circuit the `tool` family in `VerifyVersion` before `Artifacts` is called.

Also corrected: the UEFI Shell endpoint (§4.2) and the API arch gate decision (§4.4).

### Two "could not verify" items, subsequently RESOLVED from source

The Gate 2 review left two SystemRescue cmdline questions open, and this document previously
deferred both to the lab gate. Background research (2026-07-29) settled both against primary
sources; neither is an open question, and neither should be re-opened in the lab.

- **`initrd=initrd.magic` — keep it.** It is an **iPXE UEFI mechanism** (iPXE commit `e5f0255`),
  not a kernel/archiso/netboot.xyz one: iPXE's EFI build synthesizes a file of that name holding
  every loaded initrd concatenated, because the Linux EFI stub loads only the one file `initrd=`
  names. It is a provable no-op on BIOS (`bzimage.c` never reads it; the kernel's `memparse`
  rejects the value) and unread on iPXE ≥ Feb 2023 (`6a004be0` serves the same blob via
  `EFI_LOAD_FILE2_PROTOCOL`, which the stub prefers) — but **load-bearing on older iPXE under
  UEFI**, where its absence means no initrd loads at all. booty does not ship iPXE (the operator
  stages the binaries), so the build is unknown and the zero-cost insurance is kept. The Flatcar
  breakage (netbootxyz#1070, Feb 2022) predates LoadFile2 and involved an embedded-initramfs
  kernel, a shape SystemRescue does not have.
- **`BOOTIF` — matters only on multi-NIC.** Its entire effect is filling the device field of
  klibc's `ip=` string (`mkinitcpio-archiso` `hooks/archiso_pxe_common:13-30`); with one
  non-loopback NIC, pinning and racing are identical. It is **inert unless `ip=` is also present**
  (the hook gates on it), and an unmatched value degrades to race-all rather than failing.
  `${netX}` is an iPXE **built-in** (`netdev_settings.c` registers it; `find_netdev` maps it to
  `last_opened_netdev()`), not a netboot.xyz variable, so it needs no preamble in a standalone
  script. The bare form is retained over the canonical `01-…:hexhyp` PXELINUX form: both are
  byte-identical after archiso's `${BOOTIF#01-}` and `-`→`:` transformations, and the bare form is
  the one upstream ships.

> **Process note:** `mcp__agentgateway__critical-thinking_criticalthinking` was not registered in
> any of these sessions, so neither the design nor its two reviews passed the mandated
> critical-thinking double-check gate. All three are evidence-backed against code and live upstream
> data instead — the Gate 2 review additionally by executing the code.

## 13. Missed alternatives

### 13.1 Vendoring `endpoints.yml` instead of fetching it (re-examined)

D1 chose runtime fetch by analogy with the other OSes. The review correctly notes the analogy is
imperfect: the other OSes fetch *their own vendor's* versioned API, whereas this fetches a third
party's YAML describing *someone else's* mirror. Tool versions move roughly annually — UEFI Shell
is `edk2-stable202002`, frozen since 2020.

Vendoring the eight entries and bumping by PR would remove the runtime dependency, the schema-drift
risk, the parse hazards, and the memo entirely. **Decision unchanged** (runtime fetch, per the
user's explicit choice), but recorded as consciously weighed rather than decided by analogy. If the
runtime dependency ever proves troublesome, vendoring is the escape hatch and requires no
architectural change.

Note also that `master` serves `endpoints.yml` and the repo cuts release tags, so a stable pinned
ref is available even while keeping the runtime fetch. Worth considering in the plan: `development`
is the current default branch, but pinning to a tag would trade freshness for reproducibility.
