package http

import (
	"cmp"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// handlePreseedRequest serves a Debian preseed: a DB-resolved config (rungs
// 1–2, kind preseed OR debianconfig — both render to a flat preseed) when the
// host is bound, else the --preseedFile server default (rung 4).
// Debian has no legacy per-host file column, so rung 3 does not apply.
func handlePreseedRequest(store *db.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("preseed request", "uri", r.RequestURI)
		host := identifyHost(r)

		if host != nil {
			// The preseed family is 1:many (design §7): raw `preseed` and curated
			// `debianconfig` both render to a flat preseed body. Dispatch renderConfig
			// on the RESOLVED kind (M2) — the guard and the family contract are
			// single-sourced in familyAllowsKind.
			if src, kind, ok := resolveConfig(store, host); ok && familyAllowsKind("preseed", kind) {
				vars := preseedVars(store, host)
				out, ct, _, err := renderConfig(kind, src, vars)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "render bound preseed", err)
					return
				}
				out = withDVDMirror(store, host, vars.ServerIP, out)
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
		vars := preseedVars(store, host)
		out, ct, _, err := renderConfig("preseed", src, vars)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "preseed render failed", err)
			return
		}
		out = withDVDMirror(store, host, vars.ServerIP, out)
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(out)
	}
}

// withDVDMirror appends booty's local apt mirror directives to a rendered
// preseed when the requesting host boots a dvd-mode Debian target — covering
// raw preseed, debianconfig, and the server default alike (design: appending
// is safe for debconf since a later directive overrides an earlier one, so
// this needs no TemplateVars field and render.go/debiangen.go stay untouched).
// A netinst / non-debian / unidentified host is unaffected: out is returned
// as-is. mirrorHost is vars.ServerIP (already host:port) — reused rather than
// recomputed, since preseedVars is the single source of that construction.
func withDVDMirror(store *db.Store, host *hardware.Host, mirrorHost string, out []byte) []byte {
	dir, ok := debianDVDMirrorDir(store, host)
	if !ok {
		return out
	}
	return appendDVDMirror(out, mirrorHost, dir)
}

// appendDVDMirror appends the three d-i mirror directives that point apt at
// booty's local extracted DVD tree — the single source of that directive
// template.
func appendDVDMirror(out []byte, mirrorHost, dir string) []byte {
	return fmt.Appendf(out, "\nd-i mirror/country string manual\nd-i mirror/http/hostname string %s\nd-i mirror/http/directory string %s\n",
		mirrorHost, dir)
}

// debianDVDMirrorDir resolves the client-facing cache directory for a
// dvd-mode Debian host's local mirror, or ok=false when the host is nil,
// not Debian, or its resolved target is not (yet) dvd-mode — the netinst
// (and non-Debian) case, which must serve its preseed unchanged.
//
// arch is hardcoded "amd64" and the suite falls back to the literal "13"
// absent an assigned channel, mirroring pkg/tftp's bootTokens debian case
// (Task 8): Debian has no config.DebianArchitecture/DebianChannel flag, so
// there is no other per-deployment default to read.
func debianDVDMirrorDir(store *db.Store, host *hardware.Host) (string, bool) {
	if host == nil || host.OS != "debian" {
		return "", false
	}
	const arch = "amd64"
	params, _ := cache.DecodeParams(host.AssignedParams)
	suite := cmp.Or(params["channel"], "13")

	encoded, err := cache.EncodeParams(map[string]string{"channel": suite})
	if err != nil {
		return "", false
	}
	t, err := store.GetTargetByIdentity("debian", arch, encoded)
	if err != nil || t.SourceMode != "dvd" {
		return "", false
	}
	version := cache.NewestCached("debian", arch, map[string]string{"channel": suite})
	return cache.CacheURLPath("debian", suite, arch, version), true
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
