package http

import (
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
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

			rr := fetchConfig(t, machineConfigHandler(s, failingARP), target)

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

			rr := fetchConfig(t, handlePreseedRequest(s), target)

			checkDefaultRefusal(t, rr.Code, c.want, rr.Body.String(), sentinel)
		})
	}
}
