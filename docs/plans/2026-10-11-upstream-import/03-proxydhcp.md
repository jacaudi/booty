# Phase 3: proxyDHCP protocol fix (item 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first.

**Goal:** booty's proxyDHCP answers DISCOVER only on 67 and REQUEST only on 4011, matches upstream's replies byte for byte, gives iPXE clients `booty.ipxe` on the OFFER, ignores packets without option 93, and handles relayed packets correctly when `--proxyDHCPRelay` is set.

**Architecture:** Port upstream's pure protocol function `handle` and its helpers into a new file `pkg/proxydhcp/pxe.go`; the existing socket layer (`proxydhcp.go`) keeps its lifecycle but takes ports from `Config`, passes the listener's configured port into `handle`, and sends to `replyDestination`.

**Tech Stack:** `github.com/insomniacslk/dhcp` (current pin; no bump needed in this phase, design §9.1).

**Spec:** `docs/designs/2026-10-11-upstream-import-design.md` item 1, §8.3, §8.5, §9.1.

## Global Constraints

See `README.md`. Upstream source: `upstream/main` @ `8a5a227` (the `upstream` remote must exist: see README "Prerequisites").

## Review Focus

1. A REQUEST arriving on the 67 listener (the client confirming its real lease) must get **no** reply. Ported test `TestRequestOnPort67IsIgnored`.
2. An iPXE DISCOVER must get an OFFER with `file` and option 67 `booty.ipxe`; a firmware DISCOVER must not. Task 2 tests.
3. A relayed DISCOVER with `--proxyDHCPRelay` on must be sent to `giaddr:67`, never broadcast locally. Ported `TestReplyDestination` + Task 3 socket test.
4. A packet with `giaddr` set and relay off must be ignored. Ported `TestRelayedPacketsNeedRelayFlag`.
5. Option 93 missing → silent. Ported `TestSilentWithoutArchitecture`.

---

### Task 1: Port the protocol core and golden tests

**Files:**
- Create: `pkg/proxydhcp/pxe.go`, `pkg/proxydhcp/pxe_test.go`, `pkg/proxydhcp/golden_test.go`
- Create: `pkg/proxydhcp/testdata/golden/*.hex` (9 files, copied unchanged)
- Delete: `pkg/proxydhcp/proxydhcp_test.go` tests for removed functions (Task 3 rewrites the file)

**Interfaces:**
- Produces (package `proxydhcp`):
  - `type Config struct { ServerIP net.IP; Relay bool; BIOSFile, EFIFile, ARM64File, IPXEScript string; Port67, Port4011 int }`
  - constants `DefaultBIOSFile = "undionly.kpxe"`, `DefaultEFIFile = "ipxe.efi"`, `DefaultARM64File = "ipxe-arm64.efi"`, `DefaultIPXEScript = "booty.ipxe"`
  - `func (c Config) withDefaults() Config`, `func (c Config) validate() error`
  - `func handle(pkt *dhcpv4.DHCPv4, c Config, localPort int) (*dhcpv4.DHCPv4, bool)`
  - `func replyDestination(pkt *dhcpv4.DHCPv4, src *net.UDPAddr) *net.UDPAddr`

- [ ] **Step 1: Copy the golden files unchanged.**

```bash
mkdir -p pkg/proxydhcp/testdata/golden
for f in discover-bios discover-efi-x64 discover-efi-bc request-bios request-efi-x64 request-efi-bc request-efi-arm64 request-ipxe-userclass request-ipxe-rfc3004; do
  git show upstream/main:pkg/dhcp/testdata/golden/$f.hex > pkg/proxydhcp/testdata/golden/$f.hex
done
git show upstream/main:pkg/dhcp/testdata/golden/discover-bios.hex | sha256sum
sha256sum pkg/proxydhcp/testdata/golden/discover-bios.hex
```

Expected: the two sha256 lines are equal.

- [ ] **Step 2: Create `pkg/proxydhcp/pxe.go`** from `git show upstream/main:pkg/dhcp/pxe.go`, with exactly these edits:
  - Line 1: `package proxydhcp`. Add header comment `// Ported from jeefy/booty pkg/dhcp/pxe.go @ 454c865 (MIT).`
  - Imports: remove `"sync"`.
  - Delete the constants `httpClientPrefix` and `maxHTTPBootReply` (33-40) and `DefaultHTTPBootEFIFile`, `DefaultHTTPBootARM64File`, `SnponlyHTTPBootEFIFile` (49-54).
  - In `type Config`, delete `Listen` (62), `HTTPBoot`, `HTTPBootBase`, `HTTPBootEFIFile`, `HTTPBootARM64File`, `HostHTTPBoot` (71-87) and their comments.
  - In `withDefaults`, delete the HTTP Boot defaults (106-111).
  - In `handle`, delete the `if isHTTPBootClient(pkt) { ... }` block (145-150).
  - Delete `isHTTPBootClient`, `httpBootDisabledOnce`, `handleHTTPBoot`, `httpBootOffer` and `httpBootFileForArch` (257-367).
  - Keep `replyDestination` (444-458), which upstream defines in `pxe.go`.
  - **iPXE OFFER (design §8.3).** In `buildOffer`, after the `reply.UpdateOption(... offerVendorOptions ...)` line, add:

