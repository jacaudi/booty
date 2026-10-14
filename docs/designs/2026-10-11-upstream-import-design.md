# Design: import the worthwhile parts of upstream `jeefy/booty`

**Status:** design v2. All decisions resolved with the maintainer on 2026-10-11 (§6, §7, §8); every
fact verified against code or primary sources (§9). Awaiting final approval. Nothing is built yet.
**Base:** `main` @ `eaea955` (the #86 merge).
**Upstream reviewed:** `jeefy/booty` `main` @ `8a5a227` (2026-10-08). Common ancestor `d9eafed`
(2024-10-12).
**Process:** obra/superpowers `brainstorming` → this design → `writing-plans`. Each item was
confirmed one at a time by the maintainer; decisions under each item are theirs unless marked
**(researched call)**, which means the choice follows from verified evidence cited in §9.
**Plan:** `docs/plans/2026-10-11-upstream-import/` (one file per phase).

---

## 1. Intent

Hand-port the parts of upstream worth having, and fix the bugs and security holes found while
reviewing them. Everything fits the fork's architecture: SQLite (`pkg/db`), `pkg/ostype`,
`pkg/http`, and the authenticated huma `/api/v1` group. Booty's direction is Talos-first (see
`.claude/CLAUDE.md`); this import is groundwork, not that roadmap.

### Goals
- Close two live security holes: TFTP serves secrets from `dataDir` (item 0); `/machineconfig`
  serves Talos cluster CA keys to anyone with a member's MAC (item 0b, interim fix).
- Fix five shared bugs (items 1–5).
- Make the boot chain self-contained: embedded iPXE, MAC-based identification (items 6–8).
- Add Wake-on-LAN and opt-in node health / running-version reports (items 9–10).
- Clean up build and dependencies (items 11–13). Item 14 (vendored skills) is done.

