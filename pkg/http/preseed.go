package http

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/nodereport"
	"github.com/jeefy/booty/pkg/ostype"
	"github.com/spf13/viper"
)

// handlePreseedRequest serves a Debian preseed: a DB-resolved config (rungs
// 1–2, kind debianconfig — the only authorable Debian kind, #59) when the
// host is bound, else the --preseedFile server default (rung 4, served
// verbatim via renderPreseedFile — a raw d-i template, not an authored kind).
// Debian has no legacy per-host file column, so rung 3 does not apply.
func handlePreseedRequest(store *db.Store) http.HandlerFunc {
	return preseedHandler(store, nil)
}

// preseedHandler is handlePreseedRequest with the optional node health report
// unit (n nil = feature off, output unchanged). Only a DB-bound debianconfig
// is injected; the raw --preseedFile default never is.
func preseedHandler(store *db.Store, n *NodeReports) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("preseed request", "uri", r.RequestURI)
		host := configHost(identifyHost(r))

		if host != nil {
			// The debian family authors `debianconfig` only (#59: raw `preseed` as
			// an authorable kind was retired). Dispatch renderConfig on the RESOLVED
			// kind (M2) — the guard and the family contract are single-sourced in
			// familyAllowsKind / authoringKindsForFamily.
			if src, kind, ok := resolveConfig(store, host); ok && familyAllowsKind("preseed", kind) {
				vars := preseedVars(store, host)
				healthCmd := ""
				if kind == kindDebianConfig {
					if s, u, ok := n.script(r.Context(), store, host); ok {
						healthCmd = nodereport.DebianLateCommand(s, u)
					}
				}
				out, ct, err := renderPreseed(kind, src, vars, healthCmd)
				if err != nil {
					// The error can quote template values, including the join string (#138).
					writeError(w, http.StatusInternalServerError, "render bound preseed", errors.New(redactJoinString(err.Error(), vars.JoinString)))
					return
				}
				out = withDVDMirror(store, host, vars.ServerIP, out)
				w.Header().Set("Content-Type", ct)
				writeBody(w, out)
				return
			}
		}

		// Rung 4: server-default file.
		path, err := resolveWithinDataDir(viper.GetString(config.PreseedFile))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "preseed template unavailable", err)
			return
		}
		src, err := os.ReadFile(path)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "preseed template unavailable", err)
			return
		}
		vars := preseedVars(store, host)
		out, err := renderPreseedFile(src, vars)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "preseed render failed", err)
			return
		}
		out = withDVDMirror(store, host, vars.ServerIP, out)
		w.Header().Set("Content-Type", "text/plain")
		writeBody(w, out)
	}
}

// renderPreseed is renderConfig, except that a debianconfig with a non-empty
// healthCmd (the node report late_command) also gets that command and curl.
func renderPreseed(kind string, src []byte, vars TemplateVars, healthCmd string) ([]byte, string, error) {
	if kind == kindDebianConfig && healthCmd != "" {
		rendered, rerr := executeTemplate(src, vars)
		if rerr != nil {
			return nil, "", rerr
		}
		out, err := translateDebianConfigWith(rendered, preseedOptions{
			ExtraLateCommand: healthCmd,
			ExtraPackages:    []string{"curl"},
		})
		return out, contentTypeText, err
	}
	out, contentType, _, err := renderConfig(kind, src, vars)
	return out, contentType, err
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
// arch is hardcoded "amd64" and the suite falls back to ostype.DefaultDebianChannel
// absent an assigned channel, mirroring pkg/tftp's bootTokens debian case
// (Task 8): Debian has no config.DebianArchitecture/DebianChannel flag, so
// there is no other per-deployment default to read.
//
// arch is NOT read from host.AssignedArch (#1), matching pkg/tftp's
// bootTokens: the only production writer of that column always passes "" for
// arch, so it is never actually populated — wiring it through here would be
// dead code. Real per-host arch resolution for the assigned-boot path is
// separate multi-arch work.
func debianDVDMirrorDir(store *db.Store, host *hardware.Host) (string, bool) {
	if host == nil || host.OS != "debian" {
		return "", false
	}
	const arch = "amd64"
	params, _ := cache.DecodeParams(host.AssignedParams)
	suite := cmp.Or(params["channel"], ostype.DefaultDebianChannel)

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
		vars.Hostname = hardware.StripControl(host.Hostname)
		vars.MAC = host.MAC
		vars.IP = host.IP
		vars.Roles = roleNames(store, host.MAC)
	}
	return vars
}

// identifyHost resolves the requesting host by ?mac= or ARP (nil when
// unidentified), mirroring the ignition/machineconfig identification block.
func identifyHost(r *http.Request) *hardware.Host {
	return identifyHostWith(r, arpLookup)
}

// identifyHostWith is identifyHost with an injectable ARP lookup. An empty
// ?mac= counts as absent; a non-empty one is resolved by resolveBootHost
// (normalised, then looked up). Any error gives nil.
func identifyHostWith(r *http.Request, arp arpFunc) *hardware.Host {
	h, _, err := resolveBootHost(r, arp)
	if err != nil {
		return nil
	}
	return h
}

// configHost is the host a config route may serve. An unapproved host gets
// the unknown-host answer, because approval is the gate (#158).
func configHost(h *hardware.Host) *hardware.Host {
	if h == nil || !h.Approved {
		return nil
	}
	return h
}

// resolveBootHost is the lookup behind identifyHostWith, and also returns the
// normalised MAC and the lookup error so a caller can tell an unregistered MAC
// (hardware.ErrNotFound) from a malformed one or a store failure. mac is ""
// when the request names no MAC or one that does not parse.
func resolveBootHost(r *http.Request, arp arpFunc) (host *hardware.Host, mac string, err error) {
	raw := r.URL.Query().Get("mac")
	if raw == "" {
		ip, _, serr := net.SplitHostPort(r.RemoteAddr)
		if serr == nil {
			if parsed := net.ParseIP(ip); parsed != nil {
				if hw, aerr := arp(parsed); aerr == nil {
					raw = hw.String()
				}
			}
		}
	}
	if raw == "" {
		return nil, "", nil
	}
	mac, nerr := hardware.NormalizeMAC(raw)
	if nerr != nil {
		return nil, "", nerr
	}
	host, err = hardware.GetMacAddress(mac)
	return host, mac, err
}
