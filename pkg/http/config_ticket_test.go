package http

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/bootticket"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

// checkServed asserts a 200 that carries marker.
func checkServed(t *testing.T, rr *httptest.ResponseRecorder, marker string) {
	t.Helper()
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), marker) {
		t.Errorf("got %d %q, want 200 with %q", rr.Code, rr.Body.String(), marker)
	}
}

// configURLFrom expands the iPXE variables a real client would and returns the
// request target of the config URL the kernel sees: the value after key, up to
// the first newline or space (Linux ends a parameter at an unquoted space).
func configURLFrom(t *testing.T, script, key, mac string) string {
	t.Helper()
	expanded := strings.NewReplacer(
		"${mac}", mac,
		"${uuid}", "00000000-0000-0000-0000-000000000000",
		"${serial}", "Default string",
		"${hostname}", "",
	).Replace(script)
	_, after, ok := strings.Cut(expanded, key)
	if !ok {
		t.Fatalf("script has no %q:\n%s", key, script)
	}
	raw, _, _ := strings.Cut(after, "\n")
	raw, _, _ = strings.Cut(raw, " ")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("config URL %q: %v", raw, err)
	}
	return u.RequestURI()
}

// TestConfigServesWithValidTicketOffL2: the acceptance control for T1. A
// valid ticket is enough from a source ARP cannot resolve (a routed VLAN, NAT,
// the QEMU lab).
func TestConfigServesWithValidTicketOffL2(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:71"
	for _, rt := range ticketRoutes() {
		for _, bind := range []bool{false, true} {
			name := rt.path + "/server-default"
			if bind {
				name = rt.path + "/bound"
			}
			t.Run(name, func(t *testing.T) {
				s, marker := seedTicketHost(t, rt, mac, bind)
				target := withTicket(t, testTicketer(t), rt.path+"?mac="+mac, mac, rt.family)
				checkServed(t, fetchConfig(t, regressHandler(t, s, rt.path, failingARP), target), marker)
			})
		}
	}
}

func TestConfigRefusesTicketForAnotherMAC(t *testing.T) {
	const macA, macB = "aa:bb:cc:dd:ee:76", "aa:bb:cc:dd:ee:77"
	for _, rt := range ticketRoutes() {
		t.Run(rt.path, func(t *testing.T) {
			s, marker := seedTicketHost(t, rt, macA, false)
			if err := hardware.WriteMacAddress(macB, hardware.Host{MAC: macB, OS: rt.os}); err != nil {
				t.Fatal(err)
			}
			approveHost(t, macB)
			target := withTicket(t, testTicketer(t), rt.path+"?mac="+macB, macA, rt.family)
			checkRefused(t, rt.path, fetchConfig(t, regressHandler(t, s, rt.path, failingARP), target), marker)
		})
	}
}

// TestConfigRefusesTicketForAnotherFamily: #88's rung-4 hole. A Flatcar boot's
// ticket does not unlock the Talos or Debian route, and so on.
func TestConfigRefusesTicketForAnotherFamily(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:72"
	wrong := map[string]string{"talos": "ignition", "debian": "talos", "ignition": "talos"}
	for _, rt := range ticketRoutes() {
		t.Run(rt.path, func(t *testing.T) {
			s, marker := seedTicketHost(t, rt, mac, false)
			target := withTicket(t, testTicketer(t), rt.path+"?mac="+mac, mac, wrong[rt.family])
			checkRefused(t, rt.path, fetchConfig(t, regressHandler(t, s, rt.path, failingARP), target), marker)
		})
	}
}

// TestConfigTicketIgnoresMACSpelling: both sides sign the stored MAC, so the
// query's spelling of it does not matter.
func TestConfigTicketIgnoresMACSpelling(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:73"
	rt := routeFor(t, "/machineconfig")
	s, marker := seedTicketHost(t, rt, mac, false)
	target := withTicket(t, testTicketer(t), rt.path+"?mac=AA-BB-CC-DD-EE-73", mac, rt.family)
	checkServed(t, fetchConfig(t, regressHandler(t, s, rt.path, failingARP), target), marker)
}

