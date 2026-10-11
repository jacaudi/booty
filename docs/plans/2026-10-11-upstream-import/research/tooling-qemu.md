# Tooling and QEMU PXE research (verified 2026-10-11)

Scratch root: `SP=/tmp/claude-0/-home-user-booty/d59ac83a-7590-5a41-ad64-142ee85fc598/scratchpad`.
Container: Ubuntu 24.04.5, root, no `/dev/kvm`. `/home/user/booty` was not modified. The only `git status` entry is `M docs/designs/2026-10-11-upstream-import-design.md`, which was already there and not made by this research.
Network notes: `api.github.com`/`github.com/.../releases/latest` returned 403 through the egress proxy. Release *downloads* (`github.com/.../releases/download/...`), `git clone`/`ls-remote` of github.com, `proxy.golang.org` and `gcr.io` worked. **`vuln.go.dev` and `go.googlesource.com` are blocked (403 CONNECT)**.

Toolchain note (affects every `go install`/`go run` command): `/usr/local/go/bin/go` is **go1.24.7**. In `/home/user/booty`, `go version` prints go1.26.2 because `go.mod` has `toolchain go1.26.2` and `GOTOOLCHAIN=auto`. Outside the repo, `go install`/`go run pkg@ver` switch to the newest toolchain that the tool's go.mod requires. In this run that was **go1.26.9**: `go: golang.org/x/vuln@v1.9.0 requires go >= 1.26.0; switching to go1.26.9`. The latest toolchains on proxy.golang.org are go1.26.9 for 1.26 and go1.27.2 overall (`curl https://proxy.golang.org/golang.org/toolchain/@v/list`).

---

## 1. go-task (Taskfile)

| Fact | Value | Source |
|---|---|---|
| Latest stable | **v3.54.0** (2026-10-01, commit b5aef1c) | `proxy.golang.org/github.com/go-task/task/v3/@latest`; `git ls-remote --tags` (top tags: v3.53.0, v3.53.1, v3.54.0) |
| task go.mod `go` directive | `go 1.26.4` | `dl/task/go.mod:3` @v3.54.0 |
| Verified install | `GOBIN=$SP/gobin go install github.com/go-task/task/v3/cmd/task@v3.54.0` succeeded in 1m10s. `task --version` gives `3.54.0`. `go version -m` shows it was built with go1.26.9 (auto-switched) | command output |

**Implication:** go1.26.2 cannot build task v3.54.0 under `GOTOOLCHAIN=local`, because the module needs go ≥ 1.26.4. With the default `GOTOOLCHAIN=auto` it works.

Documented install methods, from `website/src/latest/docs/installation.md` @v3.54.0:
- `:543` `go install github.com/go-task/task/v3/cmd/task@latest`
- `:552-556` tip: "For CI environments we recommend using the install script … faster and more stable"
- `:485-497` `sh -c "$(curl --location https://taskfile.dev/install.sh)" -- -d -b ~/.local/bin v3.42.1`. The docs example pins v3.42.1, so substitute v3.54.0.
- `:562-577` `go get -tool github.com/go-task/task/v3/cmd/task@latest`, then `go tool task …`. This modifies the project's go.mod.

**Lowercase `taskfile.yml` is auto-discovered.** `taskfile/taskfile.go:15-23` (`DefaultTaskfiles`) lists, in order: `Taskfile.yml`, `taskfile.yml`, `Taskfile.yaml`, `taskfile.yaml`, `Taskfile.dist.yml`, `taskfile.dist.yml`, `Taskfile.dist.yaml`, `taskfile.dist.yaml`. `init.go:12` sets `defaultFilename = "Taskfile.yml"` for `task --init`. I tested this: the test file below is named lowercase `taskfile.yml`, and `task --list` found it.

Schema: `version: '3'`. `website/src/public/schema.json:757-765` accepts a semver-pattern string or a number, and `:906` makes `version` required.