### Non-goals
- Upstream autopilot, kubeadm/k0s, CNI, Bluefin Server, Storage view, power tracker, actuators.
- UEFI HTTP Boot / Secure Boot.
- Talos health reports (Talos runs no systemd units).
- Routed/VLAN boot (#71) as a whole. Items 1 (relay), 2 and 7 are building blocks only.
- The external secrets store and authenticated config fetch (own design; item 0b is interim).
- Rebuilding CI. `BOOTY_*` env for every flag, `booty init`, hostname templates,
  `--proxyDHCPListen`, `--tftpBlockSize`.

### Anti-goals
- Any new state-changing endpoint outside `/api/v1` auth. New base-mux routes are read-only,
  except `/node/report`, which carries its own per-host credential and pre-auth limits.
- A boot regression for a deployment that sets no new flags, except the deliberate, release-noted
  changes listed in §5.
- A fresh-clone `go build ./...` that fails (item 6 commits its outputs).

---

## 2. Items

File references are the fork's unless prefixed `upstream:`. Line numbers are at `f36482c`.

### Item 0. TFTP serves only an allowlist (security, lands first)

**Bug.** `pkg/tftp/tftp.go:197-215` opens any non-special filename under `dataDir`
(`safeJoin` + `os.Open`, no allowlist). `dataDir` holds `api-token` (`cmd/main.go:322`),
`booty.db` (`pkg/config/config.go:174`) and `config/`. HTTP `/data/` already allowlists
(`pkg/http/http.go:134-158`).

**Change.** A pure function decides what TFTP may open, mirroring `isAllowedDataPath`:
- allowed: paths strictly below `cache/` (`cache.DirName`) or `public/`, after cleaning with a
  leading `/`; and the exact top-level names `undionly.kpxe`, `ipxe.efi`, `snponly.efi`,
  `ipxe-arm64.efi`, plus the current `--proxyDHCPBootfileBIOS/UEFI/ARM64` values;
- everything else → `os.ErrNotExist`, with a `slog.Warn` naming client and file;
- the `.partial`/download-suffix exclusion that `/data/` applies (`isPartialPath`,
  `pkg/http/http.go:201-204`) applies here too **(researched call: same rule as HTTP)**.

The serving branch of `readHandler` calls the new function. Tests call the function directly
(`readHandler` is untestable with fakes: it type-asserts `tftp.OutgoingTransfer` and
`tftp.RequestPacketInfo`, `tftp.go:110-111`).

**Release note.** Security fix; rotate the API token if UDP/69 was reachable by untrusted hosts;
move other top-level TFTP files under `public/`.

### Item 0b. Talos cluster-member configs require ARP identity (security, interim)

**Bug.** `pkg/http/machineconfig.go:28-60`: the host comes from `?mac=` (ARP only if absent); a
cluster member (`host.NodeConfigID != nil`) is served its decrypted frozen config
(`serveFrozenNodeConfig`, `:193-206`). A control-plane config holds every cluster CA private key.

**Change.** When the resolved host is a cluster member, booty ARP-resolves the request's source IP
and serves the member config only if that MAC equals `host.MAC`. ARP failure or mismatch → `403`
via `writeError` with the message `cluster member config requires the request to come from the
member`. Non-members are unchanged. The ARP lookup is injected into the handler as a function
parameter (`func(net.IP) (net.HardwareAddr, error)`), defaulting to `arping.Ping` in `baseMux`, so
tests need no network **(researched call: dependency injection, per CLAUDE.md, not a package var)**.

**Limits, documented.** L2-adjacent spoofing remains possible. Members booted through a DHCP relay
(item 1) cannot be ARP-resolved and are refused until the authenticated-fetch design lands.

### Item 1. ProxyDHCP protocol fix

**Bugs.** `pkg/proxydhcp/proxydhcp.go:75-78` answers DISCOVER and REQUEST/INFORM on both 67 and
4011; a port-67 ACK to the client's REQUEST for the real lease makes EDK2 send DHCPDECLINE
(upstream `d7d92a7`, seen on the wire with OVMF). Relayed packets are answered by a broadcast on
booty's own segment (`:231-234`), which never reaches the client.

**Change.** Port `upstream:pkg/dhcp/pxe.go` `handle` and helpers (§9.1 lists exact lines) into
`pkg/proxydhcp`, minus HTTP Boot (delete `pxe.go:145-150`, the `sync` import and the HTTP Boot
functions). Behaviour, in check order:
1. nil packet or not BootRequest → silent;
2. option 60 not prefixed `PXEClient` → silent;
3. **no option 93 → silent**, debug log with the MAC (maintainer decision 3; changes today's
   BIOS assumption, release-noted);
4. `giaddr` set and relay disabled → silent;
5. option 54 present and not booty → silent;
6. DISCOVER answered only on the port-67 listener; REQUEST only on the 4011 listener;
   INFORM and others → silent.

Replies:
- **OFFER** = upstream's bytes (opt 53/54/60/97 copied, opt 43 `offerVendorOptions`, no file, no
  opt 66/67, no `sname`), **except** when the request carries user class `iPXE`
  (`hasUserClass`): then the OFFER also sets `siaddr` = ServerIP, file `booty.ipxe` and opt 67
  `booty.ipxe` (maintainer decision, §8.3). Reason: iPXE never sends the 4011 REQUEST when the
  offer carries a boot menu (iPXE `net/udp/dhcp.c:307-320,612-631`), so a non-embedded iPXE
  would otherwise get no filename from booty.
- **ACK** (4011) = upstream's: copies `ciaddr`, file and opt 67 = `booty.ipxe` for user class
  `iPXE`, else `bootFileForArch`; opt 66 = ServerIP; opt 43 `ackVendorOptions`.
- **Destination** = upstream `replyDestination` (`server.go:447-458`): `giaddr:67` if relayed;
  `ciaddr:68` if set; broadcast `255.255.255.255:68` if broadcast flag, no source, or source
  unset; else the source address.

Config:
- Port `Config.withDefaults()` and `validate()`. Fields: `ServerIP`, `Relay`, `BIOSFile`,
  `EFIFile`, `ARM64File`, `IPXEScript`, `Port67`, `Port4011`. Empty file names get defaults
  (`undionly.kpxe`, `ipxe.efi`, `ipxe-arm64.efi`, `booty.ipxe`); ports default to 67 and 4011.
  `NewServer` stops rejecting empty names.
- New flag `--proxyDHCPRelay` (bool, default false, maintainer decision §8.5). Help text says
  routed Talos cluster members are refused by item 0b until the authenticated-fetch design.
- The serve loop passes the listener's local port to `handle`.

Tests: upstream's pure tests (§9.1) plus its 9 golden `.hex` files **unchanged** (they are
`go test -update` outputs from code, not wire captures; they pass on the fork's current dhcp
library, so no library bump is needed for parity); one fork-only golden
`discover-ipxe-userclass.hex` created with `-update`; `TestReplyDestination` ported; the port-gating
and silent-case tables.

### Item 2. Pass the MAC on config URLs