```go
	// iPXE never sends the 4011 REQUEST when the offer carries a boot menu
	// (iPXE net/udp/dhcp.c:307-320), so it would get no filename from us.
	// Booty's embedded iPXE does not need one, but any other iPXE does.
	if hasUserClass(pkt, ipxeUserClass) {
		reply.BootFileName = c.IPXEScript
		reply.UpdateOption(dhcpv4.OptBootFileName(c.IPXEScript))
	}
```

  `baseReply` already sets `siaddr` via `dhcpv4.WithServerIP`, so nothing else is needed.
  - **Missing option 93 log (decision 3).** Upstream logs `"ProxyDHCP ignoring PXE packet without client architecture"` at debug level, and its `log` value already carries the MAC. Keep that unchanged.
- [ ] **Step 3: Create `pkg/proxydhcp/pxe_test.go`** from `git show upstream/main:pkg/dhcp/pxe_test.go`, with these edits:
  - `package proxydhcp`, plus the provenance comment `// Ported from jeefy/booty pkg/dhcp/pxe_test.go @ d7d92a7 (MIT).`
  - In `portFor`, replace `pkt.MessageType() == dhcpv4.MessageTypeRequest && !isHTTPBootClient(pkt)` with `pkt.MessageType() == dhcpv4.MessageTypeRequest`.
  - Keep: the helpers (lines 14-81) and these tests: `TestDiscoverProducesPXEOffer`, `TestRequestAckBootFileByArch`, `TestRequestCopiesClientFields`, `TestSilentWithoutClassIdentifier`, `TestSilentForNonPXEClass`, `TestSilentWithoutArchitecture`, `TestRelayedPacketsNeedRelayFlag`, `TestSilentForOtherServersAndOwnReplies`, `TestIPXEUserClassGetsScriptNotBinary`, `TestConfigFileOverrides`, `TestReplyDestination`, `TestParseSubOptionsRejectsMalformed`, `TestOfferVendorOptionsLayout`, `TestRequestOnPort67IsIgnored`.
  - Delete: `TestParsePorts`, `TestStartRejectsBadServerIP`, `TestStartServesDiscoverAndShutsDown`, and any helper only they use. The compiler and `go vet` report helpers left unused.
  - `TestDiscoverProducesPXEOffer` asserts that a firmware OFFER has no boot file (upstream `pxe_test.go:115-116`). Its packets carry no user class, so it stays valid unchanged and already covers "firmware OFFER has no file".
- [ ] **Step 4: Add the iPXE OFFER test** to `pkg/proxydhcp/pxe_test.go`:

```go
func TestIPXEDiscoverOfferCarriesScript(t *testing.T) {
	for name, mod := range map[string]dhcpv4.Modifier{
		"raw":     dhcpv4.WithUserClass("iPXE", false),
		"rfc3004": dhcpv4.WithUserClass("iPXE", true),
	} {
		t.Run(name, func(t *testing.T) {
			reply := mustHandle(t, pxePacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64, mod), testConfig())
			if reply.MessageType() != dhcpv4.MessageTypeOffer {
				t.Fatalf("type = %v, want OFFER", reply.MessageType())
			}
			if reply.BootFileName != "booty.ipxe" || reply.BootFileNameOption() != "booty.ipxe" {
				t.Errorf("file = %q / opt67 = %q, want booty.ipxe", reply.BootFileName, reply.BootFileNameOption())
			}
			if !reply.ServerIPAddr.Equal(testServerIP) {
				t.Errorf("siaddr = %v, want %v", reply.ServerIPAddr, testServerIP)
			}
		})
	}
}

```

- [ ] **Step 5: Create `pkg/proxydhcp/golden_test.go`** from `git show upstream/main:pkg/dhcp/golden_test.go`:
  - `package proxydhcp`, plus the provenance comment `// Ported from jeefy/booty pkg/dhcp/golden_test.go @ f25ad4c (MIT).`
  - Add one entry to `goldenCases`: `"discover-ipxe-userclass": pxePacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64, xid, guid, dhcpv4.WithUserClass("iPXE", false)),`
  - Add a comment above it: `// Fork-only: upstream's OFFER carries no file for iPXE (design §8.3).`
