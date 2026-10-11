package tftp

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/j-keck/arping"
	"github.com/pin/tftp/v3"
	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/ostype"
)

// errPathEscapes is returned by renderMenuSelection when a menu selection
// path would resolve outside its cache subtree.
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

// bootScript renders the booty.ipxe script for host and reports which kind of
// boot (holding, assigned, menu) it chose.
func bootScript(host *hardware.Host, urlHost string) (string, string) {
	kind, _ := bootDispatch(host)
	switch kind {
	case "assigned":
		return kind, assignedScript(host, urlHost)
	case "menu":
		var inWindow, archEntries []cache.CacheEntry
		if s := currentStore(); s != nil {
			inWindow, archEntries = cache.PartitionCached(s)
		} else {
			inWindow = cache.ListCached() // no store: everything in-window (safe default)
		}
		return kind, renderMenu(inWindow, archEntries, viper.GetString(config.ServerIP))
	default: // holding
		return kind, holdingScript(urlHost)
	}
}

// lookupHost identifies the requesting host by ARP and loads its record. It
// returns nil when the MAC cannot be resolved or is unknown. A request for
// booty.ipxe from a host flagged DoInstall clears the flag (best effort).
func (s *server) lookupHost(ip net.IP, filename string) *hardware.Host {
	hwAddr, err := s.arp(ip)
	if err != nil {
		//nolint:sloglint // default logger: no injected logger in this package yet
		slog.Warn("error with ARP request", "err", err)
		return nil
	}
	macAddress := hwAddr.String()
	host, lookupErr := hardware.GetMacAddress(macAddress)
	if lookupErr != nil && !errors.Is(lookupErr, hardware.ErrNotFound) {
		//nolint:sloglint // default logger: no injected logger in this package yet
		slog.Warn("TFTP: error looking up host", "mac", macAddress, "err", lookupErr)
	}
	if host != nil && host.DoInstall && filename == "booty.ipxe" {
		modified := *host
		modified.DoInstall = false
		if writeErr := hardware.WriteMacAddress(macAddress, modified); writeErr != nil {
			//nolint:sloglint // default logger: no injected logger in this package yet
			slog.Warn("TFTP: error persisting DoInstall flip", "mac", macAddress, "err", writeErr)
			// Best-effort: continue serving the iPXE script even if
			// the persist failed; the next boot will retry.
		}
	}
	return host
}

// readHandler is called when client starts file download from server
func (s *server) readHandler(filename string, rf io.ReaderFrom) error {
	slog.Info("TFTP get", "file", filename)
	raddr := rf.(tftp.OutgoingTransfer).RemoteAddr()
	laddr := rf.(tftp.RequestPacketInfo).LocalIP()
	slog.Debug("RRQ", "from", raddr.String(), "to", laddr.String())

	host := s.lookupHost(raddr.IP, filename)

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
		kind, toServe := bootScript(host, urlHost)
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
	if served, err := s.serveEmbedded(filename, rf); served {
		return err
	}
	file, err := openDataFile(s.root, filename, s.topLevel)
	if err != nil {
		//nolint:sloglint // default logger: no injected logger in this package yet
		slog.Warn("TFTP rejected", "client", raddr.String(), "requested", filename, "err", err)
		return os.ErrNotExist
	}
	defer file.Close()
	n, err := rf.ReadFrom(file)
	if err != nil {
		return err
	}
	slog.Info("bytes sent", "bytes", n, "file", filename)
	return nil
}

// serveEmbedded sends filename from the embedded boot files. served is false
// when there is no such embedded file, so the caller falls through to dataDir.
func (s *server) serveEmbedded(filename string, rf io.ReaderFrom) (bool, error) {
	if s.bootFiles == nil {
		return false, nil
	}
	b, readErr := fs.ReadFile(s.bootFiles, filename)
	if readErr != nil {
		return false, nil //nolint:nilerr // any read error means "not embedded": fall through to dataDir
	}
	n, sendErr := rf.ReadFrom(bytes.NewReader(b))
	if sendErr != nil {
		return true, sendErr
	}
	//nolint:sloglint // default logger: no injected logger in this package yet
	slog.Info("bytes sent", "bytes", n, "file", filename, "source", "embedded")
	return true, nil
}

// applyTokens replaces every [[token]] in s with its value. Tokens are distinct
// bracketed keys, so replacement order does not matter.
func applyTokens(s string, tokens map[string]string) string {
	for k, v := range tokens {
		s = strings.ReplaceAll(s, k, v)
	}
	return s
}