// TestConfigTicketFirstValueWins: a second ticket= later in the URL (for
// example smuggled in through ${hostname}) does not override the first.
func TestConfigTicketFirstValueWins(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:74"
	rt := routeFor(t, "/machineconfig")
	s, marker := seedTicketHost(t, rt, mac, false)
	tk := testTicketer(t)
	good := withTicket(t, tk, rt.path+"?mac="+mac, mac, rt.family) + "&hostname=x&ticket=bogus"
	checkServed(t, fetchConfig(t, regressHandler(t, s, rt.path, failingARP), good), marker)
	bad := rt.path + "?mac=" + mac + "&ticket=bogus&ticket=" + tk.Issue(mac, rt.family)
	checkRefused(t, rt.path, fetchConfig(t, regressHandler(t, s, rt.path, failingARP), bad), marker)
}

// TestBootScriptCarriesVerifiableTicket: what /booty.ipxe issues, each config
// route accepts, after iPXE expansion and the kernel's split at the first space.
// ${serial} expands to "Default string", so a ticket placed after it would be
// cut off (Gate 1 B1).
func TestBootScriptCarriesVerifiableTicket(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:75"
	for _, c := range []struct{ os, key, path string }{
		{"talos", "talos.config=", "/machineconfig"},
		{"flatcar", "ignition.config.url=", "/ignition.json"},
		{"coreos", "set CONFIGURL ", "/ignition.json"},
		{"debian", "preseed/url=", "/preseed"},
	} {
		t.Run(c.os, func(t *testing.T) {
			rt := routeFor(t, c.path)
			rt.os = c.os
			s, marker := seedTicketHost(t, rt, mac, false)
			if err := hardware.SetAssignment(mac, c.os, "", ""); err != nil {
				t.Fatal(err)
			}
			script := fetchConfig(t, bootScriptHandler(s, failingARP, testTicketer(t)), "/booty.ipxe?mac="+mac).Body.String()
			if strings.Contains(script, "[[") || !strings.Contains(script, "ticket=v1.") {
				t.Fatalf("script must carry a ticket and no tokens:\n%s", script)
			}
			target := configURLFrom(t, script, c.key, mac)
			checkServed(t, fetchConfig(t, regressHandler(t, s, c.path, failingARP), target), marker)
		})
	}
}

// TestMenuSelectionCarriesTicket: a Flatcar host in menu mode that picks Talos
// gets a ticket for the Talos route. The host's own OS must not decide it.
func TestMenuSelectionCarriesTicket(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:7a"
	rt := routeFor(t, "/machineconfig")
	rt.os = "flatcar" // approveHost leaves boot_mode at the 'menu' default
	s, marker := seedTicketHost(t, rt, mac, false)
	if err := os.MkdirAll(filepath.Join(viper.GetString(config.DataDir), "cache", "talos", "schemA", "amd64", "v1.10.5"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := fetchConfig(t, menuSelectionHandler(failingARP, testTicketer(t)), "/menu/talos/schemA/amd64/v1.10.5/boot.ipxe?mac="+mac).Body.String()
	target := configURLFrom(t, script, "talos.config=", mac)
	checkServed(t, fetchConfig(t, regressHandler(t, s, "/machineconfig", failingARP), target), marker)
}

// muxGet runs target against mux.
func muxGet(t *testing.T, mux http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody))
	return rr
}

