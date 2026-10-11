# Design: import the worthwhile parts of upstream `jeefy/booty`

**Status:** design, awaiting approval. Nothing below is built yet.
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

### Item 1. ProxyDHCP protocol fix (full)

**Bug.** `pkg/proxydhcp/proxydhcp.go:75-78` answers DISCOVER *and* REQUEST/INFORM on **both**
UDP/67 and UDP/4011. A PXE client broadcasts its DHCPREQUEST for the *real* server's lease on 67;
booty ACKs it with `yiaddr=0` and a bootfile, EDK2/OVMF firmware logs "Lease confirmed isn't the
same as that in the offer", sends DHCPDECLINE, and the boot fails (upstream `d7d92a7`, verified on
the wire with OVMF).

**Change.** Rewrite `buildReply` as `buildReply(req, cfg, localPort)` following
`upstream:pkg/dhcp/pxe.go` `handle()`:
- DISCOVER answered **only on 67** with an OFFER carrying PXE option 43 (no bootfile needed;
  keep it anyway, harmless and helps non-conforming ROMs **(builder's call)**).
- REQUEST answered **only on 4011** with an ACK carrying the bootfile; `ciaddr` echoed.
- INFORM: dropped (upstream drops it; nothing in the PXE flow needs it).
- Drop packets with `giaddr` set unless a new `--proxyDHCPRelay` flag (default false) is on.
- Drop packets whose server-identifier (option 54) names another server.
- Option 43: OFFER gets upstream's `offerVendorOptions` (discovery control `0x07`, boot-server
  list with booty only, one-entry menu "Booty", zero-timeout prompt); ACK gets
  `ackVendorOptions` (boot item). Port verbatim with attribution.
- iPXE detection: port `hasUserClass` (raw and RFC 3004 forms) to replace the substring check.
- **Keep the fork's behaviour** of treating a missing option 93 as BIOS (upstream drops such
  packets). Old BIOS ROMs omit it. **(builder's call)**
- `handle()` passes the listener's port; `loop` already knows which conn it reads.

**Tests.** Port `upstream:pkg/dhcp/golden_test.go`'s `goldenCases`: synthetic request packets
built in code for 9 PXE/iPXE cases (3 DISCOVER, 6 REQUEST: BIOS, EFI x64, EFI BC, ARM64, iPXE
raw user-class, iPXE RFC 3004). Upstream's `.hex` files are *its own replies* and will not match
the fork's (bootfile kept on the OFFER, option 66/`sname` set, missing option 93 → BIOS), so
**regenerate the fork's own `.hex` files with `-update`** and commit them as regression guards
**(builder's call)**. The protocol behaviour that matters is asserted directly by table tests: REQUEST on 67 → silent; DISCOVER on 4011 → silent; giaddr with/without
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
- Call it wherever a hostname enters through `/api/v1`: `create-host` (POST `/hosts`),
  `put-host` (PUT `/hosts/{mac}`), and the `hostname:` field of a **debianconfig** body on config
  create/update (`pkg/http/debiangen.go:21,39`; it reaches `d-i netcfg/get_hostname` at
  `debiangen.go:549`) and of the preseed→debianconfig converter (`preseedconv.go:23,38`)
  **(builder's call: same injection risk, same fix)**. Cluster-member add takes no hostname
  (`api_clusters.go:41` is an output struct). Empty stays allowed (means "unset"). Invalid → 400 via the
  existing huma error path.
- `/machineconfig`: a **non-empty** `?hostname=` that fails validation → **400** (maintainer's
  decision). Empty is still allowed (DHCP often gives none).
- Existing rows are not migrated or rewritten. A stored invalid hostname keeps rendering, with a
  `slog.Warn` at render time so operators can find it **(builder's call)**.

**Tests.** Upstream's table test; API 400 cases; machineconfig 400 and empty-passes cases.

### Item 5. Auto-detect `--serverIP`

**Bug.** `cmd/main.go:183` defaults `--serverIP` to `127.0.0.1`, which is never right for a LAN
client and silently breaks every URL booty renders.

**Change.**
- Default becomes `""`. Port `upstream:pkg/config/config.go` `DetectServerIP` (UDP "dial" to
  `192.0.2.1:9`; no packet sent; returns the default-route source address).
- In `cmd/main.go`, beside the existing serverHttpPort resolution (~line 366): if empty, detect,
  `viper.Set`, and `slog.Info("serverIP autodetected", ...)`. Detection failure is a **startup
  error** naming `--serverIP` (maintainer: "fall back to error").
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
  - The three binaries, `VERSION`, `SHA256SUMS`, committed. **First commit (builder's call):**
    run `go generate ./boot/` if podman or docker is available on the build host. If not, copy
    upstream's committed `boot/` outputs, which were built by the same script from the same
    `embed.ipxe` and `v2.0.0` tag, keeping upstream's `VERSION` (it records the iPXE commit, builder
    image and make line) plus a line naming the upstream commit they came from. The next
    `go generate` replaces them.
- `THIRD_PARTY_NOTICES.md` at the root: iPXE is GPL-2.0-or-later (with UBDL); state the source
  URL, tag, commit, and that the corresponding source is the pinned upstream tag. Port upstream's
  text.
- Delete the root `undionly.kpxe`.
- **TFTP** (`readHandler`): after the special names (`booty.ipxe`, `menu/…`, `pxelinux.cfg/default`)
  and **before** the `dataDir` lookup, serve `boot.Lookup(filename)` if present.
- **HTTP**: new `GET|HEAD /boot/{name}` on the base mux serving only the embedded files, with
  `Content-Length`, no redirects, `application/efi` for `.efi` and `application/octet-stream`
  otherwise (shape from `upstream:pkg/server/http.go` `bootHandler`; no `/boot/sb/`).
- **proxyDHCP defaults** (`undionly.kpxe`, `ipxe.efi`) already match the embedded names; ARM64
  (`ipxe-arm64.efi`) is not built and still comes from `dataDir`.
- **Embedded script** (`boot/embed.ipxe`):
  ```
  #!ipxe
  :start
  dhcp || goto retry
  chain tftp://${next-server}/booty.ipxe || chain http://${next-server}/booty.ipxe?mac=${mac} || goto shell
  exit
  :retry
  sleep 5
  goto start
  :shell
  echo Booty: could not reach ${next-server} - dropping to the iPXE shell
  shell
  ```
  The HTTP fallback only reaches booty on port 80 (the port is unknown at iPXE build time). The
  TFTP leg is the normal path and item 7 makes it chain to HTTP on the right port.
- **Task**: `task ipxe` runs `go generate ./boot/` (item 13).
- **Drift guard**: there is no CI to run it in after item 11, so `task check:ipxe` verifies
  `sha256sum -c SHA256SUMS` locally and fails if a binary does not match. Reproducing a byte-identical
  rebuild is a non-goal (gcc versions vary); the guard is that the committed bytes match their sums.

**Tests.** `boot.Lookup` finds all three and rejects others; TFTP serves the embedded copy even
when `dataDir` has a same-named file; `/boot/ipxe.efi` returns 200 with the right length and type,
`/boot/../x` and unknown names 404, POST 405.

### Item 7. Identify the booting host by MAC over HTTP (ARP fallback)

**Today.** `pkg/tftp/tftp.go:115` learns the MAC with `arping.Ping(raddr.IP)` on every TFTP read;
needs `CAP_NET_RAW`, fails across routers, and the whole per-host decision (dispatch, menu,
DoInstall flip) lives in the TFTP handler.

**Change.**
- Extract the script renderers into an exported, transport-neutral entry point in `pkg/tftp`
  (keep the package; a rename is churn): `RenderBootScript(host *hardware.Host, urlHost string)`
  returning `(script, kind string)`, and `RenderMenuSelection(host, path, urlHost)`.
  `bootDispatch`, `assignedScript`, `renderMenu`, `holdingScript` are unchanged underneath.
- **New HTTP endpoints** on the base mux (unauthenticated, boot path, read-only apart from the
  DoInstall flip that TFTP already does unauthenticated today):
  - `GET /booty.ipxe?mac=…` → identify host by `?mac=` (normalised with `hardware.NormalizeMAC`;
    malformed → treated as unidentified), else ARP on `RemoteAddr`; render via
    `RenderBootScript`; `text/plain`. **The DoInstall flip moves here.**
  - `GET /menu/{selection}/boot.ipxe?mac=…` → `RenderMenuSelection`.
- **TFTP `booty.ipxe` becomes a stub**:
  `#!ipxe` / `chain http://[[server]]/booty.ipxe?mac=${mac} || shell` (where `[[server]]` is
  `serverIP[:serverHttpPort]`). It does **no** ARP and **no** DoInstall flip. The TFTP
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
  `/ignition.json?mac=`. No new exposure.

**Tests.** HTTP `/booty.ipxe` with `?mac=` for holding / assigned / menu hosts; DoInstall flips on
the HTTP fetch and **not** on the TFTP stub fetch; malformed MAC → holding script; every rendered
`chain` line now targets `http://…?mac=${mac}`; existing TFTP menu tests still pass.

### Item 8. TFTP hardening + `pin/tftp` v3

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
  Unknown host or bad token → 401 with no detail.
- `http.MaxBytesReader` 64 KiB; `Normalize`; upsert `host_health`; also update `hosts.last_seen`.
- Per-MAC rate limit: at most one accepted report per 10 s; extra → 429 **(builder's call)**.

**Node side.**
- Script: port `upstream:pkg/ignition/health.go` `HealthReportScript`, changed to send the token
  header, post to `/node/report`, and **use a MAC baked into the rendered script** (the
  `?mac=` the config was fetched with, i.e. the booting NIC) instead of upstream's default-route
  NIC lookup. The token is derived from that MAC, so on a multi-NIC host the two would differ and
  every report would 401 **(builder's call)**. Unit: `HealthUnit`, minus the
  upstream `booted` unit dependency.
- **Ignition (Flatcar/CoreOS):** after Butane → Ignition translation in `pkg/http/ignition.go`
  (both the DB-bound path and the file-template path), merge an Ignition fragment that writes
  `/opt/booty/health-report` (0755, contains server and token) and enables
  `booty-health.service`. A config that already defines a unit or file with the same name wins
  (operator override); log at debug.
- **Debian (debianconfig only):** `buildPreseedView` (`pkg/http/debiangen.go`) composes
  one more ordered `late_command` source that writes the script and unit with `in-target` and
  enables it; adds `curl` to the package list via `appendIfAbsent`. Raw preseed files (rung 4,
  `--preseedFile`) are **not** modified: debconf keeps only the last `late_command`, so appending
  would silently replace the operator's.
- **Opt-out:** `--nodeReports` flag, default **on**. When off, nothing is injected and
  `/node/report` returns 404.

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
  `v3_6_experimental`), so the bump **must** change the import to `config/v3_5/types`. The
  hand-built reboot config (`ignition.go:129`) keeps declaring version `3.4.0`; operator Butane
  output keeps whatever spec its `variant`/`version` produce. No spec change is intended
  **(builder's call)**.
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
- Keep `gcr.io/distroless/base-debian12` and the no-default `TARGETOS`/`TARGETARCH` handling.
  (Moving to `distroless/static` is possible since sqlite is pure Go, but not confirmed; leave it.)
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

1. Design doc + vendored skills (item 14).
2. Items 3 + 8 (TFTP v3, OpenRoot, nil write handler).
3. Item 1 (proxyDHCP).
4. Items 2, 4, 5 (small bug fixes; one commit each).
5. Item 6 (embedded iPXE).
6. Item 7 (HTTP boot script, MAC identification). Depends on 6 for the embedded script.
7. Item 12 (deps).
8. Item 9 (WoL).
9. Item 10 (node reports). Largest; may split into receiver+storage, Ignition, Debian, UI.
10. Items 11 + 13 (workflows, Taskfile, Dockerfile, doc updates).

## 5. Left to the builder

- Exact UI placement and wording, within existing HostsView patterns.
- Whether `RenderBootScript` lives in `pkg/tftp` or a new small package, as long as TFTP and HTTP
  share one implementation.
- v3 `pin/tftp` API adaptation details.
- Exact Taskfile variable plumbing.

## 6. Calls made without the maintainer (please check)

1. Item 10 token is **HMAC-derived with a per-host generation**, not "stored hashed" (see item 10).
2. Item 10 health reporting is **on by default** (`--nodeReports`), with Debian limited to
   debianconfig-kind configs.
3. Item 1 keeps treating a missing option 93 as BIOS instead of dropping the packet.
4. Item 4 leaves existing invalid hostnames in place and only warns.
5. Item 6 drift guard is a local `task check:ipxe` (no CI after item 11).
6. Item 12 moves the import to `config/v3_5/types` but changes no emitted spec version.
8. Item 1 golden `.hex` files are regenerated against the fork, not copied from upstream.
9. Item 4 also validates the debianconfig `hostname:` field.
10. Item 6's first commit may copy upstream's prebuilt iPXE bytes if no container runtime is
    available here.
11. Item 10's script uses a MAC baked in at render time.
7. Item 13 keeps the distroless **base** image.
