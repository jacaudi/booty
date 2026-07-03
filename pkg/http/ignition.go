package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"text/template"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
	coreOSType "github.com/coreos/ignition/v2/config/v3_5_experimental/types"
	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// handleIgnitionRequest returns the handler as a closure over store so a
// DB-resolved config (precedence rungs 1–2) can be tried before falling
// through to the existing file-based path (rungs 3–4, unchanged).
func handleIgnitionRequest(store *db.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If we don't identify the host, tell FlatCar to reboot
		// Reboot the host till we identify it
		// Cool so, we want to have logic based around a recognized MAC address
		// Therefore what we need to do is collect the MAC address

		slog.Info("ignition request", "uri", r.RequestURI)

		macAddress := ""

		if r.URL.Query().Get("mac") == "" {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				slog.Warn("error splitting user ip; not IP:port", "remote", r.RemoteAddr)
			}
			remoteIP := net.ParseIP(ip)

			if hwAddr, _, err := arping.Ping(remoteIP); err != nil {
				slog.Warn("error with ARP request", "err", err)
			} else {
				macAddress = hwAddr.String()
				slog.Debug("mac address from ARP", "mac", macAddress)
			}
		} else {
			macAddress = r.URL.Query().Get("mac")
			slog.Debug("mac address url override", "mac", macAddress)
		}

		slog.Debug("using mac address", "mac", macAddress)
		var host *hardware.Host
		// An empty MAC means we never identified the host (no ?mac= and ARP found
		// nothing). That is the expected unidentified-host case, not an error: skip
		// the lookup so host stays nil and we serve the reboot config below without
		// logging a spurious warning. A non-empty but malformed MAC still flows
		// through GetMacAddress and a genuine lookup error is worth a Warn.
		if macAddress != "" {
			var err error
			host, err = hardware.GetMacAddress(macAddress)
			if err != nil && !errors.Is(err, hardware.ErrNotFound) {
				slog.Warn("error looking up host", "mac", macAddress, "err", err)
				// Treat unexpected errors the same as a miss — fall through to the
				// reboot-config path so the machine doesn't boot loop on a bad DB.
			}
		}

		// DB-resolved config (precedence rungs 1–2) takes priority over the file.
		// The ignition family only ever resolves a butane config (family guard),
		// which we translate exactly like the file path.
		if host != nil {
			if src, kind, ok := resolveConfig(store, host); ok && kind == "butane" {
				vars := ignitionVars(store, host)
				out, ct, _, err := renderConfig("butane", src, vars)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "render bound ignition config", err)
					return
				}
				w.Header().Set("Content-Type", ct)
				_, _ = w.Write(out)
				return
			}
		}

		var tpl bytes.Buffer

		templateData := struct {
			JoinString string
			ServerIP   string
			Hostname   string
		}{
			JoinString: viper.GetString(config.JoinString),
			ServerIP:   fmt.Sprintf("%s:%s", viper.GetString(config.ServerIP), viper.GetString(config.ServerHttpPort)),
		}

		ignitionFile := viper.GetString(config.IgnitionFile)
		if host != nil {
			if host.IgnitionFile != "" {
				ignitionFile = host.IgnitionFile
			}
			templateData.Hostname = host.Hostname
		}
		t, err := template.ParseFiles(fmt.Sprintf("%s/%s", viper.GetString(config.DataDir), ignitionFile))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "ignition template unavailable", err)
			return
		}

		if err = t.Execute(&tpl, templateData); err != nil {
			writeError(w, http.StatusInternalServerError, "ignition render failed", err)
			return
		}

		if host == nil {
			coreosConfig := coreOSType.Config{}
			coreosConfig.Ignition.Version = "3.4.0"
			truePointer := true
			contentsPointer := `
[Service]
Type=simple
ExecStart=reboot

[Install]
WantedBy=default.target
`
			coreosConfig.Systemd.Units = append(coreosConfig.Systemd.Units, coreOSType.Unit{
				Name:     "Reboot now please",
				Enabled:  &truePointer,
				Contents: &contentsPointer,
			})
			dataOut, err := json.Marshal(&coreosConfig)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to marshal reboot config", err)
				return
			}
			w.Write(dataOut)
			return
		}

		ignCfg, report, err := butaneConfig.TranslateBytes(tpl.Bytes(), butaneCommon.TranslateBytesOptions{
			Pretty: true,
		})
		if err != nil {
			slog.Error("rendered ignition template", "template", tpl.String())
			for _, entry := range report.Entries {
				slog.Error("ignition report entry", "entry", entry.String())
			}
			writeError(w, http.StatusInternalServerError, "error parsing coreos ignition", err)
			return
		}
		if len(report.Entries) > 0 {
			slog.Warn("problems parsing coreos ignition", "report", report.String())
			for _, entry := range report.Entries {
				slog.Warn("ignition report entry", "entry", entry.String())
			}
			if report.IsFatal() {
				slog.Error("rendered ignition template", "template", tpl.String())
				writeError(w, http.StatusInternalServerError, "fatal coreos ignition report", errors.New(report.String()))
				return
			}
		}

		w.Write(ignCfg)
	}
}

// ignitionVars populates TemplateVars for the IGNITION family: .ServerIP is
// host:port (examples/config/ignition.yaml depends on it) — never host-only.
// This reuses the exact same host:port expression as the file path above so
// bound and unbound configs stay consistent.
func ignitionVars(store *db.Store, host *hardware.Host) TemplateVars {
	vars := TemplateVars{
		JoinString: viper.GetString(config.JoinString),
		ServerIP:   fmt.Sprintf("%s:%s", viper.GetString(config.ServerIP), viper.GetString(config.ServerHttpPort)),
	}
	if host != nil {
		vars.Hostname = host.Hostname
		vars.MAC = host.MAC
		vars.IP = host.IP
		vars.Roles = roleNames(store, host.MAC)
	}
	return vars
}

// roleNames returns a host's role names (name-asc) for the .Roles var; a lookup
// error yields nil (roles are optional).
func roleNames(store *db.Store, mac string) []string {
	roles, err := store.ListHostRoles(mac)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return names
}
