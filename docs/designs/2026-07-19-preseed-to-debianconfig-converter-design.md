# Design: `preseed → debianconfig` converter (issue #60)

**Status:** approved (brainstorming); SGE (fable) design review folded (verdict
AMEND-BEFORE-PLANNING → B1–B4 + non-blocking findings applied); pending user spec review
**Date:** 2026-07-19
**Issue:** #60 — CLI: preseed to debianconfig converter
**Unblocks:** #59 — Remove the preseed config kind end-to-end

## 1. Problem & goal

The config create UI no longer offers raw `preseed` — `debianconfig` (structured,
booty-owned authoring) is the only Debian format the picker exposes
(`web/src/api/configKinds.ts` `OS_CHOICES`). An operator who already has a hand-written
d-i preseed (or an existing `kind='preseed'` config) has no path onto the structured
format except hand-transcription.

**Goal:** a CLI that reads a flat d-i preseed and emits the equivalent structured
`debianconfig` YAML, **dropping nothing** — whatever cannot be mapped onto a structured
field lands in the `raw_preseed` escape hatch verbatim. This is the migration aid that
must ship *before* #59 removes the `preseed` kind, so raw-preseed authors have somewhere
to land.

Non-goal: it does not touch the DB, does not create/modify configs, and does not need
booty to be running. It is a pure text transformation.

## 2. CLI surface

```
booty convert-preseed [FILE]
```

- Reads `FILE`, or **stdin** when no arg (so `booty convert-preseed < preseed.cfg` and
  piping an API/UI-exported config source both work).
- Writes the `debianconfig` YAML to **stdout**.
- Writes round-trip warnings + a summary to **stderr** (so `> out.yaml` captures only the
  YAML).
- Exit code 0 on success even when there are warnings (the warnings are advisory — see §5);
  non-zero only on unreadable input or an internal error.

Wired as `newConvertPreseedCmd()` in `cmd/convert_preseed.go`, added via
`Cmd.AddCommand(...)` in `cmd/main.go` `init()`, matching the existing `newVersionCmd()`
pattern. Use `RunE`, and set an `Example` block (go-standards §6.2), e.g.
`booty convert-preseed preseed.cfg > debian.yaml` and the stdin form
`booty convert-preseed < preseed.cfg`.

## 3. Architecture

Two units, clean seam between transformation and CLI plumbing:

- **`pkg/http/preseedconv.go`** — the converter core, exported as:
  ```go
  func ConvertPreseedToDebianConfig(preseed []byte) (out []byte, warnings []string, err error)
  ```
  Lives in `package http` deliberately, to:
  - reuse the authoritative `debianConfigSpec` schema (DRY — the parse target is the same
    contract `translateDebianConfig` consumes), and
  - call the existing `translateDebianConfig` for round-trip verification (§5).

- **`cmd/convert_preseed.go`** — a thin cobra subcommand: resolve stdin/FILE → call the
  core → write stdout/stderr. No transformation logic here.

Rationale: the transformation is testable in isolation as a `[]byte → []byte` function;
the CLI is a trivial adapter. New OS converters later would be new sibling files, not a
rewrite (No-Wall).

## 4. Mapping: flat preseed → structured

### 4.1 Parse

