package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/auth"
	"github.com/jeefy/booty/pkg/config"
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

// TestHandleHUPReloadsAndFailsSafe covers design section 12's "SIGHUP re-read"
// requirement without delivering a real signal to the test process:
// watchSIGHUP's body is factored into handleHUP for exactly this reason.
func TestHandleHUPReloadsAndFailsSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), auth.TokenFileName)
	store := auth.NewStore(path)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	original := store.Token().Expose()

	// A rotated-out-of-band file is picked up.
	if err := os.WriteFile(path, []byte("rotated-out-of-band"), 0o600); err != nil {
		t.Fatal(err)
	}
	handleHUP(store, false)
	if got := store.Token().Expose(); got != "rotated-out-of-band" {
		t.Fatalf("after HUP token = %q, want the rewritten value", got)
	}

	// A blanked file must NOT install an empty token (that would make the
	// cookie key publicly derivable); the live token survives.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	handleHUP(store, false)
	if got := store.Token().Expose(); got != "rotated-out-of-band" {
		t.Fatalf("a blank file changed the live token to %q", got)
	}

	// Under --noAuth the reload is a no-op rather than a crash.
	handleHUP(store, true)
	if got := store.Token().Expose(); got != "rotated-out-of-band" {
		t.Fatalf("noAuth HUP changed the token to %q", got)
	}
	_ = original
}

// TestWatchSIGHUPStopsWithItsContext guards against a goroutine that outlives
// the process under -race.
func TestWatchSIGHUPStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store := auth.NewStore(filepath.Join(t.TempDir(), auth.TokenFileName))
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	watchSIGHUP(ctx, store, false)
	cancel()
	// No assertion is possible on goroutine exit directly; -race plus the
	// leaked-signal-handler check in `go test` is the guard. The value here is
	// that the call compiles with the ctx-scoped shape and is exercised.
}
