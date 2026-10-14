// Ported from jeefy/booty pkg/dhcp/pxe_test.go @ d7d92a7 (MIT).

package proxydhcp

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"net"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

var (
	testMAC      = net.HardwareAddr{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xcc}
	testServerIP = net.IPv4(192, 168, 1, 10)
)

func testConfig() Config {
	return Config{ServerIP: testServerIP}
}

func pxeClass(arch iana.Arch) string {
	if arch == iana.INTEL_X86PC {
		return "PXEClient:Arch:00000:UNDI:002001"
	}
	return "PXEClient:Arch:00007:UNDI:003016"
}

func pxePacket(t *testing.T, mt dhcpv4.MessageType, arch iana.Arch, mods ...dhcpv4.Modifier) *dhcpv4.DHCPv4 {
	t.Helper()
	base := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier(pxeClass(arch))),
		dhcpv4.WithOption(dhcpv4.OptClientArch(arch)),
	}
	pkt, err := dhcpv4.NewDiscovery(testMAC, append(base, mods...)...)
	if err != nil {
		t.Fatalf("building packet: %v", err)
	}
	return pkt
}

// portFor is the port a well-behaved client sends this message to: DISCOVER
// (and everything else) to 67, the PXE boot-server REQUEST to 4011.
func portFor(pkt *dhcpv4.DHCPv4, c Config) int {
	c = c.withDefaults()
	if pkt.MessageType() == dhcpv4.MessageTypeRequest {
		return c.Port4011
	}
	return c.Port67
}

func mustHandle(t *testing.T, pkt *dhcpv4.DHCPv4, c Config) *dhcpv4.DHCPv4 {
	t.Helper()
	reply, ok := handle(pkt, c, portFor(pkt, c))
	if !ok || reply == nil {
		t.Fatalf("expected a reply, got silence")
	}
	return reply
}

func mustBeSilent(t *testing.T, pkt *dhcpv4.DHCPv4, c Config) {
	t.Helper()
	if reply, ok := handle(pkt, c, portFor(pkt, c)); ok || reply != nil {
		t.Fatalf("expected silence, got %s", reply.Summary())
	}
}

func subOptions(t *testing.T, reply *dhcpv4.DHCPv4) map[uint8][]byte {
	t.Helper()
	raw := reply.Options.Get(dhcpv4.OptionVendorSpecificInformation)
	if raw == nil {
		t.Fatal("reply has no option 43")
	}
	subs, err := parseSubOptions(raw)
	if err != nil {
		t.Fatalf("option 43 %x: %v", raw, err)
	}
	return subs
}

