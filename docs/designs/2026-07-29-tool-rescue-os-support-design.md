# Design: Tool / rescue OS support (netboot.xyz-sourced)

**Type:** Design
**Date:** 2026-07-29
**Issue:** none yet — this design precedes the tracking issue
**Status:** Approved by user 2026-07-29 (section-by-section); independent `sr-go-engineer` review
(opus, cold) 2026-07-29 returned `AMEND-BEFORE-PLANNING` — **all findings folded, see §12**.
Pending `superpowers:writing-plans`.
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
| D7 | On-disk version identity | **The netboot.xyz release tag** (`13.01-d20a63ac`), not the pretty `version`. The pretty version is used for the **menu label only**. | Revised post-review. The tag changes exactly when the artifacts change, making version→path total; the pretty version does not (§8.1). |
| D8 | Boot-script shape | **One literal iPXE script per tool**, tokenized on `[[baseurl]]` — **no form taxonomy**. | Revised post-review. The eight tools need 1–3 `initrd` lines, `${platform}` branches, and per-tool cmdlines that no three-form abstraction can express (§6.1). This is also the repo's existing idiom and strictly less code. |
| D9 | Snapshot memoization shape | **Mutex-guarded memo + explicit reset**, mirroring `pkg/ostype/streams.go`. No TTL, no single-flight. | Revised post-review. Targets reconcile **sequentially** (`pkg/cache/reconciler.go:113-120`), so single-flight guards nothing; the mutex exists for the API-goroutine reader (§8.3). |

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
- `Artifacts` — reads the entry's `path` + `files`, emitting one `Artifact` per file at
  `https://github.com/netbootxyz<path><file>`. No `SHA256`/`SigURL` — none are published (§8.5).
- `RequiredParams` — empty. Tools have no path-discriminating params.
- `ValidateVersion` — path-safe charset (§8.2).
- `CompareVersions` — string compare (§8.4 documents the consequences).
- `Family` — the new `tool` family (§4.3).

### 4.2 `pkg/ostype/tools.go` — the data rows

