package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"text/template"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
	coreostype "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/nodereport"
	"github.com/jeefy/booty/pkg/ostype"
	"github.com/spf13/viper"
)

// ignitionHandler serves a Flatcar/CoreOS Ignition config: the DB-resolved
// config (precedence rungs 1–2) when one is bound, else the file template
// (rungs 3–4). n nil leaves the node health unit out. A host it will not serve
// (unknown, unapproved, or a request without a valid boot ticket for it, #88)
// gets the reboot config, so it PXE-boots again and fetches a fresh ticket.
func ignitionHandler(store *db.Store, n *NodeReports, arp arpFunc, proof configProof) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.InfoContext(r.Context(), "ignition request", "path", r.URL.Path, "mac", loggableMAC(r.URL.Query().Get("mac")))
		host := configHost(identifyHostWith(r, arp))
		if host != nil && !proof.proven(r, host, ostype.FamilyIgnition) {
			host = nil
		}
		if host == nil {
			serveRebootIgnition(w)
			return
		}
		// DB-resolved config (precedence rungs 1–2) takes priority over the file.
		if serveBoundIgnition(w, r, n, store, host) {
			return
		}
		serveFileIgnition(w, r, n, store, host)
	}
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

// serveRebootIgnition answers a host booty will not serve with an Ignition
// config whose only unit reboots it, so it PXE-boots again. It reads no
// template: a missing or broken --ignitionFile cannot turn it into a 5xx,
// which Ignition retries forever instead of rebooting (design R4).
func serveRebootIgnition(w http.ResponseWriter) {
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
}

// serveFileIgnition serves the file-based config (precedence rungs 3–4) for an approved host.
func serveFileIgnition(w http.ResponseWriter, r *http.Request, n *NodeReports, store *db.Store, host *hardware.Host) {
	var tpl bytes.Buffer

	templateData := struct {
		JoinString string
		ServerIP   string
		Hostname   string
	}{
		JoinString: viper.GetString(config.JoinString),
		ServerIP:   fmt.Sprintf("%s:%s", viper.GetString(config.ServerIP), viper.GetString(config.ServerHttpPort)),
		Hostname:   hardware.StripControl(host.Hostname),
	}

	ignitionFile := viper.GetString(config.IgnitionFile)
	if host.IgnitionFile != "" {
		ignitionFile = host.IgnitionFile
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