Proven Taskfile (`$SP/work/tf/taskfile.yml`, run with task 3.54.0):
```yaml
version: '3'

vars:
  GIT_SHA:
    sh: git rev-parse --short HEAD 2>/dev/null || echo unknown
  GOVERSION:
    sh: go env GOVERSION

tasks:
  gen:
    desc: Generate something
    cmds:
      - echo "gen sha={{.GIT_SHA}} go={{.GOVERSION}}"
  build:
    desc: Build after gen
    deps: [gen]
    preconditions:
      - sh: command -v go
        msg: "go is not installed"
      - test -d ../sub
    cmds:
      - cmd: pwd
      - echo "building {{.GIT_SHA}}"
    dir: sub
  fail:
    desc: Precondition that fails
    preconditions:
      - sh: test -f /nonexistent
        msg: "missing /nonexistent"
    cmds:
      - echo never
```
Results:
- `task --list` printed `* build: Build after gen`, `* fail: Precondition that fails` and `* gen: Generate something`.
- `task build` ran `gen` first (deps), then `pwd` printed `.../tf/sub`.
- `task fail` printed `task: missing /nonexistent` and `precondition not met`, with exit code **201**.

Gotchas, verified by running them:
- **Preconditions run inside the task's `dir:`.** With `dir: sub`, the check `test -d sub` failed, so the path had to be `../sub`.
- **`dir:` is task-level only.** The schema's `cmd_call` properties are `cmd, silent, set, shopt, ignore_error, platforms, if, timeout`, and there is no `dir`. A `- cmd: pwd` entry with `dir: /tmp` is **silently ignored**: it printed the Taskfile directory, with no error. For a per-command directory, use `- task: other` where `other` has its own `dir:`. That approach printed `/tmp`.

## 2. golangci-lint v2

- Latest is **v2.14.0** (2026-09-24, commit 114493f). Sources: proxy `@latest` and `git ls-remote --tags` (v2.13.1, v2.13.2, v2.14.0). The binary reports `golangci-lint has version 2.14.0 built with go1.27.0 from 114493f9 on 2026-09-24T11:07:15Z`.
- The recommended method is the **binary install**, per `docs/content/docs/welcome/install/local.md` @v2.14.0:
  - `:10` `curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.14.0`
  - `:116-118` "Using `go install`/`go get`, "tools pattern", and `tool` command/directives installations aren't guaranteed to work. We recommend using binary installation."
  - `:137` "We don't recommend using `go tool`."
  - Fallback at `:131`: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`
  - `https://golangci-lint.run/install.sh` was blocked here (403). Instead I fetched `https://github.com/golangci/golangci-lint/releases/download/v2.14.0/golangci-lint-2.14.0-linux-amd64.tar.gz` plus `golangci-lint-2.14.0-checksums.txt`, and `sha256sum -c` printed `OK`.
- Minimal valid v2 config. `golangci-lint config verify --config <f>` returned rc=0 for both of these:
  ```yaml
  version: "2"
  ```
  ```yaml
  version: "2"
  linters:
    default: standard
    enable: [gosec, misspell]
  formatters:
    enable: [gofmt, goimports]
  ```
  A v1-style file without `version` failed with rc=3: `unsupported version of the configuration: ""`. An unknown key failed with rc=3: `additional properties 'bogus' not allowed`.
