package main

import (
	"testing"

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
