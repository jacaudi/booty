# Upstream port research: what to lift from `jeefy/booty` into the fork

Upstream ref: `upstream/main` @ `8a5a227ceb0bce6c0bdcf73d4369c16311c15e2c`.
Fork ref: working tree of `/home/user/booty` (not modified).
Notation: `upstream:<path>:<lines>` is read with `git show upstream/main:<path>`. Paths with no prefix are in the fork.

## How this was checked

- `git archive upstream/main` was extracted to `scratchpad/research/uptree`. Running
  `go test ./pkg/dhcp/ ./pkg/hardware/ ./pkg/power/ ./pkg/tftp/ ./pkg/ignition/ ./pkg/config/ ./pkg/server/`
  there passed every package.
- **Library-parity experiment.** A copy (`uptree-oldlib`) was pinned to the fork's
  `github.com/insomniacslk/dhcp v0.0.0-20260603135910-a415979eb11e` (fork `go.mod:20`). All 9 golden
  cases and every other `pkg/dhcp` test still pass.
- **HTTP Boot strip experiment.** In the same copy, HTTP Boot was removed from `pxe.go` as described in §1.3,
  `httpboot_test.go` was deleted and `portFor` was edited. Result: `go vet` is clean and all 18 remaining tests pass,
  golden included.
- `diff -rq` of the two dhcp module versions in the module cache: the only differences are
  `dhcpv4/nclient4/conn_unix.go` (+ a new test file), dhcpv6 files, CI workflows, README and go.mod/go.sum.
  **dhcpv4 serialisation is identical**, so the library bump is not needed for byte parity. It is harmless.
- `sha256sum -c boot/SHA256SUMS` in the extracted tree: all three binaries OK.

---

## 1. ProxyDHCP: `upstream:pkg/dhcp/pxe.go` + tests + golden files

Last commits:
- `pkg/dhcp/pxe.go`: `454c865630ba8ab53c13a34d550689247259fde7` (2026-09-27, "feat(dhcp): per-host UEFI HTTP Boot answers")
- `pkg/dhcp/pxe_test.go`: `d7d92a7ac7f06f94163a2b7add3f50884b1d8d41` (2026-09-26, "answer PXE REQUESTs on 4011 only")
- `pkg/dhcp/golden_test.go` and `pkg/dhcp/testdata/golden/*`: `f25ad4c89b2aefa39b9924d93e9ba2e1af0133b8` (2026-09-26)
- `pkg/dhcp/server.go`: `d7d92a7ac7f06f94163a2b7add3f50884b1d8d41`

History of `pkg/dhcp`, oldest first:
1. `dd075fc` creates the server.
2. `f25ad4c` adds the golden files.
3. `fbae012` adds HTTP Boot.
4. `d7d92a7` adds port gating.
5. `454c865` adds per-host HTTP Boot.

### 1.1 What to port, with line ranges (`upstream:pkg/dhcp/pxe.go`)

| Item | Lines | Port? |
|---|---|---|
| imports | 3-13 | yes, minus `sync` (line 9, used only by `httpBootDisabledOnce`) |
| PXE sub-option tags `pxeDiscoveryControl=6, pxeBootServers=8, pxeBootMenu=9, pxeMenuPrompt=10, pxeBootItem=71, pxeEnd=255`, `pxeBootServerListOnly=0x07`, `pxeClientPrefix="PXEClient"`, `pxeMenuText="Booty"`, `ipxeUserClass="iPXE"` | 15-31 | yes |
| `httpClientPrefix`, `maxHTTPBootReply` | 33-40 | **no** (HTTP Boot) |
| `DefaultBIOSFile="undionly.kpxe"`, `DefaultEFIFile="ipxe.efi"`, `DefaultARM64File="ipxe-arm64.efi"`, `DefaultIPXEScript="booty.ipxe"` | 43-47 | yes |
| `DefaultHTTPBootEFIFile`, `DefaultHTTPBootARM64File`, `SnponlyHTTPBootEFIFile` | 49-54 | no |
| `type Config` | 57-91 | keep `ServerIP` (63), `Relay` (64), `BIOSFile/EFIFile/ARM64File/IPXEScript` (66-69) and `Port67/Port4011` (89-90). Drop `HTTPBoot*` and `HostHTTPBoot` (71-87). `Listen` (62) is out of scope (`--proxyDHCPListen` is a non-goal) |
| `Config.withDefaults` | 93-119 | yes, minus the HTTPBoot defaults at 106-111. `Port67` defaults to `dhcpv4.ServerPort` (=67) and `Port4011` to 4011 |
| `Config.validate` | 121-126 | yes: rejects nil, non-IPv4 or unspecified `ServerIP` |
| `handle(pkt, c, localPort)` | 128-186 | yes, with lines 145-150 removed (see §1.3) |
| `baseReply` | 188-196 | yes |
| `buildOffer` | 198-210 | yes |
| `buildAck` | 212-235 | yes |
| `bootFileForArch` | 237-255 | yes |
| `isHTTPBootClient` | 257-276 | **no** |
| `httpBootDisabledOnce` | 278 | no |
| `handleHTTPBoot` | 280-327 | no |
| `httpBootOffer` | 329-348 | no |
| `httpBootFileForArch` | 350-367 | no |
| `hasUserClass` | 369-380 | yes |
| `isSet` | 382-384 | yes |
| `offerVendorOptions` | 386-408 | yes |
| `ackVendorOptions` | 410-415 | yes |
| `appendSubOption` | 417-420 | yes (dependency of the two above) |
| `parseSubOptions` | 422-442 | yes. Production code never calls it, but `pxe_test.go` `subOptions` (70-81), `TestParseSubOptionsRejectsMalformed` and the decode checks do |
| `replyDestination` | 444-458 | optional. See §1.5 |

### 1.2 Dependencies outside `pxe.go`

- **No booty package.** `pxe.go` imports only stdlib (`encoding/binary`, `fmt`, `log/slog`, `net`, `strings`) plus
  `github.com/insomniacslk/dhcp/dhcpv4` and `.../iana`.
- Library identifiers used. All exist in the fork's pinned version, which the passing build in uptree-oldlib proves:
  - Constructors and modifiers: `dhcpv4.NewReplyFromRequest`, `WithMessageType`, `WithServerIP`, `WithOption`,
    `WithOptionCopied`, `OptServerIdentifier`, `OptClassIdentifier`, `OptGeneric`, `OptBootFileName`, `OptTFTPServerName`.
  - Option codes: `OptionClientMachineIdentifier`, `OptionVendorSpecificInformation`, `OptionUserClassInformation`.
  - Constants: `OpcodeBootRequest`, `MessageTypeDiscover/Request/Offer/Ack`, `ServerPort`, `ClientPort`.
  - Methods: `(*DHCPv4).ClassIdentifier`, `ClientArch`, `ServerIdentifier`, `MessageType`, `UserClass`, `UpdateOption`,
    `Options.Get`, `IsBroadcast`.
  - Architectures: `iana.INTEL_X86PC`, `EFI_X86_64`, `EFI_BC`, `EFI_IA32`, `EFI_ARM64`.
- `upstream:pkg/dhcp/server.go` is **not** needed for `handle`. It is the socket layer:
  - `Start` (30-48), `Addrs` (51-59), `Shutdown(timeout)` (62-75), `serve` (83-109), `listen` with `SO_REUSEADDR` and
    `BindToInterface` (113-142), `ParsePorts` (153-173), `maxPacket=1500` (18).
  - It uses `config.CloseQuietly` (79, 138).
  - `serve` passes the port as `conn.LocalAddr().(*net.UDPAddr).Port` (100) and replies to
    `replyDestination(pkt, src)` (104).

