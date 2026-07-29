# Design: Tool / rescue OS support (netboot.xyz-sourced)

**Type:** Design
**Date:** 2026-07-29
**Issue:** none yet — this design precedes the tracking issue
**Status:** Approved by user 2026-07-29 (design sections walked section-by-section); pending
`superpowers:writing-plans`
**Roadmap slice:** OS-support wishlist — the "7 tool OSes" cohort, plus Tails

---

## 1. Problem

booty's interactive boot menu (`boot_mode='menu'`, see `docs/BOOT-MENU.md`) lists whatever is
cached on disk. Today that can only ever be Flatcar, Fedora CoreOS, Talos, or Debian, because
`pkg/ostype`'s registry is a compile-time map of exactly those four (`pkg/ostype/ostype.go:57-62`)
and `catalog.yaml` validation rejects any other `os:` value at startup.

The operator-facing want is a rescue kit at the console: pick a machine, netboot it, and get
Memtest86 or SystemRescue without touching its assignment or carrying USB sticks. That is the
classic netboot.xyz use case and it fits menu mode exactly — but no amount of `catalog.yaml`
editing can produce it.

This design adds a **tool OS** class sourced from netboot.xyz's `endpoints.yml`, so those images
become ordinary cache targets and therefore ordinary menu entries.

## 2. Goals / Non-goals

**Goals**

- Eight tool images become cacheable, catalog-declarable targets: Memtest86, SystemRescue,
  Clonezilla, ShredOS, UEFI Shell, ZFSBootMenu, Rescatux, Tails. This design covers all eight;
  **the slice it specifies delivers three** (D3), with the rest landing additively per §11.
- They appear in the boot menu under their own `Tools & rescue...` submenu.
- Versions track netboot.xyz automatically, the way every other OS tracks its upstream.
- booty **hosts and caches** the artifacts itself; it does not chainload netboot.xyz mirrors at
  boot time (user decision, 2026-07-01).
- Adding tool #9 later is additive — one row in two files, siblings untouched.

**Non-goals (YAGNI)**

- Per-host config for tools. They take none; there is no Ignition/machineconfig/preseed analogue.
- A UI control for assigning a tool to a host. Menu mode is the intended path.
- Mirroring netboot.xyz's full endpoint catalogue. Eight curated tools, chosen 2026-07-01.
- Building booty-specific tool images, or customizing the upstream ones.
- Signature/checksum verification of tool artifacts — see §8, no mechanism is available upstream.

## 3. Decisions

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| D1 | How is `endpoints.yml` consumed? | **Runtime fetch**, memoized per reconcile tick. | Consistent with how every other OS discovers versions; keeps tool versions self-updating; the opaque release tag is always in hand. Accepts a runtime dependency on a third-party schema. |
| D2 | Menu placement | **`Tools & rescue...` submenu**, parallel to `Archived OSes...`. | Keeps the installable-OS list short and scannable; reuses the nested-menu shape `renderMenu` already has; matches netboot.xyz's own organization. |
| D3 | First-slice scope | **Architecture + 3 tools**, one per boot form: SystemRescue, UEFI Shell, Memtest86. | Every boot form is lab-proven before the remaining five land; those five are then genuinely additive. Keeps the lab gate at 3 boots. |
| D4 | Taxonomy shape | **One shared implementation, one data row per tool** (approach C). | The netboot.xyz plumbing is one piece of knowledge that changes together — DRY. Per-tool variation (name, endpoint key, arch set, boot form) is data. Precedent: `ostype.go:31` argues `Family` should be data, not an interface. |
| D5 | Where does boot data live? | Discovery data in `pkg/ostype`; boot scripts and cmdlines in `pkg/tftp`. | Exactly how every existing OS is already split (`ostype/debian.go` + `pxe_config.go["debian.ipxe"]`). Consistency beats co-location. |
| D6 | Default catalog | Tools are **not** in the flag-derived default; opt-in via `catalog.yaml`. | Follows the Debian-DVD precedent that a fresh install downloads nothing until an operator asks. SystemRescue and Tails are ~1 GB and ~1.5 GB. |
| D7 | Version grammar | Path-safe charset validation only; `CompareVersions` is a string compare. | netboot.xyz versions have no shared grammar (`11.7`, `2025.11_31_x86-64_0.42`, `edk2-stable202002`, `current`). Imposing one would be fiction. |

