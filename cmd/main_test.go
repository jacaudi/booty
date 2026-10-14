package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/auth"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// TestStartProxyDHCPDisabledReturnsNil proves the default-off invariant: with
// proxyDHCPEnabled at its default (false), startProxyDHCP opens no listener and
// returns nil. This is the guard that keeps an opt-in subsystem from changing
// any behavior when it is not requested.
func TestStartProxyDHCPDisabledReturnsNil(t *testing.T) {
	if got := startProxyDHCP(); got != nil {
		t.Fatalf("startProxyDHCP() with proxyDHCP disabled = %v, want nil", got)
	}
}

// TestStartProxyDHCPUnusableServerIPReturnsNil proves that even when enabled, a
// serverIP that cannot serve as a reachable next-server is rejected before any
// socket bind, so a misconfiguration cannot start a responder on the wrong
// address. Each case returns nil without ever calling Start(). These are the
// reject paths reachable in CI: a usable IP would attempt a real UDP/67 bind
// (CAP_NET_BIND_SERVICE), so only the pre-bind rejections are asserted here.
func TestStartProxyDHCPUnusableServerIPReturnsNil(t *testing.T) {
	cases := []struct {
		name     string
		serverIP string
	}{
		{name: "empty", serverIP: ""},
		{name: "unparseable", serverIP: "not-an-ip"},
		{name: "loopback", serverIP: "127.0.0.1"},
		{name: "unspecified", serverIP: "0.0.0.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreViper(t, config.ProxyDHCPEnabled)
			restoreViper(t, config.ServerIP)
			viper.Set(config.ProxyDHCPEnabled, true)
			viper.Set(config.ServerIP, tc.serverIP)

			if got := startProxyDHCP(); got != nil {
				t.Fatalf("startProxyDHCP() with serverIP %q = %v, want nil", tc.serverIP, got)
			}
		})
	}
}

// restoreViper snapshots a viper key and restores it after the test so that
// mutating process-global viper state does not leak into sibling tests.
func restoreViper(t *testing.T, key string) {
	t.Helper()
	prev := viper.Get(key)
	t.Cleanup(func() { viper.Set(key, prev) })
}

// TestResolveServerHTTPPort proves the fallback rule: an unset (zero)
// serverHTTPPort advertises the listen port (httpPort), while a non-zero
// explicit value always wins (proxy-fronted deploys set it explicitly).
func TestResolveServerHTTPPort(t *testing.T) {
	cases := []struct {
		name           string
		serverHTTPPort int
		httpPort       int
		want           int
	}{
		{name: "unset falls back to listen port", serverHTTPPort: 0, httpPort: 8080, want: 8080},
		{name: "explicit proxy port wins", serverHTTPPort: 80, httpPort: 8080, want: 80},
		{name: "explicit non-standard port wins", serverHTTPPort: 9090, httpPort: 8080, want: 9090},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveServerHTTPPort(tc.serverHTTPPort, tc.httpPort); got != tc.want {
				t.Errorf("resolveServerHTTPPort(%d, %d) = %d, want %d", tc.serverHTTPPort, tc.httpPort, got, tc.want)
			}
		})
	}
}

func TestResolveServerIP(t *testing.T) {
	ok := func(ip string) func() (string, error) { return func() (string, error) { return ip, nil } }
	fail := func() (string, error) { return "", errors.New("no route") }
	cases := []struct {
		name     string
		explicit string
		detect   func() (string, error)
		want     string
		wantErr  bool
	}{
		{"explicit wins over a failing detector", "10.0.0.5", fail, "10.0.0.5", false},
		{"explicit wins over a working detector", "10.0.0.5", ok("192.168.1.9"), "10.0.0.5", false},
		{"empty uses detector", "", ok("192.168.1.9"), "192.168.1.9", false},
		{"detector error", "", fail, "", true},
		{"detector returns loopback", "", ok("127.0.0.1"), "", true},
		{"detector returns unspecified", "", ok("0.0.0.0"), "", true},
		{"detector returns IPv6", "", ok("fd00::1"), "", true},
		{"detector returns garbage", "", ok("not-an-ip"), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveServerIP(c.explicit, c.detect)
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("resolveServerIP(%q) = %q, %v; want %q, err=%v", c.explicit, got, err, c.want, c.wantErr)
			}
			if c.wantErr && !strings.Contains(err.Error(), "set --serverIP") {
				t.Fatalf("error must tell the operator what to do: %v", err)
			}
		})
	}
}

