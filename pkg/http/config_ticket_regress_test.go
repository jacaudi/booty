package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

// #88: a request that names an approved host's MAC but carries no valid boot
// ticket for it gets the unknown-host answer, from any source. These tests were
// shown failing before the ticket check existed.

const ticketSentinel = "SENTINEL-TICKET-JOIN"

// ticketButane is a valid butane template that renders the join string.
const ticketButane = "variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: /etc/x\n      contents:\n        source: http://{{ .JoinString }}/x\n"

// ticketRoute is one config route under the boot-ticket rule.
type ticketRoute struct {
	path        string
	os          string // the host's OS
	family      string // the OS family a valid ticket for this route names
	kind        string // the kind of a config bound to the host
	bound       string // that config's source
	boundMarker string // text only the bound config renders
	setup       func(t *testing.T)
}

func ticketRoutes() []ticketRoute {
	return []ticketRoute{
		{
			path: "/machineconfig", os: "talos", family: "talos", kind: "machineconfig",
			bound: "machine:\n  token: SENTINEL-TICKET-BOUND\n", boundMarker: "SENTINEL-TICKET-BOUND",
			setup: func(t *testing.T) {
				t.Helper()
				viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
				writeFile(t, "config/machineconfig.yaml", "cluster:\n  token: {{ .JoinString }}\n")
			},
		},
		{
			path: "/preseed", os: "debian", family: "debian", kind: "debianconfig",
			bound: "hostname: \"sentinel-ticket-bound\"\nlocale: en_US.UTF-8\n", boundMarker: "sentinel-ticket-bound",
			setup: func(t *testing.T) {
				t.Helper()
				viper.Set(config.PreseedFile, "config/preseed.cfg")
				writeFile(t, "config/preseed.cfg", "d-i booty/join string {{ .JoinString }}\n")
			},
		},
		{
			path: "/ignition.json", os: "flatcar", family: "ignition", kind: "butane",
			bound: ticketButane, boundMarker: ticketSentinel,
			setup: func(t *testing.T) {
				t.Helper()
				viper.Set(config.IgnitionFile, "config/ignition.yaml")
				writeFile(t, "config/ignition.yaml", ticketButane)
			},
		},
	}
}

// routeFor is the ticketRoute for path.
func routeFor(t *testing.T, path string) ticketRoute {
	t.Helper()
	for _, rt := range ticketRoutes() {
		if rt.path == path {
			return rt
		}
	}
	t.Fatalf("no ticket route %q", path)
	return ticketRoute{}
}

// regressHandler is the config handler for path, with arp as its ARP lookup and
// the test boot-ticket checker as its proof.
func regressHandler(t *testing.T, s *db.Store, path string, arp arpFunc) http.HandlerFunc {
	t.Helper()
	tk := testTicketer(t)
	switch path {
	case "/machineconfig":
		return machineConfigHandler(s, arp, tk)
	case "/preseed":
		return preseedHandler(s, nil, arp, tk)
	default:
		return ignitionHandler(s, nil, arp, tk)
	}
}

// seedTicketHost registers and approves mac for rt (binding rt's config when
// bind is set) and returns the store and the text only the host's real config
// renders.
func seedTicketHost(t *testing.T, rt ticketRoute, mac string, bind bool) (*db.Store, string) {
	t.Helper()
	s := servingStore(t)
	viper.Set(config.JoinString, ticketSentinel)
	rt.setup(t)
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: rt.os}); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	if !bind {
		return s, ticketSentinel
	}
	bindConfig(t, s, mac, "bound", rt.kind, rt.bound)
	return s, rt.boundMarker
}

// checkRefused asserts path's unknown-host answer, without marker.
func checkRefused(t *testing.T, path string, rr *httptest.ResponseRecorder, marker string) {
	t.Helper()
	body := rr.Body.String()
	if strings.Contains(body, marker) {
		t.Errorf("refusal served the host's config: %q", body)
	}
	if path == "/ignition.json" {
		if rr.Code != http.StatusOK || !strings.Contains(body, "booty-reboot.service") {
			t.Errorf("got %d %q, want the reboot config", rr.Code, body)
		}
		return
	}
	if rr.Code != http.StatusNotFound || !strings.Contains(body, "no approved host for this request") {
		t.Errorf("got %d %q, want 404 %q", rr.Code, body, "no approved host for this request")
	}
}

func TestConfigRefusesApprovedMACWithoutTicket(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:71"
	for _, rt := range ticketRoutes() {
		for _, bind := range []bool{false, true} {
			name := rt.path + "/server-default"
			if bind {
				name = rt.path + "/bound"
			}
			t.Run(name, func(t *testing.T) {
				s, marker := seedTicketHost(t, rt, mac, bind)
				rr := fetchConfig(t, regressHandler(t, s, rt.path, failingARP), rt.path+"?mac="+mac)
				checkRefused(t, rt.path, rr, marker)
			})
		}
	}
}

// TestIgnitionApprovedWithoutTicketRebootsWithoutTemplate: refusing an approved
// host is the reboot config even when the template file is missing (R4).
func TestIgnitionApprovedWithoutTicketRebootsWithoutTemplate(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:78"
	s := servingStore(t)
	viper.Set(config.IgnitionFile, "config/missing.yaml")
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar"}); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	rr := fetchConfig(t, regressHandler(t, s, "/ignition.json", failingARP), "/ignition.json?mac="+mac)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "booty-reboot.service") {
		t.Fatalf("got %d %q, want 200 with the reboot config", rr.Code, rr.Body.String())
	}
}

// TestMemberWithoutTicketIsRefused: a cluster member needs a ticket as well as
// the ARP match; the ARP match alone no longer serves its frozen config.
func TestMemberWithoutTicketIsRefused(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:79"
	s := servingStore(t)
	testSecretsKey(t)
	seedMember(t, s, mac)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/machineconfig?mac="+mac, http.NoBody)
	req.RemoteAddr = "10.0.0.50:1234"
	rr := httptest.NewRecorder()
	regressHandler(t, s, "/machineconfig", fixedARP(mac))(rr, req)
	if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "frozen bytes") {
		t.Fatalf("got %d %q, want 404 without the frozen config", rr.Code, rr.Body.String())
	}
}