- [ ] **Step 6:** At this point the old `pkg/proxydhcp/proxydhcp.go` still defines a different `Config`, which conflicts. Do Task 2 Steps 1-2 before running anything. Do not commit yet.

### Task 2: Adapt the socket layer

**Files:**
- Modify: `pkg/proxydhcp/proxydhcp.go`
- Rewrite: `pkg/proxydhcp/proxydhcp_test.go`

**Interfaces:**
- Consumes: Task 1's `Config`, `handle`, `replyDestination`.
- Produces: `func NewServer(cfg Config) (*Server, error)`, `(*Server).Start() error`, `(*Server).Shutdown()`. The signatures are unchanged and the `Config` shape is new. The unexported method `(*Server).respond(conn *net.UDPConn, src *net.UDPAddr, req *dhcpv4.DHCPv4, port int)` replaces `(*Server).handle`.

- [ ] **Step 1: Edit `pkg/proxydhcp/proxydhcp.go`:**
  - Delete `bootfileIPXEScript`, the old `Config`, `selectBootfile`, `buildReply`, `pxeVendorOptions`, `isIPXE`, `clientArch` (lines 21-134), and the now-unused `iana` import.
  - Update the package doc comment (lines 1-8): pass 1 is the firmware DISCOVER on 67 (answered with an OFFER pointing at booty) followed by its REQUEST on 4011 (answered with the bootfile); iPXE clients get `booty.ipxe` on the OFFER.
  - `NewServer(cfg)`: `cfg = cfg.withDefaults()`; `if err := cfg.validate(); err != nil { return nil, err }`. Remove the "bootfile names must all be set" check.
  - `Start()`: listen on `cfg.Port67` and `cfg.Port4011` instead of the literals 67 and 4011. Call `enableBroadcast` on **both** conns, since `replyDestination` may broadcast from either. Update the "proxyDHCP listening" log to include `"relay", s.cfg.Relay` and the file names from the new fields.
  - `loop(conn *net.UDPConn, port int)`: replace the `broadcast bool` parameter. `Start` calls `s.loop(conn67, s.cfg.Port67)` and `s.loop(conn4011, s.cfg.Port4011)`. Pass the **configured** port, not the bound one, so `handle`'s port gating matches the configuration.
  - Rename `handle(conn, src, req, broadcast)` to `respond(conn, src, req, port)`. Body: `resp, ok := handle(req, s.cfg, port)`; return if not ok; `dst := replyDestination(req, src)`; `conn.WriteToUDP(resp.ToBytes(), dst)`; then `slog.Info("proxyDHCP reply", "client", req.ClientHWAddr.String(), "type", resp.MessageType().String(), "to", dst.String(), "file", resp.BootFileName)`.
- [ ] **Step 2: Rewrite `pkg/proxydhcp/proxydhcp_test.go`** with these tests:
  - `TestNewServerThenShutdown`: keep the existing test (lines 125-141) with `NewServer(Config{ServerIP: net.IPv4(192, 0, 2, 10)})`.
  - `TestNewServerRequiresServerIP`: `NewServer(Config{})` returns a non-nil error.
  - `TestRespondSendsRequestAckToSource`, adapted from the existing `TestHandleWritesReplyToSource` (lines 148-170):
    - `s := &Server{cfg: Config{ServerIP: net.IPv4(127, 0, 0, 1)}.withDefaults(), done: make(chan struct{})}`;
    - the request is `pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64)`;
    - call `s.respond(srvConn, cliAddr, req, s.cfg.Port4011)`;
    - expect an ACK whose `BootFileName` is `ipxe.efi`.
  - `TestRespondIgnoresRequestOn67`: same setup, but `port = s.cfg.Port67`. Expect no packet within 300 ms (the read deadline expires).
- [ ] **Step 3:** Run `go vet ./pkg/proxydhcp/ && go test -race ./pkg/proxydhcp/`.
  Expected: FAIL only in `TestGoldenPXEReplies/discover-ipxe-userclass` ("missing golden file"). Every other test passes, including the 9 upstream goldens.
- [ ] **Step 4: Generate the one fork-only golden file.**

```bash
go test ./pkg/proxydhcp/ -run 'TestGoldenPXEReplies/discover-ipxe-userclass' -update
ls pkg/proxydhcp/testdata/golden/discover-ipxe-userclass.hex
```

Expected: `pkg/proxydhcp/testdata/golden/discover-ipxe-userclass.hex` now exists. Then confirm the 9 copied files are still byte-identical to upstream's. This must print nothing:

