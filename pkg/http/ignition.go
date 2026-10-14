package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"text/template"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
	coreostype "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/nodereport"
	"github.com/spf13/viper"
)

// handleIgnitionRequest returns the handler as a closure over store so a
// DB-resolved config (precedence rungs 1–2) can be tried before falling
// through to the existing file-based path (rungs 3–4, unchanged).
func handleIgnitionRequest(store *db.Store) http.HandlerFunc {
	return ignitionHandler(store, nil)
}

// ignitionHandler is handleIgnitionRequest with the optional node health
// report unit (n nil = feature off, output unchanged).
func ignitionHandler(store *db.Store, n *NodeReports) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If we don't identify the host, tell FlatCar to reboot
		// Reboot the host till we identify it
		// Cool so, we want to have logic based around a recognized MAC address
		// Therefore what we need to do is collect the MAC address

		slog.Info("ignition request", "uri", r.RequestURI)

		macAddress := ignitionRequestMAC(r)
		slog.Debug("using mac address", "mac", macAddress)
		host := ignitionHost(macAddress)

		// DB-resolved config (precedence rungs 1–2) takes priority over the file.
		if host != nil && serveBoundIgnition(w, r, n, store, host) {
			return
		}
		serveFileIgnition(w, r, n, store, host)
	}
}

// ignitionRequestMAC is the MAC the request names: ?mac= when present, else
// what ARP resolves for the client's address, else "".
func ignitionRequestMAC(r *http.Request) string {
	if mac := r.URL.Query().Get("mac"); mac != "" {
		slog.DebugContext(r.Context(), "mac address url override", "mac", mac)
		return mac
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		slog.WarnContext(r.Context(), "error splitting user ip; not IP:port", "remote", r.RemoteAddr)
	}
	remoteIP := net.ParseIP(ip)

	hwAddr, _, err := arping.Ping(remoteIP)
	if err != nil {
		slog.WarnContext(r.Context(), "error with ARP request", "err", err)
		return ""
	}
	macAddress := hwAddr.String()
	slog.DebugContext(r.Context(), "mac address from ARP", "mac", macAddress)
	return macAddress
}

// ignitionHost loads the host for macAddress, or nil. An empty MAC means we
// never identified the host (no ?mac= and ARP found nothing). That is the
// expected unidentified-host case, not an error: skip the lookup so the
// caller serves the reboot config without logging a spurious warning. A
// non-empty but malformed MAC still flows through GetMacAddress and a genuine
// lookup error is worth a Warn.
func ignitionHost(macAddress string) *hardware.Host {
	if macAddress == "" {
		return nil
	}
	host, err := hardware.GetMacAddress(macAddress)
	if err != nil && !errors.Is(err, hardware.ErrNotFound) {
		slog.Warn("error looking up host", "mac", macAddress, "err", err)
		// Treat unexpected errors the same as a miss — fall through to the
		// reboot-config path so the machine doesn't boot loop on a bad DB.
	}
	return host
}

// serveBoundIgnition serves the DB-resolved config bound to host and reports
// whether it answered. The ignition family only ever resolves a butane config
// (family guard), which we translate exactly like the file path.
func serveBoundIgnition(w http.ResponseWriter, r *http.Request, n *NodeReports, store *db.Store, host *hardware.Host) bool {
	src, kind, ok := resolveConfig(store, host)
	if !ok || kind != "butane" {
		return false
	}
	vars := ignitionVars(store, host)
	out, ct, _, err := renderConfig("butane", src, vars)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "render bound ignition config", errors.New(redactJoinString(err.Error(), vars.JoinString)))
		return true
	}
	w.Header().Set("Content-Type", ct)
	writeBody(w, withNodeReport(r.Context(), n, store, host, out))
	return true
}

// rebootUnitContents is the unit served to a host booty cannot identify: it
// reboots the machine until it can be.
const rebootUnitContents = `
[Service]
Type=simple
ExecStart=reboot

[Install]
WantedBy=default.target
`

// serveFileIgnition serves the file-based config (precedence rungs 3–4), or
// the reboot config when host is nil.
func serveFileIgnition(w http.ResponseWriter, r *http.Request, n *NodeReports, store *db.Store, host *hardware.Host) {
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
		templateData.Hostname = hardware.StripControl(host.Hostname)
	}
	ignitionPath, err := resolveWithinDataDir(ignitionFile)
	if err != nil {
		// The name is host- or operator-supplied, so a bad value is a
		// configuration fault, not a server fault -- but the status stays
		// 500 to match the surrounding handler and to avoid telling an
		// unauthenticated boot client which names exist.
		writeError(w, http.StatusInternalServerError, "ignition template unavailable", err)
		return
	}
	t, err := template.ParseFiles(ignitionPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ignition template unavailable", err)
		return
	}

	if err = t.Execute(&tpl, templateData); err != nil {
		writeError(w, http.StatusInternalServerError, "ignition render failed", err)
		return
	}

	if host == nil {
		coreosConfig := coreostype.Config{}
		coreosConfig.Ignition.Version = "3.4.0"
		coreosConfig.Systemd.Units = append(coreosConfig.Systemd.Units, coreostype.Unit{
			Name:     "booty-reboot.service",
			Enabled:  new(true),
			Contents: new(rebootUnitContents),
		})
		dataOut, err := json.Marshal(&coreosConfig)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to marshal reboot config", err)
			return
		}
		writeBody(w, dataOut)
		return
	}

	ignCfg, report, err := butaneConfig.TranslateBytes(tpl.Bytes(), butaneCommon.TranslateBytesOptions{
		Pretty: true,
	})
	// Log Butane's messages and positions, never the rendered template: it
	// carries the join string, and so can Butane's text when it quotes a value.
	join := templateData.JoinString
	if err != nil {
		for _, entry := range report.Entries {
			slog.ErrorContext(r.Context(), "ignition report entry", "entry", redactJoinString(entry.String(), join))
		}
		writeError(w, http.StatusInternalServerError, "error parsing coreos ignition", errors.New(redactJoinString(err.Error(), join)))
		return
	}
	if len(report.Entries) > 0 {
		slog.WarnContext(r.Context(), "problems parsing coreos ignition", "report", redactJoinString(report.String(), join))
		for _, entry := range report.Entries {
			slog.WarnContext(r.Context(), "ignition report entry", "entry", redactJoinString(entry.String(), join))
		}
		if report.IsFatal() {
			writeError(w, http.StatusInternalServerError, "fatal coreos ignition report", errors.New(redactJoinString(report.String(), join)))
			return
		}
	}

	writeBody(w, withNodeReport(r.Context(), n, store, host, ignCfg))
}

// redactJoinString returns s with every occurrence of the join string
// replaced, for logging. Butane quotes template content in its errors and
// report entries, and ignition templates are rendered with the join string.
// Callers log a new error built from the result rather than wrapping the
// original, which would carry the unredacted text along with it.
func redactJoinString(s, join string) string {
	if join == "" {
		return s
	}
	return strings.ReplaceAll(s, join, "[redacted]")
}

// withNodeReport merges the health-report unit into an Ignition config when
// the feature is on for host; on any failure it logs and returns out as is.
func withNodeReport(ctx context.Context, n *NodeReports, store *db.Store, host *hardware.Host, out []byte) []byte {
	s, u, ok := n.script(ctx, store, host)
	if !ok {
		return out
	}
	merged, err := nodereport.MergeIgnition(out, s, u)
	if err != nil {
		slog.WarnContext(ctx, "node report injection skipped", "mac", host.MAC, "err", err)
		return out
	}
	return merged
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
		vars.Hostname = hardware.StripControl(host.Hostname)
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