// TestMuxWiresTicketsEndToEnd: through the real baseMux, each OS's boot script,
// and a menu selection, lead to a served config. A wiring slip in baseMux (a nil
// issuer or checker on one route) would refuse every boot of that OS (Gate 2 S2).
func TestMuxWiresTicketsEndToEnd(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:7d"
	for _, c := range []struct {
		name, os, key, path string
		menu                bool
	}{
		{"talos", "talos", "talos.config=", "/machineconfig", false},
		{"flatcar", "flatcar", "ignition.config.url=", "/ignition.json", false},
		{"debian", "debian", "preseed/url=", "/preseed", false},
		{"menu selection", "flatcar", "talos.config=", "/machineconfig", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := routeFor(t, c.path)
			rt.os = c.os
			s, marker := seedTicketHost(t, rt, mac, false)
			mux := mustBaseMux(t, APIDeps{Store: s, Tickets: testTicketer(t)}, testDataRoot(t, t.TempDir()))
			scriptPath := "/booty.ipxe?mac=" + mac
			if c.menu {
				// approveHost left boot_mode at the 'menu' default.
				if err := os.MkdirAll(filepath.Join(viper.GetString(config.DataDir), "cache", "talos", "schemA", "amd64", "v1.10.5"), 0o755); err != nil {
					t.Fatal(err)
				}
				scriptPath = "/menu/talos/schemA/amd64/v1.10.5/boot.ipxe?mac=" + mac
			} else if err := hardware.SetAssignment(mac, c.os, "", ""); err != nil {
				t.Fatal(err)
			}
			script := muxGet(t, mux, scriptPath).Body.String()
			checkServed(t, muxGet(t, mux, configURLFrom(t, script, c.key, mac)), marker)
		})
	}
}

// TestMemberNeedsTicketAndARP: a member with a ticket still needs the ARP match.
func TestMemberNeedsTicketAndARP(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:7b"
	for _, c := range []struct {
		name string
		arp  arpFunc
		want int
	}{
		{"ticket and ARP match", fixedARP(mac), http.StatusOK},
		{"ticket, ARP names another host", fixedARP("aa:bb:cc:dd:ee:99"), http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := servingStore(t)
			testSecretsKey(t)
			frozen := seedMember(t, s, mac)
			target := withTicket(t, testTicketer(t), "/machineconfig?mac="+mac, mac, "talos")
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
			req.RemoteAddr = "10.0.0.50:1234"
			rr := httptest.NewRecorder()
			regressHandler(t, s, "/machineconfig", c.arp)(rr, req)
			if rr.Code != c.want {
				t.Fatalf("code = %d, want %d; body %q", rr.Code, c.want, rr.Body.String())
			}
			if c.want == http.StatusOK && rr.Body.String() != string(frozen) {
				t.Fatal("body not the frozen config")
			}
		})
	}
}

// TestConfigRefusalIsLogged: every refusal leaves one Warn record with the
// reason, so an expired or missing ticket can be told apart from an unknown
// host. Expiry's reason text is covered in pkg/bootticket (T5).
func TestConfigRefusalIsLogged(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:7c"
	other, err := bootticket.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	foreign := other.Issue(mac, "talos")
	for _, c := range []struct{ name, ticket, wantErr string }{
		{"missing", "", "no ticket"},
		{"signed under another key", foreign, "invalid ticket"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := routeFor(t, "/machineconfig")
			s, marker := seedTicketHost(t, rt, mac, false)
			var logBuf bytes.Buffer
			orig := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
			t.Cleanup(func() { slog.SetDefault(orig) })
			target := rt.path + "?mac=" + mac
			if c.ticket != "" {
				target += "&ticket=" + c.ticket
			}
			checkRefused(t, rt.path, fetchConfig(t, regressHandler(t, s, rt.path, failingARP), target), marker)
			logs := logBuf.String()
			for _, want := range []string{"config refused: no valid boot ticket", "family=talos", "mac=" + mac, c.wantErr} {
				if !strings.Contains(logs, want) {
					t.Errorf("log lacks %q:\n%s", want, logs)
				}
			}
			if n := strings.Count(logs, "config refused: no valid boot ticket"); n != 1 {
				t.Errorf("refusal records = %d, want 1:\n%s", n, logs)
			}
			if c.ticket != "" && strings.Contains(logs, c.ticket) {
				t.Errorf("log carries the ticket:\n%s", logs)
			}
		})
	}
}
