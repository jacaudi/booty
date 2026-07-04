package http

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// handlePreseedRequest serves a Debian preseed: a DB-resolved config (rungs 1–2)
// when the host is bound, else the --preseedFile server default (rung 4).
// Debian has no legacy per-host file column, so rung 3 does not apply.
func handlePreseedRequest(store *db.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("preseed request", "uri", r.RequestURI)
		host := identifyHost(r)

		if host != nil {
			if src, kind, ok := resolveConfig(store, host); ok && kind == "preseed" {
				out, ct, _, err := renderConfig("preseed", src, preseedVars(store, host))
				if err != nil {
					writeError(w, http.StatusInternalServerError, "render bound preseed", err)
					return
				}
				w.Header().Set("Content-Type", ct)
				_, _ = w.Write(out)
				return
			}
		}

		// Rung 4: server-default file.
		path := filepath.Join(viper.GetString(config.DataDir), viper.GetString(config.PreseedFile))
		src, err := os.ReadFile(path)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "preseed template unavailable", err)
			return
		}
		out, ct, _, err := renderConfig("preseed", src, preseedVars(store, host))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "preseed render failed", err)
			return
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(out)
	}
}

func preseedVars(store *db.Store, host *hardware.Host) TemplateVars {
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

// identifyHost resolves the requesting host by ?mac= or ARP (nil when
// unidentified), mirroring the ignition/machineconfig identification block.
func identifyHost(r *http.Request) *hardware.Host {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil {
			if hw, _, aerr := arping.Ping(net.ParseIP(ip)); aerr == nil {
				mac = hw.String()
			}
		}
	}
	if mac == "" {
		return nil
	}
	h, err := hardware.GetMacAddress(mac)
	if err != nil {
		return nil
	}
	return h
}
