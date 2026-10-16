package http

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/hardware"
)

const menuMarker = "menu Booty - select an image to boot"

func seedMenuHost(t *testing.T, mac string) {
	t.Helper()
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac}); err != nil {
		t.Fatal(err)
	}
	if err := hardware.Approve(mac); err != nil {
		t.Fatal(err)
	}
}

func TestBootScriptHandlers(t *testing.T) {
	s := servingStore(t)
	const menuMAC = "aa:bb:cc:dd:ee:a1"
	seedMenuHost(t, menuMAC)
	noARP := func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no arp") }
	menuARP := func(net.IP) (net.HardwareAddr, error) {
		return net.ParseMAC(menuMAC)
	}

	cases := []struct {
		name    string
		handler http.HandlerFunc
		target  string
		remote  string
		want    string
	}{
		{"unknown MAC", bootScriptHandler(s, noARP), "/booty.ipxe?mac=aa:bb:cc:dd:ee:a0", "", "not yet approved"},
		{
			"malformed MAC never falls back to ARP", bootScriptHandler(nil, menuARP),
			"/booty.ipxe?mac=zz", "10.0.0.9:1", "not yet approved",
		},
		{"menu host", bootScriptHandler(nil, noARP), "/booty.ipxe?mac=" + menuMAC, "", menuMarker},
		{"ARP fallback", bootScriptHandler(nil, menuARP), "/booty.ipxe", "10.0.0.9:1", menuMarker},
		{
			"menu selection, uncached tuple", menuSelectionHandler(noARP),
			"/menu/talos/x/amd64/v9.9.9/boot.ipxe?mac=" + menuMAC, "", "not yet approved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.target, http.NoBody)
			if tc.remote != "" {
				req.RemoteAddr = tc.remote
			}
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}
			if !strings.Contains(rr.Body.String(), tc.want) {
				t.Errorf("body missing %q:\n%s", tc.want, rr.Body.String())
			}
		})
	}

	t.Run("unknown MAC is recorded", func(t *testing.T) {
		rows, err := s.ListDiscoveredHosts(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].MAC != "aa:bb:cc:dd:ee:a0" {
			t.Errorf("discovered hosts = %+v, want exactly aa:bb:cc:dd:ee:a0", rows)
		}
	})
}

func TestBootScriptRejectsPost(t *testing.T) {
	noARP := func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no arp") }
	mux := http.NewServeMux()
	mux.Handle("GET /booty.ipxe", bootScriptHandler(nil, noARP))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/booty.ipxe", http.NoBody))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestBootScriptDoesNotChangeDoInstall(t *testing.T) {
	servingStore(t)
	const mac = "aa:bb:cc:dd:ee:d1"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, DoInstall: true}); err != nil {
		t.Fatal(err)
	}
	noARP := func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no arp") }
	rr := httptest.NewRecorder()
	bootScriptHandler(nil, noARP)(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/booty.ipxe?mac="+mac, http.NoBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	host, err := hardware.GetMacAddress(mac)
	if err != nil {
		t.Fatal(err)
	}
	if !host.DoInstall {
		t.Fatal("DoInstall was cleared by fetching /booty.ipxe")
	}
}

// TestBootScriptLogsNormalisedMAC: the "boot script" log line carries the
// normalised MAC, or "" when ?mac= does not parse, never the raw query value.
func TestBootScriptLogsNormalisedMAC(t *testing.T) {
	servingStore(t)
	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })
	noARP := func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no arp") }

	for _, tc := range []struct{ query, want, absent string }{
		{"AA-BB-CC-DD-EE-B0", "mac=aa:bb:cc:dd:ee:b0", "AA-BB"},
		{"evil%0Afake=1", `mac=""`, "evil"},
	} {
		logBuf.Reset()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/booty.ipxe?mac="+tc.query, http.NoBody)
		bootScriptHandler(nil, noARP)(httptest.NewRecorder(), req)
		got := logBuf.String()
		if !strings.Contains(got, `msg="boot script"`) || !strings.Contains(got, "kind=") ||
			!strings.Contains(got, tc.want) || strings.Contains(got, tc.absent) {
			t.Errorf("?mac=%s: log %q, want %q and no %q", tc.query, got, tc.want, tc.absent)
		}
	}
}