`pkg/tftp/pxe_config.go:29` (flatcar), `:36` (coreos) and `:56` (debian) get `?mac=${mac}`
appended to `/ignition.json` and `/preseed`. Line 17 (pxelinux) has no iPXE variables; unchanged.
Handlers already prefer `?mac=` (`ignition.go:37`, `preseed.go:148`).

### Item 3. Refuse TFTP writes

`tftp.NewServer(readHandler, nil)`; delete `writeHandler`. Verified behaviour: today the client
gets an invalid 4-zero-byte reply and times out; with a nil handler pin/tftp v3 sends ERROR code 1
"server does not support write requests" (`pin/tftp/v3@v3.2.0/server.go:420-428`). Lands with
item 8.

### Item 4. Hostname validation

- Port `upstream:pkg/hardware/hostname.go` lines 12-41 (`MaxHostnameLen`, `ErrInvalidHostname`,
  `ValidateHostname`) and its test table to `pkg/hardware/hostname.go`. Empty is rejected by the
  function; callers treat empty as "unset" and skip it.
- `create-host` (`POST /api/v1/hosts`, `api_hosts.go:144-230`) validates a non-empty hostname →
  `huma.Error400BadRequest`. This is the only API path that accepts a hostname (`approve-host`
  takes none; `put-host` is a 403 stub, `api_hosts.go:378-386`).
- `/machineconfig`: a non-empty `?hostname=` that fails validation → `400`.
- Stored hostnames (decision 4): one startup `slog.Warn` listing every stored hostname that fails
  validation; every render strips control characters (`unicode.IsControl`) from the host's
  hostname, at the vars-building sites `ignitionVars`, `ignition.go:105`, `machineConfigVarsCore`,
  `machineconfig.go:89/102`, `preseedVars`.
- debianconfig (decision 9): no save-time check; `translateDebianConfig` returns an error when the
  templated `hostname` or `domain` contains a control character.

### Item 5. Auto-detect `--serverIP`

- Flag default becomes `""`.
- `pkg/config`: add `DetectServerIP() (string, error)` (port of `upstream:pkg/config/config.go:542-555`,
  `net.Dial("udp4","192.0.2.1:9")`, no packet sent) and `CloseQuietly`.
- `cmd/main.go`: a pure `resolveServerIP(explicit string, detect func() (string, error)) (string, error)`.
  Explicit wins. Empty → detect; a loopback, unspecified or non-IPv4 result is an error. The
  error reads `no default route; set --serverIP to the address clients reach booty on`. Startup
  fails on error (decisions 5 and finding 9). Applied next to the serverHttpPort resolution
  (`cmd/main.go:371`). `slog.Info("serverIP autodetected", "ip", ip)` on success.
- Tests: table tests of `resolveServerIP` with fake detectors (no test runs `run()`).

### Item 6. Embedded iPXE, built with `go generate`

- New package `boot` at repo root: `boot.go` (`//go:generate ./build-ipxe.sh`,
  `//go:embed undionly.kpxe ipxe.efi snponly.efi`, `var Files embed.FS`,
  `func Lookup(name string) ([]byte, bool)`), `embed.ipxe`, `build-ipxe.sh`, the three binaries,
  `VERSION`, `SHA256SUMS`, `LICENCE.txt` (the output of iPXE's licence report, below).
- `boot/embed.ipxe`:
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
  Verified: `${next-server}` is the site DHCP server's siaddr when it sets one; booty's proxyDHCP
  reply is the separate `proxydhcp` block (iPXE `dhcp.c:596-631`, `settings.c:438-466`). An unset
  setting expands to empty (`settings.c:2374-2390`); `isset` guards it, and a skipped command keeps
  the previous non-zero status so `||` continues (`core/exec.c:177-203,308-342`). The HTTP fallback
  reaches booty only on port 80.
- `boot/build-ipxe.sh`: port of `upstream:hack/build-ipxe.sh` with:
  - `BOOT_DIR=$(cd "$(dirname "$0")" && pwd)`;
  - **native mode** when `IPXE_NATIVE=1` or when no runtime can run a container: builds with the
    host's gcc, binutils, make, perl and liblzma (the only tools these three targets need;
    mtools and xz-utils are not needed, `util/genfsimg:329-330`, `Makefile.housekeeping:1491-1500`);
  - the embed check greps `isset ${proxydhcp/next-server}` in `bin/undionly.kpxe.tmp`,
    `bin-x86_64-efi/ipxe.efi` and `bin-x86_64-efi/snponly.efi`;
  - runs `make bin/undionly.kpxe.licence bin-x86_64-efi/ipxe.efi.licence bin-x86_64-efi/snponly.efi.licence`
    and writes the output to `boot/LICENCE.txt` (iPXE `COPYING`: the licence varies by build
    target and is reported by `make bin/xxx.licence`);
  - `VERSION` records mode (container/native), toolchain, iPXE commit/tag, embed sha256, make line.
