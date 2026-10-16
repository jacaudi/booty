package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

const evilHostname = "evil\nbad"

func seedEvilHost(t *testing.T, mac, os string) *hardware.Host {
	t.Helper()
	h := hardware.Host{MAC: mac, OS: os, Hostname: evilHostname, Approved: true}
	if err := hardware.WriteMacAddress(mac, h); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	return &h
}

func TestVarsStripStoredHostname(t *testing.T) {
	s := servingStore(t)
	host := seedEvilHost(t, "aa:bb:cc:00:00:10", "flatcar")
	for name, got := range map[string]string{
		"ignitionVars":          ignitionVars(s, host).Hostname,
		"preseedVars":           preseedVars(s, host).Hostname,
		"machineConfigVarsCore": machineConfigVarsCore(s, host, "", "", "").Hostname,
	} {
		if got != "evilbad" {
			t.Errorf("%s hostname = %q, want %q", name, got, "evilbad")
		}
	}
}

func TestIgnitionFileTemplateStripsHostname(t *testing.T) {
	s := servingStore(t)
	const mac = "aa:bb:cc:00:00:11"
	seedEvilHost(t, mac, "flatcar")
	viper.Set(config.IgnitionFile, "config/ignition.yaml")
	writeFile(t, "config/ignition.yaml", "variant: flatcar\nversion: 1.1.0\nstorage:\n  files:\n"+
		"    - path: /etc/hostname\n      contents:\n        inline: {{ .Hostname }}\n")

	rec := httptest.NewRecorder()
	handleIgnitionRequest(s)(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ignition.json?mac="+mac, http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "evilbad") {
		t.Errorf("body lacks evilbad: %q", rec.Body.String())
	}
}

func TestMachineConfigFileTemplateStripsHostname(t *testing.T) {
	s := servingStore(t)
	const mac = "aa:bb:cc:00:00:12"
	seedEvilHost(t, mac, "talos")
	viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
	writeFile(t, "config/machineconfig.yaml", "machine:\n  network:\n    hostname: {{ .Hostname }}\n")

	rec := httptest.NewRecorder()
	handleMachineConfigRequest(s)(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/machineconfig?mac="+mac, http.NoBody))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, body)
	}
	if !strings.Contains(body, "hostname: evilbad") || strings.Contains(body, evilHostname) {
		t.Errorf("hostname not stripped: %q", body)
	}
}

func TestPreseedFileTemplateStripsHostname(t *testing.T) {
	s := servingStore(t)
	const mac = "aa:bb:cc:00:00:13"
	seedEvilHost(t, mac, "debian")
	viper.Set(config.PreseedFile, "config/preseed.cfg")
	writeFile(t, "config/preseed.cfg", "d-i netcfg/get_hostname string {{ .Hostname }}\n")

	rec := httptest.NewRecorder()
	handlePreseedRequest(s)(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/preseed?mac="+mac, http.NoBody))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, body)
	}
	if !strings.Contains(body, "get_hostname string evilbad") {
		t.Errorf("hostname not stripped: %q", body)
	}
}
