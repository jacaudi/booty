package tftp

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/pin/tftp"
	"github.com/spf13/viper"
)

// absDataDir is the absolute, cleaned form of viper's DataDir, resolved once
// in StartTFTP. safeJoin reads it; do not mutate after StartTFTP returns.
var absDataDir string

// errPathEscapes is returned by safeJoin when the requested path resolves
// outside absDataDir (e.g. via "..", absolute paths, or sneaky combinations).
var errPathEscapes = errors.New("tftp: path escapes dataDir")

// storeMu guards the package-level store handle. Mirrors hardware.SetStore/
// withRLockedStore discipline exactly: set once at startup from cmd/main.go,
// read under RLock for each request. Never opens a second DB handle.
var (
	storeMu sync.RWMutex
	tftpDB  *db.Store
)

// SetStore injects the shared DB store the menu path uses to partition cached
// versions into in-window vs archived. Wired once at startup from cmd/main.go,
// mirroring hardware.SetStore. Safe if nil (menu then treats all as in-window).
func SetStore(s *db.Store) {
	storeMu.Lock()
	defer storeMu.Unlock()
	tftpDB = s
}

func currentStore() *db.Store {
	storeMu.RLock()
	defer storeMu.RUnlock()
	return tftpDB
}

// safeJoin resolves requested against absDataDir and returns an absolute,
// cleaned path under absDataDir, or errPathEscapes if the result would lie
// outside the root.
//
// Note: this does not call filepath.EvalSymlinks. If absDataDir contains a
// symlink whose target is outside the directory, safeJoin will not detect it.
// TFTP is read-only and the operator controls dataDir contents; this is an
// acceptable limitation.
func safeJoin(requested string) (string, error) {
	if absDataDir == "" {
		return "", errors.New("tftp: absDataDir not initialized")
	}
	// Reject absolute-path requests as a security policy: TFTP clients must
	// not be able to address files by absolute path, even though
	// filepath.Join would in practice keep the result under absDataDir
	// (Join("/dataDir", "/etc/passwd") returns "/dataDir/etc/passwd").
	if filepath.IsAbs(requested) {
		return "", errPathEscapes
	}
	joined := filepath.Join(absDataDir, requested)
	cleaned := filepath.Clean(joined)
	if cleaned != absDataDir &&
		!strings.HasPrefix(cleaned, absDataDir+string(filepath.Separator)) {
		return "", errPathEscapes
	}
	return cleaned, nil
}

// bootDispatch is the pure host-state -> boot-decision function (design §2.5).
// It returns the kind of boot to serve and, for "assigned", the OS to load.
//   - no host / unapproved        -> "holding"
//   - approved + assigned         -> "assigned", assigned OS (host.OS fallback)
//   - approved + menu             -> "menu"
//   - approved + unknown/empty    -> "holding"
func bootDispatch(host *hardware.Host) (kind, osToLoad string) {
	if host == nil || !host.Approved {
		return "holding", ""
	}
	if host.BootMode == "assigned" {
		osToLoad := host.AssignedOS
		if osToLoad == "" {
			osToLoad = host.OS
		}
		return "assigned", osToLoad
	}
	if host.BootMode == "menu" {
		return "menu", ""
	}
	return "holding", ""
}

