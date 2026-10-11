package proxydhcp

import (
	"net"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
	"github.com/stretchr/testify/require"
)

// TestNewServerThenShutdown verifies the lifecycle invariant that a server which
// was constructed but never Start()ed can still be Shutdown() cleanly — no panic,
// no deadlock. T5 starts proxyDHCP best-effort, so a never-started server reaching
// Shutdown is a real, reachable state.
func TestNewServerThenShutdown(t *testing.T) {
	s, err := NewServer(Config{ServerIP: net.IPv4(192, 0, 2, 10)})
	require.NoError(t, err)
	require.NotNil(t, s)

	done := make(chan struct{})
	go func() { s.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown on a never-started server hung")
	}
}

func TestNewServerRequiresServerIP(t *testing.T) {
	s, err := NewServer(Config{})
	require.Error(t, err)
	require.Nil(t, s)
}

// respondFixture returns a Server and a connected loopback pair: srvConn is
// what respond writes through, cliConn is where the reply is observed.
func respondFixture(t *testing.T) (*Server, *net.UDPConn, *net.UDPConn) {
	t.Helper()
	srvConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srvConn.Close() })

	cliConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cliConn.Close() })

	s := &Server{cfg: Config{ServerIP: net.IPv4(127, 0, 0, 1)}.withDefaults(), done: make(chan struct{})}
	return s, srvConn, cliConn
}

// TestRespondSendsRequestAckToSource proves respond wires handle to the socket:
// the 4011 ACK goes to the request source, where a loopback reader can see it.
// It uses unprivileged 127.0.0.1 sockets so it runs without root in CI.
func TestRespondSendsRequestAckToSource(t *testing.T) {
	s, srvConn, cliConn := respondFixture(t)

	req := pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64)
	s.respond(srvConn, cliConn.LocalAddr().(*net.UDPAddr), req, s.cfg.Port4011)

	require.NoError(t, cliConn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, maxPacketSize)
	n, _, err := cliConn.ReadFromUDP(buf)
	require.NoError(t, err, "no reply delivered to the request source")

	resp, err := dhcpv4.FromBytes(buf[:n])
	require.NoError(t, err)
	require.Equal(t, dhcpv4.MessageTypeAck, resp.MessageType())
	require.Equal(t, "ipxe.efi", resp.BootFileName)
}

// TestRespondIgnoresRequestOn67 pins the EDK2 lease-decline fix at the socket
// layer: a REQUEST on port 67 belongs to the site DHCP server.
func TestRespondIgnoresRequestOn67(t *testing.T) {
	s, srvConn, cliConn := respondFixture(t)

	req := pxePacket(t, dhcpv4.MessageTypeRequest, iana.EFI_X86_64)
	s.respond(srvConn, cliConn.LocalAddr().(*net.UDPAddr), req, s.cfg.Port67)

	require.NoError(t, cliConn.SetReadDeadline(time.Now().Add(300*time.Millisecond)))
	buf := make([]byte, maxPacketSize)
	_, _, err := cliConn.ReadFromUDP(buf)
	var ne net.Error
	require.ErrorAs(t, err, &ne)
	require.True(t, ne.Timeout(), "expected a read timeout, got %v", err)
}