- `THIRD_PARTY_NOTICES.md` at the root: upstream's iPXE section adapted. The licence line points at
  `boot/LICENCE.txt` and iPXE's `COPYING`, `COPYING.GPLv2`, `COPYING.UBDL`. Paths: `task ipxe`,
  `boot/build-ipxe.sh`. Byte-identical rebuilds are not claimed.
- First build (decision 10): in the implementing session; start `dockerd` and run the container
  recipe; if it cannot start, native mode. Never copy upstream's bytes.
- Delete the root `undionly.kpxe`.
- TFTP: embedded files are served before the `dataDir` lookup. HTTP: `GET|HEAD /boot/{name}`,
  routed before `http.ServeMux` in `StartHTTP` (`pkg/http/http.go:72-83`) through a wrapper
  function that tests call directly. Port of `upstream:pkg/server/http.go:290-352` minus Secure
  Boot: non-GET/HEAD → 405; unclean or trailing-slash or `..` path → 404; unknown name → 404;
  `Content-Type` `application/efi` for `.efi`, else `application/octet-stream`; `Content-Length`;
  `Cache-Control: no-cache`; HEAD writes headers only.
- Drift guard (decision 5): `boot/boot_test.go` hashes every embedded file and compares it with
  `SHA256SUMS`.
- QEMU check (finding 3) before committing binaries: §9.6 gives the verified recipe and
  expected evidence for both topologies.

### Item 7. Identify the booting host by MAC over HTTP

- New exported renderer in `pkg/tftp` shared by TFTP and HTTP:
  `RenderBootScript(host *hardware.Host, urlHost string) (script, kind string)` and
  `MenuSelectionScript(host *hardware.Host, filename, urlHost string) string` (the existing `menuSelectionScript`, exported).
- New open, read-only base-mux routes in `pkg/http`:
  `GET /booty.ipxe?mac=…` and `GET /menu/{selection}/boot.ipxe?mac=…`. Host from `?mac=`
  (normalised; malformed → unidentified), else ARP (injected function, as item 0b).
  `Content-Type: text/plain`.
- TFTP `booty.ipxe` becomes the stub `#!ipxe\nchain http://<urlHost>/booty.ipxe?mac=${mac} || shell\n`
  (no ARP).
- Every `chain tftp://…/booty.ipxe` and `chain tftp://…/menu/${sel}/boot.ipxe` becomes
  `chain http://<urlHost>/…?mac=${mac}`: `pxe_config.go:47`, `menu.go:172,184,198,202`,
  `tool_scripts.go:69,117,149`. `renderMenu` takes `urlHost` instead of the bare server IP.
- The TFTP `menu/…/boot.ipxe` path keeps working (ARP) for clients holding old scripts.
- **DoInstall flip removed** (finding 2): nothing reads `DoInstall`; the only writer is
  `tftp.go:125-133`. Delete the flip; keep the column; correct README "One-shot install"
  (`README.md:242-244`), `docs/schema/API.md:55`, `docs/schema/DATABASE.md:30,145`.

### Item 8. TFTP hardening + `pin/tftp` v3

- `go.mod`: drop the `replace` block (both entries) and `pin/tftp v2.2.0+incompatible`; require
  `github.com/pin/tftp/v3 v3.2.0`; import path change at `pkg/tftp/tftp.go:21` only.
- Open `dataDir` with `os.Root` once in a new `tftp.Start`, keep the item 0 allowlist in front of
  it, and reject NUL bytes and directories. Verified `os.Root` semantics (§9.4): escaping and
  **absolute** symlinks fail; relative symlinks inside work; directories open but must be refused
  by an explicit `IsDir` check; its escape error is not `fs.ErrNotExist`, so all failures map to
  `os.ErrNotExist`. Release note: absolute symlinks under `dataDir` are no longer followed over
  TFTP (booty itself creates none).
- `--tftpPort` (default 69). No block-size flag (decision §8.2): keep `SetBlockSize(512)` and
  `EnableSinglePort()` exactly as today.