## 4. Architecture

### 4.1 `pkg/ostype/netbootxyz.go` — shared machinery, exactly once

A memoized snapshot of `endpoints.yml`: fetch, parse, hold in-process behind a TTL and a
single-flight guard so one HTTP round-trip serves every tool target in a reconcile tick rather
than one fetch per target.

**TTL is 5 minutes**, matching the default `--updateSchedule` cadence: within one reconcile pass
every tool reads the same snapshot, and a later pass re-fetches. The single-flight guard is what
makes the tick-level guarantee hold — without it, eight targets reconciling concurrently would
each miss a cold cache and issue their own fetch.

`netbootxyzOS` implements the `ostype.OS` interface once for all tools:

- `DiscoverVersions` — reads its endpoint's `version` from the snapshot, validates it (§8), returns
  it as a single-element slice.
- `Artifacts` — reads the same entry's `path` + `files`, emitting one `Artifact` per file at
  `https://github.com/netbootxyz<path><file>`. No `SHA256`/`SigURL` — none are published (§8).
- `RequiredParams` — empty. Tools have no path-discriminating params.
- `ValidateVersion` — path-safe charset (§8).
- `CompareVersions` — string compare (D7).
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
    name:      "memtest86",
    endpoints: map[string]string{"amd64": "memtest86"},
})
```

`endpoints` is an explicit `arch → endpoint key` map rather than a `"systemrescue-{arch}"`
template. netboot.xyz's keys are irregular in three different ways — `systemrescue-amd64` is
per-arch, `memtest86` carries no arch at all, `uefi-shell-x64` uses a different arch token — and an
explicit map absorbs all three without inventing a mini-language to paper over the irregularity.

### 4.3 The `tool` family

```go
families["tool"] = {Name: "tool", ConfigKind: "", Template: ""}
```

Tools take no per-host config, so there is no authorable config kind and no config-URL directive to
inject into the kernel cmdline.

This exposes a latent bug that must be fixed in the same change: `authoringKindsForFamily`
(`pkg/http/render.go:35-44`) falls through to `return []string{familyConfigKind}`, so a
config-less family would advertise `authoringKinds: [""]` on `GET /api/v1/families`, and the web
UI's config-kind picker would render an empty option. It needs an explicit arm returning an empty
slice for a family whose `ConfigKind` is `""`.

### 4.4 What needs no change

Verified against current code, not assumed:

- **`ValidateTargetParams`** (`pkg/cache/catalog.go:141-158`) derives everything from
  `o.RequiredParams()`. A tool returning no required params validates cleanly against an omitted or
  empty `spec:`. The catalog schema already accommodates param-less targets.
- **`ValidCachedSelection`** (`pkg/cache/list.go:111-125`) resolves through `ostype.Lookup` +
  `ValidateVersion`, so the menu-selection boot path accepts tool tuples as soon as a tool is
  registered.
- **`paramSegment`** (`pkg/cache/layout.go:120-128`) already returns the `"-"` sentinel for
  param-less targets, and `menuItemText` (`pkg/tftp/menu.go:26-40`) already suppresses the bracket
  suffix when `Segment == "-"`.
- **`landArtifact`/`DownloadStaged`** already derive the on-disk filename via `path.Base(u.Path)`
  and reject unsafe filenames (`pkg/config/config.go:114-116`), so the third-party `files` list is
  not a traversal vector.

## 5. Data flow

```
catalog.yaml  { os: systemrescue, arch: amd64 }     (no spec — no required params)
      │  reconcile tick
      ▼
DiscoverVersions ──▶ memoized endpoints.yml snapshot ──▶ "13.01"
      │
      ▼
Artifacts("13.01", "amd64") ──▶ snapshot entry path + files
      └─▶ https://github.com/netbootxyz/asset-mirror/releases/download/13.01-d20a63ac/
              {vmlinuz, initrd, airootfs.sfs}
      │  landArtifact (existing staged-download path, unchanged)
      ▼
<dataDir>/cache/systemrescue/-/amd64/13.01/
      │  ListCached ──▶ PartitionCached
      ▼
