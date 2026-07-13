# Debian preseed structured authoring (`debianconfig`) — Design

**Date:** 2026-07-10 · **Slice:** new (v1 roadmap; slots beside the existing Debian/preseed support, independent of P7+) · **PR target:** `jacaudi/booty:main` · depends on **P4** (config kinds, revisions, roles, host binding, `renderConfig`/`resolveConfig`, the family-match guard) which is shipped.

> **Session note:** brainstormed 2026-07-10. The load-bearing upstream facts — that there is **no vendorable structured→preseed library**, that **ext4/xfs** are native to `partman` while **root-on-ZFS is not supported by the Debian installer at all**, and that **mdadm software RAID1** *is* a first-class `partman-auto` primitive while **btrfs/zfs mirroring is not** — were each **verified against current Debian sources** during the brainstorm (citations inline in §6). The internal seams (the `renderConfig` template→translate pipeline, the strict 1:1 family-kind guard in three call sites, the `configs.kind` CHECK constraint) were **verified against live code** (`pkg/http/render.go`, `resolve.go`, `api_hosts.go`, `pkg/db/migrations/0005_clusters.sql`). All decisions **D1–D7 user-approved** during the brainstorm (each as recommended); alternatives on file in §14.
>
> **SGE review amendment (2026-07-10):** an `sr-go-engineer` design review (verdict *AMEND-BEFORE-PLANNING*, 0 blocking, no approved decision flagged defective) was folded. The three load-bearing seams — the exact three family-guard call sites (§7), the FK-off migration runner (§9), and the "free" validation path (§8) — were **re-verified clean against live code**. Folded corrections: **I1** the preview vars need no reroute (`ignitionVars`≡`preseedVars` today — no skew exists; §8/§11), **I2** the huma `enum:` tag at `api_configs.go:75` is a second required wiring point (§8), **I3** the `raid: mirror` recipe is `text/template`-parameterized over the device list, not a static map value, and is materially larger than a filesystem swap (§5/§6.2/§11), **I4** disk-coherence checks fire only when a `disk:` block is present (§6.5), **M1** `configKindForFamily`'s removal also retargets its unit test (§7), **M2** the serve widening also switches the `preseed.go:28` literal to the resolved kind (§8), **M3** `ssh_authorized_keys` has no native preseed directive and is generated into a `late_command` block (§4/§5), **M4** the static-network fields are specified (§4).

---

## 1. Context & problem

### 1.1 What already exists

booty already treats Debian as a supported OS and `preseed` as a first-class config kind:

- `preseed` is in the `configs.kind` enum; create / update / preview / rollback / revisions all work (`pkg/http/api_configs.go`).
- `renderConfig` runs a config source through Go `text/template` against `TemplateVars`, then translates per kind. For `preseed` the "translation" is a **no-op** — it returns the rendered bytes as `text/plain` (`render.go:68`). So today's "preseed hydration" is **generic template substitution** (`{{ .Hostname }}`) into a **hand-written flat preseed file**.
- The serve path is complete: `/preseed` resolves a bound host's config (rungs 1–2) or the `--preseedFile` server default (rung 4) via `pkg/http/preseed.go`; the Debian boot cmdline already carries `auto url=<preseed-url>` (`pkg/ostype/ostype.go:61`).
- The Debian ostype has a `channel` param and `Artifacts()` (netboot `linux` + `initrd.gz`).

### 1.2 What's missing

There is **no structured authoring** for Debian. For Flatcar/CoreOS we author **butane** YAML and `coreos/butane` translates it to ignition; for Talos we author a cluster spec and `pkg/machinery` generates each machineconfig. For Debian the operator still hand-writes the entire flat debconf preseed with a few `{{ }}` holes — there is no durable-intent model, no schema, no validation beyond "does the template parse."

This slice adds that missing layer: **a curated structured config kind that booty translates into a flat d-i preseed** — the butane→ignition analog for Debian.

| Layer | Flatcar/CoreOS (shipped) | Debian (this slice) |
|---|---|---|
| High-level authored | butane YAML | **`debianconfig` YAML** (curated fields) |
| Translation engine | `coreos/butane` (vendored library) | **booty-owned generator** (no library exists — §5) |
| Low-level served | ignition (translated) | **flat d-i preseed** (translated), served at `/preseed` |