// TestHandleHUPReloadsAndFailsSafe covers the SIGHUP re-read requirement --
// a rotated-on-disk token takes effect without a process restart, and a
// blank or unreadable file must fail safe rather than dropping booty into an
// unauthenticatable state -- without delivering a real signal to the test
// process: watchSIGHUP's body is factored into handleHUP for exactly this
// reason.
func TestHandleHUPReloadsAndFailsSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), auth.TokenFileName)
	store := auth.NewStore(path)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// A rotated-out-of-band file is picked up.
	if err := os.WriteFile(path, []byte("rotated-out-of-band"), 0o600); err != nil {
		t.Fatal(err)
	}
	handleHUP(store, false, false)
	if got := store.Token().Expose(); got != "rotated-out-of-band" {
		t.Fatalf("after HUP token = %q, want the rewritten value", got)
	}

	// A blanked file must NOT install an empty token (that would make the
	// cookie key publicly derivable); the live token survives.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	handleHUP(store, false, false)
	if got := store.Token().Expose(); got != "rotated-out-of-band" {
		t.Fatalf("a blank file changed the live token to %q", got)
	}

	// Under --noAuth the reload is a no-op rather than a crash.
	handleHUP(store, true, false)
	if got := store.Token().Expose(); got != "rotated-out-of-band" {
		t.Fatalf("noAuth HUP changed the token to %q", got)
	}
}

// TestHandleHUPPreservesAnExplicitToken closes a hole where SIGHUP would
// silently replace a --apiToken/BOOTY_API_TOKEN credential with whatever is
// sitting in <dataDir>/api-token -- reachable whenever an operator switches
// from file-generated auth to an explicit token without ever deleting the
// stale file (SetExplicit deliberately never touches disk, so the old file
// just sits there). A HUP under that config must be a no-op, not a silent
// reload from the stale file.
func TestHandleHUPPreservesAnExplicitToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), auth.TokenFileName)
	// A different, populated token file exists on disk -- e.g. left over from
	// an earlier run before --apiToken/BOOTY_API_TOKEN was set.
	if err := os.WriteFile(path, []byte("stale-file-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(path)
	if err := store.SetExplicit("explicit-token"); err != nil {
		t.Fatal(err)
	}

	handleHUP(store, false, true)

	if got := store.Token().Expose(); got != "explicit-token" {
		t.Fatalf("HUP against an explicitly-set token installed %q, want the explicit token preserved", got)
	}
}

// TestWatchSIGHUPStopsWithItsContext guards against a goroutine that outlives
// the process under -race. watchSIGHUP's returned channel is closed by the
// goroutine's own defer, so this is a direct assertion of exit rather than
// the absence-of-a-crash the earlier version of this test settled for.
func TestWatchSIGHUPStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store := auth.NewStore(filepath.Join(t.TempDir(), auth.TokenFileName))
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	done := watchSIGHUP(ctx, store, false, false)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchSIGHUP's goroutine did not exit within 2s of ctx being cancelled")
	}
}

// TestNodeReportsFlagRegistered pins the flag name, default and help text.
func TestNodeReportsFlagRegistered(t *testing.T) {
	f := Cmd.Flags().Lookup(config.NodeReports)
	if f == nil {
		t.Fatal("--nodeReports is not registered")
	}
	if f.DefValue != "false" {
		t.Errorf("--nodeReports default = %q, want false", f.DefValue)
	}
	const wantUsage = "Inject a boot-time health/running-version report into Flatcar, CoreOS and debianconfig Debian hosts and accept reports at POST /node/report"
	if f.Usage != wantUsage {
		t.Errorf("--nodeReports usage = %q", f.Usage)
	}
}