- `Start(cfg Config, errCh chan<- error) (*tftp.Server, error)`: bind errors returned, serve
  errors sent on `errCh`, no `os.Exit`. `cmd/main.go` treats either as fatal.

### Item 9. Wake-on-LAN

- New package `pkg/power`: port `upstream:pkg/power/wol.go` and `wol_test.go` (commit
  `6809c54`) with a local `ErrInvalidMAC` (no import of `pkg/hardware`), and
  `broadcast_unix.go` with `//go:build unix`.
- `POST /api/v1/hosts/{mac}/wake` (`wake-host`, gated): unknown host → 404; malformed MAC → 422;
  success → 200 `{"sentTo": [...]}`; no destination accepted → 502.
- Hosts page: a **Wake** action with an antd `Popconfirm`, using the existing `act` helper.

### Item 10. Node health + running-version reports (opt-in)

- `--nodeReports` (bool, default **false**, decision 2).
- New package `pkg/nodereport`: `Health`, `DMI`, `Normalize` and caps (port of
  `upstream:pkg/hardware/health.go:15-135` minus autopilot parts), `Token(key []byte, mac string, gen int64) string`
  (hex HMAC-SHA256 of `"booty-node-report\x00" + mac + "\x00" + gen`), `Script(server, mac, token string) string`
  (port of `upstream:pkg/ignition/health.go:36-102` posting to `/node/report` with header
  `X-Booty-Node-Token`, MAC baked in), `Unit(scriptPath string) string` (upstream `HealthUnit`
  without `booty-booted.service` in `After=`), `MergeIgnition(cfg []byte, files, units) ([]byte, error)`
  (JSON-level append to `storage.files` and `systemd.units`, operator entries with the same path or
  name win, result validated with `github.com/coreos/ignition/v2/config.Parse`; on failure the
  caller serves the un-merged config and warns), and the bounded rate limiters.
- Key file `<dataDir>/node-report-key`: 32 random bytes, 0600, created on first start when
  `--nodeReports` is on.
- Migration `0010_host_health.sql`: table `host_health` (one row per MAC, latest report,
  `ON DELETE CASCADE`) and column `hosts.report_token_gen INTEGER NOT NULL DEFAULT 0`. Update the
  five `user_version` asserts in `pkg/db/migrate_test.go` (lines 32-33, 56-57, 69-70, 301-302,
  471-472).
- Receiver `POST /node/report?mac=…` (base mux; 404 when the flag is off), in this order:
  malformed MAC or missing header → 400/401 with no DB work; per-source-IP limit (10 per minute,
  LRU-capped at 4096 entries) → 429; token check against an in-memory cache of per-host
  generations (refreshed on rotate; otherwise entries expire after 60 s) → uniform 401; per-MAC limit (1 per 10 s)
  → 429; `http.MaxBytesReader` 64 KiB → 413; JSON decode / `Normalize` error → 400; upsert
  `host_health`, update `hosts.last_seen` → 204.
- Injection: Flatcar/CoreOS via `MergeIgnition` on both ignition paths; Debian only for
  debianconfig-kind configs on the serve path (not the converter round-trip, not preview), as a
  `late_command` fragment placed after ESP sync and before the operator's commands (finding 7),
  plus `curl` via `appendIfAbsent`. The fragment is passed into `buildPreseedView` through a new
  options argument; `translateDebianConfig` keeps its current signature for the converter and
  preview.
- API: `GET /api/v1/hosts` adds an optional `health` summary per host (`running`, `kernel`,
  `firmware`, `receivedAt`, `failedUnitCount`, `vendor`, `product`). New
  `GET /api/v1/hosts/{mac}/health` (decision §8.4) returns the full stored report.
  `POST /api/v1/hosts/{mac}/report-token/rotate` bumps the generation.
- UI: Hosts table columns **Running** and **Last report** (`—` when there is no report; the UI is not
  told whether `--nodeReports` is on, so it cannot distinguish "off" from "not yet"); a failed-units
  badge; a drawer that loads `/hosts/{mac}/health` **(researched call: avoids a new info field for
  one hint)**.

### Item 11. Remove the publish workflows

Delete `.github/workflows/docker-publish.yml` and `image.yml`. Keep `go-test.yml` and
`web-test.yml`. The README Quickstart notes images are built locally (`task image`).

### Item 12. Go dependency bumps

