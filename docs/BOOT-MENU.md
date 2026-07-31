# Interactive boot menu (`boot_mode='menu'`)

An approved host is normally pinned to one OS: it boots the newest cached version of its
assigned OS every time (`boot_mode='assigned'`). **Menu mode** replaces that with an
interactive iPXE menu — the machine is offered every image booty currently has cached, and
whoever is at the console picks one.

Use it for a machine you re-image often, for trying a specific Talos schematic or an older
version without editing the host's assignment, or as a rescue path when you want to boot
something other than what the host is assigned.

The selection is **per boot**. Nothing is written back: the menu is shown again on the next
network boot, and the host's assigned OS is left untouched.

---

## Putting a host into menu mode

**Web UI** — Hosts view, the **Boot menu** button on the host's row.

**API:**

```bash
curl -X POST http://<booty>:8080/api/v1/hosts/aa:bb:cc:dd:ee:ff/menu
```

This approves the host if it isn't approved yet, then sets `boot_mode='menu'`. It does *not*
touch `approved_os` / the assignment, so the host's previous assignment is still there when
you switch back. `404` if the MAC is unknown.

**To leave menu mode**, approve the host again:

```bash
curl -X POST http://<booty>:8080/api/v1/hosts/aa:bb:cc:dd:ee:ff/approve
```

Approve sets `boot_mode='assigned'` as a side effect of re-assigning the host to its
self-reported OS. Note the edge case: a host that never reported an OS (`os` empty) is not
re-assigned by approve, so it stays in menu mode — set the OS on the host first if you need
it pinned.

---

## What the menu shows

The menu is generated fresh on **every** `booty.ipxe` fetch, and it is built from **what is
actually on disk in the cache** — not from the declared target list. A target that is
declared but hasn't finished downloading yet does not appear. This is deliberate: the menu
can never offer a choice that 404s at boot.

One entry per cached `(os, schematic, arch, version)` tuple, labelled like:

```
Talos v1.10.5 (amd64) [376567988]
Flatcar 4230.2.0 (amd64)
Fedora CoreOS 42.20250705.3.0 (x86_64)
Debian 13.1 (amd64)
```

The bracketed suffix is the first 8 characters of the schematic ID, shown only for targets
that have one (Talos), so several schematics of the same version stay distinguishable.

**Two tiers.** Versions inside the target's retention window appear in the main menu.
Versions that have rotated out are **archived, not deleted** — they move to a nested
**`Archived OSes...`** submenu (with a `Back` item) and remain bootable from there.

**Always present:** a `Wait / retry` item, which is also the default. The menu waits 5
minutes; on timeout it re-chains `booty.ipxe`, which re-renders the menu with whatever has
since finished caching. An empty cache therefore still produces a valid menu that simply
loops until something is available.

### Tools & rescue

Alongside the OS entries, booty can also cache **tools** — netboot.xyz-sourced rescue and
diagnostic images that take no per-host config: `systemrescue` (SystemRescue, ~1 GB),
`uefi-shell` (UEFI Shell, EFI-only), and `memtest86plus` (Memtest86+). Whenever at least one
tool target is cached, the main menu grows a `Tools & rescue...` item — a nested submenu
alongside (and shaped like) `Archived OSes...`, with its own `Back` item, listing every cached
tool.

**A tool's menu label shows its release tag, not a prettier version.** netboot.xyz release
tags share no common grammar (`13.01-d20a63ac`, `edk2-stable202002-a6917535`), so booty uses
the tag itself as the label's version, e.g.:

```
Memtest86+ 8.00-32a14678 (amd64)
```

An earlier revision of this design intended a prettier upstream version string; that was
withdrawn — `menuItemText` reads the version straight off disk, and resolving a nicer label
would mean fetching the upstream manifest from the boot path itself, which booty does not do.

Booting UEFI Shell on a BIOS-mode client doesn't fail silently: the script detects the
firmware, prints "UEFI Shell requires an EFI client; this machine booted in BIOS mode.", and
re-chains back into `booty.ipxe` after a short pause rather than hanging.

