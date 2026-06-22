package proxydhcp

import (
	"net"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
	"github.com/stretchr/testify/require"
)

func testCfg() Config {
	return Config{
		BootfileBIOS:  "undionly.kpxe",
		BootfileUEFI:  "ipxe.efi",
		BootfileARM64: "ipxe-arm64.efi",
	}
}

// reqWithArch builds a minimal PXEClient DISCOVER carrying a client-arch option.
func reqWithArch(t *testing.T, arch iana.Arch) *dhcpv4.DHCPv4 {
	t.Helper()
	req, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeDiscover),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient")),
		dhcpv4.WithOption(dhcpv4.OptClientArch(arch)),
	)
	require.NoError(t, err)
	return req
}

func TestSelectBootfile_Arch(t *testing.T) {
	cfg := testCfg()
	require.Equal(t, "undionly.kpxe", selectBootfile(reqWithArch(t, iana.INTEL_X86PC), cfg))
	require.Equal(t, "ipxe.efi", selectBootfile(reqWithArch(t, iana.EFI_X86_64), cfg))
	require.Equal(t, "ipxe.efi", selectBootfile(reqWithArch(t, iana.EFI_IA32), cfg))
	require.Equal(t, "ipxe-arm64.efi", selectBootfile(reqWithArch(t, iana.EFI_ARM64), cfg))
}

func TestSelectBootfile_NoArchDefaultsBIOS(t *testing.T) {
	cfg := testCfg()
	req, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeDiscover),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient")),
	)
	require.NoError(t, err)
	require.Equal(t, "undionly.kpxe", selectBootfile(req, cfg))
}

func TestSelectBootfile_IPXEUserClassWinsOverArch(t *testing.T) {
	cfg := testCfg()
	req := reqWithArch(t, iana.EFI_X86_64)
	req.UpdateOption(dhcpv4.OptUserClass("iPXE"))
	require.Equal(t, bootfileIPXEScript, selectBootfile(req, cfg))
}

func TestSelectBootfile_IPXELengthPrefixedUserClass(t *testing.T) {
	cfg := testCfg()
	req := reqWithArch(t, iana.EFI_X86_64)
	// RFC-3004 length-prefixed: one class, len 4, "iPXE"
	req.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionUserClassInformation, []byte{0x04, 'i', 'P', 'X', 'E'}))
	require.Equal(t, bootfileIPXEScript, selectBootfile(req, cfg))
}

func replyCfg() Config {
	c := testCfg()
	c.ServerIP = net.IPv4(192, 168, 1, 10)
	return c
}

func TestBuildReply_IgnoresNonPXEClient(t *testing.T) {
	req, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeDiscover),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("not-pxe")),
	)
	require.NoError(t, err)
	_, ok := buildReply(req, replyCfg())
	require.False(t, ok, "non-PXEClient requests must be dropped")
}

func TestBuildReply_DiscoverBecomesOffer(t *testing.T) {
	resp, ok := buildReply(reqWithArch(t, iana.EFI_X86_64), replyCfg())
	require.True(t, ok)
	require.Equal(t, dhcpv4.MessageTypeOffer, resp.MessageType())
}

func TestBuildReply_RequestBecomesAck(t *testing.T) {
	req := reqWithArch(t, iana.EFI_X86_64)
	req.UpdateOption(dhcpv4.OptMessageType(dhcpv4.MessageTypeRequest))
	resp, ok := buildReply(req, replyCfg())
	require.True(t, ok)
	require.Equal(t, dhcpv4.MessageTypeAck, resp.MessageType())
}

func TestBuildReply_NeverAssignsLease(t *testing.T) {
	resp, ok := buildReply(reqWithArch(t, iana.EFI_X86_64), replyCfg())
	require.True(t, ok)
	require.True(t, resp.YourIPAddr.Equal(net.IPv4zero), "must not offer an IP address")
}

func TestBuildReply_SetsNextServerAndBootfile(t *testing.T) {
	resp, ok := buildReply(reqWithArch(t, iana.EFI_X86_64), replyCfg())
	require.True(t, ok)
	require.Equal(t, "ipxe.efi", resp.BootFileName)
	require.Equal(t, "192.168.1.10", resp.ServerHostName)
}

func TestBuildReply_UnknownMessageTypeDropped(t *testing.T) {
	req := reqWithArch(t, iana.EFI_X86_64)
	req.UpdateOption(dhcpv4.OptMessageType(dhcpv4.MessageTypeRelease))
	_, ok := buildReply(req, replyCfg())
	require.False(t, ok)
}

func TestBuildReply_EchoesClientMachineID(t *testing.T) {
	req := reqWithArch(t, iana.EFI_X86_64)
	guid := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	req.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, guid))
	resp, ok := buildReply(req, replyCfg())
	require.True(t, ok)
	require.Equal(t, guid, resp.Options.Get(dhcpv4.OptionClientMachineIdentifier))
}