### 1.3 Removing HTTP Boot from `handle()`

Delete exactly `upstream:pkg/dhcp/pxe.go:145-150`:
```go
	if isHTTPBootClient(pkt) {
		if localPort != c.Port67 {
			return nil, false
		}
		return handleHTTPBoot(pkt, c, log)
	}
```
After that, an HTTP Boot client is answered by nothing. A client with `HTTPClient` in option 60 fails the `PXEClient`
prefix check (151-154) and is silently ignored, so behaviour stays safe. Also delete the `sync` import and the
functions listed as "no" above.

In the test helper `portFor` (`upstream:pkg/dhcp/pxe_test.go:46-52`), change
`pkt.MessageType() == dhcpv4.MessageTypeRequest && !isHTTPBootClient(pkt)` to
`pkt.MessageType() == dhcpv4.MessageTypeRequest`. **No golden case depends on HTTP Boot:**
- all 9 are built by `pxePacket` with option 60 = `PXEClient:...` (`pxe_test.go:23-41`);
- the golden files predate HTTP Boot (commit `f25ad4c` message: "Generated from main before any HTTP-Boot change");
- the strip experiment passes all 9.

`TestRequestAckBootFileByArch` has a `{iana.EFI_X86_64_HTTP, "undionly.kpxe"}` row (`pxe_test.go:154`). It still passes
after the strip: the packet is a PXEClient, so it falls through to `bootFileForArch`'s default (BIOS).

### 1.4 Behaviour of `handle()` (for the fork's table tests)

Checks, in order:
1. nil packet or not a `BootRequest` → silent (140-142). This also silences Booty's own replies.
2. Option 60 not prefixed `PXEClient` → silent (151-154).
3. No option 93 → silent (155-159).
4. `giaddr` set and `!Relay` → silent (160-163).
5. Option 54 present and not our IP → silent (164-167).
6. DISCOVER answered only when `localPort == Port67` (171-175).
7. REQUEST answered only when `localPort == Port4011` (177-181).

**INFORM is dropped.** The `default:` branch (182-184) logs at debug and returns `(nil,false)`. This is asserted by
`TestSilentForOtherServersAndOwnReplies` (`pxe_test.go:254-255`). The fork today answers INFORM with an ACK
(`pkg/proxydhcp/proxydhcp.go:77`).

Reply shape:
- `baseReply` (188-196) sets the message type, `siaddr=ServerIP`, opt 54 = ServerIP and opt 60 = `"PXEClient"`, and
  copies opt 97 (client GUID). `NewReplyFromRequest` itself copies `xid`, `chaddr` and flags, sets `giaddr` from the
  request, and copies opt 82 and opt 61 (library `dhcpv4packet.go:241-251` in the module cache).
- The OFFER adds only opt 43 = `offerVendorOptions`:
  `06 01 07 | 08 07 0000 01 <ip4> | 09 08 0000 05 "Booty" | 0a 06 00 "Booty" | ff`.
  It has no file, no opt 66/67 and no sname.
- The ACK:
  - copies `ciaddr` (221);
  - sets `file` = `IPXEScript` when the user class is `iPXE`, otherwise `bootFileForArch(archs[0])`;
  - sets opt 67 = file, opt 66 = `ServerIP.String()`, and opt 43 = `47 04 00000000 ff`.
- `yiaddr` stays 0 because `New()` zeroes it.
- Fields the fork sets that upstream does not:
  - `sname = ServerIP` (`proxydhcp.go:100`) is not set upstream.
  - The fork's `0x06 01 08 ff` option 43 (`proxydhcp.go:113-115`) is replaced.

`bootFileForArch` (239-255):

| Architecture | File returned | Log |
|---|---|---|
| `INTEL_X86PC` | BIOSFile | none |
| `EFI_X86_64` / `EFI_BC` | EFIFile | none |
| `EFI_IA32` | EFIFile | warn |
| `EFI_ARM64` | ARM64File | warn |
| anything else | BIOSFile | warn |

`hasUserClass` (369-380) matches opt 77 when either the raw bytes equal `"iPXE"` or a parsed RFC 3004 entry equals
it. This is an exact match, not the fork's substring check (`proxydhcp.go:120-123`).

### 1.5 Changes needed to compile in `pkg/proxydhcp`

- **Package name:** `dhcp` → `proxydhcp`.
- **Name clashes:**
  - The fork already has a method `(*Server).handle` (`proxydhcp.go:226`). A package-level func `handle` is legal
    beside it, but confusing, so rename one of them (for example the free function to `reply`).
  - The fork's `maxPacketSize` (`proxydhcp.go:197`) duplicates upstream's `maxPacket`.
- **Config fields:** the fork has `BootfileBIOS/BootfileUEFI/BootfileARM64` (`proxydhcp.go:30-35`); upstream has
  `BIOSFile/EFIFile/ARM64File/IPXEScript`.
  - Either rename, or adapt `withDefaults` and the tests.
  - `cmd/main.go:526` builds the fork's Config.
  - The fork's `bootfileIPXEScript` const (`proxydhcp.go:24`) becomes `DefaultIPXEScript`. It must still equal the
    TFTP special case `booty.ipxe` (`pkg/tftp/tftp.go:155`).
- **`NewServer` validation:** it rejects empty bootfiles (`proxydhcp.go:141-143`). Replace it with
  `withDefaults()` + `validate()`.
- **Ports:** they are hard-coded at `proxydhcp.go:150` and `:160`. The `loop(conn, broadcast bool)` signature
  (`proxydhcp.go:199`) carries no port.
  - Pass the local port instead: `conn.LocalAddr().(*net.UDPAddr).Port`, as upstream does at `server.go:100`.
  - Or add `Port67/Port4011` to the fork's `Config` and listen on them. Upstream's port-gated tests need distinct
    ports, but they call `handle` directly, so the socket port does not matter for them.
- **Reply destination:** the fork broadcasts every reply received on 67 to `255.255.255.255:68` and unicasts 4011
  replies to the source (`proxydhcp.go:231-234`).
  - Upstream's `replyDestination` (444-458) adds RFC 2131 relay handling: `giaddr` replies go to `giaddr:67`, and a
    set `ciaddr` gets a unicast reply.
  - **Port it if `--proxyDHCPRelay` is added**, otherwise relayed replies get broadcast on the wrong segment.
- **`NewServer` needs a `ServerIP`.** `startProxyDHCP` already rejects loopback (`cmd/main.go:520-523`).

### 1.6 Tests to port

`upstream:pkg/dhcp/golden_test.go` (all 68 lines):
- `var updateGolden = flag.Bool("update", false, ...)` (16).
- `goldenCases` (22-39).
- `TestGoldenPXEReplies` (41-68). It reads and writes `testdata/golden/<name>.hex` as one lowercase hex line plus
  `\n` (54), and compares with `strings.TrimSpace` (63).
- Imports: `encoding/hex`, `flag`, `net`, `os`, `path/filepath`, `strings`, `testing`, dhcpv4, iana.

