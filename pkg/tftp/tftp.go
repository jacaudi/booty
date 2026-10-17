// Package tftp serves boot files over TFTP: the embedded iPXE binaries, an
// explicit allowlist of dataDir, and the iPXE stub and menu scripts from
// package bootscript.
package tftp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/j-keck/arping"
	"github.com/pin/tftp/v3"

	"github.com/jeefy/booty/pkg/bootscript"
	"github.com/jeefy/booty/pkg/hardware"
)

// server owns the state readHandler needs for one running TFTP server.
type server struct {
	root      *os.Root
	topLevel  []string
	bootFiles fs.FS
	arp       func(net.IP) (net.HardwareAddr, error)
	tickets   bootscript.TicketIssuer
}

// hostFromARP identifies the requesting host by ARP and loads its record. It
// returns nil when the MAC cannot be resolved or is unknown.
func (s *server) hostFromARP(ip net.IP) *hardware.Host {
	hwAddr, err := s.arp(ip)
	if err != nil {
		slog.Warn("error with ARP request", "err", err)
		return nil
	}
	macAddress := hwAddr.String()
	host, lookupErr := hardware.GetMacAddress(macAddress)
	if lookupErr != nil && !errors.Is(lookupErr, hardware.ErrNotFound) {
		slog.Warn("TFTP: error looking up host", "mac", macAddress, "err", lookupErr)
	}
	return host
}

// readHandler is called when client starts file download from server
func (s *server) readHandler(filename string, rf io.ReaderFrom) error {
	slog.Info("TFTP get", "file", filename)
	raddr := rf.(tftp.OutgoingTransfer).RemoteAddr()
	laddr := rf.(tftp.RequestPacketInfo).LocalIP()
	slog.Debug("RRQ", "from", raddr.String(), "to", laddr.String())

	urlHost := bootscript.URLHost()

	if strings.HasPrefix(filename, "menu/") && strings.HasSuffix(filename, "/boot.ipxe") {
		toServe := bootscript.MenuSelectionScript(s.hostFromARP(raddr.IP), filename, urlHost, s.tickets)
		r := strings.NewReader(toServe)
		n, rerr := rf.ReadFrom(r)
		if rerr != nil {
			slog.Warn("TFTP: error sending menu selection response", "err", rerr)
			return rerr
		}
		slog.Info("bytes sent", "bytes", n, "file", filename, "kind", "menu-selection")
		return nil
	}

	if filename == "booty.ipxe" {
		r := strings.NewReader(bootscript.StubScript(urlHost))
		n, err := rf.ReadFrom(r)
		if err != nil {
			slog.Warn("error reading iPXE config", "err", err)
			return err
		}
		slog.Info("bytes sent", "bytes", n, "file", filename, "kind", "stub")
		return nil
	}

	if served, err := s.serveEmbedded(filename, rf); served {
		return err
	}
	file, err := openDataFile(s.root, filename, s.topLevel)
	if err != nil {
		slog.Warn("TFTP rejected", "client", raddr.String(), "requested", filename, "err", err)
		return os.ErrNotExist
	}
	defer file.Close()
	n, err := rf.ReadFrom(file)
	if err != nil {
		return err
	}
	slog.Info("bytes sent", "bytes", n, "file", filename)
	return nil
}

// serveEmbedded sends filename from the embedded boot files. served is false
// when there is no such embedded file, so the caller falls through to dataDir.
func (s *server) serveEmbedded(filename string, rf io.ReaderFrom) (bool, error) {
	if s.bootFiles == nil {
		return false, nil
	}
	b, readErr := fs.ReadFile(s.bootFiles, filename)
	if readErr != nil {
		return false, nil //nolint:nilerr // any read error means "not embedded": fall through to dataDir
	}
	n, sendErr := rf.ReadFrom(bytes.NewReader(b))
	if sendErr != nil {
		return true, sendErr
	}
	slog.Info("bytes sent", "bytes", n, "file", filename, "source", "embedded")
	return true, nil
}

const (
	blockSize       = 512
	transferTimeout = 60 * time.Second // v3 defaults to 5 s; slow PXE clients need longer
)

// Config is everything Start needs. Port is used as given; 0 binds an
// ephemeral port (tests). cmd always passes --tftpPort (default 69).
type Config struct {
	Port              int
	DataDir           string
	TopLevelBootFiles []string
	BootFiles         fs.FS                                  // embedded iPXE files served first; nil = none
	ARP               func(net.IP) (net.HardwareAddr, error) // nil means arping.Ping
	Tickets           bootscript.TicketIssuer                // mints the boot ticket in menu/ selection scripts; required
}

// Server is a running TFTP server.
type Server struct {
	srv  *tftp.Server
	conn net.PacketConn
	root *os.Root
	once sync.Once
}

// errNoTicketIssuer is Start's refusal when Config.Tickets is nil.
var errNoTicketIssuer = errors.New("tftp: no boot ticket issuer")

// Start opens dataDir, binds the UDP port and serves TFTP in a background
// goroutine. Bind errors are returned; serve errors are sent on errCh. The
// server is read-only: it has no write handler.
func Start(cfg Config, errCh chan<- error) (*Server, error) {
	if cfg.Tickets == nil {
		return nil, errNoTicketIssuer
	}
	root, err := os.OpenRoot(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("tftp: open dataDir: %w", err)
	}
	arp := cfg.ARP
	if arp == nil {
		arp = func(ip net.IP) (net.HardwareAddr, error) {
			hw, _, pingErr := arping.Ping(ip)
			return hw, pingErr
		}
	}
	h := &server{root: root, topLevel: cfg.TopLevelBootFiles, bootFiles: cfg.BootFiles, arp: arp, tickets: cfg.Tickets}

	// A nil write handler makes the library answer WRQ with an error.
	srv := tftp.NewServer(h.readHandler, nil)
	srv.SetBlockSize(blockSize)
	srv.EnableSinglePort()
	srv.SetTimeout(transferTimeout)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.Port})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("tftp: listen on UDP/%d: %w", cfg.Port, err), root.Close())
	}
	go func() {
		if serveErr := srv.Serve(conn); serveErr != nil {
			errCh <- fmt.Errorf("tftp: %w", serveErr)
		}
	}()
	slog.Info("TFTP server started", "addr", conn.LocalAddr().String())
	return &Server{srv: srv, conn: conn, root: root}, nil
}

// Addr returns the bound local address.
func (s *Server) Addr() net.Addr { return s.conn.LocalAddr() }

// Shutdown stops serving, closes the socket and the root, and returns the
// errors from closing them. It is idempotent: only the first call closes.
// In single-port mode the library's Shutdown only cancels and sets a read
// deadline, so the socket is closed here.
func (s *Server) Shutdown() error {
	var err error
	s.once.Do(func() {
		s.srv.Shutdown()
		err = errors.Join(s.conn.Close(), s.root.Close())
	})
	return err
}
