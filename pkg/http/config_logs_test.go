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

// configLogRoute is one config route under a log capture: the request record
// its handler emits, and the setup that makes the approved host servable.
type configLogRoute struct {
	path, os, msg string
	setup         func(t *testing.T)
	handler       func(s *db.Store) http.HandlerFunc
}

func configLogRoutes() []configLogRoute {
	return []configLogRoute{
		{
			"/machineconfig", "talos", "machineconfig request",
			func(t *testing.T) {
				t.Helper()
				viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
				writeFile(t, "config/machineconfig.yaml", "machine: {}\n")
			},
			func(s *db.Store) http.HandlerFunc { return machineConfigHandler(s, failingARP, allowProof{}) },
		},
		{
			"/preseed", "debian", "preseed request",
			func(t *testing.T) {
				t.Helper()
				viper.Set(config.PreseedFile, "config/preseed.cfg")
				writeFile(t, "config/preseed.cfg", "d-i debian-installer/locale string en_US.UTF-8\n")
			},
			func(s *db.Store) http.HandlerFunc { return preseedHandler(s, nil, failingARP, allowProof{}) },
		},
		{
			"/ignition.json", "flatcar", "ignition request",
			func(t *testing.T) {
				t.Helper()
				viper.Set(config.IgnitionFile, "config/ignition.yaml")
				writeFile(t, "config/ignition.yaml", "variant: fcos\nversion: 1.5.0\n")
			},
			func(s *db.Store) http.HandlerFunc { return ignitionHandler(s, nil, arpLookup, allowProof{}) },
		},
	}
}

// captureLogs routes the default slog logger into the returned buffer for the
// test's lifetime.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &logBuf
}

// TestConfigLogsNeverCarryTicket: a boot ticket is a short-lived bearer for a
// host's config, so booty never logs it. The config handlers log the path and
// the normalised MAC, not the request URI. The request record must exist, or
// the absence check would pass with no logging at all.
func TestConfigLogsNeverCarryTicket(t *testing.T) {
	const ticket = "SENTINEL-TICKET"
	const mac = "aa:bb:cc:dd:ee:62"
	for _, rt := range configLogRoutes() {
		t.Run(rt.path, func(t *testing.T) {
			s := servingStore(t)
			rt.setup(t)
			if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: rt.os}); err != nil {
				t.Fatal(err)
			}
			approveHost(t, mac)
			logBuf := captureLogs(t)

			fetchConfig(t, rt.handler(s), rt.path+"?mac="+mac+"&ticket="+ticket)

			logs := logBuf.String()
			if strings.Contains(logs, ticket) {
				t.Fatalf("a log record carries the ticket:\n%s", logs)
			}
			for _, want := range []string{`msg="` + rt.msg + `"`, "path=" + rt.path, "mac=" + mac} {
				if !strings.Contains(logs, want) {
					t.Errorf("log lacks %q:\n%s", want, logs)
				}
			}
		})
	}
}

// TestConfigLogsNeverCarryRawMAC: the request record carries the normalised
// MAC, or "" when ?mac= does not parse, never the client's raw query value, so
// a crafted value cannot forge log lines.
func TestConfigLogsNeverCarryRawMAC(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:62"
	for _, rt := range configLogRoutes() {
		t.Run(rt.path, func(t *testing.T) {
			s := servingStore(t)
			rt.setup(t)
			if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: rt.os}); err != nil {
				t.Fatal(err)
			}
			approveHost(t, mac)
			logBuf := captureLogs(t)

			for _, tc := range []struct {
				query  string
				want   string
				absent []string
			}{
				{"AA-BB-CC-DD-EE-62", "mac=" + mac, []string{"AA-BB-CC-DD-EE-62"}},
				{"evil%0Afake=1", `mac=""`, []string{"evil", "fake=1"}},
			} {
				logBuf.Reset()
				fetchConfig(t, rt.handler(s), rt.path+"?mac="+tc.query)
				logs := logBuf.String()
				if !strings.Contains(logs, `msg="`+rt.msg+`"`) || !strings.Contains(logs, tc.want) {
					t.Errorf("?mac=%s: log lacks the %q record with %q:\n%s", tc.query, rt.msg, tc.want, logs)
				}
				for _, absent := range tc.absent {
					if strings.Contains(logs, absent) {
						t.Errorf("?mac=%s: log carries raw %q:\n%s", tc.query, absent, logs)
					}
				}
			}
		})
	}
}