func TestDiscoverProducesPXEOffer(t *testing.T) {
	pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC,
		dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, []byte{0, 1, 2, 3})))
	reply := mustHandle(t, pkt, testConfig())

	if reply.OpCode != dhcpv4.OpcodeBootReply {
		t.Errorf("opcode = %v, want BootReply", reply.OpCode)
	}
	if reply.MessageType() != dhcpv4.MessageTypeOffer {
		t.Errorf("message type = %v, want OFFER", reply.MessageType())
	}
	if reply.TransactionID != pkt.TransactionID {
		t.Errorf("xid = %v, want %v", reply.TransactionID, pkt.TransactionID)
	}
	if !bytes.Equal(reply.ClientHWAddr, testMAC) {
		t.Errorf("chaddr = %v, want %v", reply.ClientHWAddr, testMAC)
	}
	if !reply.YourIPAddr.IsUnspecified() {
		t.Errorf("yiaddr = %v, want 0.0.0.0", reply.YourIPAddr)
	}
	if !reply.ServerIPAddr.Equal(testServerIP) {
		t.Errorf("siaddr = %v, want %v", reply.ServerIPAddr, testServerIP)
	}
	if !reply.ServerIdentifier().Equal(testServerIP) {
		t.Errorf("option 54 = %v, want %v", reply.ServerIdentifier(), testServerIP)
	}
	if reply.ClassIdentifier() != "PXEClient" {
		t.Errorf("option 60 = %q, want PXEClient", reply.ClassIdentifier())
	}
	if got := reply.Options.Get(dhcpv4.OptionClientMachineIdentifier); !bytes.Equal(got, []byte{0, 1, 2, 3}) {
		t.Errorf("option 97 = %x, want echoed 00010203", got)
	}
	if reply.BootFileName != "" || reply.Options.Has(dhcpv4.OptionBootfileName) {
		t.Errorf(
			"OFFER must not carry a boot file, got file=%q opt67=%v",
			reply.BootFileName,
			reply.Options.Has(dhcpv4.OptionBootfileName),
		)
	}

	subs := subOptions(t, reply)
	if got := subs[pxeDiscoveryControl]; !bytes.Equal(got, []byte{0x07}) {
		t.Errorf("PXE_DISCOVERY_CONTROL = %x, want 03", got)
	}
	wantServers := append([]byte{0x80, 0x00, 0x01}, testServerIP.To4()...)
	if got := subs[pxeBootServers]; !bytes.Equal(got, wantServers) {
		t.Errorf("PXE_BOOT_SERVERS = %x, want %x", got, wantServers)
	}
	wantMenu := append([]byte{0x80, 0x00, byte(len("Booty"))}, "Booty"...)
	if got := subs[pxeBootMenu]; !bytes.Equal(got, wantMenu) {
		t.Errorf("PXE_BOOT_MENU = %x, want %x", got, wantMenu)
	}
	wantPrompt := append([]byte{0x00}, "Booty"...)
	if got := subs[pxeMenuPrompt]; !bytes.Equal(got, wantPrompt) {
		t.Errorf("PXE_MENU_PROMPT = %x, want %x", got, wantPrompt)
	}
	raw := reply.Options.Get(dhcpv4.OptionVendorSpecificInformation)
	if raw[len(raw)-1] != pxeEnd {
		t.Errorf("option 43 does not end with 0xff: %x", raw)
	}
	if _, ok := subs[pxeBootItem]; ok {
		t.Error("OFFER must not contain PXE_BOOT_ITEM")
	}
}

func TestRequestAckBootFileByArch(t *testing.T) {
	cases := []struct {
		arch iana.Arch
		want string
	}{
		{iana.INTEL_X86PC, "undionly.kpxe"},
		{iana.EFI_IA32, "ipxe.efi"},
		{iana.EFI_X86_64, "ipxe.efi"},
		{iana.EFI_BC, "ipxe.efi"},
		{iana.EFI_ARM64, "ipxe-arm64.efi"},
		{iana.EFI_X86_64_HTTP, "undionly.kpxe"},
	}
	for _, tc := range cases {
		t.Run(tc.arch.String(), func(t *testing.T) {
			pkt := pxePacket(t, dhcpv4.MessageTypeRequest, tc.arch,
				dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 50)),
				dhcpv4.WithOption(dhcpv4.OptServerIdentifier(testServerIP)))
			reply := mustHandle(t, pkt, testConfig())

			if reply.MessageType() != dhcpv4.MessageTypeAck {
				t.Fatalf("message type = %v, want ACK", reply.MessageType())
			}
			if reply.BootFileName != tc.want {
				t.Errorf("file = %q, want %q", reply.BootFileName, tc.want)
			}
			if got := reply.BootFileNameOption(); got != tc.want {
				t.Errorf("option 67 = %q, want %q", got, tc.want)
			}
			if !reply.ServerIPAddr.Equal(testServerIP) {
				t.Errorf("siaddr = %v, want %v", reply.ServerIPAddr, testServerIP)
			}
			if got := reply.TFTPServerName(); got != testServerIP.String() {
				t.Errorf("option 66 = %q, want %q", got, testServerIP)
			}
			if reply.ClassIdentifier() != "PXEClient" {
				t.Errorf("option 60 = %q, want PXEClient", reply.ClassIdentifier())
			}
			if !reply.ClientIPAddr.Equal(net.IPv4(192, 168, 1, 50)) {
				t.Errorf("ciaddr = %v, want copied from request", reply.ClientIPAddr)
			}
			subs := subOptions(t, reply)
			if got := subs[pxeBootItem]; !bytes.Equal(got, []byte{0x80, 0, 0, 0}) {
				t.Errorf("PXE_BOOT_ITEM = %x, want 80000000", got)
			}
			if len(subs) != 1 {
				t.Errorf("ACK option 43 should only carry PXE_BOOT_ITEM, got %v", subs)
			}
		})
	}
}