Tool artifacts are never checksum- or signature-verified — netboot.xyz publishes neither — so
their `verified` state stays unset (NULL) in the Cache view regardless of
`--signaturePolicy`. Tools are opt-in: none are cached by default, see
[Add a target](#1-add-a-target-catalogyaml) below.

---

## Adding more OSes and versions to the menu

Because the menu lists what's cached, **you grow the menu by growing the pre-cache** — which
is configuration, not code. Three levers, in increasing specificity:

### 1. Add a target — `catalog.yaml`

Each catalog entry becomes one cache target, and every version it caches becomes a menu
item. Copy the shipped example and edit it:

```bash
cp docs/examples/catalog.yaml <dataDir>/catalog.yaml
```

```yaml
schemaVersion: 1
catalog:
  # Fedora CoreOS is NOT in the built-in default — add it to see it in the menu
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec:
      channel: stable

  # A second Talos schematic: shows up as its own menu entries, tagged [<id 1-8>]
  - os: talos
    arch: amd64
    retain: 3
    spec:
      schematic: "<your-factory-schematic-id>"

  # Flatcar beta alongside stable
  - os: flatcar
    arch: amd64
    retain: 1
    spec:
      channel: beta

  # Tools & rescue: uncomment any of these to add them to the "Tools & rescue..."
  # submenu. They take no spec and retain must stay 1 — see CATALOG.md.
  # - os: systemrescue      # ~1 GB (airootfs.sfs)
  #   arch: amd64
  #   retain: 1
  # - os: uefi-shell        # a few MB; EFI clients only
  #   arch: amd64
  #   retain: 1
  # - os: memtest86plus     # a few MB
  #   arch: amd64
  #   retain: 1
```

booty reconciles to this file on each cache tick — new entries are created and downloaded,
removed entries are **disabled, never deleted**. A malformed catalog **aborts startup**
rather than silently mass-downloading or mass-disabling; that's intentional.

The families booty can cache today are `ignition` (Flatcar / Fedora CoreOS), `talos`,
`debian`, and `tool` (`systemrescue` / `uefi-shell` / `memtest86plus`). Adding a family beyond
those is a code change, not a catalog edit. See [schema/CATALOG.md](schema/CATALOG.md) for the
full schema, the per-OS `arch` tokens and `spec` keys, and the Debian `sourceMode: netinst|dvd`
options.

### 2. Offer more versions of a target — `retain`

`retain: N` keeps the newest N versions cached (for Talos, the newest N *minor lines*).
Raising it is the direct way to put a version *choice* in the menu rather than a single
newest build:

```yaml
  - os: flatcar
    arch: amd64
    retain: 3        # three Flatcar versions in the menu instead of one
    spec:
      channel: stable
```

Versions that later rotate out of the window aren't removed — they drop into the
`Archived OSes...` submenu, so raising and later lowering `retain` doesn't lose you anything
already on disk. Disk cost scales with N; a Debian `dvd` target is ~44 GB per set.

### 3. Pin one exact version — manual version pin

To keep a *specific* version available indefinitely, regardless of retention, pin it on the
target:

```bash
curl -X POST http://<booty>:8080/api/v1/targets/<id>/versions \
  -H 'Content-Type: application/json' \
  -d '{"version":"1.9.5"}'
```

Manually pinned versions are **never pruned** by the retention pass, and they appear in the
main menu like any other cached version. This is the right tool for "keep 1.9.5 bootable
forever" without inflating `retain` for everything.

You can also pin a cached version against eviction from the **Cache** view in the web UI.

---

## Boot mechanics (contract)

Useful when debugging with a TFTP client or reading logs:

| Step | What happens |
|------|--------------|
| 1 | Host fetches `booty.ipxe` over TFTP. booty identifies it by MAC (via ARP on the requesting IP). |
| 2 | Approved + `boot_mode='menu'` → booty renders the menu script and serves it in place of an OS script. |
| 3 | The chosen item chains `tftp://<serverIP>/menu/<os>/<schematic>/<arch>/<version>/boot.ipxe`. |
| 4 | booty validates that synthetic path against the cache and renders that exact tuple's OS boot script. |
| 5 | The machine boots that image, fetching its config (`/ignition.json`, `/machineconfig`, `/preseed`) as usual. |

The `menu/.../boot.ipxe` path is **re-gated**: booty re-checks that the requesting host is
still approved and still in menu mode, so a host in another state can't boot a tuple by
requesting the path directly. Malformed, unknown, or uncached tuples fall back to the
holding loop — arbitrary files are never served through this path.

---

## Troubleshooting

**The menu is empty (only `Wait / retry`).** Nothing is cached yet. Check the Cache view or
`GET /api/v1/cache`; a freshly configured target takes a discovery tick plus a download
before it appears.

**The host loops on "not yet approved" instead of showing a menu.** booty didn't identify
the host, or it isn't approved. Confirm the MAC is registered and approved, and that the
machine is on the **same layer-2 segment** as booty — host identification uses ARP, which
doesn't cross a router (see [Networking](../README.md#deployment-notes)).

**An expected image isn't listed.** It's declared but not yet on disk, or it rotated out of
the retention window — check the `Archived OSes...` submenu, and see `retain` above.

**A selection drops back to the holding loop.** The tuple failed validation (usually the
version was evicted between the menu render and the selection). Let it re-chain; the
regenerated menu will reflect what's actually there.

---

## Limitations

- **Ephemeral by design** — no "remember my choice"; the menu returns every boot. To make a
  choice stick, assign the host to that OS instead.
- **No filtering or theming** — every cached tuple is offered to every menu-mode host; there
  is no per-host allowlist, no default-OS auto-boot, and no branding.
- **TFTP-only** — the menu and its selection round-trip are served over TFTP; there is no
  HTTP equivalent.
- **Same layer-2 segment required** — like all of booty's boot path, menu mode depends on
  ARP-based host identification and cannot serve machines on another VLAN.