`upstream:pkg/dhcp/pxe_test.go` helpers (portable as-is once `portFor` is edited):
- `testMAC = 52:54:00:aa:bb:cc`, `testServerIP = 192.168.1.10` (14-17).
- `testConfig() = Config{ServerIP: testServerIP}` (19-21).
- `pxeClass` (23-28) returns `PXEClient:Arch:00000:UNDI:002001` for BIOS, else `PXEClient:Arch:00007:UNDI:003016`.
- `pxePacket` (30-42) uses `dhcpv4.NewDiscovery(testMAC, WithMessageType, opt60, opt93, mods...)`.
- `portFor` (46-52), `mustHandle` (54-61), `mustBeSilent` (63-68), `subOptions` (70-81).

Pure-function tests that port directly:
- `TestDiscoverProducesPXEOffer` (83-142)
- `TestRequestAckBootFileByArch` (144-193)
- `TestRequestCopiesClientFields` (195-214)
- `TestSilentWithoutClassIdentifier` (216-219)
- `TestSilentForNonPXEClass` (221-225)
- `TestSilentWithoutArchitecture` (227-231)
- `TestRelayedPacketsNeedRelayFlag` (233-243)
- `TestSilentForOtherServersAndOwnReplies` (245-256)
- `TestIPXEUserClassGetsScriptNotBinary` (258-280)
- `TestConfigFileOverrides` (282-299), which needs the upstream field names
- `TestParseSubOptionsRejectsMalformed` (326-339)
- `TestOfferVendorOptionsLayout` (467-482)
- `TestRequestOnPort67IsIgnored` (488-501)

Tests that need upstream's `server.go` API and do **not** port as-is:
- `TestReplyDestination` (301-324) needs `replyDestination`.
- `TestParsePorts` (341-353) needs `ParsePorts`.
- `TestStartRejectsBadServerIP` (355-364) needs `Start(Config, chan error)` and `Listen`.
- `TestStartServesDiscoverAndShutsDown` (382-465) needs `Start`, `Addrs`, `Shutdown(timeout)`, `maxPacket` and
  configurable ports.

The fork's `NewServer/Start()/Shutdown()` with fixed ports cannot bind 67 in tests. Rewrite these against the fork's
lifecycle, or add configurable ports.

`upstream:pkg/dhcp/httpboot_test.go` is excluded (HTTP Boot).

### 1.7 Golden cases and every value that shapes the bytes

`goldenCases` (`golden_test.go:22-39`). All cases share `xid = de ad be ef` (24).

| name | msg type | arch (opt 93) | extra modifiers |
|---|---|---|---|
| discover-bios | DISCOVER | INTEL_X86PC | guid |
| discover-efi-x64 | DISCOVER | EFI_X86_64 | guid |
| discover-efi-bc | DISCOVER | EFI_BC | – |
| request-bios | REQUEST | INTEL_X86PC | sid, ciaddr, guid |
| request-efi-x64 | REQUEST | EFI_X86_64 | sid, ciaddr, guid |
| request-efi-bc | REQUEST | EFI_BC | sid, ciaddr |
| request-efi-arm64 | REQUEST | EFI_ARM64 | sid, ciaddr |
| request-ipxe-userclass | REQUEST | EFI_X86_64 | sid, ciaddr, `WithUserClass("iPXE", false)` |
| request-ipxe-rfc3004 | REQUEST | INTEL_X86PC | sid, ciaddr, `WithUserClass("iPXE", true)` |

Modifier values:
- `sid` = opt 54 `192.168.1.10` (25).
- `ciaddr = 192.168.1.50` (26).
- `guid` = opt 97 bytes `00..10`, 17 bytes (27).

Values that reach the reply bytes:
- `testServerIP 192.168.1.10`, which appears in `siaddr`, opt 54, opt 66 (as a string) and the opt 43 boot-server list.
- `testMAC`.
- `xid`.
- `ciaddr` (ACK only).
- opt 97 GUID (echoed).
- Default file names: `undionly.kpxe`, `ipxe.efi`, `ipxe-arm64.efi`, `booty.ipxe`.
- The `"Booty"` menu text and `0x07` discovery control.
- The `"PXEClient"` class.
- The broadcast flag: `NewDiscovery` leaves it 0, and the decoded replies show `flags: Unicast (0x00)`.
- `giaddr 0`.
- The dhcpv4 library's option serialisation. Decoded order: 43, 53, 54, 60, [66, 67], [97].

The request's opt 55 and its class string do not reach the reply.

Decoded lengths (bytes): discover-bios 313, discover-efi-bc 300, discover-efi-x64 313, request-bios 318,
request-efi-arm64 300, request-efi-bc 300. The 300-byte floor is BOOTP padding.

**Correction to the design doc.** Item 1 says the `.hex` files are replies "run against OVMF in QEMU upstream".
They were produced by `go test -update` from code (`f25ad4c` message). Only the port-67/4011 split (`d7d92a7`) was
observed on the wire with OVMF.

---

## 2. `upstream:pkg/hardware/hostname.go` `ValidateHostname`

Last commit for both files: `cae75c847874e8a8a11472d726340a22bf055aac` (2026-09-25).

