package http

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

// #158: the config routes (/ignition.json, /machineconfig, /preseed) serve a
// host only once it is approved. A registered but unapproved host (never
// approved, or approved then revoked) gets the same answer as an unknown MAC.

// fetchConfig runs h against GET path and returns the recorded response.
func fetchConfig(t *testing.T, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

// bindConfig creates a config of kind with src as its active revision and binds
// it to mac.
func bindConfig(t *testing.T, s *db.Store, mac, name, kind, src string) {
	t.Helper()
	cid, err := s.CreateConfig(name, kind)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	rid, _, err := s.AddConfigRevision(cid, base64.StdEncoding.EncodeToString([]byte(src)), "sha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetActiveRevision(cid, rid); err != nil {
		t.Fatal(err)
	}
	if err = hardware.SetHostConfig(mac, &cid); err != nil {
		t.Fatal(err)
	}
}

func TestIgnitionUnapprovedHostGetsRebootConfig(t *testing.T) {
	const sentinel = "SENTINEL-JOIN"
	const butane = "variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: /etc/x\n      contents:\n        source: http://{{ .JoinString }}/x\n"
	cases := []struct {
		name string
		bind bool // true: butane bound to the host (rungs 1-2); false: file template (rung 3)
	}{
		{"file template", false},
		{"bound config", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := servingStore(t)
			viper.Set(config.JoinString, sentinel)
			viper.Set(config.IgnitionFile, "config/ignition.yaml")
			writeFile(t, "config/ignition.yaml", butane)
			const mac = "aa:bb:cc:dd:ee:a1"
			if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar"}); err != nil {
				t.Fatal(err)
			}
			if c.bind {
				bindConfig(t, s, mac, "bound", "butane", butane)
			}
			h := handleIgnitionRequest(s)

			rr := fetchConfig(t, h, "/ignition.json?mac="+mac)
			if rr.Code != http.StatusOK {
				t.Fatalf("unapproved: code = %d, want 200; body %q", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "booty-reboot.service") {
				t.Errorf("unapproved host did not get the reboot config: %s", rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), sentinel) {
				t.Errorf("unapproved host was served its real config: %s", rr.Body.String())
			}

			// Approving the same host flips it to the real config.
			approveHost(t, mac)
			rr = fetchConfig(t, h, "/ignition.json?mac="+mac)
			if !strings.Contains(rr.Body.String(), sentinel) {
				t.Errorf("approved host did not get its real config: %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestIgnitionRevokedHostGetsRebootConfig(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.JoinString, "SENTINEL-JOIN")
	viper.Set(config.IgnitionFile, "config/ignition.yaml")
	writeFile(t, "config/ignition.yaml",
		"variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: /etc/x\n      contents:\n        source: http://{{ .JoinString }}/x\n")
	const mac = "aa:bb:cc:dd:ee:a2"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar"}); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	h := handleIgnitionRequest(s)
	if rr := fetchConfig(t, h, "/ignition.json?mac="+mac); !strings.Contains(rr.Body.String(), "SENTINEL-JOIN") {
		t.Fatalf("approved host did not get its real config: %d %s", rr.Code, rr.Body.String())
	}

	if err := hardware.Revoke(mac); err != nil {
		t.Fatal(err)
	}
	rr := fetchConfig(t, h, "/ignition.json?mac="+mac)
	if !strings.Contains(rr.Body.String(), "booty-reboot.service") || strings.Contains(rr.Body.String(), "SENTINEL-JOIN") {
		t.Errorf("revoked host did not get the reboot config: %d %s", rr.Code, rr.Body.String())
	}
}

func TestMachineConfigUnapprovedHostGetsUnknownAnswer(t *testing.T) {
	const tmpl = "machine:\n  hostname: {{ .Hostname }}\n  mac: {{ .MAC }}\n  ip: {{ .IP }}\n  schematic: {{ .Schematic }}\n"
	cases := []struct {
		name string
		bind bool
	}{
		{"file template", false},
		{"bound config", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := servingStore(t)
			viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
			writeFile(t, "config/machineconfig.yaml", tmpl)
			const mac = "aa:bb:cc:dd:ee:a3"
			if err := hardware.WriteMacAddress(mac, hardware.Host{
				MAC: mac, OS: "talos", Hostname: "sentinel-host", IP: "10.9.9.9", Schematic: "sentinel-schematic",
			}); err != nil {
				t.Fatal(err)
			}
			if c.bind {
				bindConfig(t, s, mac, "bound", "machineconfig", "machine:\n  token: SENTINEL-BOUND\n")
			}
			h := machineConfigHandler(s, failingARP)

			want := fetchConfig(t, h, "/machineconfig?mac=aa:bb:cc:dd:ee:f1") // unregistered MAC
			got := fetchConfig(t, h, "/machineconfig?mac="+mac)
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Errorf("unapproved host answer differs from unknown host:\ngot  %d %q\nwant %d %q",
					got.Code, got.Body.String(), want.Code, want.Body.String())
			}
			for _, leak := range []string{"SENTINEL-BOUND", "sentinel-host", "10.9.9.9", "sentinel-schematic", mac} {
				if strings.Contains(got.Body.String(), leak) {
					t.Errorf("unapproved host response leaks %q: %s", leak, got.Body.String())
				}
			}

			// Approving the same host flips it to its own config.
			approveHost(t, mac)
			approved := fetchConfig(t, h, "/machineconfig?mac="+mac)
			if approved.Body.String() == want.Body.String() {
				t.Errorf("approved host still gets the unknown-host answer: %s", approved.Body.String())
			}
		})
	}
}

func TestMachineConfigRevokedHostGetsUnknownAnswer(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
	writeFile(t, "config/machineconfig.yaml", "machine:\n  hostname: {{ .Hostname }}\n")
	const mac = "aa:bb:cc:dd:ee:a4"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos", Hostname: "sentinel-host"}); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	h := machineConfigHandler(s, failingARP)
	if rr := fetchConfig(t, h, "/machineconfig?mac="+mac); !strings.Contains(rr.Body.String(), "sentinel-host") {
		t.Fatalf("approved host did not get its own config: %d %s", rr.Code, rr.Body.String())
	}

	if err := hardware.Revoke(mac); err != nil {
		t.Fatal(err)
	}
	want := fetchConfig(t, h, "/machineconfig?mac=aa:bb:cc:dd:ee:f1")
	got := fetchConfig(t, h, "/machineconfig?mac="+mac)
	if got.Code != want.Code || got.Body.String() != want.Body.String() {
		t.Errorf("revoked host answer differs from unknown host:\ngot  %d %q\nwant %d %q",
			got.Code, got.Body.String(), want.Code, want.Body.String())
	}
}

func TestMachineConfigUnapprovedMemberIsForbidden(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:a5"
	s := servingStore(t)
	viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
	writeFile(t, "config/machineconfig.yaml", "machine: {}\n")
	testSecretsKey(t)
	seedMember(t, s, mac)
	if err := hardware.Revoke(mac); err != nil {
		t.Fatal(err)
	}

	// The ARP matches, so the 403 is the approval gate and not the ARP check.
	rr := fetchConfig(t, machineConfigHandler(s, fixedARP(mac)), "/machineconfig?mac="+mac)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403; body %q", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); strings.Contains(body, "frozen bytes") || strings.Contains(body, "machine: {}") {
		t.Errorf("403 body leaked a config: %q", body)
	}
}

func TestPreseedUnapprovedHostGetsDefault(t *testing.T) {
	const tmpl = "d-i mirror/http/hostname string {{ .ServerIP }}\nd-i netcfg/get_hostname string {{ .Hostname }}\n"
	s := servingStore(t)
	viper.Set(config.PreseedFile, "config/preseed.cfg")
	writeFile(t, "config/preseed.cfg", tmpl)
	const mac = "aa:bb:cc:dd:ee:a6"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "debian", Hostname: "sentinel-host"}); err != nil {
		t.Fatal(err)
	}
	bindConfig(t, s, mac, "bound", "debianconfig", "hostname: \"sentinel-bound\"\nlocale: en_US.UTF-8\n")
	h := handlePreseedRequest(s)

	want := fetchConfig(t, h, "/preseed?mac=aa:bb:cc:dd:ee:f1") // unregistered MAC
	got := fetchConfig(t, h, "/preseed?mac="+mac)
	if got.Code != want.Code || got.Body.String() != want.Body.String() {
		t.Errorf("unapproved host answer differs from unknown host:\ngot  %d %q\nwant %d %q",
			got.Code, got.Body.String(), want.Code, want.Body.String())
	}
	for _, leak := range []string{"sentinel-bound", "sentinel-host", "en_US.UTF-8"} {
		if strings.Contains(got.Body.String(), leak) {
			t.Errorf("unapproved host response leaks %q: %s", leak, got.Body.String())
		}
	}

	// Approving the same host flips it to its bound config; revoking flips it back.
	approveHost(t, mac)
	if rr := fetchConfig(t, h, "/preseed?mac="+mac); !strings.Contains(rr.Body.String(), "sentinel-bound") {
		t.Fatalf("approved host did not get its bound config: %d %s", rr.Code, rr.Body.String())
	}
	if err := hardware.Revoke(mac); err != nil {
		t.Fatal(err)
	}
	if rr := fetchConfig(t, h, "/preseed?mac="+mac); rr.Body.String() != want.Body.String() {
		t.Errorf("revoked host answer differs from unknown host:\ngot  %q\nwant %q", rr.Body.String(), want.Body.String())
	}
}