```bash
for f in pkg/proxydhcp/testdata/golden/*.hex; do n=$(basename $f); [ "$n" = discover-ipxe-userclass.hex ] && continue; git show upstream/main:pkg/dhcp/testdata/golden/$n | cmp - $f; done
```

- [ ] **Step 5:** Run `go test -race ./pkg/proxydhcp/`. Expected: `ok`.

### Task 3: `--proxyDHCPRelay` and `cmd` wiring

**Files:**
- Modify: `pkg/config/config.go` (const block; `LoadConfig`)
- Modify: `cmd/main.go` (`args`; `init()` near lines 200-223; `startProxyDHCP` lines 516-541)

- [ ] **Step 1:** Add the constant `ProxyDHCPRelay = "proxyDHCPRelay"` to `pkg/config/config.go`, and `viper.SetDefault(ProxyDHCPRelay, false)` next to line 76.
- [ ] **Step 2:** In `cmd/main.go`, add the field `proxyDHCPRelay bool` and, after the `proxyDHCPBootfileARM64` flag:

```go
	flags.BoolVar(
		&args.proxyDHCPRelay,
		"proxyDHCPRelay",
		false,
		"Answer relayed PXE requests (giaddr set) by replying to the relay agent. Talos cluster members booted through a relay are refused their config until authenticated config fetch exists",
	)
```

- [ ] **Step 3:** In `startProxyDHCP`, build:

```go
proxydhcp.Config{
	ServerIP:  ip,
	Relay:     viper.GetBool(config.ProxyDHCPRelay),
	BIOSFile:  viper.GetString(config.ProxyDHCPBootfileBIOS),
	EFIFile:   viper.GetString(config.ProxyDHCPBootfileUEFI),
	ARM64File: viper.GetString(config.ProxyDHCPBootfileARM64),
}
```

- [ ] **Step 4:** Run `go build ./... && go vet ./... && go test -race ./cmd/ ./pkg/proxydhcp/ ./pkg/config/`. Expected: `ok`.
- [ ] **Step 5:** Commit all of phase 3 together, since the package does not compile part-way:

```bash
git add pkg/proxydhcp pkg/config cmd
git commit -m "fix(proxydhcp): answer DISCOVER on 67 and REQUEST on 4011 only; add --proxyDHCPRelay

Port upstream's protocol core (jeefy/booty pkg/dhcp/pxe.go @ 454c865):
a port-67 ACK to the client's lease REQUEST made EDK2 decline the lease.
Replies match upstream's golden bytes; iPXE clients also get booty.ipxe
on the OFFER because iPXE skips the 4011 REQUEST. Packets without option
93, INFORM, and relayed packets (unless --proxyDHCPRelay) are ignored;
relayed replies go to giaddr:67."
```

### Task 4: Docs

- [ ] **Step 1:** In `docs/schema/API.md`, replace the two "Pass 1" / "Pass 2" bullets (lines 81-84) with:
  - `**Firmware** (PXE ROM): DISCOVER on UDP 67 → OFFER naming booty as the only boot server (no file); REQUEST on UDP 4011 → ACK with the architecture's iPXE binary (--proxyDHCPBootfileBIOS / UEFI / ARM64). A REQUEST on 67 belongs to the site DHCP server and is ignored.`
  - `**iPXE** (user class iPXE): the OFFER itself carries booty.ipxe, because iPXE does not send the 4011 REQUEST.`
  - `Ignored: packets without option 93, INFORM, packets naming another server (option 54), and relayed packets (giaddr set) unless --proxyDHCPRelay is set; relayed replies are sent to the relay on UDP 67.`
- [ ] **Step 2:** In `docs/CONFIGURATION.md`, add a row after `--proxyDHCPBootfileARM64` (line 39): `| --proxyDHCPRelay | false | Answer relayed PXE requests by replying to the relay agent (giaddr). Talos cluster members behind a relay are refused their config until authenticated fetch exists. |`
- [ ] **Step 3:** Append to `docs/releases/UNRELEASED.md` `## Changes`:
  - `proxyDHCP no longer answers the client's port-67 REQUEST for its real lease (UEFI firmware declined the lease and the boot failed).`
  - `proxyDHCP ignores PXE packets without client architecture (option 93) and INFORM; very old BIOS boot ROMs that omit option 93 need the site DHCP server pointed at booty.`
  - `New --proxyDHCPRelay.`
- [ ] **Step 4:** Commit: `git add docs && git commit -m "docs: proxyDHCP protocol changes and --proxyDHCPRelay"`.

### Phase exit check

Run: `go build ./... && go vet ./... && go test -race ./...`. Expected: all `ok`.