// readHandler is called when client starts file download from server
func readHandler(filename string, rf io.ReaderFrom) error {
	slog.Info("TFTP get", "file", filename)
	raddr := rf.(tftp.OutgoingTransfer).RemoteAddr()
	laddr := rf.(tftp.RequestPacketInfo).LocalIP()
	slog.Debug("RRQ", "from", raddr.String(), "to", laddr.String())

	var host *hardware.Host
	if hwAddr, _, err := arping.Ping(raddr.IP); err != nil {
		slog.Warn("error with ARP request", "err", err)
	} else {
		macAddress := hwAddr.String()
		var lookupErr error
		host, lookupErr = hardware.GetMacAddress(macAddress)
		if lookupErr != nil && !errors.Is(lookupErr, hardware.ErrNotFound) {
			slog.Warn("TFTP: error looking up host", "mac", macAddress, "err", lookupErr)
		}
		if host != nil {
			if host.DoInstall && filename == "booty.ipxe" {
				modified := *host
				modified.DoInstall = false
				if err := hardware.WriteMacAddress(macAddress, modified); err != nil {
					slog.Warn("TFTP: error persisting DoInstall flip", "mac", macAddress, "err", err)
					// Best-effort: continue serving the iPXE script even if
					// the persist failed; the next boot will retry.
				}
			}
		}
	}

	urlHost := viper.GetString(config.ServerIP)
	hostPort := viper.GetInt(config.ServerHttpPort)
	if hostPort != 80 {
		urlHost = fmt.Sprintf("%s:%d", urlHost, hostPort)
	}

	if strings.HasPrefix(filename, "menu/") && strings.HasSuffix(filename, "/boot.ipxe") {
		toServe := menuSelectionScript(host, filename, urlHost)
		r := strings.NewReader(toServe)
		n, rerr := rf.ReadFrom(r)
		if rerr != nil {
			slog.Warn("TFTP: error sending menu selection response", "err", rerr)
			return rerr
		}
		slog.Info("bytes sent", "bytes", n, "file", filename, "kind", "menu-selection")
		return nil
	}

	if filename == "booty.ipxe" {
		kind, osToLoad := bootDispatch(host)
		var toServe string
		switch kind {
		case "assigned":
			toServe = applyTokens(PXEConfig[fmt.Sprintf("%s.ipxe", osToLoad)], bootTokens(osToLoad, urlHost, host))
		case "menu":
			var inWindow, archEntries []cache.CacheEntry
			if s := currentStore(); s != nil {
				inWindow, archEntries = cache.PartitionCached(s)
			} else {
				inWindow = cache.ListCached() // no store: everything in-window (safe default)
			}
			toServe = renderMenu(inWindow, archEntries, viper.GetString(config.ServerIP))
		default: // holding
			toServe = applyTokens(PXEConfig["holding.ipxe"], map[string]string{
				"[[server]]":    urlHost,
				"[[server-ip]]": viper.GetString(config.ServerIP),
			})
		}
		r := strings.NewReader(toServe)
		n, err := rf.ReadFrom(r)
		if err != nil {
			slog.Warn("error reading iPXE config", "err", err)
			return err
		}
		slog.Info("bytes sent", "bytes", n, "file", filename, "kind", kind)
		return nil
	}

	if filename == "pxelinux.cfg/default" {
		// pxelinux.cfg/default — legacy syslinux path; selection preserved verbatim.
		pxeOS := "flatcar"
		if host != nil && host.OS != "" {
			pxeOS = host.OS
		}
		r := strings.NewReader(applyTokens(PXEConfig[pxeOS], map[string]string{"[[server]]": urlHost}))
		n, err := rf.ReadFrom(r)
		if err != nil {
			slog.Warn("error reading PXE config", "err", err)
			return err
		}
		slog.Info("bytes sent", "bytes", n, "file", filename)
		return nil
	}
	path, err := safeJoin(filename)
	if err != nil {
		if errors.Is(err, errPathEscapes) {
			slog.Warn("TFTP rejected: path escapes dataDir", "client", raddr.String(), "requested", filename)
		}
		return os.ErrNotExist
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	n, err := rf.ReadFrom(file)
	if err != nil {
		return err
	}
	slog.Info("bytes sent", "bytes", n, "file", filename)
	return nil
}

// applyTokens replaces every [[token]] in s with its value. Tokens are distinct
// bracketed keys, so replacement order does not matter.
func applyTokens(s string, tokens map[string]string) string {
	for k, v := range tokens {
		s = strings.ReplaceAll(s, k, v)
	}
	return s
}

// bootTokensFor builds the [[token]] substitution map for one fully-specified
// (osToLoad, segment, arch, version) tuple. osToLoad is the ON-DISK os name
// (flatcar|coreos|talos). segment is the path-discriminating cache segment —
// the talos schematic or the flatcar/coreos channel — the same value carried
// in the menu selection path's tuple, so no translation is needed here.
func bootTokensFor(osToLoad, segment, arch, version, urlHost string) map[string]string {
	tokens := map[string]string{
		"[[server]]": urlHost,
	}
	switch osToLoad {
	case "coreos":
		tokens["[[coreos-arch]]"] = arch
		tokens["[[coreos-version]]"] = version
		tokens["[[coreos-baseurl]]"] = "http://" + cache.CacheURLBase(urlHost, "coreos", segment, arch, version)
	case "flatcar":
		tokens["[[flatcar-arch]]"] = arch
		tokens["[[flatcar-version]]"] = version
		tokens["[[flatcar-baseurl]]"] = "http://" + cache.CacheURLBase(urlHost, "flatcar", segment, arch, version)
	case "talos":
		tokens["[[talos-schematic]]"] = segment
		tokens["[[talos-arch]]"] = arch
		tokens["[[talos-version]]"] = version
		tokens["[[talos-baseurl]]"] = "http://" + cache.CacheURLBase(urlHost, "talos", segment, arch, version)
	}
	return tokens
}

// hostParams decodes host.AssignedParams (the P1c field; canonical JSON set by
// the host API). nil host, empty field, or malformed JSON all yield an empty
// map — the boot path then falls back to the flag defaults.
// Noted asymmetry (#48 SGE #5): talos resolves its variant from the typed
// host.Schematic column; flatcar/coreos read this JSON. Deliberately NOT
// unified in #48.
func hostParams(host *hardware.Host) map[string]string {
	if host == nil || host.AssignedParams == "" {
		return map[string]string{}
	}
	p, err := cache.DecodeParams(host.AssignedParams)
	if err != nil {
		slog.Warn("tftp: ignoring malformed assignedParams", "err", err)
		return map[string]string{}
	}
	return p
}

// bootTokens builds the per-request substitution map for the ASSIGNED/legacy
// boot path. Each OS resolves its path-discriminating variant the same way:
// host override, else flag — schematic for talos (typed column), channel for
// flatcar/coreos (AssignedParams) — then serves the newest cached version
// under that segment.
func bootTokens(osToLoad, urlHost string, host *hardware.Host) map[string]string {
	switch osToLoad {
	case "coreos":
		channel := cmp.Or(hostParams(host)["channel"], viper.GetString(config.CoreOSChannel))
		arch := viper.GetString(config.CoreOSArchitecture)
		version := cache.NewestCached("coreos", arch, map[string]string{"channel": channel})
		return bootTokensFor("coreos", channel, arch, version, urlHost)
	case "flatcar":
		channel := cmp.Or(hostParams(host)["channel"], viper.GetString(config.FlatcarChannel))
		arch := viper.GetString(config.FlatcarArchitecture)
		version := cache.NewestCached("flatcar", arch, map[string]string{"channel": channel})
		return bootTokensFor("flatcar", channel, arch, version, urlHost)
	case "talos":
		schematic := viper.GetString(config.TalosSchematic)
		if host != nil && host.Schematic != "" {
			schematic = host.Schematic
		}
		arch := viper.GetString(config.TalosArchitecture)
		// Empty when nothing is cached yet (pre-first-sync) → BASEURL 404s, same
		// failure mode as the other OSes before their first version check.
		version := cache.NewestCached("talos", arch, map[string]string{"schematic": schematic})
		return bootTokensFor("talos", schematic, arch, version, urlHost)
	}
	// unknown os: only the shared [[server]] token (identical to the old fall-through).
	return bootTokensFor(osToLoad, "", "", "", urlHost)
}

// writeHandler is called when client starts file upload to server
func writeHandler(filename string, wt io.WriterTo) error {
	slog.Info("TFTP writes are not supported", "file", filename)
	return nil
}

// StartTFTP starts the TFTP server in a background goroutine and returns it so
// the caller can Shutdown() it during graceful shutdown. The returned server's
// Shutdown drains outstanding transfers before stopping the listener.
func StartTFTP() *tftp.Server {
	resolved, err := filepath.Abs(viper.GetString(config.DataDir))
	if err != nil {
		slog.Error("TFTP: failed to resolve dataDir", "err", err)
		os.Exit(1)
	}
	absDataDir = resolved

	// use nil in place of handler to disable read or write operations
	s := tftp.NewServer(readHandler, writeHandler)
	s.SetBlockSize(512)
	s.EnableSinglePort()
	s.SetTimeout(60 * time.Second) // optional
	go func() {
		err := s.ListenAndServe(":69") // blocks until s.Shutdown() is called
		if err != nil {
			slog.Error("TFTP server error", "err", err)
			os.Exit(1)
		}
	}()
	slog.Info("TFTP server started")
	return s
}
