package http

import (
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

// #162: /machineconfig and /preseed render the server-default template only for
// an approved host. A caller with no approved host (unknown MAC, no MAC at all,
// a registered host that was never approved, or one that was revoked) gets a 404
// and the template is not rendered, so a join string in it cannot leak.

const noApprovedHostMsg = "no approved host for this request"

// defaultRefusalCase is one caller of a config route.
type defaultRefusalCase struct {
	name     string
	query    string // appended to the route path; %s is the host's MAC
	register bool   // register the host first
	approve  bool   // then approve it
	revoke   bool   // then revoke it
	want     int
}

var defaultRefusalCases = []defaultRefusalCase{
	{name: "unknown MAC", query: "?mac=aa:bb:cc:dd:ee:f1", want: http.StatusNotFound},
	{name: "no MAC and ARP failure", query: "", want: http.StatusNotFound},
	{name: "unapproved host", query: "?mac=%s", register: true, want: http.StatusNotFound},
	{name: "revoked host", query: "?mac=%s", register: true, approve: true, revoke: true, want: http.StatusNotFound},
	{name: "approved host without a bound config", query: "?mac=%s", register: true, approve: true, want: http.StatusOK},
}

// prepareDefaultRefusalHost applies c's registration, approval and revocation to
// mac, then returns the request target for path.
func prepareDefaultRefusalHost(t *testing.T, c defaultRefusalCase, path, mac, osName string) string {
	t.Helper()
	if c.register {
		if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: osName}); err != nil {
			t.Fatal(err)
		}
	}
	if c.approve {
		approveHost(t, mac)
	}
	if c.revoke {
		if err := hardware.Revoke(mac); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(c.query, "%s") {
		return path + strings.Replace(c.query, "%s", mac, 1)
	}
	return path + c.query
}

// checkDefaultRefusal asserts the status, and that a refusal carries the message
// and no part of the template, while an answer carries the sentinel.
func checkDefaultRefusal(t *testing.T, got, want int, body, sentinel string) {
	t.Helper()
	if got != want {
		t.Fatalf("code = %d, want %d; body %q", got, want, body)
	}
	if want == http.StatusOK {
		if !strings.Contains(body, sentinel) {
			t.Errorf("approved host did not get the rendered server default: %q", body)
		}
		return
	}
	if strings.Contains(body, sentinel) {
		t.Errorf("refusal leaks the join string: %q", body)
	}
	if !strings.Contains(body, noApprovedHostMsg) {
		t.Errorf("refusal body = %q, want it to contain %q", body, noApprovedHostMsg)
	}
}

func TestMachineConfigDefaultRefusedWithoutApprovedHost(t *testing.T) {
	const sentinel = "SENTINEL-JOIN"
	for _, c := range defaultRefusalCases {
		t.Run(c.name, func(t *testing.T) {
			s := servingStore(t)
			viper.Set(config.JoinString, sentinel)
			viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
			writeFile(t, "config/machineconfig.yaml", "cluster:\n  token: {{ .JoinString }}\n")
			target := prepareDefaultRefusalHost(t, c, "/machineconfig", "aa:bb:cc:dd:ee:b1", "talos")

			rr := fetchConfig(t, machineConfigHandler(s, failingARP, allowProof{}), target)

			checkDefaultRefusal(t, rr.Code, c.want, rr.Body.String(), sentinel)
		})
	}
}

func TestPreseedDefaultRefusedWithoutApprovedHost(t *testing.T) {
	const sentinel = "SENTINEL-JOIN"
	for _, c := range defaultRefusalCases {
		t.Run(c.name, func(t *testing.T) {
			s := servingStore(t)
			viper.Set(config.JoinString, sentinel)
			viper.Set(config.PreseedFile, "config/preseed.cfg")
			writeFile(t, "config/preseed.cfg", "d-i booty/join string {{ .JoinString }}\n")
			target := prepareDefaultRefusalHost(t, c, "/preseed", "aa:bb:cc:dd:ee:b2", "debian")

			rr := fetchConfig(t, preseedHandler(s, nil, arpLookup, allowProof{}), target)

			checkDefaultRefusal(t, rr.Code, c.want, rr.Body.String(), sentinel)
		})
	}
}

// TestConfigDefaultByARPFallback: with no ?mac=, the host the source IP
// ARP-resolves to decides the answer. An approved host gets the rendered server
// default; an unapproved or unregistered one gets the 404 (the boot-ticket
// proof is stubbed by allowProof; the ticket rule is pinned in
// config_ticket_test.go).
func TestConfigDefaultByARPFallback(t *testing.T) {
	const sentinel = "SENTINEL-JOIN"
	const mac = "aa:bb:cc:dd:ee:b3"
	cases := []struct {
		name     string
		register bool
		approve  bool
		want     int
	}{
		{"approved host", true, true, http.StatusOK},
		{"unapproved host", true, false, http.StatusNotFound},
		{"unregistered host", false, false, http.StatusNotFound},
	}
	routes := []struct {
		name string
		path string
		os   string
		cfg  func(*testing.T)
		h    func(*db.Store, arpFunc) http.HandlerFunc
	}{
		{
			"machineconfig", "/machineconfig", "talos",
			func(t *testing.T) {
				t.Helper()
				viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
				writeFile(t, "config/machineconfig.yaml", "cluster:\n  token: {{ .JoinString }}\n")
			},
			func(s *db.Store, arp arpFunc) http.HandlerFunc { return machineConfigHandler(s, arp, allowProof{}) },
		},
		{
			"preseed", "/preseed", "debian",
			func(t *testing.T) {
				t.Helper()
				viper.Set(config.PreseedFile, "config/preseed.cfg")
				writeFile(t, "config/preseed.cfg", "d-i booty/join string {{ .JoinString }}\n")
			},
			func(s *db.Store, arp arpFunc) http.HandlerFunc { return preseedHandler(s, nil, arp, allowProof{}) },
		},
	}
	for _, rt := range routes {
		for _, c := range cases {
			t.Run(rt.name+"/"+c.name, func(t *testing.T) {
				s := servingStore(t)
				viper.Set(config.JoinString, sentinel)
				rt.cfg(t)
				if c.register {
					if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: rt.os}); err != nil {
						t.Fatal(err)
					}
				}
				if c.approve {
					approveHost(t, mac)
				}

				rr := fetchConfig(t, rt.h(s, fixedARP(mac)), rt.path)

				checkDefaultRefusal(t, rr.Code, c.want, rr.Body.String(), sentinel)
			})
		}
	}
}