func TestRequestCopiesClientFields(t *testing.T) {
	relay := net.IPv4(10, 0, 0, 1)
	pkt := pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64,
		dhcpv4.WithBroadcast(true), dhcpv4.WithGatewayIP(relay))
	c := testConfig()
	c.Relay = true
	reply := mustHandle(t, pkt, c)
	if reply.TransactionID != pkt.TransactionID {
		t.Errorf("xid not copied")
	}
	if !bytes.Equal(reply.ClientHWAddr, testMAC) {
		t.Errorf("chaddr not copied")
	}
	if !reply.IsBroadcast() {
		t.Errorf("broadcast flag not copied")
	}
	if !reply.GatewayIPAddr.Equal(relay) {
		t.Errorf("giaddr = %v, want %v", reply.GatewayIPAddr, relay)
	}
}

func TestSilentWithoutClassIdentifier(t *testing.T) {
	pkt := pxePacket(
		t,
		dhcpv4.MessageTypeDiscover,
		iana.INTEL_X86PC,
		dhcpv4.WithoutOption(dhcpv4.OptionClassIdentifier),
	)
	mustBeSilent(t, pkt, testConfig())
}

func TestSilentForNonPXEClass(t *testing.T) {
	pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC,
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("MSFT 5.0")))
	mustBeSilent(t, pkt, testConfig())
}

func TestSilentWithoutArchitecture(t *testing.T) {
	pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC,
		dhcpv4.WithoutOption(dhcpv4.OptionClientSystemArchitectureType))
	mustBeSilent(t, pkt, testConfig())
}

func TestRelayedPacketsNeedRelayFlag(t *testing.T) {
	pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC, dhcpv4.WithGatewayIP(net.IPv4(10, 0, 0, 1)))
	mustBeSilent(t, pkt, testConfig())

	c := testConfig()
	c.Relay = true
	reply := mustHandle(t, pkt, c)
	if reply.MessageType() != dhcpv4.MessageTypeOffer {
		t.Errorf("message type = %v, want OFFER", reply.MessageType())
	}
}

func TestSilentForOtherServersAndOwnReplies(t *testing.T) {
	other := pxePacket(t, dhcpv4.MessageTypeRequest, iana.INTEL_X86PC,
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(192, 168, 1, 1))))
	mustBeSilent(t, other, testConfig())

	discover := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC)
	offer := mustHandle(t, discover, testConfig())
	mustBeSilent(t, offer, testConfig())

	inform := pxePacket(t, dhcpv4.MessageTypeInform, iana.INTEL_X86PC)
	mustBeSilent(t, inform, testConfig())
}

func TestIPXEUserClassGetsScriptNotBinary(t *testing.T) {
	for name, mod := range map[string]dhcpv4.Modifier{
		"raw":     dhcpv4.WithUserClass("iPXE", false),
		"rfc3004": dhcpv4.WithUserClass("iPXE", true),
	} {
		t.Run(name, func(t *testing.T) {
			pkt := pxePacket(t, dhcpv4.MessageTypeRequest, iana.INTEL_X86PC, mod)
			reply := mustHandle(t, pkt, testConfig())
			if reply.BootFileName != "booty.ipxe" {
				t.Errorf("file = %q, want booty.ipxe", reply.BootFileName)
			}
			if got := reply.BootFileNameOption(); got != "booty.ipxe" {
				t.Errorf("option 67 = %q, want booty.ipxe", got)
			}
			if reply.Options.Has(dhcpv4.OptionEtherboot) {
				t.Error("reply must not carry option 175")
			}
			if !reply.ServerIPAddr.Equal(testServerIP) {
				t.Errorf("siaddr = %v, want %v", reply.ServerIPAddr, testServerIP)
			}
		})
	}
}

