package proxydhcp

import (
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
