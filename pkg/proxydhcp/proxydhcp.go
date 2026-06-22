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
	"fmt"
	"log/slog"
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

// buildReply constructs the proxyDHCP reply for a request, or returns ok=false
// when the request must be ignored (not a PXEClient, or an unhandled message
// type). It never assigns an IP lease: YourIPAddr stays 0.0.0.0. This function
// is pure (no I/O) — it is the unit-tested core.
func buildReply(req *dhcpv4.DHCPv4, cfg Config) (*dhcpv4.DHCPv4, bool) {
	if !strings.HasPrefix(req.ClassIdentifier(), "PXEClient") {
		return nil, false
	}

	var respType dhcpv4.MessageType
	switch req.MessageType() {
	case dhcpv4.MessageTypeDiscover:
		respType = dhcpv4.MessageTypeOffer
	case dhcpv4.MessageTypeRequest, dhcpv4.MessageTypeInform:
		respType = dhcpv4.MessageTypeAck
	default:
		return nil, false
	}

	bootfile := selectBootfile(req, cfg)
	resp, err := dhcpv4.NewReplyFromRequest(req,
		dhcpv4.WithMessageType(respType),
		dhcpv4.WithServerIP(cfg.ServerIP),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(cfg.ServerIP)),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient")),
		dhcpv4.WithOption(dhcpv4.OptTFTPServerName(cfg.ServerIP.String())),
		dhcpv4.WithOption(dhcpv4.OptBootFileName(bootfile)),
		dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.OptionVendorSpecificInformation, pxeVendorOptions())),
	)
	if err != nil {
		return nil, false
	}

	// No lease: this is proxyDHCP, the real DHCP server assigns the address.
	resp.YourIPAddr = net.IPv4zero
	// BOOTP fields some PXE firmwares read directly.
	resp.ServerHostName = cfg.ServerIP.String()
	resp.BootFileName = bootfile
	// Echo the client machine GUID when present so firmware can correlate.
	if guid := req.Options.Get(dhcpv4.OptionClientMachineIdentifier); guid != nil {
		resp.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, guid))
	}
	return resp, true
}

// pxeVendorOptions returns the PXE vendor-specific information (option 43):
// PXE discovery control = 0x08 (boot from the supplied bootfile, skip the
// boot-server menu/prompt), terminated by 0xff. Copied verbatim from standard
// proxyDHCP practice.
func pxeVendorOptions() []byte {
	return []byte{0x06, 0x01, 0x08, 0xff}
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

// NewServer validates cfg and returns a non-started Server.
func NewServer(cfg Config) (*Server, error) {
	if cfg.ServerIP == nil {
		return nil, fmt.Errorf("proxydhcp: ServerIP is required")
	}
	if cfg.BootfileBIOS == "" || cfg.BootfileUEFI == "" || cfg.BootfileARM64 == "" {
		return nil, fmt.Errorf("proxydhcp: bootfile names must all be set")
	}
	return &Server{cfg: cfg, done: make(chan struct{})}, nil
}

// Start opens the UDP/67 and UDP/4011 listeners and serves in the background.
// UDP/67 requires CAP_NET_BIND_SERVICE (or root); the error says so.
func (s *Server) Start() error {
	conn67, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 67})
	if err != nil {
		return fmt.Errorf("proxydhcp: listen UDP/67: %w (needs root or CAP_NET_BIND_SERVICE)", err)
	}
	if err := enableBroadcast(conn67); err != nil {
		conn67.Close()
		return fmt.Errorf("proxydhcp: enable broadcast on UDP/67: %w", err)
	}
	s.conn67 = conn67

	conn4011, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 4011})
	if err != nil {
		conn67.Close()
		return fmt.Errorf("proxydhcp: listen UDP/4011: %w", err)
	}
	s.conn4011 = conn4011

	slog.Info("proxyDHCP listening",
		"ports", "67+4011", "next-server", s.cfg.ServerIP.String(),
		"bios", s.cfg.BootfileBIOS, "uefi", s.cfg.BootfileUEFI, "arm64", s.cfg.BootfileARM64)

	s.wg.Add(2)
	go s.loop(conn67, true)
	go s.loop(conn4011, false)
	return nil
}

// Shutdown stops both listeners and waits for the serve goroutines to exit.
// Closing the conns unblocks ReadFromUDP, so this is inherently bounded.
func (s *Server) Shutdown() {
	close(s.done)
	if s.conn67 != nil {
		s.conn67.Close()
	}
	if s.conn4011 != nil {
		s.conn4011.Close()
	}
	s.wg.Wait()
}

// loop reads and answers requests until Shutdown. broadcast picks the reply
// destination: true (UDP/67) broadcasts to :68, false (UDP/4011) unicasts to
// the request source.
func (s *Server) loop(conn *net.UDPConn, broadcast bool) {
	defer s.wg.Done()
	buf := make([]byte, 1500)
	for {
		select {
		case <-s.done:
			return
		default:
		}
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.done:
				return // expected: conn closed by Shutdown
			default:
			}
			slog.Warn("proxyDHCP read error", "err", err)
			continue
		}
		req, err := dhcpv4.FromBytes(buf[:n])
		if err != nil {
			slog.Warn("proxyDHCP parse error", "err", err)
			continue
		}
		s.handle(conn, src, req, broadcast)
	}
}

func (s *Server) handle(conn *net.UDPConn, src *net.UDPAddr, req *dhcpv4.DHCPv4, broadcast bool) {
	resp, ok := buildReply(req, s.cfg)
	if !ok {
		return
	}
	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
	if !broadcast {
		dst = src
	}
	if _, err := conn.WriteToUDP(resp.ToBytes(), dst); err != nil {
		slog.Warn("proxyDHCP send error", "err", err)
		return
	}
	slog.Info("proxyDHCP offer",
		"client", req.ClientHWAddr.String(), "type", req.MessageType().String(),
		"bootfile", resp.BootFileName)
}