Plus the toolchain and vulnerability fixes in §8.9. Verified to build and pass all tests with exactly these changes (§9.3): `pin/tftp/v3 v3.2.0`
(item 8), `spf13/viper v1.21.0`, `coreos/butane v0.29.0`, `coreos/ignition/v2 v2.27.0`,
`joho/godotenv v1.5.1`, `buger/jsonparser v1.6.1` (maintainer decision), `insomniacslk/dhcp`
`v0.0.0-20260901064844-234b97448fae`; `go mod tidy`. Code changes: the `pkg/tftp/tftp.go:21`
import, and `pkg/http/ignition.go:15` → `github.com/coreos/ignition/v2/config/v3_4/types`
(reboot JSON verified byte-identical). Release note: Butane `fcos 1.6.0-experimental` no longer
translates; `flatcar 1.2.0-experimental` now emits Ignition `3.7.0-experimental`; stable variants
are unchanged.

### Item 13. Taskfile, lint tasks and Dockerfile

- Delete `Makefile`; add lowercase `taskfile.yml` (go-task v3 schema `version: '3'`;
  lowercase is auto-discovered, §9.7). Tasks: `web`, `build` (stripped, `-trimpath`, version and
  timestamp ldflags), `run`, `test`, `test:web`, `vet`, `fmt`, `lint`, `vuln`, `ipxe`
  (`go generate ./boot/`), `check:ipxe` (`go test ./boot/`), `clean`, `image`, `image:push`,
  `image:buildx`, `image:run` (`--cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW`).
- `.golangci.yml` containing only `version: "2"` (default linters). `task lint` and `task vuln`
  exist; lint findings (132 at baseline) are recorded, not fixed (§8.7).
- Dockerfile: `npm ci`; a `go mod download` layer; `-s -w`; build stage `golang:1.26.9-alpine`;
  final stage **`gcr.io/distroless/static-debian13`** (root; §8.10); OCI labels; `EXPOSE 8080/tcp 69/udp`.
- Docs: README, `docs/development.md`, `docs/standards.md` move from `make` to `task`.

### Item 14. Vendored superpowers skills

Done (`1ca3cd7`).

---

## 3. Constraints

- Go toolchain go1.26.2 (from `go.mod`); `CGO_ENABLED=0`.
- New runtime dependencies: only `pin/tftp/v3`. Promote `coreos/go-systemd/v22` to direct for
  the unit-parsing test (already indirect, `go.mod:47`).
- `.claude/CLAUDE.md` principles apply to new and touched code: no new package-level mutable
  state; dependencies (ARP lookup, detectors, clocks, embedded FS) injected as parameters.
- Ported code carries `// Ported from jeefy/booty <path> @ <commit> (MIT).`

## 4. Execution order

Plan files under `docs/plans/2026-10-11-upstream-import/`, each phase green on
`go build ./...`, `go vet ./...`, `go test -race ./...` and (when `web/` changes) `npm test` and
`npx tsc -b`:

1. Item 0 (TFTP allowlist), item 0b (member ARP identity).
2. Items 8 + 3 (pin/tftp v3, `os.Root`, `Start`, nil write handler).
3. Item 1 (proxyDHCP).
4. Items 2, 4, 5.
5. Item 6 (embedded iPXE, QEMU check).
6. Item 7 (HTTP boot script, DoInstall removal).
7. Item 12 (remaining deps).
8. Item 9 (WoL).
9. Item 10 (node reports).
10. Items 11 + 13 (workflows, Taskfile, Dockerfile, docs).

## 5. Deliberate behaviour changes (release notes)

1. TFTP serves only the allowlist (item 0).
2. Talos cluster members must fetch their config from their own MAC's IP (item 0b).
3. proxyDHCP ignores PXE packets without option 93, INFORM, and relayed packets unless
   `--proxyDHCPRelay` (item 1).
4. `--serverIP` is auto-detected; startup fails without a default route unless it is set (item 5).
5. `DoInstall` no longer changes (item 7); it never had an effect.
6. Absolute symlinks under `dataDir` are not followed over TFTP (item 8).
7. Butane experimental variants (item 12).

### Known risks not addressed here

- `hardware.trackUnknown` (`pkg/hardware/mac.go:550`) keeps unknown MACs with no size cap.
  `GET /booty.ipxe?mac=<random>` (item 7) is a new unauthenticated way to grow it;
  `/ignition.json?mac=` already was. Capping it is follow-up work.
  - Update (2026-10-11, final fix wave): the list is now capped at 1024 entries
    (`maxUnknownHosts`); the earliest-tracked MAC is evicted first.
