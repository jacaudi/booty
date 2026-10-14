// Package proxydhcp implements a gated, default-off proxyDHCP responder.
//
// It answers PXEClient boot requests with next-server + bootfile only and
// never assigns an IP lease, so it coexists with the site's real DHCP server.
// Bare-metal firmware speaks plain PXE, so it answers in two steps: the
// firmware DISCOVER on UDP 67 gets an OFFER pointing at booty, and the REQUEST
// that follows on UDP 4011 gets the architecture-appropriate iPXE binary.
// iPXE clients (user class "iPXE") never send the 4011 REQUEST, so their OFFER
// itself carries booty.ipxe, which booty's TFTP server already serves.
package proxydhcp

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

// Server is the running proxyDHCP responder (two UDP listeners).
type Server struct {
	cfg       Config
	conn67    *net.UDPConn
	conn4011  *net.UDPConn
	wg        sync.WaitGroup
	done      chan struct{}
	closeOnce sync.Once
}

// NewServer validates cfg and returns a non-started Server.
func NewServer(cfg Config) (*Server, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, done: make(chan struct{})}, nil
}

// Start opens the UDP/67 and UDP/4011 listeners and serves in the background.
// UDP/67 requires CAP_NET_BIND_SERVICE (or root); the error says so.
func (s *Server) Start() error {
	conn67, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: s.cfg.Port67})
	if err != nil {
		return fmt.Errorf("proxydhcp: listen UDP/%d: %w (needs root or CAP_NET_BIND_SERVICE)", s.cfg.Port67, err)
	}
	if err := enableBroadcast(conn67); err != nil {
		return errors.Join(fmt.Errorf("proxydhcp: enable broadcast on UDP/%d: %w", s.cfg.Port67, err), conn67.Close())
	}
	s.conn67 = conn67

	conn4011, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: s.cfg.Port4011})
	if err != nil {
		return errors.Join(fmt.Errorf("proxydhcp: listen UDP/%d: %w", s.cfg.Port4011, err), conn67.Close())
	}
	if err = enableBroadcast(conn4011); err != nil {
		return errors.Join(
			fmt.Errorf("proxydhcp: enable broadcast on UDP/%d: %w", s.cfg.Port4011, err),
			conn67.Close(), conn4011.Close(),
		)
	}
	s.conn4011 = conn4011

	slog.Info("proxyDHCP listening",
		"ports", fmt.Sprintf("%d+%d", s.cfg.Port67, s.cfg.Port4011), "next_server", s.cfg.ServerIP.String(),
		"relay", s.cfg.Relay,
		"bios", s.cfg.BIOSFile, "efi", s.cfg.EFIFile, "arm64", s.cfg.ARM64File, "ipxe", s.cfg.IPXEScript)

	s.wg.Go(func() { s.loop(conn67, s.cfg.Port67) })
	s.wg.Go(func() { s.loop(conn4011, s.cfg.Port4011) })
	return nil
}

// Shutdown stops both listeners and waits for the serve goroutines to exit.
// Closing the conns unblocks ReadFromUDP, so this is inherently bounded. It is
// idempotent: the close runs once, but every caller blocks until the goroutines
// have exited. It returns the errors from closing the sockets (only the first
// call can).
func (s *Server) Shutdown() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn67 != nil {
			err = errors.Join(err, s.conn67.Close())
		}
		if s.conn4011 != nil {
			err = errors.Join(err, s.conn4011.Close())
		}
	})
	s.wg.Wait()
	return err
}

// loop reads and answers requests until Shutdown. port is the configured port
// the conn listens on, which handle uses to gate DISCOVER (67) and REQUEST (4011).
// maxPacketSize bounds a single DHCP datagram read; 1500 is the Ethernet MTU.
const maxPacketSize = 1500

func (s *Server) loop(conn *net.UDPConn, port int) {
	buf := make([]byte, maxPacketSize)
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
		s.respond(conn, src, req, port)
	}
}

// respond runs the protocol core for one request and writes any reply to the
// destination RFC 2131 section 4.1 picks.
func (s *Server) respond(conn *net.UDPConn, src *net.UDPAddr, req *dhcpv4.DHCPv4, port int) {
	resp, ok := handle(req, s.cfg, port)
	if !ok {
		return
	}
	dst := replyDestination(req, src)
	if _, err := conn.WriteToUDP(resp.ToBytes(), dst); err != nil {
		slog.Warn("proxyDHCP send error", "err", err)
		return
	}
	slog.Info("proxyDHCP reply",
		"client", req.ClientHWAddr.String(), "type", resp.MessageType().String(),
		"to", dst.String(), "file", resp.BootFileName)
}