Port these lines:
- 12-13: `const MaxHostnameLen = 253`
- 15-17: `var ErrInvalidHostname = errors.New("invalid hostname")`
- 19: `hostnameRe = ^(?i)[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
- 21-41: `ValidateHostname`, which:
  - rejects `""`;
  - rejects names longer than 253;
  - rejects any label longer than 63;
  - otherwise applies the regex.

Imports needed: `errors`, `fmt`, `regexp`, `strings`.

Do **not** port lines 43-120 (`HostnameVars`, `NewHostnameVars`, `HostnameTemplate`, `ParseHostnameTemplate`,
`Render`, `ValidateAutoRegisterOS`). They pull in `bytes`, `text/template`, `NormalizeMAC`, `IsValidOS` and
`ValidOSList`.

External identifiers: none. It compiles in the fork's `pkg/hardware` as-is; there is no existing `hostname.go`.

Callers must handle the empty string themselves. Upstream rejects `""` (26-28), and the design wants empty to mean
"unset".

Checked in uptree: `"a\n"`, `"a\nb"` and `"\na"` are rejected (Go RE2 `$` means end of text), and `"A.B"` is accepted.

Test to copy: `upstream:pkg/hardware/hostname_test.go:8-40` (imports `strings`, `testing`):
```go
func TestValidateHostname(t *testing.T) {
	long := strings.Repeat("a", 63)
	tests := []struct {
		name string
		ok   bool
	}{
		{"node1", true},
		{"node-1", true},
		{"Node-1", true},
		{"node1.example.com", true},
		{"a", true},
		{"1", true},
		{long, true},
		{long + "." + long + "." + long + "." + strings.Repeat("a", 61), true},
		{"", false},
		{"-node", false},
		{"node-", false},
		{"node_1", false},
		{"Bad_Name!", false},
		{"node..1", false},
		{".node", false},
		{"node.", false},
		{"node 1", false},
		{long + "a", false},
		{long + "." + long + "." + long + "." + strings.Repeat("a", 62), false},
	}
	for _, tc := range tests {
		err := ValidateHostname(tc.name)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateHostname(%q) err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}
```
The last two rows are 253 bytes (ok) and 254 bytes (rejected). Skip `TestNewHostnameVars` (42-47),
`TestHostnameTemplate` (49-97) and `TestValidateAutoRegisterOS` (99-110).

---

## 3. `upstream:pkg/config/config.go` `DetectServerIP` and `CloseQuietly`

Last commits:
- `pkg/config/config.go`: `4663d22e07c2e4e4ec9abb78874c27e8edc8ad5c` (2026-10-07)
- `config_test.go`: `15442005ce39c5f71614f866ee7a31977017169a` (2026-10-03)

`DetectServerIP` (542-555):
- calls `net.Dial("udp4", "192.0.2.1:9")`;
- `defer CloseQuietly(conn, "route probe")`;
- takes `conn.LocalAddr().(*net.UDPAddr)`;
- errors `"no usable local address on the default route"` if the address is nil or unspecified;
- returns `addr.IP.String()`.

It does **not** reject loopback. The design's test ("returns a non-loopback IPv4 or an error") asserts more than the
code guarantees; add a loopback check if that is wanted.

`CloseQuietly(c io.Closer, what string)` (618-625) logs a close error at debug level.

Upstream also has `ResolveServerAddress` (522-540): if `ServerIP` is empty it detects, then `viper.Set` and
`slog.Info("serverIP autodetected", "ip", ip)`. It also defaults `ServerHttpPort` to `HttpPort`. Errors are wrapped as
`"--serverIP not set and autodetection failed: %w"`. This is a useful template for the fork's `cmd/main.go` change.

Changes for the fork's `pkg/config/config.go`:
- add the `net` import (current imports are at `pkg/config/config.go:3-20`; `io` and `log/slog` are already present);
- add both functions (no `CloseQuietly` or `DetectServerIP` exists in the fork; grep finds no hits);
- inject the detector as a func var for the error-path test, per the design.

The fork's default is `"127.0.0.1"` at `cmd/main.go:180-185`. The port resolution is at `cmd/main.go:366-371`.

Upstream test `TestResolveServerAddress` (`config_test.go:220-246`):
- uses `viper.Reset` and `LoadConfig()`;
- checks that an explicit IP is kept;
- if detection fails, `t.Skipf` ("no default route"); otherwise asserts the result is non-loopback and not
  unspecified.

There is no direct `DetectServerIP` unit test upstream.

---

## 4. Wake-on-LAN: `upstream:pkg/power/wol.go`, `broadcast.go`, `wol_test.go`

Last commit for all three: `6809c54adad5f2ff81e74090f4dc5a4c8dc1c6a8` (2026-09-30). There is **no**
`broadcast_other.go` or any build-tag variant upstream; `broadcast.go` has no build constraint.

`wol.go`:

| Lines | Content |
|---|---|
| 1-7 | Package doc. It mentions Tracker/Manager/actions; rewrite it |
| 23-24 | `WoLPort = 9` |
| 26-41 | `MagicPacket(mac)`: `net.ParseMAC`, requires `len == 6`, returns 6×`0xFF` + 16×MAC, 102 bytes |
| 43-61 | `Waker{Port, Rounds, Spacing, Interfaces func() ([]net.Addr, error), Extra []string, Sleep func(ctx, d) error}` |
| 63-68 | `port()` (default 9) |
| 70-75 | `rounds()` (default 3) |
| 77-82 | `spacing()` (default 1s) |
| 84-96 | `sleep` (honours ctx) |
| 98-118 | `interfaces()`: up, non-loopback ifaces |
| 120-166 | `Destinations(hostIP)` |
| 168-210 | `Wake(ctx, mac, hostIP) ([]string, error)` |

`Destinations` (120-166) collects:
- the directed broadcast of each IPv4 `*net.IPNet` (it handles 16-byte masks);
- `255.255.255.255`;
- `hostIP` if it is IPv4 and not loopback;
- `Extra`.

The result is sorted and deduplicated.

`Wake` (168-210):
- builds the packet and opens `ListenPacket("udp4", ":0")`;
- calls `enableBroadcast`; a failure is debug-logged only;
- sends `rounds` rounds with `sleep` between them;
- per-destination send errors are debug-logged;
- returns `"wol: no destination accepted the packet"` if any round sent 0 packets.

`broadcast.go` (8-24): `enableBroadcast(conn net.PacketConn) error` sets `SO_BROADCAST` via `syscall.RawConn` with
`int(fd)`. That conversion fails to compile on Windows. The fork already restricts its equivalent to unix
(`pkg/proxydhcp/broadcast_unix.go:1` `//go:build unix`). Name the ported file `broadcast_unix.go` with that tag; the
design proposes exactly this.

Dependencies:
- stdlib: `bytes`, `context`, `errors`, `fmt`, `log/slog`, `net`, `slices`, `strconv`, `time`, `syscall`.
- one booty identifier: `hardware.ErrInvalidMAC` (`wol.go:31`, `:34`), defined at `upstream:pkg/hardware/mac.go:120`.
  The fork has no `ErrInvalidMAC`; its `NormalizeMAC` wraps the `net.ParseMAC` error with `"hardware: invalid MAC %q"`
  (`pkg/hardware/mac.go:57-63`).
- the fork has no `pkg/power`, so this is a new package.

Fix options for the MAC check: either call `hardware.NormalizeMAC(mac)` then `net.ParseMAC` and check length 6, or
define a local `ErrInvalidMAC`. `NormalizeMAC` alone is not enough: `net.ParseMAC` also accepts 8- and 20-byte
addresses, and the length-6 check (33-35) must stay.

Importing `pkg/hardware` from `pkg/power` drags in `pkg/db` and `pkg/config` (`pkg/hardware/mac.go:3-19`). A local
sentinel avoids that.

Tests (`upstream:pkg/power/wol_test.go`, stdlib only):
- `TestMagicPacketBytes` (12-34). It also checks that `""`, `"not-a-mac"` and an 8-byte MAC are refused.
- `fixedInterfaces` (about 36-43).
- `TestDestinationsCoverBroadcastsAndTheHost` (45-58) expects
  `[10.0.255.255:9 192.168.1.255:9 192.168.1.42:9 255.255.255.255:9]`.
- `TestWakeSendsThreeRoundsToAListener` (60-97) uses a loopback listener in `Extra` and a fake `Sleep`; it expects 3
  packets and spacing `[1s 1s]`.
- `TestWakeStopsWhenCancelled` (99-106).

These port verbatim. All four passed in uptree.

---

## 5. Health: `upstream:pkg/hardware/health.go` and `upstream:pkg/ignition/health.go`

Last commit for `pkg/hardware/health.go`, `pkg/ignition/health.go`, `pkg/ignition/health_test.go` and
`pkg/server/health_test.go`: `27fafe897686324904366386d003df7ce522f6ab` (2026-09-28, "report the DMI product UUID").
`pkg/ignition/builtin.go` (constants): `c06fb283c198c968f23679ec893dd4a14eccfd53`.

### 5.1 `pkg/hardware/health.go`

| Lines | Content |
|---|---|
| 15-27 | `type Health{ReceivedAt, BootID, Running, FailedUnits []string, JournalErrors []string, DMI DMI, Firmware, Kernel}` (JSON tags `receivedAt, bootID, running, failedUnits, journalErrors, dmi, firmware, kernel`) |
| 35-40 | `type DMI{Vendor, Product, BIOSVersion, ProductUUID ,omitempty}` |
| 43-46 | `FirmwareUEFI="uefi"`, `FirmwareBIOS="bios"` |
| 49-57 | Caps (see below) |
| 59 | `ErrInvalidHealth` |
| 64-90 | `(*Health).Normalize()` |
| 95-113 | `capJournal` |
| 116-128 | `clip` |
| 130 | `isRuneStart` |
| 133-135 | `SameBoot` (optional; upstream uses it to drop info logs on repeats) |
| 137-154 | **Skip** `targetVersionPattern` and `ValidateTargetVersion` (autopilot/versions) |

Caps (49-57):
- `MaxHealthFailedUnits=200`
- `MaxHealthJournalLines=50`
- `MaxHealthJournalLine=300`
- `MaxHealthJournalBytes=8<<10`
- `MaxHealthFieldLen=256`
- `MaxHealthUnitNameLen=256`
- `maxHealthFieldTruncMsg="…"`

`Normalize()` (64-90):
- trims and clips every field;
- lowercases `ProductUUID` and `Firmware`;
- rejects any firmware other than `""`, `uefi` or `bios`;
- drops empty units and caps the unit count;
- calls `capJournal`.

`capJournal` (95-113) keeps the newest 50 lines, clipped to 300, within 8 KiB total.

`clip` (116-128) cuts at a UTF-8 boundary and appends `…`. The result can be up to `n` bytes because the ellipsis is 3
bytes.

Imports after trimming: `errors`, `fmt`, `strings` (drop `regexp` with `ValidateTargetVersion`). No booty identifiers.

Doc comments at 10-14 and 29-34 mention autopilot/plan P3; rewrite them.

**There is no `pkg/hardware/health_test.go` upstream** (`git ls-tree` lists only `pkg/hardware/health.go`,
`pkg/ignition/health*.go` and `pkg/server/health*.go`). `Normalize` is tested only end-to-end through the HTTP handler
in `upstream:pkg/server/health_test.go`:
- `healthFixture` (17-28);
- `TestHealthReport` (39-110);
- `TestHealthReportSetsRunningOnlyWhenEmpty` (112-133). It checks that a 400-char journal line is clipped to at most
  300 and that an unknown firmware returns 400.

These need upstream's `newTestServer`, `register`, `do`, `hardware.Get` and `nowFunc`. Rewrite them as `Normalize`
unit tests plus fork receiver tests. The upstream receiver (`pkg/server/health.go:13-80`) uses a 64 KiB
`MaxBytesReader` (413 above that), 400 on bad JSON or `Normalize` error, and `ReceivedAt = now RFC3339`.

### 5.2 `pkg/ignition/health.go`

`HealthUnit(scriptPath)` (8-27) is a systemd unit:
- `[Unit]`: `DefaultDependencies=no`, `Conflicts=shutdown.target`, `Before=shutdown.target`,
  `After=multi-user.target network-online.target ` + **`BootedUnitName`**, `Wants=network-online.target`.
- `[Service]`: `Type=oneshot`, `RemainAfterExit=yes`, `Restart=on-failure`, `RestartSec=30s`, `ExecStart=<scriptPath>`.
- `[Install]`: `WantedBy=multi-user.target`.

`HealthReportScript(server)` (36-102) is bash that:
- honours `BOOTY_SYS`, `BOOTY_PROC`, `BOOTY_OS_RELEASE` and `BOOTY_MAC`;
- otherwise derives the MAC from `ip -o route get 1` + `/sys/class/net/<dev>/address`;
- has a pure-bash `json_escape` (control characters become `\u%04x`);
- reads failed units with `systemctl list-units --state=failed --plain --no-legend`;
- reads `journalctl -p err -b -o short-iso --no-hostname --no-pager -n 50` and keeps the newest lines (each clipped to
  300) within 8192 bytes;
- reads `firmware/efi` and the DMI files (`sys_vendor`, `product_name`, `bios_version`, `product_uuid`) via `tr`;
- adds `uname -r` and `boot_id`;
- posts with
  `curl -fsS --max-time 20 --retry 2 --retry-connrefused -X POST -H 'Content-Type: application/json' --data-binary @- "http://<server>/health?mac=$MAC"`;
  on failure it exits 1 with "booty unreachable or refused the health report".

Dependencies:
- `BootedUnitName = "booty-booted.service"` (`upstream:pkg/ignition/builtin.go:29`). The fork has no
  `booty-booted.service` (grep finds no hits), so remove it from `After=` or replace it.
- `HealthReportScriptPath = "/opt/booty/health-report"` (`builtin.go:38`) and `HealthUnitName = "booty-health.service"`
  (`builtin.go:32`) are used by the test and the caller (`builtin.go:120-122`).
- **The fork has no `pkg/ignition` package**, so this is new code or goes into the fork's renderer.
- For item 10 the script must change: the URL `/health?mac=` (line 99) becomes `/node/report`, and a per-host token
  header is needed. Update the test's server path check (`health_test.go:69`) to match.

### 5.3 The bash test harness (`upstream:pkg/ignition/health_test.go`)

It exists and runs the real script.

Helpers:
- `writeStub` (19-24) writes `#!/bin/bash\n<body>`, mode 0755, into a temp `bin/` that is prepended to `PATH`.
- `writeFile` (26-34).
- `healthBody` (36-49) mirrors the JSON.

`TestHealthReportScript` (56-180):
- Skips unless **`bash`, `curl` and `uname`** are on `PATH` (57-61). These are real binaries; **curl is not stubbed**.
  The real curl posts to an `httptest.Server` that records the body, `mac` and Content-Type.
- **Stubbed tools:**
  - `journalctl` (94-98) asserts its exact arguments, then prints 60 lines of 500 `x` followed by two lines containing
    a quote, a backslash, a tab and the control characters `\x01` and `\x1f`.
  - `systemctl` (99-101) asserts its arguments and prints two failed units.
  - `ip` (102) exits 2, which proves it is not called when `BOOTY_MAC` is set.
- Fake files: `os-release` with `VERSION_ID=26.09.673` (103); a sysfs tree with the DMI files and a `firmware/efi`
  directory (104-111); `proc/sys/kernel/random/boot_id` (112-113).
- Env: `BOOTY_OS_RELEASE`, `BOOTY_SYS`, `BOOTY_PROC`, `BOOTY_MAC=aa:bb:cc:dd:ee:01` (117-132).
- Asserts:
  - the stdout line;
  - exactly one POST, the mac, and `application/json`;
  - every field, with the kernel compared to the real `uname -r`;
  - escaping round-trips;
  - each `x` line is exactly 300;
  - at most 50 lines and at most 8192 bytes, but within 300 of the budget (133-179).
- The script also needs `tr` and `cat` (cat only when `BOOTY_MAC` is unset) from the real `PATH`, plus bash builtins.

`TestHealthReportScriptEmptyLists` (182-214): skips without curl; stubs `journalctl`/`systemctl` with `exit 0`; points
at missing sys/proc dirs; expects firmware `bios`, empty arrays and an empty DMI.

`TestHealthReportScriptFailsWhenBootyRefuses` (216-238): the server returns 404; expects a non-zero exit and the
stderr message. `BOOTY_SYS`/`BOOTY_PROC` are unset here, so it reads the real `/sys` and `/proc`.

`TestHealthUnitParses` (240-266) needs **`github.com/coreos/go-systemd/v22/unit`** `DeserializeOptions`. The fork
already has `github.com/coreos/go-systemd/v22 v22.5.0 // indirect` (`go.mod:47`) and the `unit` package exists in that
version in the module cache. Promote it to a direct dependency, and change the expected `Unit/After` value if
`BootedUnitName` is dropped.

All four tests ran (not skipped) in uptree.

---

## 6. Embedded iPXE: `boot/`, `hack/build-ipxe.sh`, `embed.go`, `THIRD_PARTY_NOTICES.md`, Makefile

All of `boot/*`, `hack/build-ipxe.sh`, `THIRD_PARTY_NOTICES.md` and `Makefile` were last touched by
`60ad48e408be848d928cf20d06ceeadc32c2b459` (2026-09-25). `embed.go` was last touched by
`f51a7b5985f27aef2fc8e208cfd578e16109d9e0` (2026-09-25).

Sizes (`git ls-tree -l upstream/main boot/`):

| File | Bytes |
|---|---|
| `SHA256SUMS` | 233 |
| `VERSION` | 496 |
| `embed.ipxe` | 255 |
| `ipxe.efi` | 1,152,512 |
| `snponly.efi` | 288,256 |
| `undionly.kpxe` | 71,448 |

The fork's root `undionly.kpxe` is 68,907 bytes, and the fork has no `boot/` or `THIRD_PARTY_NOTICES.md`.

`boot/embed.ipxe` (upstream, 11 lines):
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
The design adds a leading `chain tftp://${proxydhcp/next-server}/booty.ipxe ||`.

`boot/VERSION`:
```
source:      https://github.com/ipxe/ipxe
commit:      12798ec29aa8a64d8675c4378b99f5fe28447afb
tag:         v2.0.0
commit-date: 2026-03-06T15:48:55Z
build-date:  2026-09-25T18:04:35Z
builder:     Debian GNU/Linux 13 (trixie), gcc (Debian 14.2.0-19) 14.2.0
embed:       boot/embed.ipxe (sha256 250ce4a37cf0463e9978a2037bd3dcfa3cd2d9ca0ccd2a7cc5b41b662c069366)
make:        make -C src bin/undionly.kpxe bin-x86_64-efi/ipxe.efi bin-x86_64-efi/snponly.efi EMBED=/out/embed.ipxe BUILD_ID=0x12798ec2
```
The `embed.ipxe` sha256 matches the committed file (verified).

`boot/SHA256SUMS` (`sha256sum -c` passes):
```
74cf1b3f3d33ba329ff409c7038e3a11aa11a7d677984a6037c38acec129b7a1  undionly.kpxe
d450b67f958d9ec84dd7a825229fd609260db18fab4af4cd6ad19a65f1436aa8  ipxe.efi
d935a828199c4d3032e9e01677b12250ea37e2382106de6ca006be9921bf1aa6  snponly.efi
```

### `hack/build-ipxe.sh` (96 lines)

- `set -euo pipefail` (13).
- **`REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)`, `BOOT_DIR="$REPO_ROOT/boot"`** (15-16). If the script moves to
  `boot/build-ipxe.sh` as the design says, change this to `BOOT_DIR=$(cd "$(dirname "$0")" && pwd)`.
- Defaults: `IPXE_REPO` = github ipxe, `IPXE_REF=v2.0.0`, `IMAGE=${IPXE_BUILD_IMAGE:-docker.io/library/debian:stable-slim}` (17-19).
- Runtime selection: `CONTAINER_RUNTIME`, else podman, else docker, else exit 1 (21-30). The design's native fallback
  must replace that exit.
- Runs the container with `--rm`, the env (`IPXE_REPO`, `IPXE_REF`, `HOST_UID`, `HOST_GID`), an optional `--dns`, the
  proxy env passthrough, and `-v $BOOT_DIR:/out:Z` (34-44).
- In the container:
  - `apt-get install ca-certificates git build-essential liblzma-dev mtools perl xz-utils binutils` (48-51);
  - clone the tag shallow, or clone in full then checkout a sha (53-58);
  - `SOURCE_DATE_EPOCH = commit time` (62-63);
  - `make -C /ipxe/src -j$(nproc) bin/undionly.kpxe bin-x86_64-efi/ipxe.efi bin-x86_64-efi/snponly.efi EMBED=/out/embed.ipxe BUILD_ID=0x${COMMIT:0:8}` (65-72);
  - the embed check greps `chain tftp://\${next-server}/booty.ipxe` in `bin/undionly.kpxe.tmp` and both EFI files
    (76-78). The design's new first chain line still contains that substring, so the old grep would pass, but the
    design asks to grep the new first line;
  - copy the outputs, write `VERSION` (82-91) and `SHA256SUMS` (92), and chown (93).

### `embed.go` (repo root, package `booty`)

```go
//go:embed boot/undionly.kpxe boot/ipxe.efi boot/snponly.efi
var BootFiles embed.FS          // lines 9-10
//go:embed all:web/dist
var WebDist embed.FS            // lines 12-13
```

Upstream wires it with `fs.Sub(booty.BootFiles, "boot")` (`upstream:cmd/main.go:294`) and passes the result to
`tftp.Config.BootFiles` and `server.Options.BootFiles`.

`ensureBootFilesInDataDir` (`cmd/main.go:386-400`) also copies the files into DataDir on startup. This is optional.

The name list is `config.BootFileNames = {"undionly.kpxe","ipxe.efi","snponly.efi"}` with
`IsBootFile = slices.Contains` (`upstream:pkg/config/config.go:329-337`). The fork needs this or an equivalent
`boot.Lookup`. With the design's `boot` package, the embed directive becomes
`//go:embed undionly.kpxe ipxe.efi snponly.efi` with no `fs.Sub` needed.

### `THIRD_PARTY_NOTICES.md`, iPXE section verbatim (lines 6-54)

````
## iPXE

The files under [`boot/`](boot/) -- `undionly.kpxe` (BIOS), `ipxe.efi` and
`snponly.efi` (x86-64 UEFI) -- are unmodified builds of **iPXE**, embedded in
the Booty binary via `//go:embed` and served over TFTP and `GET /boot/<name>`.

* Upstream: <https://github.com/ipxe/ipxe> (<https://ipxe.org>)
* License: GNU General Public License v2.0 with the
  [Unmodified Binary Distribution Licence (UBDL)](https://ipxe.org/licensing)
  exception; see `COPYING`, `COPYING.GPLv2` and `COPYING.UBDL` in the upstream
  tree at the pinned commit.
* Pinned source: tag `v2.0.0`, commit `12798ec29aa8a64d8675c4378b99f5fe28447afb`.
  [`boot/VERSION`](boot/VERSION) records the commit, tag, build date and the
  exact `make` invocation; [`boot/SHA256SUMS`](boot/SHA256SUMS) lists the
  digests of the committed binaries (`sha256sum -c` from inside `boot/`).
* Embedded script: the binaries are built with
  [`boot/embed.ipxe`](boot/embed.ipxe) compiled in (`EMBED=`). That script is
  part of Booty and is MIT licensed; it is the only thing added to the
  upstream build, no iPXE source file is patched.

### Corresponding source

The complete corresponding source for the binaries in `boot/` is the upstream
iPXE repository at the pinned commit above, plus `boot/embed.ipxe` from this
repository. No other modifications are applied.

### Rebuilding

```
make ipxe            # podman (falls back to docker), writes boot/*
CONTAINER_DNS=192.168.50.17 make ipxe   # if the container needs an explicit DNS server
IPXE_REF=<tag|sha> ./hack/build-ipxe.sh # build a different upstream ref
```

[`hack/build-ipxe.sh`](hack/build-ipxe.sh) runs a throwaway
`debian:stable-slim` container, installs the iPXE build dependencies, clones
upstream at the pinned ref and runs

```
make -C src bin/undionly.kpxe bin-x86_64-efi/ipxe.efi bin-x86_64-efi/snponly.efi \
  EMBED=<repo>/boot/embed.ipxe BUILD_ID=0x<first 8 hex digits of the commit>
```

`BUILD_ID` is pinned (iPXE otherwise randomises it) and `SOURCE_DATE_EPOCH` is
set to the commit date, so a rebuild of the same ref yields byte-identical
binaries and the checked-in `SHA256SUMS` can be re-verified independently.
Run it only when bumping the iPXE version or changing `boot/embed.ipxe`, then
commit `boot/` (binaries, `VERSION`, `SHA256SUMS`) and update the commit in
this file.
````

Lines 1-4 of the file are the preamble: "Booty itself is MIT licensed (see LICENSE). The binary additionally embeds the
following third-party software."

Edits the fork must make:
- Licence wording: upstream says "GPL v2.0 with the UBDL exception"; the design says "GPL-2.0-or-later (with UBDL)".
  Pick one and verify it against iPXE's `COPYING*` at the pinned commit.
- Paths: `make ipxe` becomes `task ipxe`, and `hack/build-ipxe.sh` becomes `boot/build-ipxe.sh`.
- "Byte-identical" claim: the design calls reproducibility a non-goal. Soften the sentence, and mention native mode.
- Commit and tag: keep only if the fork builds the same `v2.0.0`.

### Makefile `ipxe` target (`upstream:Makefile:34-40`)

```
# Rebuilds the embedded iPXE binaries in boot/ from the pinned upstream ref
# (see hack/build-ipxe.sh and THIRD_PARTY_NOTICES.md). Run this only when
# bumping the iPXE version or changing boot/embed.ipxe, then commit boot/.
# Not part of `make build`: the binaries are checked in so `go build` needs
# no cross toolchain and works air-gapped.
ipxe:
	CONTAINER_DNS=$(CONTAINER_DNS) ./hack/build-ipxe.sh
```

`ipxe` is also listed in `.PHONY` (line 60).

---

## 7. HTTP `/boot/`: `upstream:pkg/server/http.go`

Last commits:
- `http.go`: `639de71f45b8746b4df40cf8d732548d4a0b7dc0` (2026-10-03)
- `http_test.go`: `b3782a3061846f865be6b4f524e99a5ed854aa55` (2026-10-03)

### Routing before the mux (`NewHandler`, lines 136-151)

```go
	// /boot/ and /bluefin/ are routed before the mux: UEFI HTTP Boot
	// firmware and shim never follow redirects, and http.ServeMux answers
	// unclean paths such as /boot/sb//ipxe.efi with a 301. Both handlers
	// clean the path themselves.
	boot := bootHandler{files: o.BootFiles, secureBootDir: config.SecureBootPath()}
	return logRequest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, bootPathPrefix) {
			boot.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, bluefinPathPrefix) {
			handleBluefinRequest(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
```

`logRequest` (182-195) logs `/boot/` at debug level (quiet list at 183).

### `bootHandler`

| Lines | Content |
|---|---|
| 265-272 | Doc comment |
| 273-276 | `type bootHandler{files fs.FS; secureBootDir string}` |
| 278-283 | Consts `bootPathPrefix="/boot/"`, `bootSBPrefix="sb/"`, `bootSecureBootPrefix="secureboot/"`, `efiContentType="application/efi"` |
| 285-288 | `bootFile{io.ReadCloser; size int64}` |
| 290-322 | `ServeHTTP` |
| 327-352 | `open` |
| 354-376 | `openSecureBoot` (excluded with HTTP Boot / SB) |

`ServeHTTP` (290-322):
- Method other than GET/HEAD → 405 JSON (291-294).
- `clean := path.Clean(r.URL.Path)` (295). The request returns 404 when any of these hold (296-299):
  - the cleaned path does not start with `/boot/`;
  - the raw path ends with `/`;
  - any raw segment is `..`.
- `rel = TrimPrefix(clean, "/boot/")`, then `h.open(rel)`; not found → 404 (300-305).
- Content type: `application/efi` when `path.Ext` is `.efi` (case-insensitive), else `application/octet-stream`
  (308-311).
- Explicit `Content-Length` and `Cache-Control: no-cache`, then `WriteHeader(200)` (312-315).
- **HEAD returns right after the headers** (316-318); GET runs `io.Copy` (319-321).

`open` (327-352), embedded branch only: `h.files != nil && config.IsBootFile(rel)` → `fs.ReadFile` → `bytes.Reader`
(343-349). For the fork, drop the `sb/`, `sb/autoexec.ipxe` and `secureboot/` cases (329-342).

Dependencies:
- `writeError(w, status, msg)` (48-50) writes JSON `{"error":...}`. The fork's `writeError` has a different signature,
  `(w, status, msg, err)` (`pkg/http/errors.go:21`).
- `config.CloseQuietly` (306).
- `config.IsBootFile`.
- stdlib: `bytes`, `io`, `io/fs`, `path`, `slices`, `strconv`, `strings`.

### Where it goes in the fork

The fork's server handler is `Handler: logRequest(myHandler)` in `StartHTTP` (`pkg/http/http.go:72-83`), where
`myHandler := baseMux(deps)` plus `RegisterAPI`. Wrap it there, before the mux.

`baseMux` returns `*http.ServeMux` and is used directly by tests (`pkg/http/auth_test.go:536`,
`pkg/http/http_test.go:406`). A wrapper inside `baseMux` therefore changes its return type; doing it in `StartHTTP`
leaves those tests untouched, but then they will not exercise `/boot/`.

### Tests (`upstream:pkg/server/http_test.go`)

- `testBootFiles` fstest.MapFS (107-111):
  `undionly.kpxe="BIOS-IPXE"`, `ipxe.efi="EFI-IPXE"`, `snponly.efi="SNP-IPXE"`.
- `TestBootHandler` (668-706):
  - GET content and types;
  - HEAD length, type and `no-cache`;
  - 404 for `/boot/`, `/boot/pxelinux.0`, `/boot/../boot/ipxe.efi/x`, `/boot/ipxe.efi/`, `/boot/booty.ipxe`,
    `/boot/sb/...`, `/boot/../ignition.json`;
  - POST → 405;
  - no files → 404.

  It uses `newTestServer` (32-105, which sets a large amount of upstream-only viper state), `do` (119-144) and
  `assertJSONError` (146-160). Port it against a minimal handler instead.
- `doHead` (708-719) sends HEAD without following redirects.
- `TestBootHandlerCollapsesSlashesWithoutRedirect` (721-740) checks that `/boot//ipxe.efi`, `/boot///ipxe.efi` and
  `/boot/./ipxe.efi` return 200 with no 301.
- `TestBootHandlerServesSecureBootBundle` (742-793) and `TestBootSecureBootAutoexecScript` (795-) are SB-only; skip
  them.
- `assertJSONError` expects a JSON body. Adapt it if the fork's 404 body differs.

---

## 8. TFTP: `upstream:pkg/tftp/tftp.go`

Last commits:
- `tftp.go`: `33e87e3964874656b06d6fc646fb9150dddba9e6` (2026-09-26)
- `tftp_test.go`: `f1c27903b7f0a2c61c402b9643149a260ea24d56` (2026-09-27)

| Lines | Content |
|---|---|
| 14 | `github.com/pin/tftp/v3` (upstream `go.mod:18` `v3.2.0`) |
| 20-26 | `type Config{Port, BlockSize int; BootFiles fs.FS}` |
| 28 | Package-global `var cfg Config` |
| 37-56 | `Start(c Config, errCh chan<- error) (*Server, error)` |
| 62-75 | `Shutdown(timeout)` |
| 77-98 | `readHandler` |
| 106-140 | `openRequest` |
| 144-146 | `IPXEStub` |
| 153-166 | `SecureBootAutoexec` (SB only) |

`Start` (37-56):
- `tftp.NewServer(readHandler, nil)` (39). **A nil write handler** makes v3 answer a WRQ with
  `abort("server does not support write requests")` (module cache `pin/tftp/v3@v3.2.0/server.go:420-428`; the doc
  comment at 15-18 says a nil handler disables the operation).
- `SetBlockSize(c.BlockSize)` (40), `EnableSinglePort()` (41), `SetTimeout(60s)` (42).
- `net.ListenUDP("udp", {Port})`: a bind error is **returned** (44-48).
- `go srv.Serve(conn)`: a serve error goes to **`errCh`** (49-53). There is no `os.Exit`.

`Shutdown(timeout)` (62-75) closes the conn first (the single-port loop blocks in `ReadFrom`), then
`srv.Shutdown()` with a timeout.

`readHandler` (77-98) uses a checked assertion `rf.(tftp.OutgoingTransfer)` and `RemoteAddr()`, which returns a
`net.UDPAddr` value in v3.

`openRequest` (106-140):
- NUL → error (107-109);
- leading `/` → error (110-112);
- `booty.ipxe` → stub (114-116);
- embedded boot file (117-119);
- **`os.OpenRoot(DataDir)` opened per request** (121-125), then `root.Open(filename)`, `Stat`, and a directory →
  error (126-139). `os.Root` rejects `..` escapes and symlinks that leave the root.

Port and blocksize are flags `tftpPort` (default 69) and `tftpBlockSize` (default **1468**)
(`upstream:pkg/config/config.go:34-35,460-461`; `upstream:cmd/main.go:51-52`).

Gaps and differences to flag:
- **No allowlist upstream.** Upstream serves *anything* in DataDir that is not a directory (`plain.txt` and
  `sub/inner.txt` are served in its own test). The fork's item 0 allowlist must be added on top. Upstream's positive
  test cases need moving under `public/` or `cache/`.
- **Blocksize subtlety.** v3 `SetBlockSize` ignores values `<= 512` (`server.go:153-158`), and `EnableSinglePort` sets
  `maxBlockLen = 512` when it is still 0 (`server.go:123-131`). `handlePacket` caps negotiation at `s.maxBlockLen`
  (`server.go:370-375`). The consequences:
  - The design's default of 512 is a no-op, so blksize negotiation is uncapped (MTU clamp only) **unless**
    single-port is enabled. Single-port then caps every transfer at 512.
  - Upstream avoids this by defaulting to 1468 and calling `SetBlockSize` before `EnableSinglePort`.
  - The old fork pin has the same `> 512` rule (`pin/tftp@v0.0.0-20210809155059-0161c5dd2e96/server.go:150-154`).
- No upstream test covers a port-bind failure, the `errCh` path, or WRQ refusal. The fork must write those tests.
- The fork still uses unchecked `rf.(tftp.RequestPacketInfo).LocalIP()` (`pkg/tftp/tftp.go:111`). v3 keeps
  `RequestPacketInfo` (`server.go`, around line 35), so this compiles.

Tests (`upstream:pkg/tftp/tftp_test.go`):
- `setupDataDir` (16-36) uses `hardware.Load()`, `viper` `DataDir`/`HardwareMap`/`ServerIP`/`ServerHttpPort`, and
  writes `plain.txt` and `sub/inner.txt`.
- `readAll` (38-50).
- `TestOpenRequestRejectsTraversal` (52-96):
  - rejected: `../outside.txt`, `/etc/passwd`, `sub/../../outside.txt`, `a/../../x`, `plain.txt\x00.ipxe`, an
    `escape` symlink pointing out of the root, the directory `sub`, and `missing.bin`;
  - served: `plain.txt`, `sub/inner.txt`, and an `inside` symlink.
- `TestOpenRequestServesEmbeddedBootFiles` (98-123): the embedded copy wins over the copy on disk.
- `TestOpenRequestBootFilesFallBackToDataDir` (125-142).
- `TestOpenRequestNoPxelinux` (144-157) is **not applicable**: the fork keeps `pxelinux.cfg/default`.
- `TestOpenRequestBootyIPXEStub` (159-170) checks
  `"#!ipxe\nchain http://192.168.1.10:8080/booty.ipxe?mac=${mac}\n"`.

The remaining tests (172-527) cover upstream's script templates and are not relevant.

---

## 9. Licence and attribution

`upstream:LICENSE` is **MIT License, "Copyright (c) 2021 Jeffrey Sica"**. Its last commit is
`65486bf349b20c4f231abd857f1c004a23bf0bfe` (2022-01-31, author Jeffrey Sica). The fork's `LICENSE` is the same file:
same last commit `65486bf`, and `git diff HEAD upstream/main -- LICENSE` is empty.

The ported upstream commits are authored by `Jeffrey Sica <me@jeefy.dev>`; for example `f25ad4c` carries
`Co-authored-by: Sisyphus <clio-agent@sisyphuslabs.ai>`. A source header such as
"Ported from jeefy/booty <path> @ <commit>, MIT, Copyright (c) 2021 Jeffrey Sica" is consistent with the licence. The
MIT notice is already satisfied repo-wide by the identical `LICENSE`.

iPXE binaries are not MIT; see §6.

---

## Non-trivial dependencies (summary)

1. **`pkg/proxydhcp` lifecycle and tests.**
   - `handle` itself is self-contained.
   - The fork's server has fixed ports and a broadcast-bool loop. Upstream's `Start/Addrs/Shutdown/ParsePorts` tests
     and `replyDestination` (needed for `--proxyDHCPRelay`) do not drop in.
   - Config field names differ.
2. **WoL** references `hardware.ErrInvalidMAC`, which does not exist in the fork. Importing `pkg/hardware` also pulls
   in `pkg/db`. Use a local sentinel plus the length-6 check.
3. **Health script.**
   - `HealthUnit` depends on `BootedUnitName` (absent in the fork).
   - The script posts to `/health?mac=` (must become `/node/report` plus a token).
   - The fork has no `pkg/ignition`.
   - `TestHealthUnitParses` needs go-systemd `unit` as a direct dependency.
   - The script tests need real bash, curl and uname.
4. **No upstream `Health.Normalize` unit tests.** They are only covered through upstream's HTTP-server test, so the
   fork needs new tests.
5. **TFTP.**
   - Upstream has no DataDir allowlist; item 0 must be layered on top.
   - The `--tftpBlockSize 512` default plus `EnableSinglePort` interplay (above).
   - No upstream tests for bind error, `errCh` or WRQ.
6. **Golden files.** Not wire captures. The library bump is not required for parity (verified), but it is harmless.
7. **`/boot/` handler.** It needs the fork's 3-argument `writeError` and a pre-mux wrapper in `StartHTTP`, because
   `baseMux` returns `*http.ServeMux` and is used by tests.