// holdingScript renders the fully-substituted holding.ipxe script. Single
// source of the holding-script token contract ("[[server]]", "[[server-ip]]")
// shared by readHandler's holding/default arm and assignedScript's
// tool-refusal branch — if that contract changes, both call sites must change
// together.
func holdingScript(urlHost string) string {
	return applyTokens(PXEConfig["holding.ipxe"], map[string]string{
		"[[server]]":    urlHost,
		"[[server-ip]]": viper.GetString(config.ServerIP),
	})
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
	case "debian":
		tokens["[[debian-arch]]"] = arch
		tokens["[[debian-baseurl]]"] = debianBaseURL(urlHost, segment, arch, version, debianSourceMode(arch, segment))
	default:
		// Tool OSes (family "tool") share one generic token set: they all resolve
		// to the same cache URL base and carry no per-OS template vocabulary.
		// Guarded by isToolOS so an unknown/typo'd OS still yields the previous
		// empty result.
		if isToolOS(osToLoad) {
			tokens["[[baseurl]]"] = "http://" + cache.CacheURLBase(urlHost, osToLoad, segment, arch, version)
			tokens["[[version]]"] = version
			tokens["[[arch]]"] = arch
			tokens["[[server-ip]]"] = viper.GetString(config.ServerIP)
		}
	}
	return tokens
}

// isToolOS reports whether an on-disk cache name belongs to the config-less
// "tool" family.
func isToolOS(cacheName string) bool {
	o, ok := ostype.Lookup(cache.CacheNameToCanonical(cacheName))
	return ok && o.Family().Name == "tool"
}

// debianBaseURL is the single source of the Debian boot base URL: the cached
// version dir for netinst, plus install.<arch>/ for dvd (where the DVD tree
// keeps the installer kernel/initrd). segment is the channel (suite).
func debianBaseURL(urlHost, segment, arch, version, sourceMode string) string {
	base := "http://" + cache.CacheURLBase(urlHost, "debian", segment, arch, version)
	if sourceMode == "dvd" {
		return base + "/install." + arch
	}
	return base
}

// debianSourceMode resolves a Debian target's effective serving mode (default
// netinst if unresolved). segment is the channel (suite).
func debianSourceMode(arch, segment string) string {
	store := currentStore()
	if store == nil {
		return "netinst"
	}
	params, _ := cache.EncodeParams(map[string]string{"channel": segment})
	t, err := store.GetTargetByIdentity("debian", arch, params)
	if err != nil || t == nil || t.SourceMode == "" {
		return "netinst"
	}
	return t.SourceMode
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

// clusterPinnedTalosVersion returns the pinned talos version for a cluster
// member (host.cluster_id set), looked up via the shared store. ok=false for a
// non-member, a nil store, or any lookup failure — the caller then falls back
// to NewestCached. A lookup failure is logged and tolerated (boot must not be
// blocked by a transient DB issue; the install image still carries the pin).
func clusterPinnedTalosVersion(host *hardware.Host) (string, bool) {
	if host == nil || host.ClusterID == nil {
		return "", false
	}
	store := currentStore()
	if store == nil {
		return "", false
	}
	c, err := store.GetCluster(*host.ClusterID)
	if err != nil {
		slog.Warn("tftp: cluster version lookup failed; falling back to newest cached",
			"mac", host.MAC, "cluster", *host.ClusterID, "err", err)
		return "", false
	}
	return c.TalosVersion, true
}

// bootTokens builds the per-request substitution map for the ASSIGNED/legacy
// boot path. Each OS resolves its path-discriminating variant the same way:
// host override, else flag — schematic for talos (typed column), channel for
// flatcar/coreos (AssignedParams) — then serves the newest cached version
// under that segment. debian has no per-deployment config flag (catalog-
// declared, potentially multiple channels/arches); it falls back to a
// literal default channel/arch instead — see the "debian" case below.
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
		// I1 Option A: a cluster member boots its cluster's PINNED talos version,
		// so the boot kernel/initramfs align with the frozen install image.
		// Non-members (and members whose cluster lookup fails) fall back to the
		// newest cached version — at the TFTP layer boot availability beats pin
		// fidelity, and the frozen machineconfig still pins the install image.
		version, pinned := clusterPinnedTalosVersion(host)
		if !pinned {
			// Empty when nothing is cached yet (pre-first-sync) → BASEURL 404s, same
			// failure mode as the other OSes before their first version check.
			version = cache.NewestCached("talos", arch, map[string]string{"schematic": schematic})
		}
		return bootTokensFor("talos", schematic, arch, version, urlHost)
	case "debian":
		// Debian has no config.DebianChannel/DebianArchitecture flag (unlike the
		// other OSes): targets are catalog-declared, potentially multiple
		// channels/arches at once (#1/#3), so there is no single per-deployment
		// default to read from viper. Fall back to ostype.DefaultDebianChannel
		// (arch stays a literal "amd64", see below) when the host has no
		// assigned channel.
		suite := cmp.Or(hostParams(host)["channel"], ostype.DefaultDebianChannel)
		// arch is hardcoded, NOT read from host.AssignedArch (#1): the only
		// production writer of that column (POST /hosts/{mac}/approve,
		// pkg/http/api_hosts.go) always passes "" for arch, so the field is
		// never actually populated — reading it here would be dead code, not
		// a real fix. The menu boot path (renderMenuSelection/bootTokensFor)
		// DOES serve arm64 correctly, since it carries arch through the
		// parsed menu-selection tuple instead of this per-OS default; only
		// the assigned-boot path is amd64-only. Wiring real per-host arch
		// (populating AssignedArch on approve/assign and reading it here) is
		// separate multi-arch work, not a one-line fix.
		arch := "amd64"
		version := cache.NewestCached("debian", arch, map[string]string{"channel": suite})
		return bootTokensFor("debian", suite, arch, version, urlHost)
	}
	// unknown os: only the shared [[server]] token (identical to the old fall-through).
	return bootTokensFor(osToLoad, "", "", "", urlHost)
}

