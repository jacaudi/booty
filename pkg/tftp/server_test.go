package tftp

import (
	"bytes"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pin/tftp/v3"
	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
)

func startTestServer(t *testing.T, dataDir string, bootFiles fs.FS) (*Server, *tftp.Client) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ServerIP, "127.0.0.1")
	viper.Set(config.ServerHttpPort, 8080)
	s, err := Start(Config{
		Port:              0,
		DataDir:           dataDir,
		TopLevelBootFiles: []string{"undionly.kpxe"},
		BootFiles:         bootFiles,
		ARP:               func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no arp in tests") },
	}, make(chan error, 1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Shutdown)
	// Dial 127.0.0.1: the bound address is the wildcard, which a client cannot dial.
	c, err := tftp.NewClient(net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Addr().(*net.UDPAddr).Port)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return s, c
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestServerServesAllowlistedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "public", "hello.txt"), "hello")
	_, c := startTestServer(t, dir, nil)
	wt, err := c.Receive("public/hello.txt", "octet")
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var buf bytes.Buffer
	if _, copyErr := wt.WriteTo(&buf); copyErr != nil {
		t.Fatalf("WriteTo: %v", copyErr)
	}
	if buf.String() != "hello" {
		t.Fatalf("got %q, want %q", buf.String(), "hello")
	}
}

func TestServerRefusesSecrets(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "api-token"), "secret")
	_, c := startTestServer(t, dir, nil)
	if _, err := c.Receive("api-token", "octet"); err == nil {
		t.Fatal("Receive(api-token) succeeded, want error")
	}
}

func TestServerRefusesWrites(t *testing.T) {
	dir := t.TempDir()
	_, c := startTestServer(t, dir, nil)
	_, err := c.Send("public/new.txt", "octet")
	if err == nil {
		t.Fatal("Send succeeded, want error")
	}
	if !strings.Contains(err.Error(), "does not support write requests") {
		t.Fatalf("err = %q, want it to contain %q", err, "does not support write requests")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "public", "new.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("public/new.txt exists or stat failed unexpectedly: %v", statErr)
	}
}

func TestStartReturnsBindError(t *testing.T) {
	l, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.LocalAddr().(*net.UDPAddr).Port
	if _, startErr := Start(Config{Port: port, DataDir: t.TempDir()}, make(chan error, 1)); startErr == nil {
		t.Fatal("Start on a busy port succeeded, want error")
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	s, err := Start(Config{Port: 0, DataDir: t.TempDir()}, make(chan error, 1))
	if err != nil {
		t.Fatal(err)
	}
	s.Shutdown()
	s.Shutdown()
}