A small preseed parser:
1. Joins line continuations (a physical line ending in `\` continues the next).
2. Drops blank lines and `#` comments.
3. Splits each logical line into `owner template type value` (owner is usually `d-i`; a few
   directives use a package owner). Malformed lines that don't fit the grammar are passed
   through to `raw_preseed` verbatim (never dropped).
4. Dispatches on `template`.

### 4.2 Scalar directive map (recognized → structured field, line consumed)

| Preseed template(s) | → field |
|---|---|
| `debian-installer/locale` | `locale` |
| `keyboard-configuration/xkb-keymap` | `keyboard` |
| `netcfg/get_hostname` / `netcfg/get_domain` | `hostname` / `domain` |
| `netcfg/choose_interface` | `network.interface` |
| static block (`netcfg/disable_autoconfig true` + `get_ipaddress`/`get_netmask`/`get_gateway`/`get_nameservers`/`confirm_static`) | `network.static.*` (DHCP when the static markers are absent) |
| `mirror/http/hostname` / `/directory` / `/proxy` | `mirror.*` |
| `mirror/country` (`manual`) | recognized/consumed (booty re-emits it); no field |
| `time/zone` | `timezone` |
| `passwd/root-login`, `passwd/root-password-crypted` | `accounts.root_password_hash` (verbatim hash) |
| `passwd/make-user`, `passwd/user-fullname`, `passwd/username`, `passwd/user-password-crypted` | `accounts.user.*` (verbatim hash) |
| `pkgsel/include` | `packages` (space-split) |
| `preseed/late_command` | `late_command` (verbatim; note it may embed ssh/sudo fragments — those stay in `late_command`, they are **not** reversed into structured `ssh_authorized_keys`/`sudo`) |

Anything whose `template` is not in this map → appended verbatim to `raw_preseed`.

**Network coherence (SGE non-blocking):** the structured schema only has `nameservers`
*under* `network.static`, so the static fields must be recognized as a coherent unit. If
the static markers (`disable_autoconfig`/`confirm_static`) are present, map the whole
static block. If a static field appears **orphaned** (e.g. `get_nameservers` with no
static markers — DHCP plus custom DNS, which the structured schema cannot express), fall
**all** the orphaned `netcfg/*` fields to `raw_preseed` rather than fabricating a partial
`static` block. `netcfg/choose_interface` still maps to `network.interface` independently.

### 4.3 Disk / partman — all-or-nothing group recognition

Disk lines are handled as **one group**, never per-line, so a structured `disk` block can
never coexist with leftover raw partman lines that would conflict (`raw_preseed` is
appended last and later duplicate debconf answers win). Group membership = templates
matching `partman*`, `partman-auto*`, `partman-auto-raid*`, `partman-lvm*`, `partman-md*`,
`partman-efi*`, `mdadm*`, and **`grub-installer/*`** (the last **must** cover both
`grub-installer/bootdev` and `grub-installer/force-efi-extra-removable`, which the
UEFI-mirror shape emits — SGE B1; a bare `grub-installer/bootdev` exact match would leak
`force-efi-extra-removable` into `raw_preseed` and break all-or-nothing).

- If the whole group matches a **recognized shape**, emit a structured `disk` block and
  **consume all** group lines:
  - `partman-auto/method regular` + `choose_recipe atomic` (+ `partman/default_filesystem`)
    → `disk: { devices, layout: plain, filesystem }`
  - lvm shape (`partman-auto/method lvm` + partman-lvm lines) → `layout: lvm`
  - mirror shape (`partman-auto/method raid` + booty's `partman-auto-raid/recipe` + mdadm)
    → `raid: mirror` (+ `boot_degraded` from `mdadm/boot_degraded`). The recipe has an
    **lvm sub-variant** (the `lvm -` root token + partman-lvm lines, template
    `debiangen.go:578-582`) → also set `layout: lvm`; a plain mirror sets `layout: plain`
    (SGE non-blocking).
  - `partman-auto/expert_recipe` present (and not the recognized mirror recipe) →
    `disk: { devices, expert_recipe: <verbatim, whitespace-collapsed> }`
- Otherwise emit **no** `disk` block and pass **all** group lines through `raw_preseed`
  verbatim.

Device list comes from `partman-auto/disk` (and `grub-installer/bootdev`). Recognition
targets the shapes booty's own generator emits plus the canonical d-i atomic/lvm forms;
unusual recipes degrade safely to raw. The round-trip check (§5) is the guarantee that a
recognized-shape mapping is actually faithful.

**ESP-sync de-duplication (SGE B2):** for a recognized **mirror** shape, booty's forward
generator composes a deterministic ESP-sync fragment (`espSyncLateCommand(devices)`,
`debiangen.go:242-257`) INTO `preseed/late_command`. So a booty-generated mirror preseed
carries ESP-sync inside `late_command`. If the converter both recognizes the mirror disk
(which makes re-render regenerate ESP-sync) **and** copies `late_command` verbatim, the
re-rendered `late_command` contains ESP-sync twice → a false round-trip warning. Therefore,
when the mirror shape is recognized, the converter **strips the reconstructable
`espSyncLateCommand(devices)` substring from the captured `late_command`** before storing
it (the device list is recovered, so the fragment is reconstructable). This is what makes
§8's "booty-generated mirror preseed round-trips with no warnings" hold. **Processing-order
dependency:** the disk group must be recognized (and its device list recovered) *before*
the ESP-sync substring can be stripped from `late_command` — the implementation must map
disk before finalizing `late_command`.

## 5. Round-trip verification (verify + warn, always emit)

After building the structured output, re-render it via the existing `translateDebianConfig`
and compare to the input:

1. **Re-render can error (SGE B4).** `translateDebianConfig` returns an *error*, not a
   preseed, when the produced YAML fails `buildPreseedView`/`buildDiskView` validation
   (e.g. a verbatim-mapped `passwd/username` that is uppercase/over-32-chars, or a
   recovered `raid: mirror` with a single device). In that case do **not** crash or
   hard-fail: emit a prominent stderr + header warning ("produced YAML did not re-render:
   `<err>`; review the structured fields") and still emit the best-effort YAML (exit 0).
2. **Normalize on logical directives, not physical lines (SGE B3).** Reuse §4.1's parser
   on BOTH sides: join `\` continuations into logical directives, then collapse internal
   whitespace per directive (`strings.Fields`-style) and drop the **owner** field (booty
   always re-renders owner `d-i`, while hand-written inputs often use a package owner such
   as `keyboard-configuration keyboard-configuration/xkb-keymap …`; comparing full lines
   would emit benign owner-mismatch noise). A naive physical-line/edge-trim comparison
   would systematically false-positive on the flattened single-line `expert_recipe`
   (`debiangen.go:170`) versus multi-line hand input, and on internal double-spaces — so
   the normalization MUST be directive-level, not line-level.
3. Compute the symmetric difference over the normalized directive sets.
4. For each difference, emit a precise **stderr warning** (input directive present but not
   re-rendered, or vice versa), plus a **header comment** in the emitted YAML summarizing
   what to double-check.

`raw_preseed` passthrough is lossless by construction, so warnings only ever concern the
**structural** mappings — disk especially. The converter always emits its best-effort YAML
(exit 0); the operator reviews the warnings and hand-lifts anything flagged. This matches
the migration-aid intent: a working starting point plus an accurate punch-list, never a
silent wrong config and never a hard block on an imperfect hand-written preseed.

A few benign warnings are expected-and-acceptable on hand-written inputs (documented so
they aren't mistaken for mapping bugs): booty re-emits `mirror/country string manual`
whenever a mirror block is present (`debiangen.go:463-465`), so a hand input with a mirror
host but no `manual` selector draws a "re-rendered present, not in input" note.

## 6. Output format

- **Emit-only-what-is-set** — mirrors the authoring philosophy in `debiangen.go`; an unset
  field produces no YAML key (`omitempty`), stable field order.
- A leading header comment records provenance (`# generated by booty convert-preseed`) and
  any round-trip caveats.
- **Controlled marshaling via a dedicated output struct (SGE non-blocking).** The raw
  `debianConfigSpec` cannot be marshaled directly: its scalar fields carry **no**
  `omitempty`/`omitzero` (`debiangen.go:21-33`), so it would emit `hostname: ""`, and its
  `sudoMode` int would emit `sudo: 0`. Adding those tags would edit the **shared** authoring
  struct, which §9 forbids. And §5 compares *preseed text*, not structs, so a
  "struct-identity round-trip" buys nothing. Therefore: emit through a **dedicated
  plain-typed output struct** (`package http`, alongside the converter) that mirrors the
  authoring schema with `omitzero` (Go 1.26 — for slices/pointers/structs, not the legacy
  `omitempty`) and a fixed field order. The converter never populates `sudo` (§9 non-goal),
  so it is simply absent from the output struct.

## 7. Error handling & edge cases

- Unreadable FILE / stdin → non-zero exit, clear message.
- Malformed preseed line (doesn't fit `owner template type value`) → passed to
  `raw_preseed` verbatim, not an error.
- Empty input → empty `debianconfig` (no keys) + a note.
- A directive that maps to a field but has an unexpected value (e.g. a hash that isn't a
  crypt string) → mapped verbatim; the round-trip check will not flag it (it re-renders
  identically), and structured validation is the server's job at config-create time.
- Duplicate directives in the input → last-wins for a mapped field (matches debconf); the
  round-trip check catches any resulting divergence.

## 8. Testing (TDD)

- Table tests: one case per scalar directive (input line → expected field).
- Network: DHCP vs static block.
- Accounts: root-only, user-only, both; hash passthrough.
- Disk group recognition: atomic→plain, lvm, mirror (plain), **mirror+lvm**,
  expert_recipe, and unrecognized→raw_preseed (all-or-nothing). Assert
  `grub-installer/force-efi-extra-removable` is consumed with the mirror group (B1).
- `raw_preseed` remainder: unknown directives + malformed lines pass through verbatim.
- **Round-trip suite:** feed booty-generated preseeds (from representative `debianconfig`
  fixtures through `translateDebianConfig`) back through the converter and assert a clean
  round-trip (**no warnings**) — including a **mirror** fixture, which proves the ESP-sync
  de-duplication (B2). Include a case where `translateDebianConfig` re-render errors (B4)
  and assert the converter still emits + warns (exit 0).
- Warning emission: an input whose disk shape is unrecognized produces the expected
  stderr/header warnings and still emits.
- CLI: file arg vs stdin; stdout carries only YAML, stderr carries warnings.

## 9. Out of scope

- Reversing `late_command` back into structured `ssh_authorized_keys`/`sudo`/package
  auto-adds (kept verbatim in `late_command`; round-trips correctly).
- Reading configs directly from the DB (operator pipes an exported source in).
- Any change to the `debianconfig` authoring schema or `translateDebianConfig`.
