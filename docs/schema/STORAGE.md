# On-disk storage layout

Everything booty persists lives under **`--dataDir`** (default `/data`). Mount this as a volume to
keep cache and registration across restarts.

```
<dataDir>/
├── hardware.json                 # host database (see DATABASE.md)
├── version.txt                   # Flatcar: current cached version marker
├── <channel>.json                # CoreOS: streams metadata (e.g. stable.json)
├── config/
│   ├── ignition.yaml             # Butane template for Flatcar/CoreOS (IGNITION_FILE)
│   └── machineconfig.yaml        # Talos machine-config template (--talosConfigFile)
├── undionly.kpxe                 # proxyDHCP pass-1 BIOS iPXE binary (if proxyDHCP enabled)
├── ipxe.efi                      # proxyDHCP pass-1 UEFI iPXE binary
├── ipxe-arm64.efi               # proxyDHCP pass-1 ARM64 iPXE binary
└── cache/                        # downloaded boot artifacts (see below)
```

## Artifact cache layout

Cached kernel/initramfs (and rootfs, for CoreOS) are stored under a uniform, segment-based path:

```
<dataDir>/cache/<os>/<schematic-or-dash>/<arch>/<version>/
```

The `<schematic-or-dash>` segment is the Talos Image Factory schematic ID for Talos, and a literal
`-` (no schematic) for Flatcar and CoreOS. The **same segments** drive both the on-disk path and the
HTTP base URL clients fetch from, so disk and URL cannot drift.

**Examples:**

```
cache/flatcar/-/amd64/3598.5.0/
    flatcar_production_pxe.vmlinuz
    flatcar_production_pxe_image.cpio.gz

cache/coreos/-/x86_64/39.20231101.3.0/
    fedora-coreos-<version>-live-kernel-x86_64
    fedora-coreos-<version>-live-rootfs.x86_64.img
    fedora-coreos-<version>-live-initramfs.x86_64.img

cache/talos/376567988ad3…b4ba/amd64/v1.10.1/
    kernel-<arch>
    initramfs-<arch>.xz
```

## How the cache is populated and pruned

- A background scheduler (`--updateSchedule`, default every 5 minutes) checks each OS upstream and
  downloads a new version's artifacts only when the cached version differs.
- **Flatcar / CoreOS:** the previous version's directory is removed when a newer one is cached
  (single current version per channel).
- **Talos:** the newest `--talosRetainMinors` minor lines are kept (default 3), per schematic and
  arch; this is a floor, not an aggressive prune. The boot path resolves a host to the newest cached
  version for its schematic at request time.

> **Forward-looking:** the v1 management plane makes caching **target-driven** (operators declare
> targets; a reconciler caches them eagerly — never on boot) and adds cache states, retention, and
> signature verification. The `cache/<os>/<schematic-or-dash>/<arch>/<version>/` layout above is the
> stable foundation those slices build on.
