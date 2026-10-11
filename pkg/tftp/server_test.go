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
	"testing/fstest"

	"github.com/pin/tftp/v3"
	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
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

// The legacy PXELINUX path is retired: pxelinux.cfg/default is not
// allowlisted, so a read gets a TFTP file-not-found error (code 1).
func TestServerPXELinuxConfigNotFound(t *testing.T) {
	_, c := startTestServer(t, t.TempDir(), nil)
	wt, err := c.Receive("pxelinux.cfg/default", "octet")
	if err == nil {
		var buf bytes.Buffer
		_, err = wt.WriteTo(&buf)
		if err == nil {
			t.Fatalf("Receive(pxelinux.cfg/default) served %q, want file-not-found", buf.String())
		}
	}
	if !strings.Contains(err.Error(), os.ErrNotExist.Error()) {
		t.Fatalf("err = %q, want it to contain %q", err, os.ErrNotExist.Error())
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

func TestServerPrefersEmbeddedBootFiles(t *testing.T) {
	cases := []struct {
		name      string
		bootFiles fs.FS
		want      string
	}{
		{"embedded wins over dataDir", fstest.MapFS{"undionly.kpxe": {Data: []byte("EMBEDDED")}}, "EMBEDDED"},
		{"nil falls back to dataDir", nil, "DISK"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "undionly.kpxe"), "DISK")
			_, client := startTestServer(t, dir, c.bootFiles)
			wt, err := client.Receive("undionly.kpxe", "octet")
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			var buf bytes.Buffer
			if _, copyErr := wt.WriteTo(&buf); copyErr != nil {
				t.Fatalf("WriteTo: %v", copyErr)
			}
			if buf.String() != c.want {
				t.Fatalf("got %q, want %q", buf.String(), c.want)
			}
		})
	}
}

func TestTFTPStubDoesNotChangeDoInstall(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:d0"
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ServerIP, "127.0.0.1")
	viper.Set(config.ServerHttpPort, 8080)
	store, err := db.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	hardware.SetStore(store)
	t.Cleanup(func() { hardware.SetStore(nil); store.Close() })
	if err = hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, DoInstall: true}); err != nil {
		t.Fatal(err)
	}

	srv, err := Start(Config{
		Port:    0,
		DataDir: t.TempDir(),
		ARP:     func(net.IP) (net.HardwareAddr, error) { return net.ParseMAC(mac) },
	}, make(chan error, 1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	c, err := tftp.NewClient(net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Addr().(*net.UDPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	wt, err := c.Receive("booty.ipxe", "octet")
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var buf bytes.Buffer
	if _, err = wt.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != StubScript("127.0.0.1:8080") {
		t.Fatalf("got %q, want the stub", buf.String())
	}
	host, err := hardware.GetMacAddress(mac)
	if err != nil {
		t.Fatal(err)
	}
	if !host.DoInstall {
		t.Fatal("DoInstall was cleared by fetching the stub")
	}
}
