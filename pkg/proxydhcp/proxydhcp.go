// Package proxydhcp implements a gated, default-off proxyDHCP responder.
//
// It answers PXEClient boot requests with next-server + bootfile only and
// never assigns an IP lease, so it coexists with the site's real DHCP server.
// Bare-metal firmware speaks plain PXE, so it answers in two passes: pass 1
// hands an architecture-appropriate iPXE binary; pass 2 (the loaded iPXE,
// identified by the "iPXE" DHCP user-class) is handed booty.ipxe, which
// booty's TFTP server already serves.
package proxydhcp

import (
	"net"
	"strings"
	"sync"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

// bootfileIPXEScript is the pass-2 bootfile. It MUST match the filename the
// TFTP readHandler special-cases (pkg/tftp/tftp.go: `filename == "booty.ipxe"`).
const bootfileIPXEScript = "booty.ipxe"

// Config holds the proxyDHCP responder settings. ServerIP is booty's
// LAN-reachable address (also used as TFTP next-server); the Bootfile*
// fields are the per-architecture pass-1 iPXE binaries the operator stages
// into dataDir.
type Config struct {
	ServerIP      net.IP
	BootfileBIOS  string
	BootfileUEFI  string
	BootfileARM64 string
}

// Server is the running proxyDHCP responder (two UDP listeners).
type Server struct {
	cfg      Config
	conn67   *net.UDPConn
	conn4011 *net.UDPConn
	wg       sync.WaitGroup
	done     chan struct{}
}

// selectBootfile picks the bootfile for a request. The iPXE user-class
// (pass 2) takes priority over architecture (pass 1) so the chain-load loop
// terminates at booty.ipxe.
func selectBootfile(req *dhcpv4.DHCPv4, cfg Config) string {
	if isIPXE(req) {
		return bootfileIPXEScript
	}
	switch clientArch(req) {
	case iana.EFI_IA32, iana.EFI_X86_64, iana.EFI_BC:
		return cfg.BootfileUEFI
	case iana.EFI_ARM64:
		return cfg.BootfileARM64
	default:
		return cfg.BootfileBIOS
	}
}

// isIPXE reports whether the request came from iPXE itself. iPXE sets the
// User Class option (77) to "iPXE"; check for the substring to tolerate both
// the raw and RFC-3004 length-prefixed encodings.
func isIPXE(req *dhcpv4.DHCPv4) bool {
	raw := req.Options.Get(dhcpv4.OptionUserClassInformation)
	return strings.Contains(string(raw), "iPXE")
}

// clientArch returns the first client architecture (DHCP option 93), or
// INTEL_X86PC (legacy BIOS) when the option is absent. Multi-arch option-93
// lists are deliberately collapsed to the first entry.
func clientArch(req *dhcpv4.DHCPv4) iana.Arch {
	archs := req.ClientArch()
	if len(archs) == 0 {
		return iana.INTEL_X86PC
	}
	return archs[0]
}
