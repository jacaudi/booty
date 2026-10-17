package tftp

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"testing"
)

// TestARPFailureLogsAtDebug: an ARP failure is normal for a relayed, NATed or
// loopback client, and TFTP's UDP source can be forged, so a per-source budget
// cannot bound it. The failure is logged at Debug, never Warn (#169).
func TestARPFailureLogsAtDebug(t *testing.T) {
	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	s := &server{arp: func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no reply") }}
	if host := s.hostFromARP(net.ParseIP("192.0.2.9")); host != nil {
		t.Fatalf("hostFromARP = %+v, want nil", host)
	}
	logs := logBuf.String()
	if !bytes.Contains(logBuf.Bytes(), []byte(`level=DEBUG msg="error with ARP request"`)) {
		t.Errorf("no Debug ARP record:\n%s", logs)
	}
	if bytes.Contains(logBuf.Bytes(), []byte("level=WARN")) {
		t.Errorf("ARP failure logged at Warn:\n%s", logs)
	}
}
