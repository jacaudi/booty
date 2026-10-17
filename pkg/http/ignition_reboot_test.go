package http

import (
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

// TestIgnitionRebootConfigNeedsNoTemplate: the reboot config is booty's answer
// to a host it will not serve, and it must not depend on the --ignitionFile
// template. A 500 there makes Ignition retry forever instead of rebooting
// (design R4, Gate 1 B2).
func TestIgnitionRebootConfigNeedsNoTemplate(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:61"
	for _, c := range []struct {
		name     string
		register bool
	}{
		{"unknown MAC", false},
		{"unapproved host", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := servingStore(t)
			viper.Set(config.IgnitionFile, "config/missing.yaml")
			if c.register {
				if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar"}); err != nil {
					t.Fatal(err)
				}
			}
			rr := fetchConfig(t, ignitionHandler(s, nil, arpLookup), "/ignition.json?mac="+mac)
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "booty-reboot.service") {
				t.Fatalf("got %d %q, want 200 with the reboot config", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestIgnitionARPFallback: with no ?mac=, the host the source IP ARP-resolves
// to is served, through the injected lookup (ignition had no test seam before).
func TestIgnitionARPFallback(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:63"
	const sentinel = "SENTINEL-ARP-JOIN"
	s := servingStore(t)
	viper.Set(config.JoinString, sentinel)
	viper.Set(config.IgnitionFile, "config/ignition.yaml")
	writeFile(t, "config/ignition.yaml", "variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: /etc/x\n      contents:\n        source: http://{{ .JoinString }}/x\n")
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar"}); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	rr := fetchConfig(t, ignitionHandler(s, nil, fixedARP(mac)), "/ignition.json")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), sentinel) {
		t.Fatalf("got %d %q, want the host's config", rr.Code, rr.Body.String())
	}
}
