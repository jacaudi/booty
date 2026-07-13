# Configuration reference

booty is configured entirely through command-line flags and environment variables — there is **no
config file**. Every flag is also bindable as an environment variable (via Viper). Defaults are the
values used when neither a flag nor an env var is set.

## Flags

| Flag | Default | Purpose |
|------|---------|---------|
| `--httpPort` | `8080` | Port the HTTP server listens on. |
| `--debug` | `false` | Enable debug-level (verbose) logging. |
| `--cacheInterval` | `5m` | Interval between cache reconcile passes (discovery refresh). |
| `--cacheConcurrency` | `4` | Max concurrent artifact downloads during a reconcile pass. |
| `--signaturePolicy` | `warn` | Artifact verification policy: `strict` \| `warn` \| `off` (see [below](#signature-verification---signaturepolicy)). An unknown value fails startup. |
| `--dataDir` | `/data` | Directory for all stateful data (cache, templates, host DB). |
| `--serverIP` | `127.0.0.1` | LAN-reachable IP that clients use to reach booty. **Set this.** |
| `--serverHttpPort` | `0` (falls back to `--httpPort`) | Client-facing HTTP port advertised in boot-config URLs. Unset (`0`) advertises whatever `--httpPort` listens on — the correct default for a single-process, no-proxy deploy. Set explicitly only when a proxy/LB fronts booty and the client-facing port differs from the listen port (e.g. `--serverHttpPort=80` with `--httpPort=8080`). |
| `--joinString` | `""` | Optional `kubeadm join` string injected into rendered Ignition. |
| `--flatcarArchitecture` | `amd64` | Architecture for Flatcar downloads. |
| `--flatcarChannel` | `stable` | Flatcar release channel — **first-boot default only** (see below). |
| `--coreOSArchitecture` | `x86_64` | Architecture for Fedora CoreOS downloads. |
| `--coreOSChannel` | `stable` | Fedora CoreOS stream/channel — **first-boot default only** (see below). |
| `--talosArchitecture` | `amd64` | Talos architecture token (`amd64` / `arm64`). |
| `--talosSchematic` | `376567988ad3…b4ba` | Default Talos Image Factory schematic ID — **first-boot default only** (see below). |
| `--talosRetainMinors` | `3` | Number of newest Talos minor lines to keep cached — **first-boot default only** (see below). |
| `--talosConfigFile` | `config/machineconfig.yaml` | Talos machine-config template, relative to `--dataDir`. |
| `--talosFactoryURL` | `https://factory.talos.dev` | Talos Image Factory base URL. |
| `--secretsKey` | `""` (unset) | Path to an age identity file (`age-keygen` output) encrypting Talos cluster secrets and frozen node configs at rest. **Fail-closed** when unset: cluster create/import/add-member/export refuse with `422`. **Fail-fast** when set but invalid: refuses startup (see [below](#talos-cluster-authoring-p6)). |
| `--proxyDHCPEnabled` | `false` | Enable the proxyDHCP responder (UDP 67 + 4011). |
| `--proxyDHCPBootfileBIOS` | `undionly.kpxe` | Pass-1 BIOS iPXE binary name (staged in `--dataDir`). |
| `--proxyDHCPBootfileUEFI` | `ipxe.efi` | Pass-1 UEFI (x86-64) iPXE binary name. |
| `--proxyDHCPBootfileARM64` | `ipxe-arm64.efi` | Pass-1 ARM64 iPXE binary name. |
| `--preseedFile` | `config/preseed.cfg` | Debian preseed template, relative to `--dataDir` — the rung-4 server-default fallback for hosts with no DB-resolved config (see [below](#boot-config-precedence-p4)). |
| `--configRevisionsKeep` | `10` | Number of newest config revisions to retain per config, applied after every `PUT /configs/{id}`. The currently-active revision is always kept, even if it falls outside the newest-N window. |

> **First-boot defaults (#48).** `--flatcarChannel`, `--coreOSChannel`, `--talosSchematic`, and
> `--talosRetainMinors` seed the **predefined** cache targets (Flatcar, Fedora CoreOS, Talos) only
> when that target's row doesn't exist yet — a fresh install, or the one-time migration on first
> startup after upgrading past #48 (see [schema/STORAGE.md](schema/STORAGE.md)). Once a predefined
> row exists, these flags are never consulted for it again: the `/api/v1/targets` API owns
> `enabled` / `retainN` / `mode` from that point on, and a value set via `PATCH` survives every
> reconcile tick. Concretely:
>
> - Changing `--flatcarChannel` / `--coreOSChannel` after first boot does **not** retarget the
>   existing predefined row — because the channel is part of the target's identity
>   (`UNIQUE(os,arch,params)`), it seeds a **new** predefined target for the new channel on the
>   next tick, alongside the old one. Disable the old channel's target with
>   `PATCH /api/v1/targets/{id} {"enabled":false}` if it should stop being cached.
> - **`--talosRetainMinors` behavior change:** bumping this flag after the Talos predefined target
>   already exists has **no effect** on that row — it only sets the initial `retainN` at creation
>   time. Adjust retention on an existing target with
>   `PATCH /api/v1/targets/{id} {"retainN":<n>}` instead.
>
> See [schema/API.md](schema/API.md#targets) for the full create-if-absent / PATCH contract.

### Retention windows for single-version-discovery OSes

Flatcar and Fedora CoreOS discovery only ever returns **one** version — the channel's current
build. For these OSes, `retainN` (via `--flatcarChannel`'s / `--coreOSChannel`'s predefined target,
or `PATCH retainN` on any flatcar/fcos target) bounds a **window over known versions** rather than
selecting from a larger discovered set: each reconcile tick, the newly-discovered version is added
to the set of versions still in-window, and the newest `retainN` of that combined set are kept. The
window therefore **grows one release at a time** as new versions are discovered — `retainN=3` takes
three upstream releases to reach three cached versions, not one. It does **not backfill** older
versions upstream no longer advertises; there is no way to retroactively populate history that was
never seen while the reconciler was running. Versions that age out of the window are archived, not
deleted (see [schema/STORAGE.md](schema/STORAGE.md)).

## Boot config precedence (P4)

Each boot-facing handler (`/ignition.json`, `/machineconfig`, `/preseed`) resolves a host's config
in a fixed 4-rung precedence, falling through to the next rung on any miss:

1. **Host `config_id`** — an explicit per-host binding (`POST /hosts/{mac}/approve` or `/bind`, see
   [schema/API.md](schema/API.md)). A bound config whose `kind` mismatches the host's OS family (or
   whose family can't be resolved) is treated as an operator error and does **not** fall through to
   a role default — it skips straight to rung 3/4.
2. **Role default by name** — the host's roles (`host_roles`), tried alphabetically; the first role
   with a non-null `default_config_id` whose kind matches the family wins.
3. **Legacy `hosts.ignition_file`** — a per-host Ignition template override (ignition family only;
   predates P4). Debian and Talos have no per-host file column, so this rung does not apply to them.
4. **Server-default file** — `--ignitionFile` (env `IGNITION_FILE`, default `config/ignition.yaml`)
   for the ignition family (Flatcar/CoreOS), `--talosConfigFile` (default
   `config/machineconfig.yaml`) for Talos, `--preseedFile` (default `config/preseed.cfg`) for
   Debian.

Rungs 1–2 are DB-resolved (`pkg/http/resolve.go`); rungs 3–4 are the pre-P4 file-based path,
unchanged. A host with no DB binding and no legacy override boots byte-identically to before P4 —
see [schema/STORAGE.md](schema/STORAGE.md).

A config's `kind` (`butane` \| `machineconfig` \| `preseed` — the dialect an operator authors) must
match the host's OS family via `configKindForFamily`; only the ignition family differs (`ignition`
family → `butane` kind, since Ignition is Butane's compiled wire format). See
[schema/DATABASE.md](schema/DATABASE.md#configs) for the enum and its relationship to family
`ConfigKind`.

## Talos schematics (P5)

A Talos **schematic** is a `configs` row of `kind='schematic'`: its `source` is Image Factory
customization YAML, and saving it **builds** against the Factory (`POST <talosFactoryURL>/schematics`)
rather than rendering a template. See [schema/API.md](schema/API.md#configs) for the full
create/edit/preview/rollback contract and [schema/DATABASE.md](schema/DATABASE.md#config_revisions)
for `derived_schematic_id`.

**Scope — extensions and overlays only.** The Image Factory's installer and initramfs images honor
only system extensions (and, for SBCs, an overlay) — `extraKernelArgs` and `meta` are silently
**ignored** on both of booty's Talos paths: the netboot `kernel`/`initramfs` served from
`/image/<schematic>/<version>/…`, and the installed system's `installer/<schematic>:<version>` image.
Exposing those two customization fields would be a footgun (a knob that visibly does nothing), so
they are documented as **not applicable** rather than supported. Kernel arguments still have a real
home in booty's flow: the iPXE boot cmdline for the netboot kernel, or `machine.install.extraKernelArgs`
in a `machineconfig`-kind config for the installed system.

**Air-gap.** There is no bare-ID import — a schematic ID with no Factory serving its bytes is
useless, since the ID only names content the Factory hosts (both the boot-asset paths and the
installer image are fetched *from* the Factory by ID). For an air-gapped deployment, point the
existing `--talosFactoryURL` flag at a private or self-hosted Image Factory: both schematic **builds**
(`POST`/`PUT /configs/{id}`) and the reconciler's runtime `/image` fetches follow the same flag, so a
private Factory serves both without further configuration.

**The vanilla schematic.** `--talosSchematic`'s default is the Image Factory's published "vanilla"
(no-extensions) schematic ID, pinned as the compile-time constant `config.DefaultTalosSchematic`. At
every startup, booty also seeds a config named `vanilla` (`kind='schematic'`, source
`customization: {}\n`) whose revision records that same constant as its `derived_schematic_id`
directly — **no Factory call is made** to do this, since schematics are content-addressed and the
vanilla ID is already known. This keeps startup safe even when the configured Factory is unreachable.
Seeding is create-if-absent by name: a config already named `vanilla` (from a prior run, or
operator-created) makes it a no-op.

**Pre-caching on save.** Saving a schematic config (create or edit) ensures a Talos discovery-mode
cache target for the built ID — windowed by `--talosRetainMinors`, the same as the predefined Talos
target — and triggers an async reconcile pass, so boot assets pre-fetch instead of waiting for a host
to request them. Schematic-derived targets are **not** pruned when their owning config is deleted:
`DELETE /api/v1/configs/{id}` is `403` until auth (P10) anyway, so this is over-caching only, not a
dangling reference.

## Talos cluster authoring (P6)

A Talos **cluster** (`clusters` row) is authored or imported state that generates, freezes, and
serves per-member machineconfigs — distinct from a single Talos host's schematic/config binding
(above). See [schema/API.md](schema/API.md#clusters-p6) for the full
create/import/add-member/export contract and [schema/DATABASE.md](schema/DATABASE.md#clusters) for
the table shapes.

### Secrets: fail-closed and fail-fast (`--secretsKey`)

A cluster's PKI/tokens/cluster-ID bundle, and each member's frozen machineconfig, are age-encrypted
at rest under `--secretsKey` — the path to an `age-keygen`-format identity file. This flag has two
failure postures:

- **Fail-closed when unset.** With no `--secretsKey`, `POST /clusters` (create), `POST /clusters/import`,
  `POST /clusters/{id}/members` (add or re-bind), and `POST /clusters/{id}/export` all refuse with
  `422` rather than minting, freezing, or exporting unencrypted secrets.
- **Fail-fast when set but broken.** A `--secretsKey` path that doesn't exist, isn't readable, or
  doesn't parse as an age identity refuses **startup** entirely — mirroring `--signaturePolicy`'s
  validation gate. booty never starts in a state where a configured-but-broken key would silently
  disable cluster operations.

### The retention pin (M3) and its limitation (D-F)

Every distinct `(schematic, talos_version)` pair referenced by a live cluster's members is pinned:
the cache reconciler's eviction sweep never evicts it, regardless of `--talosRetainMinors` or age. A
memberless cluster still pins its `talos_version` under the default schematic (`--talosSchematic`).

**Limitation — the pin does not back-fetch.** The pin only protects already-cached bytes from
**eviction**; it does not retroactively **fetch** a version that has already aged below the
discovery window at the moment its schematic's cache target is first created. If a cluster is
created (or a member added) pinning a version older than what's currently in-window for that
schematic, the version may never get cached automatically. **Workaround:** add a manual version row
via `POST /api/v1/targets/{id}/versions` (see [schema/API.md](schema/API.md#targets)) to force it
into the cache. Auto-creating that manual row at cluster-create / add-member time is a plausible
future hardening, deliberately **not implemented** in this slice.

### Deferred orchestration (D5)

booty's role stops at authoring and serving machineconfigs. Cluster bootstrap, upgrades, resets,
node drains, etcd membership, and kubeconfig retrieval are **operator-driven** — via `talosctl` /
`kubectl` against the endpoint booty pins, not through booty. Concretely:

- **Remove-member stops provisioning, not cluster membership.** `DELETE /clusters/{id}/members/{mac}`
  clears the host's binding and prunes its frozen revisions — it does **not** evict the node from
  Kubernetes or etcd. An operator must drain/remove the node through `kubectl`/`talosctl` separately.
- **Member `status` is derived, not probed.** A member's `status` (`booted` \| `pending`) reflects
  `host.Booted` — whether booty has seen the host boot — not cluster health, node readiness, or etcd
  membership. There is no health subsystem in this slice.

### The netboot pin (I1) and version-bump skew

A cluster member's TFTP boot resolves its Talos boot kernel/initramfs from `cluster.talos_version` —
the cluster's **pinned** version — rather than `NewestCached` (the non-member default), so the boot
kernel stays aligned with the installer image baked into the member's frozen machineconfig.

A `PUT /clusters/{id}` that bumps `talosVersion` immediately advances every member's **live** netboot
pin, and also pre-caches the new version's boot assets and triggers a reconcile — so a member
rebooting before re-bind can still netboot. But the member's frozen machineconfig, and therefore its
**install** image, does not change until it is **explicitly re-bound**
(`POST /clusters/{id}/members` naming the existing member). Between the version-bump `PUT` and the
re-bind, a member that reboots netboots the **new** pinned kernel but **installs the old** frozen
image — a **self-healing skew**: re-bind members promptly after a version bump to close the gap.

### Install disk (D-B)

Generated member configs default the install disk to **`/dev/sda`**, mirroring `talosctl gen config`'s
`--install-disk` default. Hardware with no `/dev/sda` (NVMe-only nodes, etc.) **must** override it via
a `machine.install` strategic-merge patch — set at the cluster, role, or per-host layer (see
[schema/API.md](schema/API.md#clusters-p6) for how patch layers compose). Validate-before-freeze
(generation's admission gate) catches a config with **no** install disk at all (`422`, refuses to
freeze) but **cannot** catch a wrong-but-present disk — an incorrect override installs to the wrong
device silently.

## Debian structured authoring (`debianconfig`)

`debianconfig` is a curated YAML config kind that booty translates into a flat
Debian d-i preseed — author structure, serve preseed, exactly like authoring
butane and serving ignition. Raw `preseed` configs remain fully supported (and
the `--preseedFile` server default is always raw preseed); `debianconfig` is
the recommended structured option, opt-in per config.

**Emission contract:** unset fields emit **no** preseed line (d-i defaults or
prompts apply). Ordering: curated fields → one composed `late_command`
(generated ssh-keys block first, then yours) → `raw_preseed` **last**, so a
duplicate debconf answer in `raw_preseed` always overrides a curated line.
Template variables (`{{ .Hostname }}`, `{{ .ServerIP }}`, …) substitute in
every field — including `raw_preseed` and `expert_recipe` — before translation.

Full schema (every field optional unless noted):

```yaml
hostname: "{{ .Hostname }}"
domain: cluster.local
locale: en_US.UTF-8
timezone: Etc/UTC
keyboard: us
mirror:                       # override-only; suite/codename comes from the
  hostname: deb.debian.org    # Debian target's channel
  directory: /debian
  proxy: ""
network:
  interface: auto             # "auto" | a named iface (e.g. eth0)
  static:                     # omit -> DHCP
    address: 10.0.0.10
    netmask: 255.255.255.0
    gateway: 10.0.0.1
    nameservers: [10.0.0.1]
accounts:                     # password HASHES only ($6$..., pre-computed crypt);
  root_password_hash: "$6$…"  # omit -> root login disabled (safe default)
  user:                       # username required if present
    fullname: Ops
    username: ops
    password_hash: "$6$…"     # optional — see below
    ssh_authorized_keys: ["ssh-ed25519 …"]   # emitted via late_command (no
                                             # native preseed directive exists)
    sudo: nopasswd             # optional — see below
packages: [openssh-server, qemu-guest-agent]
disk:
  devices: [/dev/sda, /dev/sdb]
  raid: mirror                # none (default) | mirror (UEFI mdadm RAID1 +
                              # per-disk ESP + removable-media fallback on each)
  layout: lvm                 # plain (default) | lvm
  filesystem: ext4            # ext4 (default) | xfs
  boot_degraded: true         # mirror only; default true (a node still boots
                              # on a surviving disk)
  # expert_recipe: |          # raw partman recipe; REPLACES the curated disk
  #   ...                     # recipe entirely (devices/boot_degraded still apply)
  #                           # booty always emits `partman-auto/method string regular`
  #                           # alongside expert_recipe; a recipe needing RAID/LVM must
  #                           # set its own `partman-auto/method` via raw_preseed
  #                           # (appended LAST, so it overrides the curated line).
late_command: |
  in-target systemctl enable ssh
raw_preseed: |
  d-i debian-installer/allow_unauthenticated boolean true
```

- `accounts.user.password_hash` is **optional**. Omit it for a key-only service
  account: booty emits a locked crypt sentinel (`*`) so password login is
  disabled, and **requires** at least one `ssh_authorized_keys` entry (a
  password-less, key-less account would be unreachable → rejected).
- `accounts.user.sudo` (optional): `nopasswd` grants passwordless sudo (adds the
  `sudo` package, the user to the `sudo` group, and a `440 /etc/sudoers.d/<user>`
  `NOPASSWD:ALL` drop-in); `password` grants sudo that prompts for the user's
  password (requires a `password_hash`); `false`/omitted grants no sudo. `true`
  is a friendly alias for `nopasswd`.
- `packages`: `openssh-server` is auto-added when the user declares
  `ssh_authorized_keys`, and `sudo` when `sudo` is set — both deduped against
  your list. Your own `packages` are emitted verbatim (order preserved).
- `late_command` accepts either a block scalar or a YAML list of commands; both
  are flattened to one `;`-joined d-i `late_command` line.

Worked example — key-only service account (no password, passwordless sudo):

```yaml
accounts:
  user:
    username: svc
    ssh_authorized_keys: ["ssh-ed25519 AAAA… ops@laptop"]
    sudo: nopasswd
late_command:
  - in-target systemctl enable qemu-guest-agent
```

**Disk matrix** — every curated combination is a native partman primitive
(target nodes are **UEFI**; the `raid: mirror` recipes are UEFI-native):

| `layout` × `filesystem` | `raid: none` | `raid: mirror` (UEFI) |
|---|---|---|
| plain × ext4 | guided atomic, ext4 | ESP/disk + md `/boot` (ext4) + md `/` (ext4) |
| plain × xfs | guided atomic, xfs | ESP/disk + md `/boot` (ext4) + md `/` (xfs) |
| lvm × ext4 | guided LVM, ext4 | ESP/disk + md `/boot` + LVM-on-md `/`, root LV ext4 |
| lvm × xfs | guided LVM, xfs | ESP/disk + md `/boot` + LVM-on-md `/`, root LV xfs |

`raid: mirror` requires ≥ 2 `devices`; `/boot` stays ext4 on md in all mirror
combos; the curated mirror recipes carry no swap (add one via `expert_recipe`
if needed). Whenever a `disk:` block is present booty emits
`partman-basicfilesystems/no_swap boolean false`, so a swapless recipe installs
unattended instead of stopping at d-i's "no swap space — continue?" prompt (it's
a harmless no-op when a recipe does have swap).

> **UEFI mirror — how redundancy works.** The EFI System Partition **cannot be
> mirrored** (firmware writes to it directly before mdadm assembles; d-i refuses
> `/boot/efi` on md). So each member disk gets its **own** ESP (partition 1,
> `method{ efi }`, not raid'd), and only `/boot` + root are on md. To make a
> surviving disk bootable, booty:
> 1. enables `grub-installer/force-efi-extra-removable`, so grub also installs
>    the **removable-media** bootloader (`\EFI\BOOT\BOOT<ARCH>.EFI`) to the ESP —
>    firmware boots this with **no NVRAM boot entry**, and it is automatically the
>    right architecture;
> 2. emits an **ESP-sync `late_command`** that clones the primary ESP (including
>    that removable bootloader) onto every other member's ESP after install;
> 3. marks `/boot/efi` **`nofail`** in `/etc/fstab` (fstab can only reference one
>    ESP by UUID) so a dead primary ESP doesn't stall boot in emergency mode.
>
> Result: if the primary disk dies, the node still UEFI-boots off a surviving
> member via its removable-media fallback and the degraded md array (validated in
> the netboot lab: lone-disk boot → login). booty does **not** use `efibootmgr` —
> it can't reach EFI variables inside the d-i chroot at `late_command` time, and
> the removable-media path needs no NVRAM entry. For a **BIOS/legacy** mirror
> instead, use `disk.expert_recipe` (curated BIOS mirror is a planned follow-up).
> Single-disk (`raid: none`) installs let d-i's guided recipe create the ESP itself.

Your `late_command` is **flattened to a single `;`-joined debconf line**, so
each line must be **independently sequenceable** — no multi-line shell
constructs (`for`/`if`/`while`/heredocs) across lines. Put such logic in a
script the `late_command` invokes, or keep it on one line.

**Non-goals (upstream limitations, not booty's):** root-on-ZFS (the Debian
installer does not support ZFS at all), btrfs RAID1 root, and RAID 0/5/6/10
are not curated — reachable only via `expert_recipe`/`late_command` at your
own risk. Coherence checks fire only when a `disk:` block is present; a spec
with no `disk:` emits no partman lines.

**Worked example — redundant-boot-disk node (mirror + LVM):**

```yaml
hostname: "{{ .Hostname }}"
locale: en_US.UTF-8
timezone: Etc/UTC
accounts:
  user:
    username: ops
    password_hash: "$6$…"
    ssh_authorized_keys: ["ssh-ed25519 AAAA…"]
packages: [openssh-server]
disk:
  devices: [/dev/sda, /dev/sdb]
  raid: mirror
  layout: lvm
  filesystem: ext4
```

Create it with `POST /api/v1/configs {"name":"deb-mirror","kind":"debianconfig",
"source":"…"}`, preview with `POST /api/v1/configs/{id}/preview`, bind it to a
Debian host, and the host's `GET /preseed` serves the translated preseed.

## Signature verification (`--signaturePolicy`)

booty verifies the integrity of the boot artifacts it downloads before serving them. The mechanism
is **per-OS** (Fedora CoreOS: SHA-256 from the streams JSON; Flatcar: detached GPG `.sig` against an
embedded keyring; Talos/Debian: no mechanism yet), and the **policy** is one global flag:

| Value | Behavior |
|-------|----------|
| `strict` | Any verifiable artifact that **fails** verification does not land — the whole version is refused and the prior cached version keeps serving. Refuses **both** failure classes (below). |
| `warn` *(default)* | A GPG **signature mismatch** (forgery signal) is refused, exactly as under `strict`. A **checksum / non-forgery** failure lands anyway, logs a `WARN`, and records `verified=0`. |
| `off` | No verification runs; artifacts land unchecked and `verified` stays `NULL`. |

Verification is **admission-time only** and applies to the newest discovered version; a passing
version records `verified=1`, and a version with no verification mechanism records `verified=NULL`
(see [schema/DATABASE.md](schema/DATABASE.md)). The verdict is surfaced in the Cache view and via
`POST /api/v1/cache/{id}/reverify`.

**Scope — `strict` does not refuse "unverifiable" OSes.** `strict` means *verifiable artifacts that
fail verification do not land*. It does **not** refuse OSes or versions that have **no** verification
mechanism — Talos, Debian, and FCOS pattern-fallback pins (a manually pinned older FCOS version with
no per-artifact `sha256`) — which land with `verified=NULL` under **every** policy.

### Failure classes — what the default `warn` does (D15)

`warn` is **not** "provenance is advisory." A verification failure is classified, and the two classes
are treated differently:

- **Signature mismatch (forgery signal)** — a GPG `.sig` that does not validate against the embedded
  key. This is a tamper indicator: it is **refused even under `warn`** (the version does not land and
  does not boot), identically to `strict`.
- **Corruption / non-forgery failure** — a SHA-256 mismatch, a short/unparseable sidecar, or an
  unknown/expired signing key. Under `warn` these **land** (logged, `verified=0`) — the availability
  trade-off `warn` exists for; under `strict` they are **refused**.

So `strict` refuses every failure class; `warn` refuses only forgeries; `off` verifies nothing.

**`warn` is advisory against a capable network attacker — use `strict` in production.** Because a
Flatcar artifact and its `.sig` are fetched from the **same channel**, an attacker who can tamper the
artifact can also substitute a self-signed `.sig`. That substitution surfaces as an *unknown-issuer*
verdict, which is the **corruption** class — so it **lands under `warn`** (`verified=0`, logged) even
though it is really an attack. `warn` only reliably stops the naive/accidental case (corruption, or a
forged `.sig` still signed such that it fails as a signature mismatch). **`strict` is the only policy
that closes this hole** — recommend `strict` for production.

**Policy is non-retroactive.** Tightening `warn` → `strict` does **not** auto-evict a version already
admitted under `warn`: the reconciler's idempotency skip guard leaves settled (`cached=1`, files
present) versions in place and does not re-verify them. The recourse is
`POST /api/v1/cache/{id}/reverify`, which re-checks the version under the current policy and re-records
`verified=0` so the operator can **see** it; removal is then a manual decision (`DELETE` is `403`
until auth lands in P10).

### Flatcar signing-key rotation runbook

The Flatcar image-signing public key is **embedded at compile time**
(`//go:embed keys/flatcar.asc`) — there is no hot reload. Flatcar signs releases with
**rotating subkeys**; the currently-active signing subkey
`52F145DFD00BBDCD928CBB5A32DA80F91EF52974` **expires 2027-03-08**. Before that date (or whenever
Flatcar rotates early), the operator must ship a **new booty release** that re-vendors the key:

1. Re-fetch the published key into `pkg/ostype/keys/flatcar.asc`
   (from <https://www.flatcar.org/security/image-signing-key/Flatcar_Image_Signing_Key.asc>).
2. Update the expiry-horizon assertion in `pkg/ostype/flatcar_key_test.go` (which pins the active
   subkey fingerprint and asserts it does not expire before the horizon date).
3. Build and redeploy.

Until that release ships, an expired/rotated key makes Flatcar verification fail as an
**unknown/expired signing key** (the corruption class): under `strict` this **halts all new Flatcar
caching** (a provisioning outage fixable only by a code release), while under `warn` the version
still **lands** with `verified=0`.

## Environment variables

In addition to the auto-bound flag env vars, a few settings are read directly from the environment:

| Env var | Default | Purpose |
|---------|---------|---------|
| `IGNITION_FILE` | `config/ignition.yaml` | Butane/Ignition template path, relative to `--dataDir`. |
| `HARDWARE_MAP` | `hardware.json` | Host-database filename, relative to `--dataDir`. |
| `DATABASE_PATH` | `<dataDir>/booty.db` | SQLite database path (control-plane + host state). |

## Network ports

| Port | Protocol | When | Purpose |
|------|----------|------|---------|
| `8080` (`--httpPort`) | TCP | always | HTTP: boot configs, artifacts, API, web UI. |
| `69` | UDP | always | TFTP: iPXE binaries and the `booty.ipxe` chain script. |
| `67` | UDP | `--proxyDHCPEnabled` | proxyDHCP pass-1 (firmware PXE request). |
| `4011` | UDP | `--proxyDHCPEnabled` | proxyDHCP pass-2 (iPXE re-request). |

proxyDHCP binds privileged ports — run with `CAP_NET_BIND_SERVICE` (e.g. Docker
`--cap-add=NET_BIND_SERVICE`) or as root. proxyDHCP is best-effort: if it fails to start, booty
logs the error and continues serving TFTP/HTTP.

## Notes

- booty is **not** a DHCP server. Either point your existing DHCP server's `next-server` / bootfile
  at booty, or enable `--proxyDHCPEnabled` to answer PXE requests alongside it (it never hands out
  IP leases).
- All config templates and the host database live **under `--dataDir`**; see
  [schema/STORAGE.md](schema/STORAGE.md).

> **As of P1c:** the `/api/v1` management plane is now active. The following HTTP paths are served
> on the existing `--httpPort` alongside the boot-facing endpoints, with no new flags:
>
> | Path | Purpose |
> |------|---------|
> | `/api/v1` | Versioned management API (targets, catalog, hosts). |
> | `/api/v1/docs` | Scalar interactive API documentation UI. |
> | `/api/v1/openapi.json` | OpenAPI 3.1 spec (machine-readable). |
>
> Per-target and per-host settings are managed through the API and persisted in `booty.db`. See
> [schema/API.md](schema/API.md) for the full endpoint reference.