// TestRunTFTPListenFailureIsQuietAndClean proves a runtime startup error
// (here: the TFTP port is taken) is returned without cobra's usage text.
func TestRunTFTPListenFailureIsQuietAndClean(t *testing.T) {
	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", ":0")
	if err != nil {
		t.Fatalf("occupy UDP port: %v", err)
	}
	defer pc.Close()
	port := udpAddr(t, pc.LocalAddr()).Port

	resetCmd(t)

	var out bytes.Buffer
	Cmd.SetOut(&out)
	Cmd.SetErr(&out)
	Cmd.SetArgs([]string{
		"--dataDir", t.TempDir(),
		"--serverIP", "127.0.0.1",
		"--tftpPort", strconv.Itoa(port),
		"--httpPort", "0",
	})
	err = Cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "tftp: listen") {
		t.Fatalf("Execute() error = %v, want one containing %q", err, "tftp: listen")
	}
	if strings.Contains(out.String(), "Usage:") {
		t.Errorf("runtime error printed usage:\n%s", out.String())
	}
}

// TestFlagParseErrorStillPrintsUsage keeps usage for a bad flag (cobra prints
// usage for every error unless SilenceUsage is set, so booty re-adds it here).
func TestFlagParseErrorStillPrintsUsage(t *testing.T) {
	t.Cleanup(func() { Cmd.SetArgs(nil); Cmd.SetOut(nil); Cmd.SetErr(nil) })
	var out bytes.Buffer
	Cmd.SetOut(&out)
	Cmd.SetErr(&out)
	Cmd.SetArgs([]string{"--no-such-flag"})
	if err := Cmd.Execute(); err == nil {
		t.Fatal("Execute() succeeded with an unknown flag")
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("flag-parse error did not print usage:\n%s", out.String())
	}
}

// TestRunBadNodeReportKeyFailsBeforeServicesStart proves the key is loaded
// before the TFTP server: with both a taken TFTP port and an unreadable key,
// the key error wins.
func TestRunBadNodeReportKeyFailsBeforeServicesStart(t *testing.T) {
	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", ":0")
	if err != nil {
		t.Fatalf("occupy UDP port: %v", err)
	}
	defer pc.Close()
	port := udpAddr(t, pc.LocalAddr()).Port

	dataDir := t.TempDir()
	// A directory where the key file belongs makes LoadOrCreateKey fail.
	if mkErr := os.Mkdir(filepath.Join(dataDir, "node-report-key"), 0o700); mkErr != nil {
		t.Fatal(mkErr)
	}
	resetCmd(t)
	var out bytes.Buffer
	Cmd.SetOut(&out)
	Cmd.SetErr(&out)
	Cmd.SetArgs([]string{
		"--dataDir", dataDir,
		"--serverIP", "127.0.0.1",
		"--tftpPort", strconv.Itoa(port),
		"--httpPort", "0",
		"--nodeReports",
	})
	err = Cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "node reports") {
		t.Fatalf("Execute() error = %v, want one containing %q", err, "node reports")
	}
}

// resetCmd restores the process-global Cmd (flags, args, writers) and the
// viper keys run() overrides, after a test that executes it. It must not call
// viper.Reset: that would drop the flag bindings made in init().
func resetCmd(t *testing.T) {
	t.Helper()
	restoreViper(t, config.ServerIP)
	restoreViper(t, config.ServerHttpPort)
	t.Cleanup(func() {
		Cmd.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
		Cmd.SetArgs(nil)
		Cmd.SetOut(nil)
		Cmd.SetErr(nil)
	})
}

func TestWarnShadowedBootFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ipxe.efi"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory of the same name is not a regular file and is not served.
	if err := os.Mkdir(filepath.Join(dir, "snponly.efi"), 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	warnShadowedBootFiles(dir, []string{"undionly.kpxe", "ipxe.efi", "snponly.efi"}, log)

	got := buf.String()
	if !strings.Contains(got, "embedded iPXE file shadows a file in dataDir; the dataDir copy is not served") ||
		!strings.Contains(got, "file=ipxe.efi") {
		t.Errorf("missing warning for ipxe.efi:\n%s", got)
	}
	if strings.Contains(got, "undionly.kpxe") || strings.Contains(got, "snponly.efi") {
		t.Errorf("warned about a file that is absent or not a regular file:\n%s", got)
	}
}

// udpAddr is a's *net.UDPAddr; a test that listens on UDP never sees another type.
func udpAddr(t *testing.T, a net.Addr) *net.UDPAddr {
	t.Helper()
	u, ok := a.(*net.UDPAddr)
	if !ok {
		t.Fatalf("address %v is %T, want *net.UDPAddr", a, a)
	}
	return u
}
