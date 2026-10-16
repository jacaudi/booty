package http

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"text/template"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// handleMachineConfigRequest serves a Talos machineconfig rendered from the
// template at <dataDir>/<talosConfigFile>. UUID/Serial come from the query
// string per request (not persisted in PR6); host fields come from the DB when
// the MAC identifies a host. A caller with no approved host gets a 404 (#162),
// and genuine failures return 500 + short plaintext.
// It is returned as a closure over store so a DB-resolved config (precedence
// rungs 1–2) can be tried before falling through to the existing file-based
// path (rungs 3–4, reached only for an approved host).
func handleMachineConfigRequest(store *db.Store) http.HandlerFunc {
	return machineConfigHandler(store, arpLookup)
}

// machineConfigHandler is handleMachineConfigRequest with the ARP lookup
// injected.
func machineConfigHandler(store *db.Store, arp arpFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("machineconfig request", "uri", r.RequestURI)

		macAddress := machineConfigMAC(r, arp)
		hostname := queryHostname(r, macAddress)
		host := machineConfigHost(macAddress)

		// Approval is the gate (#158). An unapproved cluster member gets a 403,
		// never its frozen config and never the generic file config below, which
		// would be worse than an error (see the member rung). Any other caller
		// with no approved host (unknown, unapproved, revoked or unidentified) is
		// refused before any template is rendered (#162), so nothing in the
		// server-default file, including the join string, can reach it.
		if host != nil && host.NodeConfigID != nil && !host.Approved {
			writeError(w, http.StatusForbidden, "cluster member config requires an approved host", nil)
			return
		}
		host = configHost(host)
		if host == nil {
			writeError(w, http.StatusNotFound, "no approved host for this request", nil)
			return
		}

		// P6 top rung: a cluster member serves its ACTIVE frozen, encrypted
		// node config VERBATIM (materialize-and-freeze, design §5). A member
		// whose frozen bytes cannot be loaded/decrypted gets a LOUD 500 and
		// NEVER falls through, since feeding a member a non-member config would
		// be worse than an error. This is the interim fix from design item 0b:
		// the request's source IP must ARP-resolve to the member's MAC. L2
		// spoofing is still possible, and members booted through a DHCP relay
		// cannot be ARP-resolved and are refused until the authenticated-fetch
		// design.
		if host.NodeConfigID != nil {
			if !requestFromMAC(r, host.MAC, arp) {
				writeError(w, http.StatusForbidden,
					"cluster member config requires the request to come from the member", nil)
				return
			}
			serveFrozenNodeConfig(w, store, host)
			return
		}

		if serveBoundMachineConfig(w, r, store, host, hostname) {
			return
		}
		serveFileMachineConfig(w, r, host, hostname)
	}
}

// machineConfigMAC is the MAC the request names: ?mac= when present, else what
// arp resolves for the client's address, else "".
func machineConfigMAC(r *http.Request, arp arpFunc) string {
	if mac := r.URL.Query().Get("mac"); mac != "" {
		return mac
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		slog.WarnContext(r.Context(), "error splitting user ip; not IP:port", "remote", r.RemoteAddr)
	}
	hwAddr, err := arp(net.ParseIP(ip))
	if err != nil {
		slog.WarnContext(r.Context(), "error with ARP request", "err", err)
		return ""
	}
	return hwAddr.String()
}

// machineConfigHost loads the host for macAddress, or nil when the MAC is
// empty, unknown or the lookup fails (a failure is logged, not fatal).
func machineConfigHost(macAddress string) *hardware.Host {
	if macAddress == "" {
		return nil
	}
	host, err := hardware.GetMacAddress(macAddress)
	if err != nil && !errors.Is(err, hardware.ErrNotFound) {
		slog.Warn("error looking up host", "mac", macAddress, "err", err)
	}
	return host
}

// serveBoundMachineConfig serves the DB-resolved config bound to host (rungs
// 1–2) and reports whether it answered.
func serveBoundMachineConfig(w http.ResponseWriter, r *http.Request, store *db.Store, host *hardware.Host, hostname string) bool {
	src, kind, ok := resolveConfig(store, host)
	if !ok || kind != "machineconfig" {
		return false
	}
	out, ct, _, err := renderConfig("machineconfig", src, machineConfigVars(store, r, host, hostname))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "render bound machineconfig", err)
		return true
	}
	w.Header().Set("Content-Type", ct)
	writeBody(w, out)
	return true
}