### 1.3 The one structural asymmetry vs. butane

For ignition there is *no* "author raw ignition" path — you always author butane. For Debian, **raw `preseed` authoring already ships and cannot go away**: the `--preseedFile` server default is inherently raw preseed, and existing raw-preseed configs must keep working. So `debianconfig` **coexists** with raw `preseed` (D2) rather than replacing it — it is the *recommended structured* option; raw `preseed` remains the low-level escape valve. This is the single reason the family-kind guard must generalize from 1:1 to 1:many (§7).

---

## 2. Goals / non-goals

**Goals**

1. A new **`debianconfig`** config kind: a curated YAML that booty translates into a flat d-i preseed, wired additively into create / validate / preview / serve.
2. A **booty-owned generator** (`translateDebianConfig`) — no subprocess, no vendored dependency (none exists — §5).
3. A **curated schema** covering the common bare-metal install (hostname/locale/network/mirror/accounts/packages/disk/late_command), plus **`raw_preseed`** and per-field escape hatches for the long tail (§4).
4. A **disk model** covering **ext4/xfs** filesystems, **plain/lvm** layout, and **mdadm RAID1 (`raid: mirror`)** for a redundant boot disk — all native `partman` primitives — plus a raw `expert_recipe` override (§6).
5. Generalize the family-kind guard from **1:1 → 1:many** so the `preseed` family accepts `{preseed, debianconfig}`, single-sourced across its three call sites (§7).
6. Migration **0006**: extend the `configs.kind` CHECK constraint to include `debianconfig` (§9).
7. The per-slice **doc gate** (`docs/schema/*` + `CONFIGURATION.md`) and **tests** (§11–§12).

**Non-goals (YAGNI — bounded deliberately)**

- **Debian version discovery** — the fixed `{12.5, 11.9}` placeholder in `pkg/ostype/debian.go` stays. Real release discovery is out of scope. (D6)
- **Debian artifact caching** — wiring the netboot `linux`/`initrd.gz` into the cache is out of scope. This slice is **authoring-only**; it does not change how (or whether) Debian assets are cached or the boot mechanism. (D6)
- **Root-on-ZFS** — the Debian installer does not support it *at all* (§6.3). Not a booty limitation; an upstream one. Reachable only via `late_command` at the operator's own risk. A future **Debian-Live/`debootstrap` script-based provisioning path** (a separate slice with its own boot payload, cmdline, config artifact, and serve handler) is noted here so the idea is not lost, but is explicitly out of scope. (D5)
- **btrfs RAID1 root** and **RAID levels other than mirror (0/5/6/10)** — not native `partman-auto` primitives (§6.3); reachable via the raw `expert_recipe` escape hatch only. (D5)
- **Replacing raw `preseed`** — it coexists (§1.3, D2).

---

## 3. The `debianconfig` kind & coexistence (D1, D2)

`debianconfig` is a new value in the `configs.kind` enum (`butane,machineconfig,preseed,schematic,taloscluster,debianconfig`). It is an **authoring dialect**, not an OS-family serving mechanism — it lives entirely in the config/http layer. The `pkg/ostype` family table is **unchanged**: Debian's family `ConfigKind` stays `preseed` (the serving mechanism / boot-URL contract). `debianconfig` and raw `preseed` both **render to a flat preseed body** served byte-for-byte at `/preseed`.

- `debianconfig` is **opt-in per config** — the operator selects the kind at create time. The family default authoring kind stays raw `preseed` (D2); we do not steer harder (no deprecation of raw authoring).
- `kind` remains the **sole dispatch key** (P4 D3): `renderConfig`, `validateConfigSource`, `resolveConfig`, and the serve guard all switch on it. No `debianconfig` config ever carries a schematic/cluster derivation — its `derived_schematic_id` is always NULL.

---

## 4. Curated schema (D3)

The `debianconfig` source is YAML. booty unmarshals it into a `debianConfigSpec` struct and emits a flat preseed. The curated surface (the ~80–90%):

