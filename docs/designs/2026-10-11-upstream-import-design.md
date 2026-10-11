# Design: import the worthwhile parts of upstream `jeefy/booty`

**Status:** design; decisions (§6) and Fable review findings (§7) resolved with the maintainer 2026-10-11; awaiting final approval. Nothing below is built yet.
**Base:** `main` @ `eaea955` (the #86 merge).
**Upstream reviewed:** `jeefy/booty` `main` @ `8a5a227` (2026-10-08). Common ancestor `d9eafed`
(2024-10-12); 392 upstream commits since, ~65k lines changed on each side.
**Process:** written with obra/superpowers `brainstorming`, to be planned with `writing-plans`.
Every item below was confirmed one at a time by the maintainer on 2026-10-11; the decisions
recorded under each item are theirs unless marked **(builder's call)**.

---

## 1. Intent

The fork and upstream split in 2024 and were rewritten independently. A merge is impossible
(different storage, packages, UI framework), so this is a **hand port** of specific pieces:
bugs upstream found in code both sides still share, boot-chain improvements, and two small fleet
features. Everything ported must fit the fork's architecture: SQLite (`pkg/db`), the `pkg/ostype`
OS registry, `pkg/http` (not upstream's `pkg/server`), and the authenticated huma `/api/v1`
surface.

### Goals
- Fix five real bugs the fork carries (items 1–5).
- Make the boot chain self-contained: booty ships its own iPXE and stops depending on ARP to
  know who is booting (items 6–8).
- Add Wake-on-LAN and per-host health / running-version reporting (items 9–10).
- Clean up the build: drop the broken publish workflows, bump stale Go dependencies, move the
  Makefile to a Taskfile, tighten the Dockerfile (items 11–13).
- Vendor the obra/superpowers skills into `.claude/skills/` (item 14).

### Non-goals
- Upstream's autopilot, kubeadm/k0s bootstrap, CNI, Bluefin Server, Storage view, power-state
  tracker, reboot/shutdown actuators. The fork's Talos cluster authoring covers its cluster story.
- UEFI HTTP Boot / Secure Boot (upstream `pkg/secureboot`). Revisit only with SB hardware.
- Talos health reporting (Talos has no systemd units; would need Talos-API polling). Later.
- Routed/VLAN boot (#71) itself. Item 7 is its prerequisite, not its delivery.
- Rebuilding CI. Item 11 only removes the publish workflows; CI is rebuilt later.
- `BOOTY_*` environment variables for every flag, `booty init`, hostname templates /
  auto-register, proxyDHCP interface binding (`--proxyDHCPListen`). Not confirmed; out of scope.

### Anti-goals (would be a failure even if it "works")
- **Any new state-changing endpoint outside `/api/v1` auth.** Upstream mounts power, register
  and health on an open mux; the fork must not. The only new unauthenticated endpoints are the
  boot-path `/booty.ipxe` and `/boot/*` (read-only, same trust level as today's `/ignition.json`)
  and the node report receiver, which carries its own per-host credential (item 10).
- A boot regression for an existing deployment that sets no new flags. Every change keeps the
  current default behaviour working: ARP stays as a fallback, the TFTP `booty.ipxe` name is
  unchanged, operator-staged iPXE files are only shadowed by identical-purpose embedded ones.
- A bare `go build ./...` on a fresh clone that fails (item 6 commits its outputs for this reason).

---

## 2. Items

Each item lists: what changes, where, the decisions, and tests. File references are the fork's
unless prefixed `upstream:`.

### Item 0. Close the TFTP data-directory leak (security fix, lands first)

**Bug (live today; Fable review finding 1).** `pkg/tftp/tftp.go:197-215`: any TFTP filename that
is not special-cased is opened from `dataDir` through `safeJoin` with no allowlist. `dataDir`
holds `api-token` (`cmd/main.go:322`), `booty.db` (`pkg/config/config.go:174`, including
`config_revisions.source_b64`, the plaintext per-host boot configs) and `config/` (server-side
templates). Any LAN client can `tftp <booty> -c get api-token` and gain full `/api/v1` access. The
HTTP `/data/` handler had the same bug and was fixed with an allowlist
(`pkg/http/http.go:120-140`); TFTP never got it.

**Change.** Mirror the HTTP fix: before opening anything from `dataDir`, TFTP allows only
- paths strictly below `cache.DirName/` and `public/`, and
- these top-level names: `undionly.kpxe`, `ipxe.efi`, `snponly.efi`, `ipxe-arm64.efi`, plus the
  current values of `--proxyDHCPBootfileBIOS`, `--proxyDHCPBootfileUEFI` and
  `--proxyDHCPBootfileARM64`.

Everything else gets `ErrNotExist` and a warn log. An allowlist, not a denylist, so a future
secret written to `dataDir` (item 10's `node-report-key`) is safe automatically. Implemented on the
current `safeJoin` path; item 8 later moves the same check onto `os.Root`.

**Tests.** `api-token`, `booty.db`, `booty.db-wal`, `config/ignition.yaml` and `node-report-key` →
not found over TFTP; `cache/…`, `public/…` and the iPXE names still served.

**Release note.** Security fix. Operators who exposed UDP/69 to untrusted machines should rotate the
API token (`booty token rotate --yes`). Operators who served other top-level files over TFTP must
move them under `public/`.

### Item 1. ProxyDHCP protocol fix (full)

**Bug.** `pkg/proxydhcp/proxydhcp.go:75-78` answers DISCOVER *and* REQUEST/INFORM on **both**
UDP/67 and UDP/4011. A PXE client broadcasts its DHCPREQUEST for the *real* server's lease on 67;
booty ACKs it with `yiaddr=0` and a bootfile, EDK2/OVMF firmware logs "Lease confirmed isn't the
same as that in the offer", sends DHCPDECLINE, and the boot fails (upstream `d7d92a7`, verified on
the wire with OVMF).

**Change.** Rewrite `buildReply` as `buildReply(req, cfg, localPort)` following
`upstream:pkg/dhcp/pxe.go` `handle()`:
- DISCOVER answered **only on 67** with an OFFER carrying PXE option 43 and **no** bootfile,
  option 66 or `sname`, exactly as upstream (review decision 8). Spec-following clients,
  including iPXE, take the boot file from the 4011 ACK.
- REQUEST answered **only on 4011** with an ACK carrying the bootfile; `ciaddr` echoed.
- INFORM: dropped (upstream drops it; nothing in the PXE flow needs it).
- Drop packets with `giaddr` set unless a new `--proxyDHCPRelay` flag (default false) is on.
- Drop packets whose server-identifier (option 54) names another server.
- Option 43: OFFER gets upstream's `offerVendorOptions` (discovery control `0x07`, boot-server
  list with booty only, one-entry menu "Booty", zero-timeout prompt); ACK gets
  `ackVendorOptions` (boot item). Port verbatim with attribution.
- iPXE detection: port `hasUserClass` (raw and RFC 3004 forms) to replace the substring check.
- A PXE packet with **no option 93** (client architecture) is **dropped**, as upstream does
  (review decision 3), with a debug log naming the MAC. This changes today's behaviour (the fork
  assumes BIOS): pre-PXE-2.1 ROMs that omit option 93 stop being answered by proxyDHCP. Call it out
  in the release notes; such machines still boot with the site DHCP server pointed at booty.
- `handle()` passes the listener's port; `loop` already knows which conn it reads.
- **Library parity** (Fable finding 5): bump `github.com/insomniacslk/dhcp` to upstream's pin
  (`v0.0.0-20260901064844-234b97448fae`) **in this item**, so reply serialisation matches the
  golden files from the first commit.
- **Defaults** (Fable finding 5): port upstream's `Config.withDefaults()`; empty bootfile names
  and ports get the standard defaults (`undionly.kpxe`, `ipxe.efi`, `ipxe-arm64.efi`,
  `booty.ipxe`, 67, 4011) instead of `NewServer` rejecting them (`proxydhcp.go:141`). The test
  config is then `Config{ServerIP: testServerIP}`, identical to upstream's `testConfig()`.

**Tests.** Port `upstream:pkg/dhcp/golden_test.go`'s `goldenCases`: synthetic request packets
built in code for 9 PXE/iPXE cases (3 DISCOVER, 6 REQUEST: BIOS, EFI x64, EFI BC, ARM64, iPXE
raw user-class, iPXE RFC 3004). Upstream's `.hex` files are its replies to those packets, run
against OVMF in QEMU upstream. The fork's replies must be **byte-identical** to upstream's, so copy
the 9 `.hex` files **unchanged** and use them as a parity check (review decision 8). Test config
values must match upstream's `testConfig`. The protocol behaviour that matters is also asserted
directly by table tests: no option 93 → silent; REQUEST on 67 → silent; DISCOVER on 4011 → silent; giaddr with/without
relay; foreign server-id → silent.

**Docs.** `docs/CONFIGURATION.md` (`--proxyDHCPRelay`), README proxyDHCP note.

### Item 2. Pass the MAC on config URLs

**Bug.** `pkg/tftp/pxe_config.go` lines 29, 36, 56: the Flatcar, CoreOS and Debian iPXE scripts
fetch `/ignition.json` and `/preseed` with no `?mac=`, so `pkg/http/ignition.go:37` and
`pkg/http/preseed.go:148` identify the host by ARP only. If ARP fails the host silently gets the
reboot config. Talos already passes `&mac=${mac}`.

**Change.** Append `?mac=${mac}` to those three URLs. The `pxelinux.cfg/default` Flatcar entry
(line 17) has no iPXE variables; leave it. Handlers already prefer `?mac=` over ARP; no handler change.
`${mac}` is iPXE's MAC of the booting NIC, same source Talos uses.

**Tests.** Extend the existing pxe_config/tftp tests to assert `mac=${mac}` in each rendered
script.

### Item 3. Refuse TFTP writes

**Bug.** `pkg/tftp/tftp.go:430-433`: `writeHandler` returns `nil`, so a client's WRQ looks
accepted. Nothing is written, but the client is told it succeeded.

**Change.** Pass `nil` as the write handler (`tftp.NewServer(readHandler, nil)`), which makes the
library answer WRQ with an error. Delete `writeHandler`. Lands together with item 8's v3 upgrade
(same constructor line).

**Tests.** A WRQ against a test server gets an error packet.

### Item 4. Hostname validation (API and machineconfig)

**Bug.** Any string is stored as a hostname (`pkg/http/api_hosts.go:205`) and rendered into
Butane, Talos YAML and Debian preseed (`d-i netcfg/get_hostname`); a newline injects lines.
`/machineconfig` also renders the unauthenticated `?hostname=` query
(`pkg/http/machineconfig.go:89`).

**Change.**
- Port `upstream:pkg/hardware/hostname.go` `ValidateHostname` + `MaxHostnameLen` +
  `ErrInvalidHostname` (RFC 1123 label or dotted name, ≤253 chars, labels ≤63) and its test into
  `pkg/hardware/hostname.go`. Do **not** port the hostname-template half.
- Call it wherever a host's hostname enters through `/api/v1`: `create-host` (POST `/hosts`) and
  `put-host` (PUT `/hosts/{mac}`). Cluster-member add takes no hostname (`api_clusters.go:41` is an
  output struct). Empty stays allowed (means "unset"). Invalid → 400 via the
  existing huma error path.
- `/machineconfig`: a **non-empty** `?hostname=` that fails validation → **400** (maintainer's
  decision). Empty is still allowed (DHCP often gives none).
- **Existing rows** (review decision 4) are not migrated or rewritten. At startup booty scans the
  hosts table once and logs every hostname that fails validation in one warning. Separately, every
  render path (Butane/Ignition, Talos machineconfig, preseed/debianconfig) **strips control
  characters** (newlines included) from the host's hostname before substituting it. That closes
  injection for old rows without refusing to boot a host whose stored name is merely non-RFC
  (e.g. contains `_`).
- **debianconfig `hostname:` / `domain:`** (review decision 9): **no** save-time check, because the
  documented form is the template `hostname: "{{ .Hostname }}"` (`docs/CONFIGURATION.md:261`), which
  would fail RFC validation before templating, and the same config's `raw_preseed:` and
  `late_command:` already let its author emit any line. Instead, `translateDebianConfig` **refuses
  to render** (error → 500 on serve and preview, with the reason) when the *templated* hostname or
  domain contains a control character.

**Tests.** Upstream's table test; API 400 cases; machineconfig 400 and empty-passes cases; startup
scan lists invalid rows; a stored `"a\nb"` renders as `ab` in all three families; debianconfig with
`{{ .Hostname }}` saves and renders; one that templates to a newline refuses to render.

### Item 5. Auto-detect `--serverIP`

**Bug.** `cmd/main.go:183` defaults `--serverIP` to `127.0.0.1`, which is never right for a LAN
client and silently breaks every URL booty renders.

**Change.**
- Default becomes `""`. Port `upstream:pkg/config/config.go` `DetectServerIP` (UDP "dial" to
  `192.0.2.1:9`; no packet sent; returns the default-route source address).
- In `cmd/main.go`, beside the existing serverHttpPort resolution (~line 366): if empty, detect,
  `viper.Set`, and `slog.Info("serverIP autodetected", ...)`. Detection failure is a **startup
  error** naming `--serverIP` (maintainer: "fall back to error"; reaffirmed for Fable finding 9). The
  message says what to do: "no default route; set --serverIP to the address clients reach booty
  on". Release-note it: hosts or containers with no default route (e.g. `--network none`,
  isolated labs) that start today on 127.0.0.1 will refuse to start until `--serverIP` is set.
  Tests that start `main` inject the detector.
- An explicit value (flag or whatever path sets it today) always wins.
- `startProxyDHCP`'s loopback guard stays.

**Tests.** `DetectServerIP` returns a non-loopback IPv4 or an error; resolution leaves an
explicit value untouched; empty + failing detector → error (detector injected as a func var).

**Docs.** README configuration table and Quickstart (the `--serverIP` "set this!" note becomes
"auto-detected; set it on multi-homed hosts").

### Item 6. Embedded iPXE, built via `go generate`

**Today.** A 2023-era `undionly.kpxe` (68,907 bytes) sits at the repo root, is not embedded and not
in the image; no UEFI binary exists. Operators stage iPXE files into `dataDir` themselves.

**Decisions (maintainer).** Build iPXE from source with `go generate`; **commit the outputs** so a
plain `go build` works anywhere, air-gapped; embedded copies **take precedence** over `dataDir`.

**Change.**
- New Go package `boot/` at the repo root (package `boot`):
  - `boot/embed.ipxe` — the embedded script (below).
  - `boot/build-ipxe.sh` — port of `upstream:hack/build-ipxe.sh` (pinned `IPXE_REF=v2.0.0`,
    builds `bin/undionly.kpxe`, `bin-x86_64-efi/ipxe.efi`, `bin-x86_64-efi/snponly.efi` inside a
    throwaway `debian:stable-slim` container via podman or docker, `SOURCE_DATE_EPOCH` from the
    iPXE commit, verifies the embedded script is present, writes `VERSION` and `SHA256SUMS`).
  - `boot/boot.go` —
    `//go:generate ./build-ipxe.sh` and
    `//go:embed undionly.kpxe ipxe.efi snponly.efi` into `var Files embed.FS`, plus a
    `Lookup(name string) ([]byte, bool)` helper.
  - The three binaries, `VERSION`, `SHA256SUMS`, committed.
  - **Native mode** (review decision 10): `build-ipxe.sh` also runs without a container when
    `IPXE_NATIVE=1`, or automatically when neither podman nor docker can run a container, using the
    host's gcc, make, perl and liblzma. `VERSION` records which mode and toolchain built the bytes.
  - **First build** (review decision 10): built from source in the implementing session. Start
    `dockerd` and run `go generate ./boot/` with the container recipe; if the daemon cannot start
    there, use native mode. Upstream's prebuilt bytes are **not** copied. If QEMU is available,
    boot the result under OVMF and SeaBIOS before committing.
- `THIRD_PARTY_NOTICES.md` at the root: iPXE is GPL-2.0-or-later (with UBDL); state the source
  URL, tag, commit, and that the corresponding source is the pinned upstream tag. Port upstream's
  text.
- Delete the root `undionly.kpxe`.
- **TFTP** (`readHandler`): after the special names (`booty.ipxe`, `menu/…`, `pxelinux.cfg/default`)
  and **before** the `dataDir` lookup, serve `boot.Lookup(filename)` if present.
- **HTTP**: new `GET|HEAD /boot/{name}` serving only the embedded files, routed **before**
  `http.ServeMux` (a small wrapper, as `upstream:pkg/server/http.go:136-144` does; Fable finding
  6), because ServeMux would 301 unclean paths such as `/boot/../x`. `Content-Length`, no redirects, `application/efi` for `.efi` and `application/octet-stream`
  otherwise (shape from `upstream:pkg/server/http.go` `bootHandler`; no `/boot/sb/`).
- **proxyDHCP defaults** (`undionly.kpxe`, `ipxe.efi`) already match the embedded names; ARM64
  (`ipxe-arm64.efi`) is not built and still comes from `dataDir`.
- **Embedded script** (`boot/embed.ipxe`):
  ```
  #!ipxe
  :start
  dhcp || goto retry
  chain tftp://${proxydhcp/next-server}/booty.ipxe || chain tftp://${next-server}/booty.ipxe || chain http://${next-server}/booty.ipxe?mac=${mac} || goto shell
  exit
  :retry
  sleep 5
  goto start
  :shell
  echo Booty: could not reach ${next-server} - dropping to the iPXE shell
  shell
  ```
  `${proxydhcp/next-server}` comes first (Fable finding 3): in iPXE, `${next-server}` is taken
  from the highest-priority settings block, and when booty's proxyDHCP runs beside a site DHCP
  server that sets its own next-server, that may be the site server. Without proxyDHCP the first
  leg is empty and fails at once, and the second behaves as today. `build-ipxe.sh`'s
  embedded-script check greps for the new first `chain` line. **QEMU check before committing the
  binaries:** boot under OVMF and SeaBIOS in two topologies: (a) booty proxyDHCP beside a DHCP
  server that hands out a different next-server; (b) the site DHCP pointed at booty, no proxyDHCP.
  The HTTP fallback only reaches booty on port 80 (the port is unknown at iPXE build time). The
  TFTP leg is the normal path and item 7 makes it chain to HTTP on the right port.
- **Task**: `task ipxe` runs `go generate ./boot/` (item 13).
- **Drift guard** (review decision 5): a Go test in `boot/` hashes each file in the embedded FS and
  compares it with `SHA256SUMS`. It runs under `go test ./...`, so in the existing `go-test.yml`
  CI (kept by item 11) and in `task test`; `task check:ipxe` is a thin wrapper for `go test ./boot/`.
  Reproducing a byte-identical rebuild is a non-goal (gcc versions vary).

**Tests.** `boot.Lookup` finds all three and rejects others; TFTP serves the embedded copy even
when `dataDir` has a same-named file; `/boot/ipxe.efi` returns 200 with the right length and type,
`/boot/../x` and unknown names 404, POST 405.

### Item 7. Identify the booting host by MAC over HTTP (ARP fallback)

**Today.** `pkg/tftp/tftp.go:115` learns the MAC with `arping.Ping(raddr.IP)` on every TFTP read;
needs `CAP_NET_RAW`, fails across routers, and the whole per-host decision (dispatch, menu,
DoInstall flip) lives in the TFTP handler.

**DoInstall (Fable finding 2).** Nothing reads `DoInstall`. The flip at `pkg/tftp/tftp.go:125-134`
is its only writer outside the DB plumbing, and no script or renderer branches on it. Moving the
flip to `GET /booty.ipxe?mac=` would make it an unauthenticated write any LAN client could aim at
any host. **Remove the flip entirely.** Keep the column and field (no migration). Correct the
README's "One-shot install" concept, which describes behaviour booty does not have. Install-once,
if wanted, gets its own design.

**Change.**
- Extract the script renderers into an exported, transport-neutral entry point in `pkg/tftp`
  (keep the package; a rename is churn): `RenderBootScript(host *hardware.Host, urlHost string)`
  returning `(script, kind string)`, and `RenderMenuSelection(host, path, urlHost)`.
  `bootDispatch`, `assignedScript`, `renderMenu`, `holdingScript` are unchanged underneath.
- **New HTTP endpoints** on the base mux (unauthenticated, boot path, **read-only**):
  - `GET /booty.ipxe?mac=…` → identify host by `?mac=` (normalised with `hardware.NormalizeMAC`;
    malformed → treated as unidentified), else ARP on `RemoteAddr`; render via
    `RenderBootScript`; `text/plain`.
  - `GET /menu/{selection}/boot.ipxe?mac=…` → `RenderMenuSelection`.
- **TFTP `booty.ipxe` becomes a stub**:
  `#!ipxe` / `chain http://[[server]]/booty.ipxe?mac=${mac} || shell` (where `[[server]]` is
  `serverIP[:serverHttpPort]`). It does **no** ARP. The TFTP
  `menu/…/boot.ipxe` path stays working (ARP) for scripts already cached in clients, but nothing
  new links to it.
- All `chain tftp://…/booty.ipxe` and `chain tftp://…/menu/${sel}/boot.ipxe` lines in
  `holding.ipxe`, `menu.go` (lines 172, 184, 198, 202) and `tool_scripts.go` (69, 117, 149) become
  `chain http://[[server]]/…?mac=${mac}`. `renderMenu` currently takes the bare serverIP; it gets
  `urlHost` instead.
- `pxelinux.cfg/default` (legacy, non-iPXE) is unchanged and keeps ARP.
- ARP stays as the fallback everywhere (maintainer's decision); `CAP_NET_RAW` is still needed
  until a later change removes ARP. Document that `?mac=` is client-asserted: a LAN client can
  fetch another host's boot script, exactly as it can already fetch that host's
  `/ignition.json?mac=`. Both endpoints are read-only, so this adds no new write surface.

**Tests.** HTTP `/booty.ipxe` with `?mac=` for holding / assigned / menu hosts; no request on any
path changes a host row (`DoInstall` included); malformed MAC → holding script; every rendered
`chain` line now targets `http://…?mac=${mac}`; existing TFTP menu tests still pass.

### Item 8. TFTP hardening + `pin/tftp` v3

Item 0's allowlist moves onto the `os.Root` path here unchanged.

**Change.**
- `go.mod`: drop `replace github.com/pin/tftp => … v0.0.0-20210809155059-0161c5dd2e96`; require
  `github.com/pin/tftp/v3 v3.2.0`; update the import. Adjust to any v3 API differences
  (`OutgoingTransfer`, `RequestPacketInfo`, `SetBlockSize`, `EnableSinglePort`).
- Read files through an `*os.Root` opened once on `dataDir` in `StartTFTP` (`os.OpenRoot`, Go 1.24+),
  replacing `safeJoin` + `os.Open`. This also blocks symlink escapes that `safeJoin`'s comment
  admits it cannot. Keep the absolute-path rejection and log line.
- Reject filenames containing NUL and requests that resolve to a directory (`ErrNotExist` to
  the client, warn log).
- New flags `--tftpPort` (default 69) and `--tftpBlockSize` (default 512, the current hard-coded
  value; RFC 2348 negotiation still applies).
- `StartTFTP` returns `(*tftp.Server, error)`; listen errors go to an error channel the caller
  watches instead of `os.Exit(1)` inside the goroutine. `cmd/main.go` treats a TFTP failure as
  fatal (same outcome, testable).

**Tests.** Existing `safeJoin` tests become `os.Root` tests (`..`, absolute, symlink-out-of-root,
NUL, directory); a port-bind failure returns an error.

### Item 9. Wake-on-LAN (API + UI button)

**Change.**
- New `pkg/power/wol.go` + `broadcast_unix.go`: port `upstream:pkg/power/wol.go` (`MagicPacket`,
  `Waker`, `Destinations`, `Wake`) and `broadcast.go`. Stdlib only. Swap its
  `hardware.ErrInvalidMAC` reference for the fork's `hardware.NormalizeMAC`. Keep its behaviour:
  directed broadcast of every non-loopback IPv4 interface, 255.255.255.255, and the host's last
  known IP; a few rounds spaced apart; one refused broadcast is logged, not fatal.
- `POST /api/v1/hosts/{mac}/wake` (operationID `wake-host`) in `pkg/http/api_hosts.go`, behind the
  existing auth gate. 404 for unknown host; 200 with `{"sentTo": [...]}`; 502 if no destination
  could be reached.
- Hosts page (`web/src/views/HostsView.tsx`): a **Wake** row action with a short confirm, success
  / error toast. Matches existing row-action style.
- Docs: README feature list; `docs/schema/API.md`. Note WoL must be enabled in the NIC/firmware
  and booty must share the L2 segment (or directed broadcast must be routed).

**Tests.** Port upstream's `wol_test.go` (packet bytes, destinations); API 404/200 with a fake
`Waker`; UI test for the action calling the endpoint.

### Item 10. Node health + running-version reports (per-host credential)

**Scope.** Flatcar and Fedora CoreOS (Ignition) and Debian (debianconfig preseed). Talos and tools
are out of scope.

**Payload.** Port `upstream:pkg/hardware/health.go` `Health`, `DMI`, `Normalize` and its caps
(200 failed units, 50 journal lines × 300 chars, 8 KiB journal total, 256-char fields). Add nothing
autopilot-specific.

**Storage.** Migration `0010_host_health.sql`:
- `host_health(mac TEXT PRIMARY KEY REFERENCES hosts(mac) ON DELETE CASCADE, received_at TEXT,
  boot_id TEXT, running TEXT, kernel TEXT, firmware TEXT, failed_units TEXT /* JSON */,
  journal_errors TEXT /* JSON */, dmi TEXT /* JSON */)`. Latest report only; no history.
- `hosts.report_token_gen INTEGER NOT NULL DEFAULT 0`.

**Credential (maintainer chose per-host token).**
- A random 32-byte key at `<dataDir>/node-report-key` (0600), generated on first start, separate
  from the API token so rotating the API token does not orphan every node.
- Host token = hex `HMAC-SHA256(key, "booty-node-report\x00" + mac + "\x00" + gen)`.
- **Deviation from the option text, flagged for the maintainer:** the option said "stored hashed in
  SQLite". That cannot work: Flatcar/CoreOS PXE hosts re-fetch and re-render their config on every
  boot, so booty must be able to reproduce the token, which a stored hash cannot do. A derived
  token gives the same property (no plaintext token in the DB) with nothing to store but the
  generation counter.
- Rotation: `POST /api/v1/hosts/{mac}/report-token/rotate` bumps `gen` (authenticated). The
  host picks up the new token on its next config fetch (next boot for PXE hosts; a Debian install
  keeps its old token until reinstalled, so rotation there means re-install; documented).
- Honest limit, documented: the token is embedded in the host's rendered config, and those configs
  are served unauthenticated by `?mac=`. Anyone who can fetch a host's config can forge that host's
  reports. It still blocks the cheap attack (spoofing any host by MAC alone) and is no weaker than
  the config itself.

**Receiver.** `POST /node/report` on the base mux (outside `/api/v1`; nodes have no API token):
- Header `X-Booty-Node-Token`, query `?mac=`. Constant-time compare against the derived token.
  Unknown host or bad token → the **same** 401 with no detail.
- **Layered limits** (Fable finding 4), in this order:
  1. Cheap rejects first: malformed MAC or missing token header → 400/401 before any DB or HMAC
     work.
  2. Pre-auth limit **per source IP** in a **size-capped** map (LRU; e.g. 10 requests/IP/minute,
     4096 entries), so attacker-chosen MACs cannot grow memory.
  3. Token check against an **in-memory cache** of per-host `report_token_gen`, refreshed on
     rotate and on host create/delete, so verification needs no DB read.
  4. Only after the token verifies: per-MAC limit of one accepted report per 10 s → 429.
- `http.MaxBytesReader` 64 KiB; `Normalize`; upsert `host_health`; also update `hosts.last_seen`.

**Node side.**
- Script: port `upstream:pkg/ignition/health.go` `HealthReportScript`, changed to send the token
  header, post to `/node/report`, and **use a MAC baked into the rendered script**: `host.MAC`,
  the canonical MAC of the host record the config was rendered for, however it was identified
  (`?mac=` or ARP; Fable finding 8; an ARP-identified fetch has no `?mac=` to copy), instead of upstream's default-route
  NIC lookup. The token is derived from that MAC, so on a multi-NIC host the two would differ and
  every report would 401 (review decision 11). Unit: `HealthUnit`, minus the
  upstream `booted` unit dependency.
- **Ignition (Flatcar/CoreOS):** after Butane → Ignition translation in `pkg/http/ignition.go`
  (both the DB-bound path and the file-template path), merge **at the JSON level** (review
  decision 6: append to `storage.files` and `systemd.units`, which mean the same in every 3.x spec,
  so any operator-chosen spec version works) a fragment that writes
  `/opt/booty/health-report` (0755, contains server and token) and enables
  `booty-health.service`. A config that already defines a unit or file with the same name wins
  (operator override); log at debug. Validate the merged result with Ignition's own parser for its
  declared version before serving; on failure serve the un-merged config and warn.
- **Debian (debianconfig only):** `buildPreseedView` (`pkg/http/debiangen.go`) composes
  one more ordered `late_command` source that writes the script and unit with `in-target` and
  enables it. It goes **before the operator's own commands** (ssh → sudo → ESP sync → health →
  operator; Fable finding 7), keeping the existing "operator last" rule at `debiangen.go:509-511`,
  so the operator's commands can still adjust it. Parts are joined with ` ; `, so one failing does
  not stop the rest. Document that a `d-i preseed/late_command` written in `raw_preseed:` replaces
  the whole composed command, health install included. It also adds `curl` to the package list via `appendIfAbsent`. Raw preseed files (rung 4,
  `--preseedFile`) are **not** modified: debconf keeps only the last `late_command`, so appending
  would silently replace the operator's.
- **Opt-in** (review decision 2): `--nodeReports` flag, default **off**, so configs served by an
  existing deployment do not change on upgrade. When off, nothing is injected and `/node/report`
  returns 404. README and the Hosts page's empty Running column say how to turn it on.

**API + UI.**
- `GET /api/v1/hosts` and `GET /api/v1/hosts/{mac}` gain an optional `health` object (running,
  kernel, firmware, receivedAt, failedUnits count, dmi vendor/product). Full journal lines only on
  the single-host GET.
- Hosts page: **Running** column (running version, plus a muted "no report yet" for in-scope OSes
  that have never reported), a **Last report** relative time, and a failed-units badge. Host detail
  drawer shows failed units and journal excerpt.

**Tests.** Token derivation and rotation; receiver 401 / 413 / 429 / 200 paths; normalisation
caps (ported); Ignition output contains the unit and file and respects an operator override;
debianconfig late_command contains the fragment after the operator's; raw preseed untouched;
script test with stubs (port upstream's harness if it fits, else a golden of the rendered script).

### Item 11. Remove the publish workflows

Delete `.github/workflows/docker-publish.yml` (pins `cosign-installer@main` and
`slsa-github-generator@main`, uses deprecated `::set-output`) and `.github/workflows/image.yml`.
Keep `go-test.yml` and `web-test.yml` untouched. No dependabot (maintainer: CI is fixed/added
later). README "Building from source" no longer implies published images are built by CI; the
`ghcr.io/jacaudi/booty:latest` Quickstart line gets a note that images are currently built
locally (`task image`).

### Item 12. Go dependency bumps

- Remove the dead `replace github.com/flatcar-linux/container-linux-config-transpiler` (no
  importer, not in `go.sum`).
- Bump: `spf13/viper` 1.10.1 → 1.21.x, `coreos/butane` 0.19.0 → 0.29.x, `coreos/ignition/v2`
  2.17.0 → 2.27.x, `joho/godotenv` 1.4 → 1.5.1, `buger/jsonparser` → 1.6.1. Then `go mod tidy`.
- Butane/Ignition: the fork imports
  `github.com/coreos/ignition/v2/config/v3_5_experimental/types` (`pkg/http/ignition.go:15`).
  That package no longer exists in newer Ignition (it has `v3_5` stable and
  `v3_6_experimental`), so the import must change. Use **`config/v3_4/types`** for the hand-built
  reboot config (`ignition.go:129`), matching the `3.4.0` it declares so the two cannot drift
  (review decision 6). Operator Butane output keeps whatever spec its `variant`/`version` produce.
  No emitted spec version changes.
- Viper 1.21 drops HCL/INI/old mapstructure; check flag binding, duration parsing and any
  `Unmarshal` call for behaviour changes.

**Tests.** Full `go test -race ./...` and `go vet ./...` green; golden Ignition outputs unchanged.

### Item 13. Taskfile (replaces Makefile) and Dockerfile

**Taskfile.** Delete `Makefile`; add **`taskfile.yml`** (lowercase, repo root; go-task v3 schema).
Tasks:
- `web` — `npm ci && npm run build` in `web/`.
- `build` — depends on `web`; `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version={{.VERSION}} -X main.timestamp={{.TIMESTAMP}}" -o bin/booty ./cmd`
  (`VERSION` from `git describe --tags --always --dirty`, `TIMESTAMP` from the HEAD commit time).
- `run` — `build` then `bin/booty --dataDir=data/`.
- `test` — `go test -race ./...` and `npm test` in `web/`.
- `vet`, `fmt` (`gofmt -l` fails if non-empty), `clean`.
- `ipxe` — `go generate ./boot/`; `check:ipxe` — verify `SHA256SUMS`.
- `image`, `image:push`, `image:buildx` (`linux/amd64,linux/arm64`).
- `image:run` — least privilege: `--cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW`
  (NET_RAW for the ARP fallback), `--read-only` not set (dataDir is a volume).

Update README, `docs/development.md` and `docs/standards.md` references from `make …` to
`task …`, and state how to install go-task.

**Dockerfile.**
- Web stage: `npm ci` instead of `npm install`.
- Go stage: copy `go.mod`/`go.sum` and `go mod download` before `COPY . .` for layer caching.
- Add `-s -w` to ldflags.
- Final stage: OCI labels (`org.opencontainers.image.source`, `.version`, `.revision`,
  `.licenses`), `EXPOSE 8080/tcp 69/udp`.
- Final image becomes **`gcr.io/distroless/static-debian12`** (root user; review decision 7). The
  binary is pure Go (`CGO_ENABLED=0`, `modernc.org/sqlite`), so `base`'s glibc and OpenSSL were
  unused. Keep the no-default `TARGETOS`/`TARGETARCH` handling. Update `docs/standards.md` §1.1,
  which names the base image.
- The iPXE binaries are committed, so the image build needs no iPXE toolchain.

### Item 14. Vendor obra/superpowers skills

Copy `obra/superpowers` `skills/*` into `.claude/skills/` at commit `bb92a77`, with its `LICENSE`
(MIT) as `.claude/skills/SUPERPOWERS-LICENSE` and a short `.claude/skills/README.md` naming the
source repo and commit. No edits to the skill files. Lands in the same push as this design doc.

---

## 3. Constraints

- Go 1.26 toolchain (already in `go.mod`); `os.OpenRoot` is available.
- Pure Go, `CGO_ENABLED=0`.
- No new runtime dependencies beyond `pin/tftp/v3`. WoL, health, HMAC are stdlib.
- Code style follows the surrounding fork code: `log/slog`, comment density and the existing
  "why, not what" comment voice, huma operations in `pkg/http/api_*.go`, migrations numbered in
  `pkg/db/migrations/`.
- Ported upstream code (MIT, same licence and copyright holder as the fork) keeps a one-line provenance comment
  naming the upstream file and commit.

## 4. Execution shape

Separate commits (or small PRs), in this order, each green on `go test -race ./...`, `go vet`, and
`npm test`:

1. Design doc + vendored skills (item 14). *(Done.)*
2. **Item 0 (TFTP allowlist security fix)**, standalone, with its release note.
3. Items 3 + 8 (TFTP v3, OpenRoot, nil write handler).
4. Item 1 (proxyDHCP, including the `insomniacslk/dhcp` bump).
5. Items 2, 4, 5 (small bug fixes; one commit each).
6. Item 6 (embedded iPXE).
7. Item 7 (HTTP boot script, MAC identification, DoInstall flip removed). Depends on 6.
8. Item 12 (deps).
9. Item 9 (WoL).
10. Item 10 (node reports). Largest; may split into receiver+storage, Ignition, Debian, UI.
11. Items 11 + 13 (workflows, Taskfile, Dockerfile, doc updates).

## 5. Left to the builder

- Exact UI placement and wording, within existing HostsView patterns.
- Whether `RenderBootScript` lives in `pkg/tftp` or a new small package, as long as TFTP and HTTP
  share one implementation.
- v3 `pin/tftp` API adaptation details.
- Exact Taskfile variable plumbing.

## 6. Decisions confirmed in review (2026-10-11)

Each was put to the maintainer one at a time with its reasoning, pros and cons, and alternatives.

1. **Item 10 token:** HMAC-derived from a key file, the MAC and a per-host counter. Nothing secret
   in SQLite; rotate by bumping the counter. (Replaces "stored hashed", which cannot work for hosts
   that re-render their config each boot.)
2. **Item 10 default:** `--nodeReports` is **off** by default. Debian injection is limited to
   debianconfig-kind configs; raw preseed files are never modified.
3. **Item 1, missing option 93:** **drop** the packet, as upstream does, with a debug log. A
   behaviour change for old ROMs; release-noted.
4. **Item 4, existing invalid hostnames:** one startup warning listing them, and control characters
   stripped from hostnames at render time.
5. **Item 6 drift guard:** a Go test in `boot/` comparing the embedded files to `SHA256SUMS`, run
   by the existing `go-test.yml` CI.
6. **Item 12 Ignition:** `config/v3_4/types` for the reboot config; item 10's fragment merged at the
   JSON level and validated before serving.
7. **Item 13 base image:** `gcr.io/distroless/static-debian12`, still root.
8. **Item 1 replies:** byte-identical to upstream's (OFFER carries no bootfile, option 66 or
   `sname`); upstream's 9 golden `.hex` files copied unchanged as a parity check.
9. **Item 4, debianconfig hostname:** no save-time check; refuse to render when the templated
   hostname or domain contains a control character.
10. **Item 6 first build:** from source in the implementing session, via `dockerd` + `go generate`,
    falling back to a new native mode in `build-ipxe.sh`. Upstream's bytes are not copied.
11. **Item 10 report MAC:** baked into the script at render time (the boot NIC's `?mac=`).

Smaller calls still left to the builder: the per-MAC report rate limit (one per 10 s, else 429);
an operator-defined unit or file of the same name wins over the injected health fragment; INFORM
packets are dropped (as upstream).

## 7. Fable design review (2026-10-11), findings resolved with the maintainer

1. **Blocking: TFTP serves `api-token`, `booty.db` and `config/`.** New item 0: an allowlist that
   mirrors HTTP `/data/`, landing first as a standalone security fix.
2. **DoInstall flip over spoofable `?mac=`.** The flip is removed; nothing reads the flag.
3. **`${next-server}` scope.** The embedded script tries `${proxydhcp/next-server}` first; QEMU
   check in both topologies before committing binaries.
4. **`/node/report` DoS.** Layered limits: cheap rejects, bounded per-IP pre-auth limit, cached
   counters, per-MAC limit only after the token verifies.
5. **Golden parity vs library version.** `insomniacslk/dhcp` bumped inside item 1; upstream's
   `withDefaults()` ported.
6. **`/boot/` and ServeMux redirects.** `/boot/` is routed before the mux.
7. **Debian late_command order.** The health fragment goes before the operator's commands; the
   `raw_preseed` override is documented.
8. **Baked MAC source.** `host.MAC`, not the `?mac=` query value.
9. **No default route.** Startup error kept; actionable message, release note, injected detector
   in tests.