// serveFileMachineConfig renders the <dataDir>/<talosConfigFile> template (rungs
// 3–4) for an approved host.
func serveFileMachineConfig(w http.ResponseWriter, r *http.Request, host *hardware.Host, hostname string) {
	schematic := viper.GetString(config.TalosSchematic)
	templateData := struct {
		Hostname       string
		MAC            string
		IP             string
		UUID           string
		Serial         string
		ServerIP       string
		ServerHTTPPort string
		JoinString     string
		Schematic      string
	}{
		Hostname:       hostname,
		MAC:            host.MAC,
		IP:             host.IP,
		UUID:           r.URL.Query().Get("uuid"),
		Serial:         r.URL.Query().Get("serial"),
		ServerIP:       viper.GetString(config.ServerIP),
		ServerHTTPPort: viper.GetString(config.ServerHttpPort),
		JoinString:     viper.GetString(config.JoinString),
		Schematic:      schematic,
	}
	if host.Hostname != "" {
		templateData.Hostname = hardware.StripControl(host.Hostname)
	}
	if host.Schematic != "" {
		templateData.Schematic = host.Schematic
	}

	path, err := resolveWithinDataDir(viper.GetString(config.TalosConfigFile))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "machineconfig template unavailable", err)
		return
	}
	t, err := template.ParseFiles(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "machineconfig template unavailable", err)
		return
	}
	var out bytes.Buffer
	if err := t.Execute(&out, templateData); err != nil {
		writeError(w, http.StatusInternalServerError, "machineconfig render failed", err)
		return
	}

	w.Header().Set("Content-Type", "text/yaml")
	writeBody(w, out.Bytes())
}

// machineConfigVars populates TemplateVars for the TALOS family: .ServerIP is
// host-ONLY with a separate .ServerHTTPPort (the live machineconfig semantics —
// must NOT be host:port); .TalosVersion is newly sourced from the newest cached
// talos version for the host's schematic; .Roles from host_roles. Hostname/UUID/
// Serial seed from the request query string; the approved host's own record
// then overrides Hostname.
func machineConfigVars(store *db.Store, r *http.Request, host *hardware.Host, hostname string) TemplateVars {
	return machineConfigVarsCore(store, host, hostname, r.URL.Query().Get("uuid"), r.URL.Query().Get("serial"))
}

// queryHostname returns the request's ?hostname=, or "" when it is not a
// valid RFC 1123 name. The value comes from DHCP on the Talos kernel line,
// so an invalid one is logged and ignored rather than failing the boot.
func queryHostname(r *http.Request, macAddress string) string {
	h := r.URL.Query().Get("hostname")
	if h == "" {
		return ""
	}
	err := hardware.ValidateHostname(h)
	if err == nil {
		return h
	}
	slog.WarnContext(r.Context(), "machineconfig: ignoring invalid hostname", "mac", loggableMAC(macAddress), "err", err)
	return ""
}

// machineConfigPreviewVars is machineConfigVars' preview-path sibling: preview
// has no *http.Request, so Hostname/UUID/Serial seed from the host record
// instead of a query string. Shares machineConfigVarsCore with the serving
// path so ServerIP/Schematic/TalosVersion/Roles computation cannot drift
// between "what would actually boot" and "what preview shows".
func machineConfigPreviewVars(store *db.Store, host *hardware.Host) TemplateVars {
	var hostname, uuid, serial string
	if host != nil {
		hostname, uuid, serial = host.Hostname, host.UUID, host.Serial
	}
	return machineConfigVarsCore(store, host, hostname, uuid, serial)
}

// machineConfigVarsCore holds the TALOS-family var population SHARED by the
// serving and preview paths. hostname/uuid/serial are pre-resolved by the
// caller because their source differs (request query vs. the host record);
// everything else (ServerIP host-only + ServerHTTPPort, Schematic resolution,
// TalosVersion, Roles, MAC/IP) is identical for both.
func machineConfigVarsCore(store *db.Store, host *hardware.Host, hostname, uuid, serial string) TemplateVars {
	schematic := viper.GetString(config.TalosSchematic)
	vars := TemplateVars{
		Hostname:       hostname,
		UUID:           uuid,
		Serial:         serial,
		ServerIP:       viper.GetString(config.ServerIP),
		ServerHTTPPort: viper.GetString(config.ServerHttpPort),
		JoinString:     viper.GetString(config.JoinString),
		Schematic:      schematic,
	}
	if host != nil {
		if host.Hostname != "" {
			vars.Hostname = hardware.StripControl(host.Hostname)
		}
		vars.MAC = host.MAC
		vars.IP = host.IP
		if host.Schematic != "" {
			vars.Schematic = host.Schematic
		}
		vars.Roles = roleNames(store, host.MAC)
		vars.TalosVersion = cache.NewestCached("talos", viper.GetString(config.TalosArchitecture),
			map[string]string{"schematic": vars.Schematic})
	}
	return vars
}

// serveFrozenNodeConfig writes a cluster member's active frozen machineconfig
// (design §5): load the encrypted revision, decrypt under --secretsKey, and
// replay the plaintext bytes byte-for-byte with the Talos content type. Every
// failure is a loud 500; this function is only reached for hosts whose
// node_config_id is set, so a fall-through is never correct.
func serveFrozenNodeConfig(w http.ResponseWriter, store *db.Store, host *hardware.Host) {
	nc, err := store.GetClusterNodeConfig(*host.NodeConfigID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load frozen node config", err)
		return
	}
	plain, err := decryptSecrets(nc.ConfigEnc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "decrypt frozen node config", err)
		return
	}
	w.Header().Set("Content-Type", "text/yaml")
	_, _ = w.Write(plain)
}