func TestConfigFileOverrides(t *testing.T) {
	c := Config{
		ServerIP:   testServerIP,
		BIOSFile:   "custom.kpxe",
		EFIFile:    "custom.efi",
		ARM64File:  "arm.efi",
		IPXEScript: "menu.ipxe",
	}
	for _, tc := range []struct {
		arch iana.Arch
		mods []dhcpv4.Modifier
		want string
	}{
		{iana.INTEL_X86PC, nil, "custom.kpxe"},
		{iana.EFI_X86_64, nil, "custom.efi"},
		{iana.EFI_ARM64, nil, "arm.efi"},
		{iana.INTEL_X86PC, []dhcpv4.Modifier{dhcpv4.WithUserClass("iPXE", false)}, "menu.ipxe"},
	} {
		reply := mustHandle(t, pxePacket(t, dhcpv4.MessageTypeRequest, tc.arch, tc.mods...), c)
		if reply.BootFileName != tc.want {
			t.Errorf("arch %v: file = %q, want %q", tc.arch, reply.BootFileName, tc.want)
		}
	}
}

func TestReplyDestination(t *testing.T) {
	src := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 50), Port: 4011}
	cases := []struct {
		name string
		mods []dhcpv4.Modifier
		src  *net.UDPAddr
		want *net.UDPAddr
	}{
		{
			"broadcast flag",
			[]dhcpv4.Modifier{dhcpv4.WithBroadcast(true)},
			nil,
			&net.UDPAddr{IP: net.IPv4bcast, Port: 68},
		},
		{"no source address", nil, &net.UDPAddr{IP: net.IPv4zero, Port: 68}, &net.UDPAddr{IP: net.IPv4bcast, Port: 68}},
		{"unicast source", nil, src, src},
		{
			"ciaddr",
			[]dhcpv4.Modifier{dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 60))},
			src,
			&net.UDPAddr{IP: net.IPv4(192, 168, 1, 60), Port: 68},
		},
		{
			// EDK2's boot-server REQUEST comes from port 4011 and its
			// ACK must go back there, not to 68.
			"ciaddr matches source",
			[]dhcpv4.Modifier{dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 50))},
			src,
			src,
		},
		{
			"ciaddr matches source from 68",
			[]dhcpv4.Modifier{dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 50))},
			&net.UDPAddr{IP: net.IPv4(192, 168, 1, 50), Port: 68},
			&net.UDPAddr{IP: net.IPv4(192, 168, 1, 50), Port: 68},
		},
		{
			"relay",
			[]dhcpv4.Modifier{dhcpv4.WithGatewayIP(net.IPv4(10, 0, 0, 1)), dhcpv4.WithBroadcast(true)},
			nil,
			&net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 67},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkt := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.INTEL_X86PC, tc.mods...)
			got := replyDestination(pkt, tc.src)
			if !got.IP.Equal(tc.want.IP) || got.Port != tc.want.Port {
				t.Errorf("destination = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseSubOptionsRejectsMalformed(t *testing.T) {
	for _, bad := range [][]byte{{6}, {6, 5, 1}, {6, 1, 3}} {
		if _, err := parseSubOptions(bad); err == nil {
			t.Errorf("parseSubOptions(%x) accepted malformed input", bad)
		}
	}
	subs, err := parseSubOptions(offerVendorOptions(testServerIP))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 4 {
		t.Errorf("offer sub-options = %v, want 4 entries", subs)
	}
}

func TestOfferVendorOptionsLayout(t *testing.T) {
	b := offerVendorOptions(testServerIP)
	want := []byte{
		6, 1, 0x07,
		8, 7, 0x80, 0, 1, 192, 168, 1, 10,
		9, 8, 0x80, 0, 5, 'B', 'o', 'o', 't', 'y',
		10, 6, 0, 'B', 'o', 'o', 't', 'y',
		255,
	}
	if !bytes.Equal(b, want) {
		t.Errorf("offerVendorOptions =\n%x\nwant\n%x", b, want)
	}
	if binary.BigEndian.Uint16(want[5:7]) != 0x8000 {
		t.Error("boot server type must be 0x8000: type 0 is local boot and EDK2 aborts with PXE-E21")
	}
}

// TestAckEchoesRequestedBootItem: EDK2's Discover sends PXE_BOOT_ITEM
// (type, layer) in its 4011 REQUEST; the ACK echoes it as dnsmasq does.
func TestAckEchoesRequestedBootItem(t *testing.T) {
	for _, item := range [][]byte{{0x80, 0x00, 0x00, 0x00}, {0x80, 0x00, 0x00, 0x01}} {
		opt43 := append(appendSubOption(nil, pxeBootItem, item), pxeEnd)
		pkt := pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64,
			dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 50)),
			dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.OptionVendorSpecificInformation, opt43)))
		reply, ok := handle(pkt, testConfig(), 4011)
		if !ok {
			t.Error("expected an ACK")
			continue
		}
		if got := subOptions(t, reply)[pxeBootItem]; !bytes.Equal(got, item) {
			t.Errorf("PXE_BOOT_ITEM = %x, want echoed %x", got, item)
		}
	}
}