```yaml
# hostname/identity
hostname: "{{ .Hostname }}"        # template runs FIRST, so host vars still substitute
domain: cluster.local

# localization
locale: en_US.UTF-8
timezone: Etc/UTC
keyboard: us

# apt mirror (override-only; codename comes from the Debian target's channel)
mirror:
  hostname: deb.debian.org
  directory: /debian
  proxy: ""                        # optional

# network (DHCP by default; set `static` for static addressing)
network:
  interface: auto                  # "auto" | a named iface (e.g. eth0)
  # static:                        # omit → DHCP
  #   address: 10.0.0.10
  #   netmask: 255.255.255.0
  #   gateway: 10.0.0.1
  #   nameservers: [ 10.0.0.1 ]

# accounts — password HASHES ONLY, never plaintext (§10)
accounts:
  root_password_hash: "$6$..."     # or omit → root login disabled
  user:
    fullname: Ops
    username: ops
    password_hash: "$6$..."
    ssh_authorized_keys: [ "ssh-ed25519 ..." ]   # emitted via late_command (§5) — no native preseed directive

# packages installed by the target
packages:
  - openssh-server
  - qemu-guest-agent

# disk — see §6
disk:
  devices: [/dev/sda, /dev/sdb]
  raid: mirror                     # none | mirror (mdadm RAID1)
  layout: lvm                      # plain | lvm
  filesystem: ext4                 # ext4 | xfs
  boot_degraded: true
  # expert_recipe: |               # raw partman override (wins over the above)

# post-install hook (raw d-i late_command, verbatim)
late_command: |
  in-target systemctl enable ssh

# ESCAPE HATCH: verbatim preseed lines, appended LAST (override semantics)
raw_preseed: |
  d-i debian-installer/allow_unauthenticated boolean true
```

**Emit-only-what-is-set.** Unset/omitted fields emit **no** preseed line, so d-i defaults (or prompts, for a non-fully-automated install) apply — booty never fabricates opinions the operator did not express.

**Ordering / precedence.** Within the generated preseed: curated fields first, then `late_command`, then `raw_preseed` **last** so later duplicate debconf answers win — the escape hatch can always override a curated line. `disk.expert_recipe`, when present, **replaces** the entire curated disk recipe (§6.4).

---

## 5. Generation — `translateDebianConfig` (D3)

**No vendorable library exists.** Unlike butane→ignition (`coreos/butane`), the preseed ecosystem is web form generators, ISO-injection scripts, and `debconf-get-selections` extraction — none is a Go library booty could drop in as a translate step (verified 2026-07-10). So booty owns the generation, structurally closer to the Talos `clustergen.go` pattern (booty composes the artifact) than to the butane arm (library does it).

New file `pkg/http/debiangen.go`:

- `type debianConfigSpec struct { ... }` — the §4 schema with `yaml` tags.
- `func translateDebianConfig(rendered []byte) (out []byte, err error)` — `yaml.Unmarshal` the (already template-substituted) source into the struct, validate coherence (§6.5), then emit the flat preseed via an **internal `text/template`**. The disk section is the non-trivial part: single-disk `plain`/`lvm` combos map cleanly from `(layout, filesystem)`, but **`raid: mirror` is not a static map value** — its `partman-auto-raid/recipe` must enumerate partitions across the N `devices`, so it is `text/template`-parameterized over the device list (see §6.2 for its full surface). Emission via a template (not hand-concatenation) keeps the line-oriented format readable and is load-bearing for the mirror recipe.
- **`ssh_authorized_keys` generation** — d-i has **no native preseed directive** for authorized keys; the curated field is generated into a **`late_command`** block (`in-target` mkdir `~/.ssh`, write `authorized_keys`, chmod/chown) that is composed **before** the operator's own `late_command` so both run and ordering/ownership are deterministic. This is the one curated field that lowers to `late_command` rather than a debconf line.

**Pipeline integration (No-Wall).** `renderConfig` already runs the shared `text/template` step on the source *before* switching on kind. `debianconfig` adds one arm, structurally identical to the `butane` arm:

```go
case "debianconfig":
    out, err := translateDebianConfig(rendered)   // rendered = post-template source
    if err != nil { return nil, "", "", err }
    return out, "text/plain", "", nil
```

