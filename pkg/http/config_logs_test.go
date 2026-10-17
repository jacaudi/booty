package http

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

// TestConfigLogsNeverCarryTicket: a boot ticket is a short-lived bearer for a
// host's config, so booty never logs it. The config handlers log the path and
// the normalised MAC, not the request URI.
func TestConfigLogsNeverCarryTicket(t *testing.T) {
	const ticket = "SENTINEL-TICKET"
	const mac = "aa:bb:cc:dd:ee:62"
	routes := []struct {
		path, os string
		setup    func(t *testing.T)
		handler  func(s *db.Store) http.HandlerFunc
	}{
		{
			"/machineconfig", "talos",
			func(t *testing.T) {
				t.Helper()
				viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
				writeFile(t, "config/machineconfig.yaml", "machine: {}\n")
			},
			func(s *db.Store) http.HandlerFunc { return machineConfigHandler(s, failingARP, allowProof{}) },
		},
		{
			"/preseed", "debian",
			func(t *testing.T) {
				t.Helper()
				viper.Set(config.PreseedFile, "config/preseed.cfg")
				writeFile(t, "config/preseed.cfg", "d-i debian-installer/locale string en_US.UTF-8\n")
			},
			func(s *db.Store) http.HandlerFunc { return preseedHandler(s, nil, failingARP, allowProof{}) },
		},
		{
			"/ignition.json", "flatcar",
			func(t *testing.T) {
				t.Helper()
				viper.Set(config.IgnitionFile, "config/ignition.yaml")
				writeFile(t, "config/ignition.yaml", "variant: fcos\nversion: 1.5.0\n")
			},
			func(s *db.Store) http.HandlerFunc { return ignitionHandler(s, nil, arpLookup, allowProof{}) },
		},
	}
	for _, rt := range routes {
		t.Run(rt.path, func(t *testing.T) {
			s := servingStore(t)
			rt.setup(t)
			if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: rt.os}); err != nil {
				t.Fatal(err)
			}
			approveHost(t, mac)
			var logBuf bytes.Buffer
			orig := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(orig) })

			fetchConfig(t, rt.handler(s), rt.path+"?mac="+mac+"&ticket="+ticket)

			if strings.Contains(logBuf.String(), ticket) {
				t.Fatalf("a log record carries the ticket:\n%s", logBuf.String())
			}
		})
	}
}