Tools & rescue submenu ──▶ chain tftp://<ip>/menu/systemrescue/-/amd64/13.01/boot.ipxe
      │  existing selection validation + re-gate, unchanged
      ▼
formKernelInitrd template + generic tokens ──▶ kernel / initrd / boot
```

## 6. Boot forms

Three forms in `pkg/tftp`, each rendering a script from per-tool data. Generated into
`PXEConfig["<tool>.ipxe"]` at init so `bootDispatch` and the menu-selection renderer keep their
existing `PXEConfig[os+".ipxe"]` lookup untouched.

| Form | Script shape | Slice-1 tool | Slice-2 tools |
|------|--------------|--------------|---------------|
| `formKernelInitrd` | `kernel [[baseurl]]/<kernel> <cmdline>` + `initrd [[baseurl]]/<initrd>` + `boot` | SystemRescue | Clonezilla, Rescatux, Tails |
| `formEFIChain` | `chain [[baseurl]]/<file>` | UEFI Shell | ZFSBootMenu |
| `formSanboot` | `sanboot [[baseurl]]/<img>` | Memtest86 | ShredOS |

The form supplies structure; a per-tool `cmdline` string supplies the tool-specific incantation
(`archiso_http_srv=` for SystemRescue, `fetch=` for Clonezilla), because the form alone does not
determine the cmdline. Those cmdlines are sourced from netboot.xyz's own menu templates — a
**second oracle** beyond `endpoints.yml`, to be pinned per tool during planning.

`bootTokensFor` (`pkg/tftp/tftp.go:234-256`) gains one tool arm emitting generic `[[baseurl]]`,
`[[version]]`, and `[[arch]]` tokens. One arm serves all tools: they all resolve to the same
`cache.CacheURLBase` shape.

**Form/firmware caveat:** `formEFIChain` only works on UEFI clients, and `formSanboot` behaves
differently under BIOS and UEFI. The lab gate (§9) exercises UEFI Shell on a UEFI client
specifically for this reason.

## 7. Menu integration

`renderMenu` (`pkg/tftp/menu.go:111-151`) splits in-window entries by family
(`ostype.Lookup(cache.CacheNameToCanonical(e.CacheName)).Family().Name == "tool"`):

- **Main menu:** `retry`, the non-tool in-window entries, then `Tools & rescue...` and
  `Archived OSes...` — each emitted only when its group is non-empty.
- **Tools submenu:** `Back`, the tool entries, chaining the same `menu/<tuple>/boot.ipxe` path in
  the same key format as the main and archived blocks.
- **Archived tools go to `Archived OSes...`, not to the tools submenu.** Archived is archived —
  one place for it, regardless of family.

`menu.go`'s package comment names the guarded `iseq`/`goto` dispatch shape as the invariant to
preserve, and a second sentinel is where that gets fragile. Each sentinel gets its **own guarded
line with an explicit fall-through label**, rather than chaining
`iseq … && goto … || iseq … && goto … || goto boot` on one line — one conditional per line keeps
`||` from binding across two of them.

**Sentinel namespace.** The keys `retry`, `tools`, `archived`, and `back` share a namespace with
cache names, so a future OS literally named `tools` would shadow the submenu. Guarded by a test
asserting no registered OS name collides with a sentinel.

**Display labels.** `osTitle` (`pkg/tftp/menu.go:16-21`) gains an entry per tool. This duplicates
naming that the web UI also carries, which is pre-existing for all four current OSes; not worth
extracting on this change.

## 8. Error handling & security

**Discovery failure degrades to stale, not broken.** `reconcileTarget` treats a discovery fetch
error as "keep the existing cached set, no prune" (`pkg/cache/reconcile.go:18-20`). A netboot.xyz
outage or schema change therefore leaves already-cached tools bootable and the menu unchanged. A
missing or renamed endpoint key fails that one target; the other seven and all four real OSes are
unaffected.

**Version strings are a trust boundary.** `ValidateVersion` currently runs when reading the cache
from disk (`list.go:67`, `newest.go:29`) and on the manual-pin API (`api_targets.go:203`) — but
**not** on versions returned by `DiscoverVersions`. Every existing OS parses its version out of a
controlled upstream format, so nothing was ever exposed. A tool's version is a free-form field in a
third party's YAML that becomes a **cache directory name** via `cacheDir(...)` and a URL segment.

The guard therefore lives in the shared `netbootxyzOS.DiscoverVersions`, which validates before
returning: the trust boundary sits with the untrusted source rather than changing the reconcile
contract for every OS.

**URL construction is validated too.** Assert the constructed artifact URL parses and that its host
is the expected one, so a garbled or hostile `path` cannot redirect a download off-domain.

**Parsing hazards** to pin in tests: `13.01` is unquoted in `endpoints.yml` and must be decoded as
text, not as a float — a float round-trip would silently turn a future `7.10` into `7.1`. Rescatux's
literal `current` must survive as an ordinary opaque version.

**No integrity verification is available — accepted risk.** `endpoints.yml` publishes no checksums
and no signatures, so `Artifact.SHA256`/`SigURL` stay empty, artifacts land as
`classNotVerifiable`, and `cache_entries.verified` stays NULL. This is identical to the existing
posture for Talos and Debian netboot artifacts. The trust anchor is HTTPS plus GitHub's
release-asset hosting.

Recording it explicitly because the risk profile is not identical to the existing cases: these are
third-party binaries executed with full hardware privilege on bare metal, and a rescue tool is
exactly the artifact an attacker would want to poison. `--signaturePolicy strict` does not help —
there is no mechanism to be strict about.

## 9. Testing

**Unit**

- Snapshot parsing: `13.01` as text not float; `current` as an opaque version; unknown endpoint key;
  malformed YAML.
- URL construction from `path` + `files`, including the off-domain rejection.
- `ValidateVersion` rejects traversal and non-path-safe strings.
- Per-form template rendering goldens for all three forms.
- `renderMenu` across all four combinations of tools present/absent × archived present/absent,
  asserting the guarded-dispatch shape and the sentinel-collision guard.
- `authoringKindsForFamily` returns an empty slice for a config-less family.

**Integration**

- Reconcile a tool target against a fixture `endpoints.yml` served locally: target → discovery →
  artifacts → cache dir → `ListCached` → menu entry.

**Lab gate (required before merge)**

Three boots in the QEMU netboot lab (see the `booty-netboot-lab` notes), one per boot form:
SystemRescue, UEFI Shell on a UEFI client, Memtest86.

This is not optional ceremony. On the debianconfig branch, byte-exact goldens and two code reviews
all passed while three real bugs sat in the output — they only manifested when debian-installer
actually executed the preseed. Tool cmdline correctness has exactly the same property: a template
can render perfectly and still not boot.

**Review**

The repo convention applies: a cold `sr-go-engineer` whole-branch review that drives the built
binary, not one that only reads the diff.

## 10. Documentation impact

- `docs/BOOT-MENU.md` — the tools submenu, and tool entries in the "adding more OSes" section.
  That file lands via the `worktree-docs-boot-menu-vlan` branch, which is a **prerequisite**: the
  execution branch rebases onto it once merged, and the plan edits the existing file rather than
  creating a second home for boot-menu documentation.
- `docs/schema/CATALOG.md` — the `os:` value list, per-OS arch tokens, and the note that tools take
  no `spec`.
- `docs/schema/API.md` — new OS names on `GET /api/v1/os`; the `tool` family on
  `GET /api/v1/families` with an empty `authoringKinds`.
- `deploy/catalog.yaml` — commented-out tool entries with their disk-size costs.
- `README.md` — the supported-OS table.

## 11. Slice 2 (deferred, additive)

Clonezilla, Rescatux, and Tails on `formKernelInitrd`; ZFSBootMenu on `formEFIChain`; ShredOS on
`formSanboot`. Each is one row in `pkg/ostype/tools.go` and one row of boot data in `pkg/tftp`,
with no change to the shared implementation, the family, the menu, or the catalog.

If slice 2 turns out to need a **fourth** boot form, that is the signal to revisit the form-switch
shape — a growing switch that every new variant must edit is the wall the No-Wall principle warns
about. Three forms with additive tools is an acceptable resting point; a steadily growing form
count is not.