Siblings untouched; the template-substitution-first ordering means `{{ .Hostname }}` (and any host var) works in every field, including `raw_preseed` and `expert_recipe`, exactly as it does for butane.

---

## 6. Disk model (D4)

The disk model is curated over **native `partman` primitives only** — every curated combination is something the Debian installer can do unattended. Anything outside is the raw `expert_recipe` escape hatch or a non-goal.

### 6.1 Filesystem — `filesystem: ext4 | xfs`

- **ext4** — native default.
- **xfs** — supported via `partman` `expert_recipe` (`filesystem{ xfs }` / `method{ format }`). ([Wikitech PartMan](https://wikitech.wikimedia.org/wiki/PartMan))

### 6.2 Layout & mirror — `layout: plain | lvm`, `raid: none | mirror`

- **plain** — direct partitions on the device(s).
- **lvm** — LVM on top (optionally on top of the mirror; LVM-over-RAID is supported).
- **`raid: mirror`** — **mdadm software RAID1** via `partman-auto/method string raid` + `partman-auto-raid/recipe`, mirroring across the `devices` list. Native, first-class d-i support, including `mdadm/boot_degraded` so a node still boots on a surviving disk after a failure. ([partman-auto-raid-recipe.txt](https://github.com/xobs/debian-installer/blob/master/doc/devel/partman-auto-raid-recipe.txt), [RAID1+LVM preseed example](https://github.com/ahamilton55/Blog-Scripts/blob/master/debian_ubuntu_preseeds/ubuntu-raid1-lvm.preseed))
  - `boot_degraded: true` (default) sets `mdadm/boot_degraded` — recommended for unattended nodes so a single dead disk does not wedge boot.
  - **Firmware: UEFI-native (user decision 2026-07-10 — target nodes are UEFI).** The ESP **cannot** live on mdadm RAID (firmware writes desync a mirrored ESP; d-i refuses `/boot/efi` on md). So the curated mirror recipe creates a **separate ESP per member disk** (fat32/esp, not a raid member) + md `/boot` + md root, installs `grub-efi`, and emits a **booty-generated `late_command` that clones the primary ESP onto the second disk and registers its `efibootmgr` entry** — so the node still UEFI-boots if the primary disk fails (true boot redundancy, not just mirrored root). Grounded in the standard ESP-per-disk pattern ([std.rocks mdadm-UEFI](https://std.rocks/gnulinux_mdadm_uefi.html), [EFI+RAID1 preseed reference](https://gist.github.com/bearice/331a954d86d890d9dbeacdd7de3aabe8)). A **BIOS/legacy** curated mirror is NOT in this slice — reachable via the raw `expert_recipe` escape hatch (fast-follow: a `firmware:` knob).
  - **late_command composition (UEFI mirror):** three ordered sources — (1) `ssh_authorized_keys` block, (2) the ESP-sync block, (3) the operator's `late_command`.
  - **Install-validation gate (I2) — MANDATORY for the UEFI mirror:** the golden tests pin emitted preseed *bytes*, not install-correctness. Before production trust, the UEFI mirror MUST pass a real UEFI install in the netboot lab (see memory `booty-netboot-lab`): boot both disks, confirm install + reboot, **then remove the primary disk and confirm the node still UEFI-boots** (proves the ESP-sync works). Also lab-check `plain+mirror`, `lvm+mirror`, and `filesystem: xfs`.

The generator emits the `(layout, filesystem, raid)` combination over a bounded matrix: `{plain,lvm} × {ext4,xfs} × {none,mirror}`. **Sizing note:** `raid: none` combos are near-static, but `raid: mirror` is a materially larger recipe, not a filesystem-line swap — it emits `partman-auto/method string raid`, a `partman-auto-raid/recipe` that enumerates member partitions **across the N `devices`** (hence `text/template`, not a static map — §5), the `partman-md/confirm*` + `partman/confirm*` booleans, `mdadm/boot_degraded`, and for `layout: lvm + raid: mirror` the extra LVM-on-md nesting (the trickiest combo). Each of the 8 combinations is golden-pinned (§11).

### 6.3 Explicit non-goals (upstream limitations, not booty's)

- **Root-on-ZFS** — *"ZFS is not supported in the Debian installer as that would mean supporting out-of-tree kernel modules"*; `partman-auto/expert_recipe` has no ZFS support. Every automated ZFS-root tool ([danfossi](https://github.com/danfossi/Debian-ZFS-Root-Installation-Script), [64kramsystem/zfs-installer](https://github.com/64kramsystem/zfs-installer), [OpenZFS HOWTO](https://openzfs.github.io/openzfs-docs/Getting%20Started/Debian/Debian%20Bookworm%20Root%20on%20ZFS.html)) **abandons d-i/preseed** and runs `debootstrap` from a live environment — a different provisioning path entirely (§2 non-goals). ([ZFS-not-in-d-i thread](https://groups.google.com/d/topic/linux.debian.maint.boot/SCnM_m8X2Kc))
- **btrfs RAID1 root** — *"not supported directly from the installer partitioning menu partman"*; requires single-disk install then `btrfs device add` + `btrfs balance` post-install. ([nuess0r gist](https://gist.github.com/nuess0r/57cad4c237a862dc30558e957cce292b))
- **RAID 0/5/6/10** — outside the curated `mirror` case; reachable via `expert_recipe`.

### 6.4 Escape hatch — `disk.expert_recipe`

When set, `expert_recipe` is emitted **verbatim** and **replaces** the entire curated disk recipe (`layout`/`filesystem`/`raid` are ignored for partitioning, though `devices` and `boot_degraded` still apply). This is the power-user path for any partman layout booty does not curate.

### 6.5 Coherence validation

Disk-coherence checks fire **only when a `disk:` block is present.** A `debianconfig` with **no** `disk:` at all is valid — it emits no partman lines, and d-i defaults/prompts apply (consistent with §4's emit-only-what-is-set contract and the "minimal spec" golden test in §11). When `disk:` **is** present, `translateDebianConfig` rejects incoherent specs before emitting (surfaced as a 422 through the normal validation path, §8):

- `raid: mirror` requires **≥ 2** `devices`; `raid: none` with a plain/lvm layout expects **≥ 1**.
- `filesystem` ∈ `{ext4, xfs}`; `layout` ∈ `{plain, lvm}`; `raid` ∈ `{none, mirror}` (unless `expert_recipe` is set, which bypasses the curated knobs).

---

## 7. Family-kind guard: 1:1 → 1:many (D1)

**The one non-trivial wiring change.** The family-match guard today is a strict equality — `k == configKindForFamily(fam.ConfigKind)` — enforced in **three** call sites: `resolve.go:30` (rung-1 explicit), `resolve.go:65` (rung-2 role default), and `api_hosts.go:38` (bind-time). For the Debian family (`ConfigKind == "preseed"`, and `configKindForFamily("preseed") == "preseed"`) that means `kind == "preseed"` **exactly** — so a `debianconfig` config would validate and create fine but then **never bind and never serve**: the guard rejects it at every rung.

Coexistence therefore requires the family↔kind relationship to become **1:many**. Single-source it: replace the equality usages with a membership helper —

```go
// familyAllowsKind reports whether an authored config kind may serve a host of
// the given family (family ConfigKind == serving mechanism). One contract, three
// consumers; the preseed family is the only 1:many case.
func familyAllowsKind(familyConfigKind, kind string) bool {
    switch familyConfigKind {
    case "ignition":
        return kind == "butane"                       // author butane, serve ignition
    case "preseed":
        return kind == "preseed" || kind == "debianconfig"
    default:
        return kind == familyConfigKind               // machineconfig, ...
    }
}
```

All three sites change from `k == configKindForFamily(x)` to `familyAllowsKind(x, k)`. `configKindForFamily` is removed if it has no remaining consumer — it is used only by these **three production sites plus its own unit test** (`render_test.go` `TestConfigKindForFamily`), so removing it also retargets/removes that test to cover `familyAllowsKind` instead (KISS, no dead code). The `preseed` family is the **only** 1:many entry; every other family stays strict equality (`default: return kind == familyConfigKind`), so no other OS behavior changes — schematic/taloscluster never reach this guard (they bind via the host-schematic / cluster path, not the boot-config resolve guard).

---

## 8. Free integrations (fall out of `debianconfig` being *renderable*)

Because `debianconfig` is a **renderable** kind (it produces a body via `renderConfig`), most wiring is automatic — with **two explicit edits** that are easy to miss (the huma enum tag and the serve literal):

- **Enum admission (explicit edit — I2).** create-config validates `kind` against a huma `enum:"butane,machineconfig,preseed,schematic,taloscluster"` tag at `api_configs.go:75`; huma enforces this from the generated JSON schema **before** the handler runs, so without adding `debianconfig` here, create returns 422 before `validateConfigSource` is ever reached. This enum tag is a **second** admission point beyond the DB CHECK (§9) and must be edited too.
- **Validation (free)** — `validateConfigSource`'s `default` arm already validates renderable kinds by a stub-var render (`renderConfig(kind, source, stubVars())`). `debianconfig` flows through `default` unchanged; its coherence checks (§6.5) surface as the same 422. **No new case** is needed — it must simply *not* be caught by the `schematic`/`taloscluster` non-renderable arms (it is not).
- **Preview (free)** — the preview handler guards only `schematic`/`taloscluster` as non-renderable, so `debianconfig` previews automatically through the existing `default` vars arm — exactly as raw `preseed` does today. **No `previewVars` change is needed:** `ignitionVars` (the `default` arm) and `preseedVars` (the serve path) set **byte-identical** `TemplateVars` today, so there is no preview/serve skew to close. The two functions are deliberate per-family seams (No-Wall) whose identity is coincidental, not shared knowledge — do **not** unify them in this slice.
- **Serve (explicit edit — M2)** — `handlePreseedRequest` gates on `kind == "preseed"` **and** hardcodes `renderConfig("preseed", src, …)` at `preseed.go:28`. Widen the guard to a membership check (`preseed` or `debianconfig`) **and** switch that literal to the resolved `kind`, so a bound `debianconfig` dispatches to its `renderConfig` arm; both render to a flat preseed body. The rung-4 `--preseedFile` default at `preseed.go:46` correctly stays `"preseed"` (the file default is always raw).

---

## 9. Migration 0006 (D7)

`configs.kind` carries a `CHECK (kind IN (...))` constraint (currently `'butane','machineconfig','preseed','schematic','taloscluster'` after 0005). SQLite cannot `ALTER` a CHECK, so adding `debianconfig` means **rebuilding the `configs` table** — the identical dance migrations **0004** and **0005** already performed: create `configs_new` with the extended CHECK, copy rows, drop old, rename.

This follows the **established** P5/P6 pattern, including the foreign-keys-OFF runner behavior introduced in P5 (the rebuild must run with `foreign_keys` OFF so `DROP TABLE configs` does not cascade-wipe `config_revisions`/`hosts`, with a `foreign_key_check` afterward). No new runner work is expected — 0006 is SQL that mirrors 0005; the plan must nonetheless confirm the runner already handles the rebuild (it does as of P5) and add nothing that diverges.

---

## 10. Security

- **Password hashes only.** `accounts.*_password_hash` take pre-computed crypt hashes (`$6$…`); booty never accepts or stores plaintext passwords and never generates hashes. This keeps plaintext credentials out of the config store and out of logs. Omitting `root_password_hash` disables root login (locked account), the safer default.
- **No new mutating endpoints.** This slice adds a config *kind*, not new routes. The existing `configs` create/update/preview endpoints (open in the trust window; DELETE wired-but-403 until P10) carry it. No P10 posture change.
- **Escape hatches are operator-authored bytes** — `raw_preseed`, `late_command`, and `expert_recipe` are emitted verbatim; they are the operator's responsibility, same trust model as a raw `preseed` config today.

---

## 11. Testing

- **Golden translate tests** (`debiangen_test.go`) — `debianConfigSpec` → expected flat preseed for: minimal spec (no `disk:` → no partman lines); full spec; **each of the 8 `(layout, filesystem, raid)` combinations, with the exact emitted `partman`/`partman-auto-raid` recipe asserted byte-for-byte** (the mirror recipe format — `method=raid`, the device-enumerated `partman-auto-raid/recipe`, confirm booleans, `mdadm/boot_degraded`, and LVM-on-md nesting — is the point of these, not incidental); `ssh_authorized_keys` → generated `late_command` block composed **before** the operator's own `late_command`; emit-only-what-is-set (omitted fields → no lines); `raw_preseed`/`late_command` ordering (appended last); `{{ }}` substitution through the template step.
- **Coherence tests** — **absent `disk:` block is valid** (no lines, no error — I4); `raid: mirror` with < 2 devices rejected; invalid `filesystem`/`layout`/`raid` rejected; `expert_recipe` bypasses curated-knob validation.
- **Guard tests** — `familyAllowsKind`: `debianconfig` allowed for the Debian family, rejected for ignition/machineconfig families; raw `preseed` still allowed; other families unchanged.
- **Integration** — the huma `enum` admits `debianconfig` (a bad kind still 422s — I2); create a `debianconfig` → preview (renders flat preseed) → bind to a Debian host → GET `/preseed` returns the rendered flat preseed byte-for-byte; a `debianconfig` bound to a non-Debian host is guard-rejected.
- **Migration test** — 0006 rebuild preserves existing configs/revisions; `debianconfig` inserts succeed; non-enum kinds still rejected by the CHECK.

## 12. Doc gate (per-slice)

- `docs/schema/*` — document the `debianconfig` kind and the `debianConfigSpec` schema (every field, defaults, the disk matrix, escape hatches, the ext4/xfs + mirror support and the zfs/btrfs-raid non-goals).
- `CONFIGURATION.md` — an authoring walkthrough with a worked `debianconfig` example (including a `raid: mirror` node) and a note that raw `preseed` remains available.

---

## 13. Decisions (all user-approved 2026-07-10)

- **D1 — New `debianconfig` kind + booty-owned curated generator**, added additively to `renderConfig`; family guard generalizes 1:1 → 1:many via `familyAllowsKind`.
- **D2 — Coexist with raw `preseed`** (not replace) — forced by the `--preseedFile` raw default and existing configs; `debianconfig` is the recommended structured option, opt-in per config.
- **D3 — Curated schema (~80–90%) + escape hatches** (`raw_preseed`, `late_command`, `disk.expert_recipe`); emit-only-what-is-set; booty owns generation (no library exists).
- **D4 — Disk: `filesystem: ext4|xfs`, `layout: plain|lvm`, `raid: none|mirror` (mdadm RAID1)** + raw `expert_recipe` override.
- **D5 — Non-goals: root-on-ZFS, btrfs RAID1, RAID 0/5/6/10** (upstream/partman limitations) — escape hatch only; a future Debian-Live/`debootstrap` provisioning path is a separate slice.
- **D6 — Authoring-only scope** — Debian version discovery and artifact caching are deferred (fixed placeholder stays).
- **D7 — Migration 0006 rebuilds `configs`** to extend the `kind` CHECK, following the 0004/0005 pattern + P5 FK-off runner.

## 14. Alternatives considered

- **Enrich `TemplateVars` with Debian-specific fields, keep raw preseed** (no new kind). Rejected: still hand-authored raw preseed with more holes — not the structured→artifact translation the slice is for; not "like Talos/Flatcar/CoreOS."
- **Replace raw `preseed` with `debianconfig`** (flip `configKindForFamily`). Rejected: the `--preseedFile` default is raw and existing configs would break; 1:1 remap can't express "both."
- **`structured: true` flag on the `preseed` kind** instead of a new kind. Rejected: `kind` is the sole dispatch key (P4 D3); a flag splits dispatch across two axes.
- **Vendor / subprocess a preseed generator.** Rejected: none exists as a library; the web/ISO tools don't fit a server translate step.
- **Programmatic string-building** for emission instead of an internal template. Rejected (weakly): a template keeps the line-oriented format readable; either would function.
- **Include full RAID matrix (0/5/6/10) / btrfs-raid1 / zfs-mirror now.** Rejected: only `mirror` is the "boot redundancy" 80%; the rest are non-native or upstream-unsupported (§6.3) — escape hatch / non-goals.
