# Phase 5: Embedded iPXE built with `go generate` (item 6) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first.

**Goal:**
- booty ships its own iPXE v2.0.0 (`undionly.kpxe`, `ipxe.efi`, `snponly.efi`), built from source with booty's embedded script and committed.
- Those binaries are served over TFTP (ahead of `dataDir`) and over HTTP at `/boot/<name>`.
- The full UEFI boot chain is proven in QEMU against booty itself.

**Architecture:**
- New Go package `boot` at the repo root: an embed FS, a `go:generate` build script and a drift test.
- `tftp.Config` gains `BootFiles fs.FS`.
- `pkg/http` gains a `/boot/` handler routed before `http.ServeMux`.
- A committed QEMU check script, `test/qemu/pxe-check.sh`.

**Tech Stack:**
- iPXE v2.0.0 (commit `12798ec29aa8a64d8675c4378b99f5fe28447afb`).
- Docker (`debian:stable-slim`) or a native gcc/make/perl/liblzma build.
- QEMU 8.2 + OVMF.

**Spec:** design item 6; finding 3 (`isset ${proxydhcp/next-server}`); decisions §6.5 (drift test) and §6.10 (build from source here).

## Global Constraints

See `README.md`.
- Never copy upstream's `boot/*.efi`/`*.kpxe` bytes.
- The binaries must be built in this phase, by Task 1.

## Review Focus

1. A TFTP request for `undionly.kpxe` is answered from the embedded copy even if `dataDir` holds a different file of that name. Task 2 test.
2. `/boot/ipxe.efi/`, `/boot/../ignition.json`, `/boot//ipxe.efi` and `/boot/booty.ipxe` must never redirect.
   - The first two, plus `/boot/booty.ipxe`, return 404.
   - `/boot//ipxe.efi` returns 200 with no redirect.
   - Task 3 tests.
3. A fresh clone with no Docker and no iPXE toolchain must still `go build ./...` (binaries committed). Task 1 Step 7.
4. UEFI firmware beside a site DHCP server that names another next-server must still reach booty. Task 5, topology `proxy`.
5. UEFI firmware with the site DHCP pointed straight at booty (no proxyDHCP) works. Task 5, topology `dhcp`.

---

### Task 1: The `boot` package and the first build

**Files:** Create:
- `boot/embed.ipxe`
- `boot/build-ipxe.sh` (mode 0755)
- `boot/boot.go`
- `boot/boot_test.go`
- `THIRD_PARTY_NOTICES.md`
- generated: `boot/undionly.kpxe`, `boot/ipxe.efi`, `boot/snponly.efi`, `boot/VERSION`, `boot/SHA256SUMS`, `boot/LICENCE.txt`

Delete: the root `undionly.kpxe`.

**Interfaces:**
- Produces (package `boot`, import path `github.com/jeefy/booty/boot`):
  - `var Files embed.FS`, holding `undionly.kpxe`, `ipxe.efi` and `snponly.efi`.
  - `var Names = []string{"undionly.kpxe", "ipxe.efi", "snponly.efi"}`.
  - `func Lookup(name string) ([]byte, bool)`: returns the bytes and true when `name` is exactly one of `Names`; otherwise `nil, false`.

- [ ] **Step 1: `boot/embed.ipxe`.** Write exactly these lines, ending with a newline. The script must never be reformatted; it is compiled into the binaries.

```
#!ipxe
:start
dhcp || goto retry
isset ${proxydhcp/next-server} && chain tftp://${proxydhcp/next-server}/booty.ipxe || chain tftp://${next-server}/booty.ipxe || chain http://${next-server}/booty.ipxe?mac=${mac} || goto shell
exit
:retry
sleep 5
goto start
:shell
echo Booty: could not reach ${next-server} - dropping to the iPXE shell
shell
```

- [ ] **Step 2: `boot/build-ipxe.sh`.** Write exactly the script below. It is a port of `upstream:hack/build-ipxe.sh` @ `60ad48e`, with these changes:
  - lives in `boot/`;
  - native mode;
  - a new embed check;
  - licence report;
  - mtools/xz removed (not needed for these targets: `research/boot-chain.md` §2).

