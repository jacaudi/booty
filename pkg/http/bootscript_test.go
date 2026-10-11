package http

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
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
	servingStore(t)
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
		{"unknown MAC", bootScriptHandler(noARP), "/booty.ipxe?mac=aa:bb:cc:dd:ee:a0", "", "not yet approved"},
		{"malformed MAC", bootScriptHandler(noARP), "/booty.ipxe?mac=zz", "", "not yet approved"},
		{"menu host", bootScriptHandler(noARP), "/booty.ipxe?mac=" + menuMAC, "", menuMarker},
		{"ARP fallback", bootScriptHandler(menuARP), "/booty.ipxe", "10.0.0.9:1", menuMarker},
		{
			"menu selection, uncached tuple", menuSelectionHandler(noARP),
			"/menu/talos/x/amd64/v9.9.9/boot.ipxe?mac=" + menuMAC, "", "not yet approved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
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
		if !slices.Contains(hardware.ListUnknownHosts(), "aa:bb:cc:dd:ee:a0") {
			t.Errorf("unknown hosts = %v", hardware.ListUnknownHosts())
		}
	})
}

func TestBootScriptRejectsPost(t *testing.T) {
	noARP := func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no arp") }
	mux := http.NewServeMux()
	mux.Handle("GET /booty.ipxe", bootScriptHandler(noARP))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/booty.ipxe", nil))
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
	bootScriptHandler(noARP)(rr, httptest.NewRequest(http.MethodGet, "/booty.ipxe?mac="+mac, nil))
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