```go
register(netbootxyzOS{
    name:      "systemrescue",
    endpoints: map[string]string{"amd64": "systemrescue-amd64"},
})
register(netbootxyzOS{
    name:      "uefi-shell",
    endpoints: map[string]string{"amd64": "uefi-shell-x64"},
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
- **Decide explicitly:** `POST /api/v1/targets` validates the OS via `ostype.Lookup` only and never
  consults `catalogArches`, so an API-created `memtest86plus/arm64` target is accepted and then
  fails in `Artifacts` every tick forever. Pre-existing for the four current OSes, but newly easy
  to hit because tools have narrow, irregular arch sets.

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
| **Memtest86+** | **four** artifacts (`memtest32.bin`, `memtest32.efi`, `memtest64.bin`, `memtest64.efi`) — a firmware **and** bitness branch |
| **UEFI Shell / ZFSBootMenu** | upstream type `direct` — `kernel <…​.efi>` + `boot`, not `chain` |

So: each tool gets a literal iPXE script in `pkg/tftp`, tokenized on `[[baseurl]]`, exactly as
`PXEConfig["debian.ipxe"]`/`["talos.ipxe"]`/`["coreos.ipxe"]` already are (`pxe_config.go:19-58`).
This is less code than three form constants plus a switch plus per-tool cmdlines, lets each tool
carry however many `initrd` lines it needs, and removes the growing-switch wall that §11 of the
earlier draft worried about — there is no switch to grow.

**The per-tool cmdlines are pinned from the oracle during planning, not deferred past it.** They
determine the script's shape, so deferring them would defer a structural decision.

### 6.2 Slice-1 tool selection (revised)

- **SystemRescue** — the multi-`initrd` case.
- **UEFI Shell** — the firmware-gated case (upstream lists it only in the EFI and ARM menus).
- **Memtest86+** — the platform/bitness-branching case. Note this **replaces Memtest86 (free)**,
  whose endpoint is `enabled: false` upstream and appears only in the ARM menu; `memtest86plus` is
  what the x86 menus actually ship.

### 6.3 Firmware-dependent entries

netboot.xyz maintains four parallel menus (`utilitiesefi`, `utilitiespcbios32`,
`utilitiespcbios64`, `utilitiesarm`) precisely because these entries are not firmware-agnostic.
booty's `renderMenu` emits one entry per cache tuple with no firmware branch, and iPXE's
`${platform}` is not consulted anywhere in `pkg/tftp` today.

**Resolution:** the branch lives *inside* the per-tool script, which D8 makes straightforward —
e.g. Memtest86+ selects `memtest64.efi` vs `memtest64.bin` on `${platform}`, and UEFI Shell's
script fails loudly with a readable message on a BIOS client rather than hanging. Menu *entries*
stay firmware-agnostic; the *script* adapts. No `renderMenu` firmware branch is introduced.

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

**Labels.** `menuItemText`/`osTitle` (`menu.go:16-40`) render the **pretty** version, not the
release tag that D7 puts on disk — so the menu shows `SystemRescue 13.01 (amd64)` while the cache
path carries `13.01-d20a63ac`. `osTitle` gains an entry per tool.

**Sentinel collision is a non-issue.** An earlier draft proposed guarding it. Item keys are the
full 4-segment tuple (`key := e.CacheName + "/" + e.Segment + "/" + e.Arch + "/" + e.Version`,
`menu.go:118` and `:139`), so every cache key contains three `/` and can never equal a bare
sentinel — an OS named `tools` yields `tools/-/amd64/1.0`. The guard test is dropped (YAGNI). What
*is* worth asserting, and is now in §9: every emitted `item` key is either a known sentinel or a
well-formed 4-segment tuple.

## 8. Error handling & security

### 8.1 Version identity (D7)

The on-disk version is the **release tag** — the last non-empty segment of the snapshot entry's
`path` (`13.01-d20a63ac`, `edk2-stable202002-a9ce7096`, `0.72-beta8-2568400c`). Every one is
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
- **The org prefix is convention, not schema.** `https://github.com/netbootxyz<path>` holds for all
  eight today, but the same file's `dts` entry points at `boot.dasharo.com`. Decide whether an
  off-org entry is a hard fail or a skip; do not assume it cannot happen.

### 8.4 Retention and ordering

`CompareVersions` is a string compare, because netboot.xyz tags have no shared grammar. The
consequence must be stated rather than discovered: lexicographically `9.05 > 13.01`, so at
`retain > 1` the reconciler can archive the newest and keep an older one, and the menu can list
them out of order (`retention.go:50-63`, `list.go:80-89`, `NewestCached`). Harmless at the default
`retain: 1`. **Either validate `retain: 1` for tools or document the caveat in CATALOG.md** —
pick one in the plan.

### 8.5 No integrity verification — accepted risk

`endpoints.yml` publishes no checksums and no signatures, so `Artifact.SHA256`/`SigURL` stay empty
and artifacts land as `classNotVerifiable`, which is accepted under **every** policy including
`strict` (`verify.go:107-116`). `--signaturePolicy strict` genuinely does not help — there is no
mechanism to be strict about. Same posture as Talos and Debian netboot artifacts today; the trust
anchor is HTTPS plus GitHub's release-asset hosting.

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
- URL construction, including the constructed-URL host check and the redirect caveat.
- Per-tool script goldens for all three slice-1 tools, including Memtest86+'s `${platform}` branch.
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
on a UEFI client (firmware-gated), Memtest86+ (platform branch, exercised on both BIOS and UEFI).

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

## 11. Slice 2 (deferred, additive)

Clonezilla, Rescatux, and Tails; ZFSBootMenu; ShredOS (`shredos-x86_64`).

Each is **four sites**, stated honestly rather than the "one row in two files" an earlier draft
claimed: the `pkg/ostype/tools.go` row, the `catalogArches` row (or its derivation, §4.4), the
literal boot script in `pkg/tftp`, and the `osTitle` label. No change to the shared implementation,
the family, the menu, or the catalog schema.

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

> **Process note:** `mcp__agentgateway__critical-thinking_criticalthinking` was not registered in
> either session, so neither the design nor its review passed the mandated critical-thinking
> double-check gate. Both are evidence-backed against code and live upstream data instead.

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