```bash
#!/usr/bin/env bash
# Builds the iPXE binaries embedded in booty (boot/undionly.kpxe, boot/ipxe.efi,
# boot/snponly.efi) from a pinned iPXE ref with boot/embed.ipxe compiled in, and
# writes VERSION, SHA256SUMS and LICENCE.txt next to them.
# Ported from jeefy/booty hack/build-ipxe.sh @ 60ad48e (MIT); native mode and the
# licence report are booty additions.
#
# Usage (from repo root): go generate ./boot/   (or: task ipxe)
#   IPXE_REF=<tag|sha>   pinned ref (default v2.0.0)
#   IPXE_NATIVE=1        build with the host toolchain (gcc, binutils, make, perl,
#                        liblzma, git) instead of a debian:stable-slim container
#   CONTAINER_RUNTIME=   podman or docker (default: podman, then docker)
set -euo pipefail

BOOT_DIR=$(cd "$(dirname "$0")" && pwd)
IPXE_REPO=${IPXE_REPO:-https://github.com/ipxe/ipxe}
IPXE_REF=${IPXE_REF:-v2.0.0}
IMAGE=${IPXE_BUILD_IMAGE:-docker.io/library/debian:stable-slim}

[[ -f "$BOOT_DIR/embed.ipxe" ]] || { echo "missing $BOOT_DIR/embed.ipxe" >&2; exit 1; }

# BUILD runs with OUT (boot dir), WORK (scratch) and MODE set.
read -r -d '' BUILD <<'EOS' || true
set -euo pipefail
if [[ "$IPXE_REF" == v* ]]; then
	git clone -q --branch "$IPXE_REF" --depth 1 "$IPXE_REPO" "$WORK/ipxe"
else
	git clone -q "$IPXE_REPO" "$WORK/ipxe"
	git -C "$WORK/ipxe" checkout -q "$IPXE_REF"
fi
COMMIT=$(git -C "$WORK/ipxe" rev-parse HEAD)
TAG=$(git -C "$WORK/ipxe" describe --tags --exact-match 2>/dev/null || echo none)
COMMIT_DATE=$(git -C "$WORK/ipxe" log -1 --format=%cI)
export SOURCE_DATE_EPOCH
SOURCE_DATE_EPOCH=$(git -C "$WORK/ipxe" log -1 --format=%ct)
TARGETS="bin/undionly.kpxe bin-x86_64-efi/ipxe.efi bin-x86_64-efi/snponly.efi"
MAKE_LINE="make -C src $TARGETS EMBED=boot/embed.ipxe BUILD_ID=0x${COMMIT:0:8}"
echo "+ $MAKE_LINE"
# shellcheck disable=SC2086
make -C "$WORK/ipxe/src" -j"$(nproc)" $TARGETS EMBED="$OUT/embed.ipxe" BUILD_ID="0x${COMMIT:0:8}" >"$WORK/build.log" 2>&1 || {
	tail -50 "$WORK/build.log"; exit 1; }
for f in bin/undionly.kpxe.tmp bin-x86_64-efi/ipxe.efi bin-x86_64-efi/snponly.efi; do
	grep -q 'isset ${proxydhcp/next-server}' "$WORK/ipxe/src/$f" || { echo "embedded script missing from $f" >&2; exit 1; }
done
# Licence: iPXE's licence depends on what a target links (COPYING). The direct
# .licence target fails for ipxe.efi (some drivers lack FILE_LICENCE()), so fall
# back to .licence_list + util/licence.pl, which iPXE's own Makefile uses.
: >"$OUT/LICENCE.txt"
for t in $TARGETS; do
	echo "== $(basename "$t")" >>"$OUT/LICENCE.txt"
	if ! make -s -C "$WORK/ipxe/src" "$t.licence" EMBED="$OUT/embed.ipxe" BUILD_ID="0x${COMMIT:0:8}" >>"$OUT/LICENCE.txt" 2>"$WORK/lic.err"; then
		LIST=$(make -s -C "$WORK/ipxe/src" "$t.licence_list" EMBED="$OUT/embed.ipxe" BUILD_ID="0x${COMMIT:0:8}")
		(cd "$WORK/ipxe/src" && perl util/licence.pl $LIST) >>"$OUT/LICENCE.txt"
	fi
done
cp "$WORK/ipxe/src/bin/undionly.kpxe" "$WORK/ipxe/src/bin-x86_64-efi/ipxe.efi" "$WORK/ipxe/src/bin-x86_64-efi/snponly.efi" "$OUT/"
cd "$OUT"
{
	echo "source:      $IPXE_REPO"
	echo "commit:      $COMMIT"
	echo "tag:         $TAG"
	echo "commit-date: $COMMIT_DATE"
	echo "build-date:  $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo "mode:        $MODE"
	echo "builder:     $(. /etc/os-release; echo "$PRETTY_NAME"), $(gcc --version | head -1)"
	echo "embed:       boot/embed.ipxe (sha256 $(sha256sum embed.ipxe | cut -d' ' -f1))"
	echo "make:        $MAKE_LINE"
} >VERSION
sha256sum undionly.kpxe ipxe.efi snponly.efi >SHA256SUMS
cat VERSION SHA256SUMS LICENCE.txt
EOS

runtime=""
if [[ "${IPXE_NATIVE:-0}" != 1 ]]; then
	for r in ${CONTAINER_RUNTIME:-podman docker}; do
		if command -v "$r" >/dev/null 2>&1 && "$r" info >/dev/null 2>&1; then runtime=$r; break; fi
	done
fi

if [[ -z "$runtime" ]]; then
	echo "Building iPXE $IPXE_REF natively -> $BOOT_DIR"
	WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT
	OUT="$BOOT_DIR" WORK="$WORK" MODE=native IPXE_REPO="$IPXE_REPO" IPXE_REF="$IPXE_REF" bash -c "$BUILD"
else
	echo "Building iPXE $IPXE_REF with $runtime ($IMAGE) -> $BOOT_DIR"
	run_args=(--rm -e IPXE_REPO="$IPXE_REPO" -e IPXE_REF="$IPXE_REF" -e OUT=/out -e WORK=/work -e MODE="container ($IMAGE)" -v "$BOOT_DIR:/out:Z")
	for v in http_proxy https_proxy HTTP_PROXY HTTPS_PROXY no_proxy NO_PROXY; do
		[[ -n "${!v:-}" ]] && run_args+=(-e "$v=${!v}")
	done
	"$runtime" run "${run_args[@]}" "$IMAGE" bash -c '
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq
		apt-get install -y -qq --no-install-recommends ca-certificates git build-essential liblzma-dev perl binutils >/dev/null
		mkdir -p /work
		bash -c "$0"' "$BUILD"
	chown "$(id -u):$(id -g)" "$BOOT_DIR"/{undionly.kpxe,ipxe.efi,snponly.efi,VERSION,SHA256SUMS,LICENCE.txt} 2>/dev/null || true
fi
```

  Then `chmod 0755 boot/build-ipxe.sh` and `bash -n boot/build-ipxe.sh`. Expected: no output. If `shellcheck` is installed, also run `shellcheck boot/build-ipxe.sh`; any finding other than SC2086 on `$TARGETS`/`$LIST` must be fixed.