// assignedScript renders the boot script for an approved, assignment-mode host.
// A tool OS is refused: tools have no per-host config and are reached through
// menu mode. Serving the holding script is the safe fallback — the alternative
// is a script with unsubstituted tokens, which design §6.4 forbids.
func assignedScript(host *hardware.Host, urlHost string) string {
	_, osToLoad := bootDispatch(host)
	if isToolOS(osToLoad) {
		slog.Warn("TFTP: refusing to boot a tool OS via assignment; use boot_mode=menu",
			"os", osToLoad, "mac", host.MAC)
		return holdingScript(urlHost)
	}
	return applyTokens(PXEConfig[fmt.Sprintf("%s.ipxe", osToLoad)], bootTokens(osToLoad, urlHost, host))
}

const (
	blockSize       = 512
	transferTimeout = 60 * time.Second // v3 defaults to 5 s; slow PXE clients need longer
)

// Config is everything Start needs. Port is used as given; 0 binds an
// ephemeral port (tests). cmd always passes --tftpPort (default 69).
type Config struct {
	Port              int
	DataDir           string
	TopLevelBootFiles []string
	BootFiles         fs.FS                                  // embedded iPXE files served first; nil = none
	ARP               func(net.IP) (net.HardwareAddr, error) // nil means arping.Ping
}

// server owns the state readHandler needs for one running TFTP server.
type server struct {
	root      *os.Root
	topLevel  []string
	bootFiles fs.FS
	arp       func(net.IP) (net.HardwareAddr, error)
}

// Server is a running TFTP server.
type Server struct {
	srv  *tftp.Server
	conn net.PacketConn
	root *os.Root
	once sync.Once
}

// Start opens dataDir, binds the UDP port and serves TFTP in a background
// goroutine. Bind errors are returned; serve errors are sent on errCh. The
// server is read-only: it has no write handler.
func Start(cfg Config, errCh chan<- error) (*Server, error) {
	root, err := os.OpenRoot(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("tftp: open dataDir: %w", err)
	}
	arp := cfg.ARP
	if arp == nil {
		arp = func(ip net.IP) (net.HardwareAddr, error) {
			hw, _, pingErr := arping.Ping(ip)
			return hw, pingErr
		}
	}
	h := &server{root: root, topLevel: cfg.TopLevelBootFiles, bootFiles: cfg.BootFiles, arp: arp}

	// A nil write handler makes the library answer WRQ with an error.
	srv := tftp.NewServer(h.readHandler, nil)
	srv.SetBlockSize(blockSize)
	srv.EnableSinglePort()
	srv.SetTimeout(transferTimeout)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.Port})
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("tftp: listen on UDP/%d: %w", cfg.Port, err)
	}
	go func() {
		if serveErr := srv.Serve(conn); serveErr != nil {
			errCh <- fmt.Errorf("tftp: %w", serveErr)
		}
	}()
	//nolint:sloglint // default logger: no injected logger in this package yet
	slog.Info("TFTP server started", "addr", conn.LocalAddr().String())
	return &Server{srv: srv, conn: conn, root: root}, nil
}

// Addr returns the bound local address.
func (s *Server) Addr() net.Addr { return s.conn.LocalAddr() }

// Shutdown stops serving, closes the socket and the root. It is idempotent.
// In single-port mode the library's Shutdown only cancels and sets a read
// deadline, so the socket is closed here.
func (s *Server) Shutdown() {
	s.once.Do(func() {
		s.srv.Shutdown()
		_ = s.conn.Close()
		_ = s.root.Close()
	})
}
