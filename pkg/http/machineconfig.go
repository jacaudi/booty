package http

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"text/template"

	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// handleMachineConfigRequest serves a Talos machineconfig rendered from the
// template at <dataDir>/<talosConfigFile>. UUID/Serial come from the query
// string per request (not persisted in PR6); host fields come from the DB when
// the MAC identifies a host. Genuine failures return 500 + short plaintext.
// It is returned as a closure over store so a DB-resolved config (precedence
// rungs 1–2) can be tried before falling through to the existing file-based
// path (rungs 3–4, unchanged).
func handleMachineConfigRequest(store *db.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("machineconfig request", "uri", r.RequestURI)

		macAddress := r.URL.Query().Get("mac")
		if macAddress == "" {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				slog.Warn("error splitting user ip; not IP:port", "remote", r.RemoteAddr)
			}
			if hwAddr, _, err := arping.Ping(net.ParseIP(ip)); err != nil {
				slog.Warn("error with ARP request", "err", err)
			} else {
				macAddress = hwAddr.String()
			}
		}

		var host *hardware.Host
		if macAddress != "" {
			h, err := hardware.GetMacAddress(macAddress)
			if err != nil && !errors.Is(err, hardware.ErrNotFound) {
				slog.Warn("error looking up host", "mac", macAddress, "err", err)
			}
			host = h
		}

		if host != nil {
			// P6 top rung: a cluster member serves its ACTIVE frozen, encrypted
			// node config VERBATIM (materialize-and-freeze, design §5). A member
			// whose frozen bytes cannot be loaded/decrypted gets a LOUD 500 and
			// NEVER falls through — feeding a member a non-member config (one
			// without its cluster's secrets/identity) would be worse than an error.
			if host.NodeConfigID != nil {
				serveFrozenNodeConfig(w, store, host)
				return
			}
		}

		if host != nil {
			if src, kind, ok := resolveConfig(store, host); ok && kind == "machineconfig" {
				out, ct, _, err := renderConfig("machineconfig", src, machineConfigVars(store, r, host))
				if err != nil {
					writeError(w, http.StatusInternalServerError, "render bound machineconfig", err)
					return
				}
				w.Header().Set("Content-Type", ct)
				_, _ = w.Write(out)
				return
			}
		}

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
			Hostname:       r.URL.Query().Get("hostname"),
			UUID:           r.URL.Query().Get("uuid"),
			Serial:         r.URL.Query().Get("serial"),
			ServerIP:       viper.GetString(config.ServerIP),
			ServerHTTPPort: viper.GetString(config.ServerHttpPort),
			JoinString:     viper.GetString(config.JoinString),
			Schematic:      schematic,
		}
		// Unlike ignition's reboot-on-unknown, Talos legitimately fetches its config
		// before it exists in the DB (identity comes from the query uuid/serial/hostname
		// at first boot), so render a host-less config rather than forcing a reboot.
		if host != nil {
			if host.Hostname != "" {
				templateData.Hostname = host.Hostname
			}
			templateData.MAC = host.MAC
			templateData.IP = host.IP
			if host.Schematic != "" {
				templateData.Schematic = host.Schematic
			}
		}

		path := fmt.Sprintf("%s/%s", viper.GetString(config.DataDir), viper.GetString(config.TalosConfigFile))
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
		_, _ = w.Write(out.Bytes())
	}
}

// machineConfigVars populates TemplateVars for the TALOS family: .ServerIP is
// host-ONLY with a separate .ServerHTTPPort (the live machineconfig semantics —
// must NOT be host:port); .TalosVersion is newly sourced from the newest cached
// talos version for the host's schematic; .Roles from host_roles. Hostname/UUID/
// Serial seed from the request query string, since Talos legitimately fetches
// its config before the host exists in the DB.
func machineConfigVars(store *db.Store, r *http.Request, host *hardware.Host) TemplateVars {
	return machineConfigVarsCore(store, host,
		r.URL.Query().Get("hostname"), r.URL.Query().Get("uuid"), r.URL.Query().Get("serial"))
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
			vars.Hostname = host.Hostname
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