// TestAckBootItemFallsBackToDefault: a REQUEST whose option 43 is malformed,
// or whose PXE_BOOT_ITEM is not 4 bytes, gets Booty's own item (0x8000,
// layer 0) instead of an echo.
func TestAckBootItemFallsBackToDefault(t *testing.T) {
	want := []byte{0x80, 0x00, 0x00, 0x00}
	for _, tc := range []struct {
		name  string
		opt43 []byte
	}{
		// A non-default item with no end tag: parseSubOptions rejects it.
		{"malformed option 43", appendSubOption(nil, pxeBootItem, []byte{0x80, 0x00, 0x00, 0x01})},
		{"item of the wrong length", append(appendSubOption(nil, pxeBootItem, []byte{0x80, 0x01}), pxeEnd)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkt := pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64,
				dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 50)),
				dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.OptionVendorSpecificInformation, tc.opt43)))
			reply, ok := handle(pkt, testConfig(), 4011)
			if !ok {
				t.Fatal("expected an ACK")
			}
			if got := subOptions(t, reply)[pxeBootItem]; !bytes.Equal(got, want) {
				t.Errorf("PXE_BOOT_ITEM = %x, want default %x", got, want)
			}
		})
	}
}

// TestBuildRepliesLogAtDebug: per-packet OFFER/ACK lines are Debug, so an
// Info-level log carries only respond's "proxyDHCP reply".
func TestBuildRepliesLogAtDebug(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, ok := buildOffer(pxePacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64), testConfig(), log); !ok {
		t.Fatal("expected an OFFER")
	}
	req := pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64, dhcpv4.WithClientIP(net.IPv4(192, 168, 1, 50)))
	if _, ok := buildAck(req, testConfig(), iana.EFI_X86_64, log); !ok {
		t.Fatal("expected an ACK")
	}
	if buf.Len() != 0 {
		t.Fatalf("buildOffer/buildAck logged at Info or above:\n%s", buf.String())
	}
}

// TestRequestOnPort67IsIgnored pins the fix for the OVMF failure seen live:
// the client's broadcast DHCPREQUEST for the real server's lease also
// reaches port 67, and a ProxyDHCP ACK to it (yiaddr 0, boot file set) made
// the firmware DECLINE the lease. Only 4011 answers REQUESTs.
func TestRequestOnPort67IsIgnored(t *testing.T) {
	c := Config{ServerIP: testServerIP}
	pkt := pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64)
	if reply, ok := handle(pkt, c, c.withDefaults().Port67); ok || reply != nil {
		t.Fatalf("REQUEST on 67 must be silent, got %s", reply.Summary())
	}
	if reply, ok := handle(
		pkt,
		c,
		c.withDefaults().Port4011,
	); !ok || reply == nil ||
		reply.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("REQUEST on 4011 must be ACKed")
	}
	disc := pxePacket(t, dhcpv4.MessageTypeDiscover, iana.EFI_X86_64)
	if reply, ok := handle(disc, c, c.withDefaults().Port4011); ok || reply != nil {
		t.Fatalf("DISCOVER on 4011 must be silent, got %s", reply.Summary())
	}
}

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