- booty's proxyDHCP broadcasts from a socket bound to `0.0.0.0`. On a host with several
  interfaces, the reply may leave through the default-route interface. Plan phase 5 measures this
  (`NOROUTE=1`); fixing it belongs with `--proxyDHCPListen` / routed boot (#71).

---

## 6. Decisions confirmed in review (first round)

1. Item 10 token: HMAC-derived, key file + MAC + per-host counter.
2. Item 10 default off; Debian only for debianconfig configs.
3. Item 1: drop packets without option 93.
4. Item 4 stored hostnames: startup warning + control-character stripping at render.
5. Item 6 drift guard: Go test against `SHA256SUMS`.
6. Item 12: `v3_4` types for the reboot config; item 10 merge at JSON level.
7. Item 13: `gcr.io/distroless/static-debian12`, root (amended by §8.10: `static-debian13`).
8. Item 1: byte parity with upstream's 9 goldens (amended by §8.3).
9. Item 4 debianconfig: render-time control-character refusal.
10. Item 6 first build: from source here (dockerd, else native).
11. Item 10 MAC baked into the script (amended by §7.8: `host.MAC`).

## 7. Fable design review findings (resolved)

1. TFTP serves secrets → item 0.
2. DoInstall flip over spoofable `?mac=` → removed.
3. `${next-server}` scope → `isset ${proxydhcp/next-server}` first leg; QEMU check.
4. `/node/report` DoS → layered limits.
5. Golden parity → `withDefaults`; dhcp bump moved to item 12 (not needed for parity, §9.1).
6. `/boot/` redirects → routed before the mux.
7. Debian late_command order → before the operator's commands.
8. Baked MAC → `host.MAC`.
9. No default route → startup error with actionable message.

## 8. Decisions from the verification round (2026-10-11, second round)

1. `/machineconfig` CA exposure → item 0b interim fix in this import.
2. `--tftpBlockSize` dropped (it would be a no-op).
3. iPXE clients get `siaddr` + `booty.ipxe` on the OFFER; parity with upstream's 9 goldens kept. (superseded for the type byte by decision 12)
4. Full health detail at `GET /api/v1/hosts/{mac}/health`.
5. `--proxyDHCPRelay` opt-in with `replyDestination`.
6. jsonparser bumped to v1.6.1.
7. Lint/vuln tasks added; findings recorded, not fixed here.
8. Secrets-store work is a later design (backends: local age, 1Password, OpenBao).
9. Toolchain go1.26.9, `golang.org/x/net` v0.60.0 and `google.golang.org/grpc` v1.83.1, in item 12 /
   plan phase 7; govulncheck must then report no called vulnerabilities.
10. Base image `gcr.io/distroless/static-debian13` (debian12 is frozen upstream).
11. `docs/plans/` is tracked in git (removed from `.gitignore`).
12. proxyDHCP departs from upstream byte parity (maintainer, 2026-10-11, after the QEMU check): boot-server and menu type 0x8000 instead of 0 (EDK2 PxeBcBoot.c:937-941 treats 0 as local boot and aborts with PXE-E21), the ACK goes to the client's source port when it equals ciaddr (EDK2 listens on 4011 only, PxeBcDhcp4.c), and the ACK echoes the requested PXE_BOOT_ITEM. The 9 goldens taken from upstream differ from it only in that type byte; the fork-only discover-ipxe-userclass golden changed the same byte. Upstream has the same bug.
13. /node/report also requires the source IP to ARP-resolve to the claimed MAC (maintainer, 2026-10-11, after the adversarial review showed the per-host token is readable by any LAN client from /ignition.json?mac=). Relayed hosts cannot report.
14. /machineconfig ignores an invalid ?hostname= (logged) instead of answering 400 (maintainer, 2026-10-11): the value comes from DHCP and a 400 would stop the Talos boot. Supersedes the /machineconfig half of item 4's 400.
15. The legacy pxelinux.cfg/default path is removed (maintainer, 2026-10-11): it could not work under the item 0 TFTP allowlist, and booty now embeds iPXE for BIOS and UEFI.

## 9. Verified facts (sources)

Full reports with every citation are kept with the plan in
`docs/plans/2026-10-11-upstream-import/research/`.

### 9.1 Upstream proxyDHCP port
`upstream:pkg/dhcp/pxe.go` (commit `454c865`): port lines 3-31 (minus `sync`), 43-47, 57-126
(minus HTTP Boot fields/defaults 71-87, 106-111, and `Listen` 62), 128-186 (minus 145-150),
188-255, 369-458. Tests from `pxe_test.go` (`d7d92a7`) and `golden_test.go` (`f25ad4c`): the 13 pure
tests named in `research/upstream-port.md` §1.6, `TestReplyDestination`, golden files. Verified: all
9 goldens and 18 tests pass with HTTP Boot removed on the fork's current dhcp library pin.

### 9.2 iPXE v2.0.0
Settings scoping, empty expansion, `isset`, `&&`/`||` semantics and the 4011 rule as cited in
items 1 and 6. Build tools: gcc, binutils, make, perl; liblzma for `undionly.kpxe` only.

### 9.3 Go dependencies
Only two import lines change; viper, dhcp, godotenv and jsonparser need no code changes; full
build + 11/11 packages pass on a copy with all bumps.

### 9.4 `os.Root` (Go 1.26)
Escaping symlinks and absolute symlinks fail ("path escapes from parent"); relative in-root
symlinks up to 8 deep work; `..` within the root works; absolute names fail; directories open and
reading fails with EISDIR; NUL → EINVAL.

### 9.5 Fork internals
Flag pattern (`args` + `flags.XxxVar` in `init()`, one `viper.BindPFlags`, `cmd/main.go:274`);
only `/api/v1` is gated (`api.go:50`); API tests build without `Auth`; migrations by sorted
filename against `PRAGMA user_version`; no `GET /hosts/{mac}`; `readHandler` has no tests.

### 9.6 QEMU test recipe

Verified in this container (QEMU 8.2.2, OVMF 2024.02, TCG). Recipes are in
`research/qemu-recipe-ovmf-*.sh`; the write-up is `research/qemu-ovmf.md`. dnsmasq stood in for
booty's proxyDHCP.
- **UEFI PXE.** The NIC is `virtio-net-pci,romfile=` (no option ROM), so EDK2's own PXE client sends
  `PXEClient:Arch:00007`. It takes the proxy offer, requests on 4011, TFTP-loads `ipxe.efi` and
  runs it.
- **Proxy beside a site DHCP.** The embedded iPXE script fetched `booty.ipxe` from the proxy
  (10.0.2.50), not from the site server's next-server (10.0.2.2), and then made the HTTP request
  with `?mac=`.
- **slirp cannot carry HTTP to the tap host.** QEMU's hub floods TCP to slirp, which forges resets.
  The check therefore uses a Linux bridge with the site DHCP server in a network namespace.
- **Site DHCP pointed at booty.** The `isset` leg is skipped, and `booty.ipxe` comes from
  `${next-server}`.
- **iPXE and a proxy menu.** A proxy offer carrying a PXE menu (43.9) makes iPXE skip the 4011
  request and use the offer's siaddr directly. Booty's iPXE offer carries siaddr and `booty.ipxe` (§8.3).
- **Licence.** `make bin-x86_64-efi/ipxe.efi.licence` fails (some drivers lack `FILE_LICENCE()`);
  `licence_list` plus `util/licence.pl` reports `GPL version 2 only`. `snponly.efi.licence` reports
  GPL-2.0-or-later.
- **Build.** About 39 s with `-j4`. Rebuilds are not byte-identical.
- Phase 5's check runs booty itself in place of dnsmasq; its result is recorded in
  `docs/testing/2026-10-ipxe-qemu.md` when executed.

### 9.7 Tooling

- go-task v3.54.0; its module needs go ≥ 1.26.4. Install with
  `go install github.com/go-task/task/v3/cmd/task@v3.54.0`. Lowercase `taskfile.yml` is in the
  default discovery list (`taskfile/taskfile.go:15-23`). A precondition runs inside the task's `dir:`,
  and `dir:` on a single command is silently ignored.
- golangci-lint v2.14.0, installed as the release binary with a checksum (its docs advise against
  `go install`). A config of only `version: "2"` verifies. Baseline: 132 issues (errcheck 130,
  staticcheck 2).
- govulncheck v1.9.0, installed as a binary (`go run …@v1.9.0` scans the wrong standard library).
  At go1.26.2 there are 29 called vulnerabilities: 24 in the standard library (all fixed by go1.26.9),
  plus `x/net` v0.53.0 (fixed in v0.60.0) and `grpc` v1.79.3 (fixed in v1.83.1).
- distroless: `static-debian12` is frozen ("Remove debian12 builds", merged 2026-09-10).
  `static-debian13:latest` exists and runs as user 0.
