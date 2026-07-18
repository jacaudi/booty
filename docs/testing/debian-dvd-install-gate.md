# Debian DVD offline-install netboot-lab gate

> **MANUAL GATE — operator-run, not CI.** This is the runbook for the two
> validations that could not be run during implementation because both need a
> real Debian DVD ISO, which was unavailable in the development session.
>
> **Status as of this branch: UNRUN / DEFERRED.** Neither gate below has been
> executed. The diskfs-based ISO9660 extraction adoption in
> `pkg/cache/debiandvd.go` (`extractISO`/`extractAndMergeISO`, using
> `github.com/diskfs/go-diskfs`) is **provisional** pending Gate 1. Do not
> treat DVD mode as production-ready until both gates have been run and
> recorded per this document.
>
> Run this **before** trusting DVD mode (`sourceMode: dvd`) against real
> traffic. Byte-level unit/golden tests in this codebase validate that the
> code emits the *bytes* it intends to — they cannot validate that a real
> Debian installer, given those bytes, produces a *working install*. That gap
> is exactly what debianconfig's F1/F2/F3 bugs slipped through (curated
> preseed byte-goldens were green while the install itself broke on real
> hardware/QEMU). This runbook is the check that closes that gap for Debian
> DVD mode.

## Contents

1. [Gate 1 — diskfs Rock-Ridge spike](#gate-1--diskfs-rock-ridge-spike) (library validation; gates whether `diskfs` is even the right dependency)
2. [Gate 2 — offline DVD-mode install](#gate-2--offline-dvd-mode-install) (end-to-end install correctness; design §10)
3. [Recording results](#recording-results)

---

## Gate 1 — diskfs Rock-Ridge spike

**What it checks:** `github.com/diskfs/go-diskfs` is used (`pkg/cache/debiandvd.go`,
`extractISO`) to read a Debian DVD's ISO9660 filesystem. Debian DVDs rely on
**Rock Ridge extensions** for deep, case-sensitive paths like
`pool/main/l/linux/linux-image-6.1.0-…-amd64_…_amd64.deb` — without Rock Ridge
support those come back as truncated 8.3 ISO9660-level-1 names
(`LINUX123.DEB;1`), which would silently corrupt the merged `pool/` tree. This
was never validated against a real ISO during implementation
(`pkg/cache/isospike_integration_test.go`, task-6-brief.md Step 0) — it is
compile-checked only.

### Step 1 — obtain a real Debian DVD-1 ISO

Download DVD-1 of any current point release (Rock Ridge structure is stable
across releases, so 12.x or 13.x both work) from a Debian mirror, e.g.:

```bash
curl -fLO https://cdimage.debian.org/debian-cd/current/amd64/iso-dvd/debian-13.0.0-amd64-DVD-1.iso
```

(Substitute the current point release; check
`https://cdimage.debian.org/debian-cd/current/amd64/iso-dvd/` for the exact
filename.) A partial/torrent download is fine as long as `pool/main/l/linux/`
is intact — the spike only reads that one directory.

### Step 2 — run the spike

```bash
BOOTY_TEST_DVD_ISO=/path/to/debian-13.0.0-amd64-DVD-1.iso \
  go test -tags isospike ./pkg/cache/ -run ISOSpike -v
```

This test is excluded from `go build ./...` / `go test ./...` by the
`isospike` build tag (see the file's doc comment,
`pkg/cache/isospike_integration_test.go:1-25`) — it will not run, and does not
need to run, in normal CI.

### PASS criterion

The test reads `pool/main/l/linux/` from the real ISO and asserts:

- the directory listing is **non-empty**,
- at least one `.deb` filename in it is a **long, versioned Debian package
  name** (not an 8.3 short name — no `;` version suffix, longer than 12
  chars),
- reading that file's bytes returns a **non-empty** payload.

`go test` exits 0 and logs `read pool/main/l/linux/<name>: long,
case-preserved Rock Ridge name OK (<N> bytes)`.

Separately (not asserted by the test, but worth a manual spot-check while you
have the ISO cached): confirm a symlinked `dists/` entry resolves, e.g.
`dists/stable -> bookworm` should resolve to the same content as
`dists/bookworm` when read through the mounted filesystem — Debian DVDs use
that symlink for the "stable" alias, and if `diskfs` doesn't follow ISO9660
Rock-Ridge symlinks (`SL` records), the merge step's `dists/` copy would
silently miss it.

### FAIL — fallback order

If Rock Ridge deep paths come back truncated/missing (`t.Fatalf` fires):

1. **Swap to `github.com/kdomanski/iso9660`'s read path.** It is a second
   pure-Go ISO9660 reader; re-point `isoExtractor`/`extractISO`
   (`pkg/cache/debiandvd.go:74,81`) at it and re-run this spike (adapted to
   the new library's API) before touching anything else in
   `extractAndMergeISO`.
2. **Last resort: shell out to `xorriso -osirrox on` or `bsdtar -xf`.** This
   adds a runtime binary dependency and **breaks the distroless/pure-Go
   posture** (design §6.1 "operationally boring: no container privileges...").
   Do **not** adopt this without a design amendment and explicit user
   sign-off — it changes the deployment/image story, not just an internal
   dependency.

---

## Gate 2 — offline DVD-mode install

**What it checks:** design §10's load-bearing claim — a real Debian installer,
booted from booty's cached-and-extracted DVD tree with **zero external
network**, completes a disc-1-resident minimal/server install using booty's
local apt mirror, and reboots into a working system. This exercises the full
chain the unit/byte tests cannot: the reconciler's download→verify→extract
pipeline against a real cdimage.debian.org source, the merged `pool/`+`dists/`
tree's actual usability by `apt`, and the preseed mirror directives
(`pkg/http/preseed.go:appendDVDMirror`) against a real `d-i` run.

### Environment

- QEMU on an Apple-Silicon Mac (`brew install qemu`).
- The DVD path is **amd64-only** (design/promote-dvd both reject non-amd64;
  Debian ships no arm64 DVD ISOs) — the guest must be a **TCG-emulated
  (`-accel tcg`) x86_64 guest**, not HVF-accelerated aarch64.

### Step 1 — cache a real DVD set

Run booty locally, pointed at a scratch data dir, with the non-Debian
channels disabled (so startup doesn't also try to discover/cache Flatcar and
CoreOS) and signature verification off (this environment has no keyring
provisioning story yet for a throwaway lab run):

```bash
go run . \
  --dataDir /tmp/booty-debian-dvd-lab \
  --httpPort 8088 \
  --serverIP 10.0.2.2 \
  --serverHttpPort 8088 \
  --flatcarChannel disabled-lab \
  --coreOSChannel disabled-lab \
  --signaturePolicy off
```

`--serverIP 10.0.2.2` is deliberate, not a placeholder: it is QEMU's default
user-mode-networking gateway address for "the host machine" as seen from
inside the guest (§"Boot offline" below). Every URL booty embeds in boot
scripts and preseeds (`urlHost`/`vars.ServerIP`,
`pkg/tftp/tftp.go:136-140`, `pkg/http/preseed.go:123`) is built from
`--serverIP`+`--serverHttpPort`, so this is what makes the guest able to
resolve them at all.

Create a `debian/amd64 {channel:12}` target (starts in the DB default
`sourceMode="netinst"`, migration `0008_debian_source_mode.sql`):

```bash
curl -sS -X POST http://localhost:8088/api/v1/targets \
  -H 'Content-Type: application/json' \
  -d '{"os":"debian","arch":"amd64","mode":"discovery","params":{"channel":"12"}}'
```

Note the returned `"id"`, then promote it to DVD mode at `dvdCount=1` (disc-1
only — the partial-mirror case design §6.3 calls out as the one this gate
exercises):

```bash
curl -sS -X POST http://localhost:8088/api/v1/targets/<id>/promote-dvd \
  -H 'Content-Type: application/json' \
  -d '{"dvdCount":1}'
```

This only records intent (`desired_mode=dvd`) and enqueues a reconcile
(`pkg/http/api_targets.go:216-254`); the actual download+GPG-verify+extract
runs asynchronously on the next reconcile tick (`--cacheInterval`, default 5m
— or restart booty / wait it out). Poll until it lands:

```bash
watch -n 5 curl -sS http://localhost:8088/api/v1/targets/<id>
```

#### Verify the cached result

The version directory is
`/tmp/booty-debian-dvd-lab/cache/debian/12/amd64/<version>/`
(`pkg/cache/layout.go:cacheDir` — same segments the URL layout uses). Confirm
it holds:

```bash
ls -la /tmp/booty-debian-dvd-lab/cache/debian/12/amd64/<version>/
```

- `debian-<version>-amd64-DVD-1.iso` — the **verbatim** ISO (retained, not
  deleted after extraction — design §6.1).
- `SHA256SUMS`, `SHA256SUMS.sign` — the verified checksum manifest.
- `dists/`, `pool/`, `install.amd64/` — the merged/extracted tree
  (`extractAndMergeISO`, `pkg/cache/debiandvd.go:164-212`).
- `.booty-dvd-complete` — the extraction-idempotency sentinel
  (`dvdSentinelName`, `pkg/cache/debiandvd.go:60`); its presence is what makes
  a re-run skip re-extraction.

Confirm the version is **pinned** (excluded from eviction — design §9/§11.2):

```bash
sqlite3 /tmp/booty-debian-dvd-lab/booty.db \
  "SELECT tv.version, ce.pinned FROM target_versions tv \
   JOIN cache_entries ce ON ce.target_version_id = tv.id \
   WHERE tv.target_id = <id>;"
```

Expect `pinned = 1`. (Equivalently, the Cache view in the web UI shows this
version with a pinned indicator.) Also confirm the target's `GET
/api/v1/targets/<id>` response now shows `"sourceMode":"dvd"` and
`"desiredMode":""` — the flip only happens **after** the tree lands
(`ensureDebianDVD`, `pkg/cache/debiandvd.go:301-368`).

### Step 2 — boot offline

The goal is: a QEMU amd64 guest boots the installer kernel/initrd from
booty's cached `install.amd64/` tree, is served a preseed whose `d-i
mirror/http/*` directives point at booty's **local** extracted tree (not the
real internet), and completes an unattended install using **only** packages
resident on disc 1 — with no route to the real internet available to prove
it. This is the same artifact set and the same `/preseed` endpoint
`debian.ipxe` (`pkg/tftp/pxe_config.go:54-58`) would resolve a real PXE client
to; booting the installer kernel/initrd directly with the equivalent
`-append` line validates the identical artifacts and identical preseed/mirror
path without also standing up a full TFTP/iPXE/proxyDHCP chain (that chain's
own token-rendering is already covered by Task 8's byte-golden tests).

**Network isolation:** QEMU's default user-mode networking (SLIRP) always
makes the host reachable at `10.0.2.2` regardless of the Mac's real WAN
state — that's how `--serverIP 10.0.2.2` above works. To make the install
genuinely offline, **disconnect the Mac's real network (Wi-Fi off / Ethernet
unplugged)** for the duration of the boot. `10.0.2.2` (booty) stays reachable
through SLIRP's internal host loop; anything the installer tries to reach on
the real internet fails outright. This is the practical, verifiable way to
get "no internet route, only booty reachable" without provisioning a
host-only bridged network — confirm Wi-Fi/Ethernet is off before booting, and
treat any successful reach to a non-`10.0.2.2` address during the run as a
gate failure (see PASS/FAIL below).

Boot the installer directly against the extracted tree:

```bash
qemu-system-x86_64 \
  -accel tcg -m 2048 -smp 2 \
  -kernel /tmp/booty-debian-dvd-lab/cache/debian/12/amd64/<version>/install.amd64/linux \
  -initrd /tmp/booty-debian-dvd-lab/cache/debian/12/amd64/<version>/install.amd64/initrd.gz \
  -append 'auto=true priority=critical preseed/url=http://10.0.2.2:8088/preseed vga=788 --- quiet' \
  -drive file=/tmp/booty-debian-dvd-lab-disk.qcow2,format=qcow2,if=virtio \
  -no-reboot \
  -serial file:/tmp/booty-debian-dvd-lab-serial.log \
  -vnc :1
```

(Create the disk first: `qemu-img create -f qcow2
/tmp/booty-debian-dvd-lab-disk.qcow2 8G`.)

Notes on the flags, from prior netboot-lab runs:

- `-no-reboot` makes QEMU **exit the process** when d-i finishes and issues
  its own reboot, instead of actually rebooting the emulated hardware — a
  clean signal that the automated install ran to completion (or crashed).
- `-serial file:...` captures the installer's console. Strip ANSI escape
  codes with `perl`, not macOS `sed` (BSD `sed` doesn't handle the escape
  sequences the same way GNU `sed` does):
  ```bash
  perl -pe 's/\e\[[0-9;]*[a-zA-Z]//g' /tmp/booty-debian-dvd-lab-serial.log > /tmp/booty-debian-dvd-lab-serial.clean.log
  ```
- `-vnc :1` (connect with any VNC viewer to `localhost:5901`) lets you watch
  the graphical installer UI live if the serial log alone isn't enough to
  diagnose a stall.
- **A curated preseed alone is not a complete unattended install.** The
  preseed served here must also carry the housekeeping directives a raw/
  server-default preseed needs — clock/timezone (`clock-setup`), disabling
  the `apt-cdrom` prompt, `tasksel`/`pkgsel` package-set selection, `grub`
  install-device selection, and `finish-install reboot` — or `d-i` will stall
  waiting for operator input partway through. If your served preseed is the
  curated/minimal kind, confirm it inherits (or explicitly sets) these before
  assuming a hang is an install bug rather than an incomplete preseed.

### PASS criteria — check all of these

1. **Kernel/initrd load from `install.amd64/`.** The `-kernel`/`-initrd`
   paths above point directly at the cached tree — if the guest boots to the
   Debian installer's language-select screen at all, this is satisfied by
   construction. (For a real PXE-path run instead of the direct-kernel
   shortcut, confirm via TFTP server logs that `debian.ipxe` resolved
   `[[debian-baseurl]]` to a `.../install.amd64` URL — `debianBaseURL`,
   `pkg/tftp/tftp.go:261-267`.)
2. **`/preseed` carries booty's local mirror.** Fetch what the installer
   fetched and confirm the three directives are present and point at the
   local tree, not a real Debian mirror hostname:
   ```bash
   curl -sS http://localhost:8088/preseed
   ```
   Expect (paths per your actual `<version>`):
   ```
   d-i mirror/country string manual
   d-i mirror/http/hostname string 10.0.2.2:8088
   d-i mirror/http/directory string /data/cache/debian/12/amd64/<version>
   ```
   (`appendDVDMirror`, `pkg/http/preseed.go:83-89`.)
3. **Zero external network during package install.** With the Mac's WAN
   disconnected (per the isolation note above), `apt` inside the installer
   has no path to any real Debian mirror — if the install proceeds and
   installs packages anyway, they came from booty's local tree. Corroborate
   by tailing the serial log for `apt`/`pkgsel` output referencing
   `10.0.2.2:8088` as the source, and confirm there is no stall/retry loop
   consistent with `apt` trying and failing to reach a real internet host.
4. **A disc-1-resident minimal/server package set installs successfully.**
   The serial log shows `pkgsel`/`tasksel` completing without error for at
   least the minimal/standard system + one representative task (e.g.
   `standard system utilities`), and no "package not found" / "unable to
   fetch" errors for packages that should live on disc 1's `pool/`.
5. **The machine reboots into a working Debian install.** QEMU exits
   (`-no-reboot`) once `finish-install` runs. Boot the resulting disk image
   (`qemu-system-x86_64 -accel tcg -drive
   file=/tmp/booty-debian-dvd-lab-disk.qcow2,format=qcow2,if=virtio`, no
   `-kernel`/`-initrd` this time — boot from the installed GRUB) and confirm
   it reaches a working login prompt.

**FAIL** on any of: a stalled/hung installer (check for a missing
housekeeping directive per the note above before concluding it's a real
bug), a preseed that still shows a real Debian mirror hostname, any package
install failure/fetch error, or a post-reboot system that doesn't boot.

---

## Recording results

When this gate is actually run, capture and commit (or attach to the tracking
issue, whichever this repo's convention is at the time) at minimum:

- The cleaned serial log (`*-serial.clean.log` from the `perl` step above).
- The fetched `/preseed` body (Step 2, check 2).
- A short findings note: date run, Debian point release + `dvdCount` used,
  PASS/FAIL per criterion above, and — if FAIL — the root cause and whether
  it was a real install-correctness bug (fixed before merge) or a runbook/lab
  artifact (e.g. an incomplete preseed missing housekeeping directives).

**This is the load-bearing rule, restated:** byte goldens validate that
booty emits the bytes it intends to emit — they do **not** validate that a
real Debian installer, given those bytes, produces a working system. That is
exactly the gap the `debianconfig` F1/F2/F3 bugs fell through: curated-preseed
byte-goldens were green while the real install broke. **Any install-
correctness bug found by this gate must be fixed before the branch merges to
production** — no unit test in this repository will catch it, because none
of them drive a real installer.