- [ ] **Step 3: Write the failing tests** in `boot/boot_test.go`:
  - `TestLookup`: each of `Names` → `ok` and non-empty bytes; `booty.ipxe`, `embed.ipxe`, `../go.mod` and `""` → `!ok`.
  - `TestEmbeddedMatchesSHA256SUMS`:
    - read `SHA256SUMS` with `os.ReadFile("SHA256SUMS")` (tests run in the package directory);
    - for each line `<hex>  <name>`, `sha256.Sum256` of `Lookup(name)`'s bytes must equal `<hex>`;
    - exactly 3 lines.
- [ ] **Step 4: `boot/boot.go`:**

```go
// Package boot holds booty's embedded iPXE binaries (see THIRD_PARTY_NOTICES.md).
// Rebuild them with `go generate ./boot/` (or `task ipxe`) and commit the outputs;
// plain `go build` never runs the generator.
package boot

//go:generate ./build-ipxe.sh

import "embed"

//go:embed undionly.kpxe ipxe.efi snponly.efi
var Files embed.FS

var Names = []string{"undionly.kpxe", "ipxe.efi", "snponly.efi"}
```

  Then implement `Lookup` using `slices.Contains(Names, name)` and `Files.ReadFile(name)`.
- [ ] **Step 5: Build the binaries (decision §6.10).**
  1. Try Docker:
     - `docker info >/dev/null 2>&1 || (sudo -n dockerd >/tmp/dockerd.log 2>&1 &)`;
     - wait up to 30 s, polling `docker info >/dev/null 2>&1` once a second.
  2. Run `go generate ./boot/`. The script picks the container if `docker info` works, else native.
     - Native mode needs `git gcc make perl` and `liblzma` headers. Check with `which git gcc make perl && ls /usr/include/lzma.h`.
     - If headers are missing and apt works: `sudo apt-get install -y liblzma-dev build-essential`.
  3. Expected:
     - the script prints `VERSION`, `SHA256SUMS` and `LICENCE.txt`;
     - `ls -l boot/` shows the three binaries; `ipxe.efi` is about 1.1 MB and `undionly.kpxe` about 70 KB (upstream's sizes for the same ref were 1,152,512 and 71,448 bytes; ours differ slightly);
     - `LICENCE.txt` has three `==` sections, each ending in a line `The overall licence for this file is: …`. Research expects `GPL version 2 only` for `ipxe.efi` and `GPL version 2 (or, at your option, any later version)` for `snponly.efi`; `undionly.kpxe` was not measured.
  4. Record which mode ran (the `mode:` line in `boot/VERSION`).
- [ ] **Step 6:** Run `go test ./boot/`. Expected: `ok`.
- [ ] **Step 7: Check the fresh-clone build.** Run `git rm undionly.kpxe` (the old root file), then `go build ./...`. Expected: success. `boot/` is committed in Step 9, so `go build` never needs the generator.
- [ ] **Step 8: `THIRD_PARTY_NOTICES.md`.** Start from `git show upstream/main:THIRD_PARTY_NOTICES.md` lines 1-54 and change:
  - `hack/build-ipxe.sh` → `boot/build-ipxe.sh`;
  - `make ipxe` → `go generate ./boot/` (or `task ipxe`);
  - the "License:" bullet → `License: varies by build target; the per-binary verdict from iPXE's own licence tooling is recorded in boot/LICENCE.txt. See COPYING, COPYING.GPLv2 and COPYING.UBDL in the upstream tree at the pinned commit.`;
  - delete the sentence claiming a rebuild "yields byte-identical binaries" (rebuilds were measured to differ: `research/qemu-ovmf.md` §4);
  - add `Native builds (IPXE_NATIVE=1) use the host toolchain; boot/VERSION records which mode built the committed files.`
- [ ] **Step 9: Commit.**

```bash
git add boot THIRD_PARTY_NOTICES.md
git commit -m "feat(boot): embed iPXE v2.0.0 built from source with booty's script (go generate)

The embedded script tries booty's proxyDHCP next-server first, because
\${next-server} is the site DHCP server's when it sets one (iPXE settings
scoping). Binaries are committed so go build needs no toolchain; a test
checks them against SHA256SUMS. Licence per binary: boot/LICENCE.txt."
```

### Task 2: TFTP serves the embedded files first

**Files:** Modify `pkg/tftp/tftp.go` (`Config`, `server`, `readHandler`), `cmd/main.go` (`tftp.Start` call); Test `pkg/tftp/server_test.go`.

**Interfaces:**
- Consumes: `boot.Files`, `boot.Names`.
- Produces: the field `BootFiles fs.FS` on `tftp.Config` (nil means none).
- `TopLevelBootFiles()` now also returns `snponly.efi`. It already did since phase 1; confirm.

- [ ] **Step 1: Test** `TestServerPrefersEmbeddedBootFiles`:
  - `dataDir` has `undionly.kpxe` containing `DISK`;
  - `Config.BootFiles = fstest.MapFS{"undionly.kpxe": {Data: []byte("EMBEDDED")}}`;
  - a client `Receive("undionly.kpxe")` returns `EMBEDDED`;
  - a second subtest with `BootFiles: nil` returns `DISK`.
- [ ] **Step 2:** Implement. In `readHandler`, before `openDataFile`: if `s.bootFiles != nil`, try `fs.ReadFile(s.bootFiles, filename)`. On success, serve `bytes.NewReader(b)` via `rf.ReadFrom`, log `"source", "embedded"`, and return. Any error falls through to `openDataFile`.
- [ ] **Step 3:** In `cmd/main.go`, pass `BootFiles: boot.Files` (import `github.com/jeefy/booty/boot`).
- [ ] **Step 4:** Run `go test -race ./pkg/tftp/ ./cmd/`. Expected: `ok`. Commit: `git add pkg/tftp cmd && git commit -m "feat(tftp): serve the embedded iPXE binaries ahead of dataDir"`.

### Task 3: HTTP `/boot/<name>` routed before the mux

**Files:** Create `pkg/http/boot.go`, `pkg/http/boot_test.go`; Modify `pkg/http/http.go` (`StartHTTP`, lines 72-83), `pkg/http/api.go` (`APIDeps`), `cmd/main.go`.

**Interfaces:**
- Produces:
  - the field `BootFiles fs.FS` on `APIDeps`;
  - `func bootFileHandler(files fs.FS) http.Handler`;
  - `func withBootRoute(files fs.FS, next http.Handler) http.Handler`: requests whose `URL.Path` starts with `/boot/` go to `bootFileHandler`, everything else to `next`.

- [ ] **Step 1: Tests**, ported from `git show upstream/main:pkg/server/http_test.go` lines 668-740 (`TestBootHandler`, `doHead`, `TestBootHandlerCollapsesSlashesWithoutRedirect`), against `withBootRoute(files, http.NotFoundHandler())` with `files := fstest.MapFS{"undionly.kpxe": {Data: []byte("BIOS-IPXE")}, "ipxe.efi": {Data: []byte("EFI-IPXE")}, "snponly.efi": {Data: []byte("SNP-IPXE")}}`. Drop every `sb/` and `secureboot/` case.
  - `GET /boot/ipxe.efi` → 200, body `EFI-IPXE`, `Content-Type: application/efi`, `Content-Length: 8`, `Cache-Control: no-cache`.
  - `GET /boot/undionly.kpxe` → 200, `application/octet-stream`.
  - `HEAD /boot/ipxe.efi` → 200, `Content-Length: 8`, empty body.
  - 404 for each of: `/boot/`, `/boot/pxelinux.0`, `/boot/../boot/ipxe.efi/x`, `/boot/ipxe.efi/`, `/boot/booty.ipxe`, `/boot/../ignition.json`.
  - `POST /boot/ipxe.efi` → 405.
  - `/boot//ipxe.efi`, `/boot///ipxe.efi` and `/boot/./ipxe.efi` → 200, never 301. Send them with a raw `http.Request` whose `URL.Path` is set directly, so the client does not clean them.
  - `bootFileHandler(nil)` → 404 for `/boot/ipxe.efi`.

  The fork's 404/405 bodies are plaintext from `writeError`. Assert status codes only, not upstream's JSON body.
- [ ] **Step 2: Implement `pkg/http/boot.go`** from `upstream:pkg/server/http.go` lines 273-352, removing the `sb/` and `secureboot/` branches (329-342) and `openSecureBoot`. The rules:
  - method not GET/HEAD → 405 (`writeError(w, http.StatusMethodNotAllowed, "method not allowed", nil)`);
  - `clean := path.Clean(r.URL.Path)`; 404 if `clean` does not start with `/boot/`, if the raw path ends with `/`, or if any raw segment is `..`;
  - the name is `strings.TrimPrefix(clean, "/boot/")`; `fs.ReadFile(files, name)` (any error → 404);
  - `Content-Type` is `application/efi` when `strings.EqualFold(path.Ext(name), ".efi")`, else `application/octet-stream`;
  - set `Content-Length` and `Cache-Control: no-cache`; HEAD returns after the headers.
  - Add the provenance comment `// Ported from jeefy/booty pkg/server/http.go @ 639de71 (MIT).`
- [ ] **Step 3:** In `StartHTTP`, set `Handler: logRequest(withBootRoute(deps.BootFiles, myHandler))`. In `cmd/main.go`, pass `BootFiles: boot.Files` in `APIDeps`.
- [ ] **Step 4:** Run `go vet ./... && go test -race ./pkg/http/ ./cmd/`. Expected: `ok`. Commit: `git add pkg/http cmd && git commit -m "feat(http): GET/HEAD /boot/<name> serves the embedded iPXE binaries without redirects"`.

### Task 4: Docs and flag help

- [ ] **Step 1:** In `cmd/main.go`, change the help text of the three `--proxyDHCPBootfile*` flags (lines 207-223):
  - from `(staged in dataDir)` to `(embedded for undionly.kpxe and ipxe.efi; other names are served from dataDir)`;
  - the ARM64 flag says `(not embedded; stage it in dataDir)`.
- [ ] **Step 2:** Docs:
  - `docs/CONFIGURATION.md:37-39`: same wording as the flags.
  - `docs/schema/API.md:83`: `The x86 binaries are embedded in booty; ARM64 must be staged in --dataDir.`
  - `docs/schema/STORAGE.md:20-22`: `undionly.kpxe` and `ipxe.efi` no longer need to be in `dataDir`; `ipxe-arm64.efi` still does.
  - `README.md:144-147` (Quickstart step 4) and `:301`: booty ships `undionly.kpxe`, `ipxe.efi` and `snponly.efi`, also downloadable from `http://<booty>:8080/boot/<name>`.
  - `docs/releases/UNRELEASED.md` `## Features`: `booty embeds iPXE v2.0.0 (undionly.kpxe, ipxe.efi, snponly.efi) and serves it over TFTP and /boot/; staging iPXE in --dataDir is no longer needed (except ARM64).`
- [ ] **Step 3:** Commit: `git add cmd docs README.md && git commit -m "docs: embedded iPXE"`.

### Task 5: QEMU boot-chain check (finding 3)

**Files:** Create `test/qemu/pxe-check.sh` (mode 0755) and `docs/testing/2026-10-ipxe-qemu.md`.

Verified basis: `research/qemu-ovmf.md` and `research/tooling-qemu.md` §5. Those runs used dnsmasq as a stand-in for booty. This task runs **booty itself**, so its result is new evidence; record it exactly.

Facts the check depends on:
- QEMU's slirp networking forges TCP resets for other hosts on its hub, so HTTP to booty fails there. The check uses a Linux bridge with a "site router" DHCP server in a network namespace instead.
- The NIC has its option ROM disabled (`romfile=`), so OVMF's own EDK2 PXE client runs and loads booty's `ipxe.efi`.

- [ ] **Step 1: Install the tools** (root required): `sudo apt-get install -y qemu-system-x86 ovmf dnsmasq-base iproute2 tcpdump`. Verify:
  - `ls /usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_VARS_4M.fd`;
  - `qemu-system-x86_64 --version` prints a version.

  If apt or root is unavailable, stop this task and report that the QEMU check could not run. Do **not** commit the binaries as "verified" in that case.
- [ ] **Step 2: Write `test/qemu/pxe-check.sh`** with exactly this content:

```bash
#!/bin/bash
# Boots a UEFI (OVMF) QEMU guest from booty and reports how far the chain got.
# TOPO=proxy: a site DHCP server (netns "site", 10.0.2.2, no boot info) plus
#             booty's proxyDHCP on br0 (10.0.2.50). Tests ${proxydhcp/next-server}.
# TOPO=dhcp:  dnsmasq is the only DHCP server on tap0 (10.77.0.1) and points
#             firmware at booty's TFTP (dhcp-boot=ipxe.efi). No proxyDHCP.
# Usage (root): BOOTY=bin/booty TOPO=proxy|dhcp [OUT=dir] [QEMU_TIMEOUT=240] test/qemu/pxe-check.sh
# Based on the verified recipes in docs/plans/2026-10-11-upstream-import/research/.
set -u
BOOTY=${BOOTY:?set BOOTY to a built booty binary}
TOPO=${TOPO:-proxy}
OUT=${OUT:-$(mktemp -d)}
MAC=52:54:00:12:34:58
mkdir -p "$OUT/data"
cp /usr/share/OVMF/OVMF_VARS_4M.fd "$OUT/vars.fd"
cleanup() {
  [ -n "${QPID:-}" ] && kill $QPID 2>/dev/null
  [ -n "${BPID:-}" ] && kill $BPID 2>/dev/null
  for p in "$OUT"/*.pid; do [ -f "$p" ] && kill "$(cat "$p")" 2>/dev/null; done
  [ -n "${TPID:-}" ] && kill $TPID 2>/dev/null
  wait 2>/dev/null
  ip link del tap0 2>/dev/null; ip link del br0 2>/dev/null; ip link del site0 2>/dev/null
  ip netns del site 2>/dev/null
  true
}
trap cleanup EXIT
cleanup
ip tuntap add tap0 mode tap
if [ "$TOPO" = proxy ]; then
  SRV=10.0.2.50
  ip link add br0 type bridge; ip link set br0 type bridge stp_state 0 forward_delay 0
  ip link set tap0 master br0; ip link set tap0 up
  ip link add site0 type veth peer name site1
  ip link set site0 master br0; ip link set site0 up
  ip netns add site; ip link set site1 netns site
  ip -n site addr add 10.0.2.2/24 dev site1; ip -n site link set site1 up; ip -n site link set lo up
  ip addr add $SRV/24 dev br0; ip link set br0 up
  IF=br0
  ip netns exec site dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file="$OUT/site.pid" \
    --user=root --port=0 --interface=site1 --bind-interfaces \
    --dhcp-range=10.0.2.15,10.0.2.99,255.255.255.0,1h --dhcp-leasefile="$OUT/site-leases" \
    --log-dhcp --log-facility="$OUT/site-dnsmasq.log" &
  PROXY=--proxyDHCPEnabled=true
else
  SRV=10.77.0.1
  ip addr add $SRV/24 dev tap0; ip link set tap0 up
  IF=tap0
  dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file="$OUT/dnsmasq.pid" \
    --user=root --port=0 --interface=tap0 --bind-interfaces \
    --dhcp-range=10.77.0.100,10.77.0.150,255.255.255.0,10m \
    --dhcp-boot=ipxe.efi,,$SRV \
    --dhcp-leasefile="$OUT/leases" --log-dhcp --log-facility="$OUT/dnsmasq.log" &
  PROXY=--proxyDHCPEnabled=false
fi
tcpdump -i $IF -n -U -w "$OUT/host.pcap" 'udp or tcp port 8080 or arp' 2>/dev/null &
TPID=$!
"$BOOTY" --dataDir="$OUT/data" --serverIP=$SRV --httpPort=8080 $PROXY --debug=true >"$OUT/booty.log" 2>&1 &
BPID=$!
for i in $(seq 1 60); do grep -q 'Booty started' "$OUT/booty.log" && break; sleep 0.5; done
grep -q 'Booty started' "$OUT/booty.log" || { echo "result=BOOTY_DID_NOT_START"; tail -20 "$OUT/booty.log"; exit 1; }
timeout ${QEMU_TIMEOUT:-240} qemu-system-x86_64 -machine q35 -accel tcg -m 512 \
  -drive if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive if=pflash,format=raw,unit=1,file="$OUT/vars.fd" \
  -display none -serial file:"$OUT/serial.log" -monitor none -no-reboot \
  -netdev tap,id=n0,ifname=tap0,script=no,downscript=no \
  -device virtio-net-pci,netdev=n0,mac=$MAC,romfile= &
QPID=$!
RESULT=TIMEOUT
for i in $(seq 1 $(( ${QEMU_TIMEOUT:-240} * 2 ))); do
  if grep -q "path=/booty.ipxe" "$OUT/booty.log"; then RESULT=HTTP_BOOT_SCRIPT; break; fi
  if grep -q 'TFTP get.*booty.ipxe' "$OUT/booty.log" && [ "${UNTIL:-http}" = tftp ]; then RESULT=TFTP_BOOTY_IPXE; break; fi
  kill -0 $QPID 2>/dev/null || { RESULT=QEMU_EXITED; break; }
  sleep 0.5
done
echo "result=$RESULT topo=$TOPO out=$OUT"
grep -E 'TFTP get|proxyDHCP reply|path=/booty.ipxe|boot script' "$OUT/booty.log" | head -40
```

  `chmod 0755 test/qemu/pxe-check.sh && bash -n test/qemu/pxe-check.sh`.

  The script's pass markers are exact log strings booty emits:
  - `TFTP get` with `file=…` comes from `readHandler`'s first log line (`pkg/tftp/tftp.go`, `slog.Info("TFTP get", "file", filename)`);
  - `path=/booty.ipxe` comes from `logRequest` (`pkg/http/http.go`), which logs `"path", r.URL.Path`;
  - `proxyDHCP reply` comes from phase 3's `respond`.

  Before running, confirm each of these strings exists with `grep -rn '"TFTP get"\|"proxyDHCP reply"\|"path", r.URL.Path' pkg/`. If one does not exist, stop and report.
- [ ] **Step 3: Run it.** Phase 6 (HTTP `/booty.ipxe`) is not done yet, so stop at the TFTP fetch of `booty.ipxe`:

```bash
go build -o bin/booty ./cmd
sudo env BOOTY=bin/booty TOPO=proxy UNTIL=tftp OUT=/tmp/pxe-proxy test/qemu/pxe-check.sh
sudo env BOOTY=bin/booty TOPO=dhcp  UNTIL=tftp OUT=/tmp/pxe-dhcp  test/qemu/pxe-check.sh
```

  Expected for both: `result=TFTP_BOOTY_IPXE`.
  - In the proxy run the log shows, in order:
    - a `proxyDHCP reply` of type OFFER, then one of type ACK with `file=ipxe.efi`;
    - `TFTP get file=ipxe.efi`;
    - a second `proxyDHCP reply` OFFER with `file=booty.ipxe`, the embedded iPXE's DHCP;
    - `TFTP get file=booty.ipxe` from a `10.0.2.x` client.

    Expected timing, from the stand-in runs: under 60 s on TCG.
  - Confirm with `tcpdump -nr /tmp/pxe-proxy/host.pcap 'udp port 69'`: the `RRQ "booty.ipxe"` goes to `10.0.2.50.69`, not `10.0.2.2`.
  - If the result is anything else, do not change booty to make it pass. Save the `OUT` directory, then report the result line and the last 40 lines of `booty.log` and `serial.log`.
- [ ] **Step 4: Record the evidence** in `docs/testing/2026-10-ipxe-qemu.md`:
  - the date, booty commit, QEMU/OVMF versions (`dpkg-query -W qemu-system-x86 ovmf`), and the iPXE `mode:` from `boot/VERSION`;
  - both result lines;
  - the grep output;
  - the tcpdump line.
- [ ] **Step 5:** Commit: `git add test/qemu docs/testing && git commit -m "test(qemu): UEFI boot-chain check against booty (proxyDHCP beside a site DHCP, and site DHCP pointed at booty)"`.

### Phase exit check

`go build ./... && go vet ./... && go test -race ./...` → all `ok`; `go test ./boot/` → `ok`.

### Follow-up in phase 6

Phase 6's exit check re-runs Task 5 without `UNTIL=tftp`. Expected: `result=HTTP_BOOT_SCRIPT` for both topologies, plus a `boot script` log line with `mac=52:54:00:12:34:58`. Append the new result to `docs/testing/2026-10-ipxe-qemu.md`.