- Default linters (`golangci-lint linters --no-config`): errcheck, govet, ineffassign, staticcheck, unused.
- **Run against booty.** Read-only, using the repo's go1.26.2: `golangci-lint run --no-config --max-issues-per-linter 0 --max-same-issues 0 --output.text.path <f> --output.json.path <f> ./...` took 11.7 s and exited with rc=1.
  - **Full counts: 132 issues, errcheck 130 and staticcheck 2.** 77 of these are in `_test.go` files.
  - With the default caps (`golangci-lint run --no-config ./...`), it shows **52 issues: errcheck 50, staticcheck 2**.
  - errcheck by package: pkg/http 48, pkg/db 39, pkg/cache 21, pkg/config 6, pkg/proxydhcp 6, pkg/tftp 3, cmd 4, pkg/hardware 2, pkg/ostype 1.
  - staticcheck: `pkg/http/preseedconv.go:451` S1011 (replace the loop with `append(rest, diskGroup...)`) and `pkg/http/debiangen_test.go:889` QF1001 (De Morgan's law).
  - govet, ineffassign and unused found nothing.
  - Outputs are in `$SP/work/gcl/booty-lint.{txt,json}`.

## 3. govulncheck

- Latest is **golang.org/x/vuln v1.9.0** (proxy `@latest`, Time 2026-10-11T03:39:53Z; the previous release was v1.8.0 on 2026-09-08). Its go.mod requires go 1.26.0.
- Plain `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` failed here with `fetching vulnerabilities: Get "https://vuln.go.dev/index/modules.json.gz": Forbidden`. That is an environment block, not a tool problem.
- Workaround used: I cloned the github mirror `golang/vulndb` (HEAD d5e0cfa, 2026-10-10, 4613 OSV files in `data/osv`) and passed `-db file://$SP/dl/vulndb/data/osv`. govulncheck v1.9.0 accepts a flat OSV directory through its "hybrid source" (`internal/client/client.go:97-101`).
- **Caveat: `go run …@v1.9.0` scans the wrong standard library.** Its header reads "scanned … the **go1.26.9** standard library", because the auto-switched toolchain propagates to the child process. That run reported only 6 called vulns, in x/net and grpc, and **hid every stdlib vuln affecting go1.26.2**. To scan the repo's real toolchain, install the binary and run it from the repo: `GOBIN=… go install golang.org/x/vuln/cmd/govulncheck@v1.9.0`, then `cd /home/user/booty && govulncheck ./...`.
- **Result at go1.26.2: rc=3, "Your code is affected by 29 vulnerabilities from 1 module and the Go standard library"**, plus 9 vulns in imported packages and 32 in required modules that the code does not call. Full output is in `$SP/work/govuln-1262.txt`; the `go run` variant is in `$SP/work/govuln-verbose.txt`.

  Called (symbol-level) vulnerabilities:
  - **Stdlib, fixed by go1.26.9:**
    - net/http: GO-2026-6617, 6613, 6612, 6611, 6610, 6609, 6605, 6603
    - net/textproto: 6608
    - crypto/tls: 6607
    - html/template: 6600, 6599
  - **Stdlib, fixed by go1.26.6 or earlier:**
    - net/url: 6218 (1.26.6)
    - html/template: 6091 (1.26.6), 4982 and 4980 (1.26.3)
    - crypto/tls: 6090 (1.26.6), 5856 (1.26.5)
    - net/http: 6089 (1.26.6), 4918 (1.26.3)
    - encoding/asn1: 5972 (1.26.6)
    - net/textproto: 5039 (1.26.4)
    - crypto/x509: 5037 (1.26.4)
    - net/mail: 4986 and 4977 (1.26.3)
    - net: 4971 (1.26.3, Windows-only)
  - **golang.org/x/net v0.53.0:** GO-2026-6617, 6612, 6611, 6610 and 6603 are fixed in v0.60.0. GO-2026-5026 is fixed in v0.55.0.
  - **google.golang.org/grpc v1.79.3:** GO-2026-6348 is fixed in v1.83.1. GO-2026-6061 is fixed in v1.82.1.

  Not called: x/crypto v0.50.0 (many, fixed ≤ v0.56.0, GO-2026-5932 has no fix), x/text v0.36.0, x/mod v0.37.0, insomniacslk/dhcp, cel-go, klauspost/compress, buger/jsonparser, and aws-sdk-go v1.47.9 (GO-2022-0646/0635, no fix).
- **Minimum fix:** bump the toolchain to go1.26.9, x/net to ≥ v0.60.0 and grpc to ≥ v1.83.1.
- Booty's `Dockerfile` pins `golang:1.26.2-alpine`.
- These results come from a local vulndb snapshot rather than vuln.go.dev. They are equivalent only as of the snapshot date, 2026-10-10.

## 4. distroless

Checked via the registry API (`curl https://gcr.io/v2/distroless/<repo>/tags/list` and `/manifests/<tag>`, then fetching the config blob):

| Image:tag | Exists | Index digest (OCI index) | amd64 manifest | Config `User` |
|---|---|---|---|---|
| static-debian12:latest | yes | `sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2` | `sha256:6d635b32…78ee03` | **`0` (root)**, WorkingDir `/` |
| static-debian12:nonroot | yes | `sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab` | `sha256:52dcfbab…8411d` | `65532`, WorkingDir `/home/nonroot` |
| static-debian13:latest | **yes** | `sha256:58133991db06659feaabe0f4e97a35cebf15ef4ea08f8a4c6d2ee5f75e4aa6a0` | `sha256:a8a6666c…7d717665` | `0` (root) |
| static-debian13:nonroot | yes | `sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3` | `sha256:2293b36c…acf150` | `65532` |

All four tags also have the `debug`, `debug-nonroot`, `latest-amd64` and `nonroot-amd64` variants.

**debian12 is deprecated and frozen.** The `static-debian12:latest` upload time is 2026-08-21, and a `deprecated-public-image-…` tag exists. In the distroless repo (HEAD 38b944f, 2026-10-10), commit 3833bee "Remove debian12 builds" was merged in PR #2156 on 2026-09-10. README `:38` lists only `static-debian13` (latest, nonroot, debug, debug-nonroot; amd64, arm64, arm, s390x, ppc64le, riscv64), `:30` says "Any other tags are considered deprecated and are no longer updated", and `:170` says images are based on Debian 13. debian13 was last pushed 2026-09-13.

**Recommendation for the plan:** use `gcr.io/distroless/static-debian13:nonroot`, pinned by digest. The booty Dockerfile currently uses `gcr.io/distroless/base-debian12`, which is frozen. The image would need port-binding capabilities as nonroot (DHCP 67, TFTP 69). That last point is my inference and is UNVERIFIED for booty.

## 5. QEMU PXE

### Packages and firmware (`apt-get install -y qemu-system-x86 ovmf seabios ipxe-qemu dnsmasq-base iproute2 tcpdump`)

`dpkg-query` versions:
- qemu-system-x86 `1:8.2.2+ds-0ubuntu1.18` (QEMU 8.2.2)
- ovmf `2024.02-2ubuntu0.10`
- seabios `1.16.3-2`
- ipxe-qemu `1.21.1+git-20220113.fbbdc3926-0ubuntu2`
- dnsmasq-base `2.91-0ubuntu0.24.04.2` (compile options include DHCP and TFTP)
- iproute2 `6.1.0-1ubuntu6.4`
- tcpdump `4.99.4-3ubuntu4.24.04.1`

Firmware paths:
- **OVMF:** `/usr/share/OVMF/OVMF_CODE_4M.fd`, `OVMF_CODE_4M.secboot.fd` (with `.ms.fd` and `.snakeoil.fd` symlinks), `OVMF_VARS_4M.fd`, `OVMF_VARS_4M.ms.fd` and `OVMF_VARS_4M.snakeoil.fd`. There is no 2M `OVMF_CODE.fd` in this version. Also present: `/usr/share/ovmf/OVMF.fd` (symlinked from `/usr/share/qemu/OVMF.fd`), `OVMF.amdsev.fd`, and descriptors in `/usr/share/qemu/firmware/*.json`.
- **SeaBIOS:** `/usr/share/seabios/bios-256k.bin`, `bios.bin`, `bios-microvm.bin` and `vgabios-*.bin`.
- **iPXE ROMs:** `/usr/lib/ipxe/qemu/efi-{e1000,e1000e,eepro100,ne2k_pci,pcnet,rtl8139,virtio,vmxnet3}.rom`, `pxe-*.rom` (legacy only) and `compat-256k-efi-*.rom`.
- `qemu-system-x86_64 -L help` lists the search path `/usr/share/qemu`, `/usr/share/seabios`, `/usr/lib/ipxe/qemu`.
- `/dev/kvm` is absent, so all runs used TCG.

### dnsmasq flags

Ubuntu-minimal excludes man pages, so I extracted `dnsmasq.8.gz` from the `.deb` (`apt-get download dnsmasq-base`) and rendered it to `$SP/work/dnsmasq.man.txt`:
- `-p, --port=<port>` (`:138`): "Setting this to zero completely disables DNS function, leaving only DHCP and/or TFTP."
- `-i, --interface` (`:166`) and `-z, --bind-interfaces` (`:210`): "really bind only the interfaces it is listening on … enables multiple instances … which provide DHCP".
- `-F, --dhcp-range=…<start-addr>[,<end-addr>|<mode>[,<netmask>…]][,<lease time>]` (`:717`). `:764`: "For IPv4, the <mode> may be proxy in which case dnsmasq will provide proxy-DHCP on the specified subnet."
- `-M, --dhcp-boot=[tag:<tag>,]<filename>,[<servername>[,<server address>|<tftp_servername>]]` (`:1064`): "Server name and address are optional: if not provided … address set to the address of the machine running dnsmasq". This sets siaddr/next-server.
- `--pxe-service=[tag:<tag>,]<CSA>,<menu text>[,<basename>|<bootservicetype>][,<server address>]` (`:1085`). `:1107` says proxy mode "simply provides the information given in --pxe-prompt and --pxe-service".
- Also used: `-d/--no-daemon` (`:97`), `-8/--log-facility=<file>` (`:108`), `-x/--pid-file` (`:124`), `--log-dhcp` (`:1153`), `-l/--dhcp-leasefile` (`:1160`), `--enable-tftp` (`:1399`), `--tftp-root` (`:1404`), `-C/--conf-file` (`:1458`).

Verified invocation (non-proxy):
```
dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file=$D/dnsmasq.pid \
  --user=root --port=0 --interface=tap0 --bind-interfaces \
  --dhcp-range=10.77.0.100,10.77.0.150,255.255.255.0,10m \
  --dhcp-boot=boot.ipxe,booty-smoke,10.77.0.1 \
  [--enable-tftp --tftp-root=$D/tftp] \
  --dhcp-leasefile=$D/leases --log-dhcp --log-facility=$D/dnsmasq.log
```
dnsmasq logged `started, version 2.91 DNS disabled` and `DHCP, sockets bound exclusively to interface tap0`.

Gotchas:
- Without `--user=root`, dnsmasq drops to `nobody` and fails with `TFTP directory … inaccessible: Permission denied` / `FAILED to start up`, because the scratch dir is 0700.
- The servername field (`booty-smoke`) is sent as **option 66** whenever the client requests it. Use `--dhcp-boot=boot.ipxe,,<ip>` to avoid that; this form was also verified.

### Recipe (a): iPXE plus dnsmasq on tap0. **WORKS**

Script: `$SP/research/qemu-recipe-a.sh`.
- Network setup: `ip tuntap add tap0 mode tap; ip addr add 10.77.0.1/24 dev tap0; ip link set tap0 up`. This worked; the container has `/dev/net/tun`. The `dummy` link type is unavailable (`Unknown device type`).
- QEMU command:
  ```
  qemu-system-x86_64 -machine pc -accel tcg -m 256 -display none -serial file:serial.log -monitor none \
    -no-reboot -boot n -netdev tap,id=n0,ifname=tap0,script=no,downscript=no \
    -device e1000,netdev=n0,mac=52:54:00:12:34:56
  ```
  This uses SeaBIOS with the iPXE ROM `efi-e1000.rom`.
- **Run a1** (next-server 10.77.0.1, dnsmasq TFTP serving `boot.ipxe`):
  - dnsmasq log: `vendor class: PXEClient:Arch:00000:UNDI:002001`, `user class: iPXE`, `DHCPDISCOVER(tap0)`, `DHCPOFFER(tap0) 10.77.0.111`, `next server: 10.77.0.1`, `option: 67 bootfile-name boot.ipxe`, `DHCPREQUEST`/`DHCPACK(tap0) 10.77.0.111`, `sent …/tftp/boot.ipxe to 10.77.0.111`.
  - tcpdump: `10.77.0.111.6580 > 10.77.0.1.69: TFTP, RRQ "boot.ipxe" octet blksize 1432 tsize 0`.
  - Timing: **7.06 s** from QEMU start to TFTP sent. The first DISCOVER arrives about 5 s after QEMU starts, under TCG.
- **Run a2** (`NEXT_SERVER=10.77.0.2 TFTP=0`, with 10.77.0.2 added as a second address on tap0):
  - dnsmasq logged `next server: 10.77.0.2`.
  - tcpdump: `10.77.0.111 > 10.77.0.2.69: TFTP, RRQ "boot.ipxe"`, answered by `ICMP 10.77.0.2 udp port 69 unreachable` and retried 4 times.
  - This proves iPXE uses siaddr/next-server for TFTP.
- The serial log was empty: this iPXE ROM build prints to VGA only, so the evidence is dnsmasq and tcpdump.

### Recipe (b): hub with slirp (restrict=on) plus tap, and dnsmasq proxy-DHCP. **Both offers reach the guest**

Script: `$SP/research/qemu-recipe-b.sh`. QEMU command:
```
qemu-system-x86_64 -machine pc -accel tcg -m 256 -display none -serial none -monitor none -no-reboot -boot n \
  -netdev user,id=u0,restrict=on,net=10.0.2.0/24 -netdev hubport,id=hp_user,hubid=0,netdev=u0 \
  -netdev tap,id=t0,ifname=tap0,script=no,downscript=no -netdev hubport,id=hp_tap,hubid=0,netdev=t0 \
  -netdev hubport,id=hp_nic,hubid=0 -device e1000,netdev=hp_nic,mac=52:54:00:12:34:57 \
  -object filter-dump,id=fd0,netdev=hp_nic,file=guest.pcap
```
`filter-dump` on the NIC's hubport captures exactly what the guest receives.

**b1.** tap0 = 10.0.2.50/24 (inside slirp's subnet). dnsmasq ran with `--dhcp-range=10.0.2.0,proxy --dhcp-boot=boot.ipxe,,10.0.2.50` and **no** `--pxe-service`.
- dnsmasq logged `DHCP, proxy on subnet 10.0.2.0` and saw the DISCOVERs (`vendor class: PXEClient…`), but **sent no reply**.
- The guest received only slirp's OFFER (`52:55:0a:00:02:02 10.0.2.2.67 > 255.255.255.255.68`).
- iPXE took 10.0.2.15 and stalled with no filename. The run hit the 30 s timeout.

**b2.** Same as b1 plus `--pxe-service=x86PC,"booty",boot.ipxe,10.0.2.50`. Result: **success in 5.05 s**.
- `guest.pcap` shows the DISCOVER, then the **slirp OFFER from 10.0.2.2**, then the **dnsmasq proxy OFFER from 10.0.2.50**, within 1 ms of each other.
- iPXE then sent a REQUEST, which slirp ACKed.
- The guest sent a boot-server REQUEST to `10.0.2.50:4011`, which dnsmasq answered (`PXE(tap0) 10.0.2.15 … boot.ipxe`, `next server: 10.0.2.50`).
- The guest sent the TFTP RRQ `boot.ipxe` to 10.0.2.50:69 and the transfer completed (`dnsmasq-tftp: sent …boot.ipxe to 10.0.2.15`).
- Slirp's broadcast replies also leak out onto tap0, which the host tcpdump saw, because a QEMU hub floods every frame to all ports.

**b3.** tap0 on a different subnet (10.77.0.50/24) with `--dhcp-range=10.77.0.0,proxy` and `--pxe-service`:
- The guest still received both OFFERs.
- It ARPed for 10.77.0.50 directly, the host answered, and the guest sent a REQUEST to `10.77.0.50:4011`. **dnsmasq never answered the 4011 request**, so the guest retried and stalled until the 20 s timeout. Artifacts are in `$SP/qemu/b/alt/`.
- **Conclusion: the proxy server's tap address must be inside slirp's subnet** (e.g. 10.0.2.x with `net=10.0.2.0/24`).

### Environment results
- tap creation, QEMU tap attach and dnsmasq binding all worked as root in this container.
- KVM was unavailable, but TCG was enough.
- Other failures, with their exact causes, are listed above: vuln.go.dev 403, golangci-lint.run/install.sh 403, api.github.com 403, the dnsmasq `nobody` TFTP permission error, and a `pkill -f` pattern that matched its own calling shell, which I fixed by using a pidfile.
- All tap, dnsmasq, qemu and tcpdump processes were cleaned up, and tap0 was deleted.

### UNVERIFIED
- OVMF/UEFI PXE runs. Only SeaBIOS + iPXE was exercised; OVMF paths were listed but not booted.
- Behaviour with booty's own proxydhcp instead of dnsmasq.
- The nonroot-capability implication for distroless.
