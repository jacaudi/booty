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
