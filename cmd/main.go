package main

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	bootyHTTP "github.com/jeefy/booty/pkg/http"
	"github.com/jeefy/booty/pkg/proxydhcp"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var Cmd = &cobra.Command{
	Use:  "booty",
	Long: "Easy iPXE server for Flatcar, CoreOS, and more",
	RunE: run,
}

var args struct {
	debug               bool
	dataDir             string
	catalogFile         string
	maxCacheAge         int
	cacheInterval       time.Duration
	cacheConcurrency    int
	cacheMaxBytes       int64
	httpPort            int
	flatcarArchitecture string
	coreOSArchitecture  string
	serverIP            string
	serverHttpPort      int
	joinString          string
	flatcarChannel      string
	coreOSChannel       string

	proxyDHCPEnabled       bool
	proxyDHCPBootfileBIOS  string
	proxyDHCPBootfileUEFI  string
	proxyDHCPBootfileARM64 string

	talosArchitecture string
	talosSchematic    string
	talosRetainMinors int
	talosConfigFile   string
	talosFactoryURL   string

	preseedFile string

	configRevisionsKeep int

	signaturePolicy string

	secretsKey string
}

var (
	version   string
	timestamp string
)

func init() {
	flags := Cmd.Flags()

	flags.IntVar(
		&args.httpPort,
		"httpPort",
		8080,
		"Port to use for the HTTP server",
	)
	flags.BoolVar(
		&args.debug,
		"debug",
		false,
		"Enable debug logging",
	)
	flags.DurationVar(
		&args.cacheInterval,
		"cacheInterval",
		5*time.Minute,
		"Interval between cache reconcile passes (discovery refresh)",
	)
	flags.IntVar(
		&args.cacheConcurrency,
		"cacheConcurrency",
		4,
		"Max concurrent artifact downloads during cache reconcile",
	)
	flags.Int64Var(
		&args.cacheMaxBytes,
		"cacheMaxBytes",
		0,
		"Max cache size in bytes before evicting oldest archived-unpinned versions (0 = unlimited)",
	)

	flags.StringVar(
		&args.signaturePolicy,
		"signaturePolicy",
		"warn",
		"Signature policy: strict (reject unverified), warn (block tampering, allow+log other verify failures), off (no verification)",
	)

	flags.StringVar(
		&args.secretsKey,
		"secretsKey",
		"",
		"Path to an age identity file (age-keygen output) encrypting Talos cluster secrets at rest; unset disables cluster create/import/generation (fail-closed)",
	)

	flags.StringVar(
		&args.dataDir,
		"dataDir",
		"/data",
		"Directory to store stateful data",
	)

	flags.StringVar(
		&args.catalogFile,
		"catalogFile",
		"",
		"Declarative cache-target catalog (YAML); default <dataDir>/catalog.yaml, embedded defaults if absent",
	)

	flags.StringVar(
		&args.flatcarArchitecture,
		"flatcarArchitecture",
		"amd64",
		"Architecture to use for the Flatcar downloads",
	)
	flags.StringVar(
		&args.coreOSArchitecture,
		"coreOSArchitecture",
		"x86_64",
		"Architecture to use for CoreOS downloads",
	)

	flags.StringVar(
		&args.flatcarChannel,
		"flatcarChannel",
		"stable",
		"Flatcar channel to look for updates",
	)

	flags.StringVar(
		&args.coreOSChannel,
		"coreOSChannel",
		"stable",
		"CoreOS channel to look for updates",
	)

	flags.StringVar(
		&args.serverIP,
		"serverIP",
		"127.0.0.1",
		"IP address that clients can connect to",
	)
	flags.IntVar(
		&args.serverHttpPort,
		"serverHttpPort",
		0,
		"Client-facing HTTP port advertised in boot-config URLs; defaults to --httpPort when unset",
	)

	flags.StringVar(
		&args.joinString,
		"joinString",
		"",
		"The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH",
	)

	flags.BoolVar(
		&args.proxyDHCPEnabled,
		"proxyDHCPEnabled",
		false,
		"Enable the proxyDHCP responder (PXEClient-only, assigns no leases; UDP/67 needs CAP_NET_BIND_SERVICE)",
	)
	flags.StringVar(
		&args.proxyDHCPBootfileBIOS,
		"proxyDHCPBootfileBIOS",
		"undionly.kpxe",
		"proxyDHCP pass-1 BIOS iPXE binary (staged in dataDir)",
	)
	flags.StringVar(
		&args.proxyDHCPBootfileUEFI,
		"proxyDHCPBootfileUEFI",
		"ipxe.efi",
		"proxyDHCP pass-1 UEFI iPXE binary (staged in dataDir)",
	)
	flags.StringVar(
		&args.proxyDHCPBootfileARM64,
		"proxyDHCPBootfileARM64",
		"ipxe-arm64.efi",
		"proxyDHCP pass-1 ARM64 iPXE binary (staged in dataDir)",
	)

	flags.StringVar(
		&args.talosArchitecture,
		"talosArchitecture",
		"amd64",
		"Architecture token for Talos artifacts (amd64/arm64)",
	)
	flags.StringVar(
		&args.talosSchematic,
		"talosSchematic",
		"376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba",
		"Default Talos Image Factory schematic ID",
	)
	flags.IntVar(
		&args.talosRetainMinors,
		"talosRetainMinors",
		3,
		"Number of newest Talos minor lines to cache",
	)
	flags.StringVar(
		&args.talosConfigFile,
		"talosConfigFile",
		"config/machineconfig.yaml",
		"Talos machineconfig template (relative to dataDir)",
	)
	flags.StringVar(
		&args.talosFactoryURL,
		"talosFactoryURL",
		"https://factory.talos.dev",
		"Talos Image Factory base URL (private-factory override)",
	)

	flags.StringVar(
		&args.preseedFile,
		"preseedFile",
		"config/preseed.cfg",
		"Debian preseed template served at /preseed (relative to dataDir)",
	)

	flags.IntVar(
		&args.configRevisionsKeep,
		"configRevisionsKeep",
		10,
		"Number of newest config revisions to retain per config (the active revision is always kept)",
	)

	Cmd.AddCommand(newVersionCmd())
	Cmd.AddCommand(newConvertPreseedCmd())

	viper.BindPFlags(flags)

	viper.SetDefault("version", "dev")
	if version != "" {
		viper.Set("version", version)
	}
	viper.SetDefault("timestamp", time.Now().Format("2006-01-02 15:04:05.000000"))
	if timestamp != "" {
		viper.Set("timestamp", timestamp)
	}
}

func main() {
	if err := Cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(0)
}

// resolveServerHTTPPort returns the client-facing port to advertise in
// boot-config URLs. serverHTTPPort=0 means "unset": advertise the listen
// port (httpPort) so a single-process, no-proxy deploy advertises the same
// port it listens on. A non-zero serverHTTPPort is an explicit operator
// choice (proxy/LB-fronted deploys where the client-facing port differs from
// the listen port) and always wins.
func resolveServerHTTPPort(serverHTTPPort, httpPort int) int {
	return cmp.Or(serverHTTPPort, httpPort)
}

func run(cmd *cobra.Command, argv []string) error {
	setupLogging(viper.GetBool(config.Debug))
	slog.Info("Starting Booty!")
	config.LoadConfig(cmd)

	if err := config.ValidateSignaturePolicy(); err != nil {
		return err
	}

	if err := config.ValidateSecretsKey(); err != nil {
		return err
	}

	// Resolve the client-facing port before anything reads ServerHttpPort
	// (TFTP boot-config URLs, HTTP ignition/machineconfig/preseed templates).
	// viper.Set takes precedence over the bound flag default, so every
	// downstream viper.Get*(config.ServerHttpPort) read sees the resolved
	// value unchanged.
	viper.Set(config.ServerHttpPort, resolveServerHTTPPort(viper.GetInt(config.ServerHttpPort), viper.GetInt(config.HttpPort)))

	// Open the authoritative SQLite store (fail-fast: pragmas + migrations run
	// here) and share it with the host store before Load runs its one-time
	// hardware.json import.
	store, err := db.Open(config.DatabasePathValue())
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer store.Close()
	hardware.SetStore(store)
	tftp.SetStore(store)

	// A cluster's frozen machineconfigs can only be decrypted/served with the age
	// key. Starting without --secretsKey when clusters already exist is allowed
	// (secrets ops fail closed per-operation, not at startup), but every member's
	// /machineconfig will 500 — warn so a silent fleet outage is diagnosable (M5).
	if viper.GetString(config.SecretsKey) == "" {
		if cs, cerr := store.ListClusters(); cerr == nil && len(cs) > 0 {
			slog.Warn("clusters exist but --secretsKey is unset; members' machineconfigs will fail to serve until it is provided", "clusters", len(cs))
		}
	}

	if err := hardware.Load(); err != nil {
		return fmt.Errorf("hardware: %w", err)
	}
	if n, err := hardware.PreserveBoot(); err != nil {
		slog.Warn("hardware: preserve existing host boot failed", "err", err)
	} else if n > 0 {
		slog.Info("hardware: preserved boot for pre-existing hosts", "count", n)
	}

	// One-time #48 migration: rewrite pre-channel target rows and rename
	// <os>/- cache dirs to the flag channel. Must run before the reconciler's
	// first pass — the catalog-apply pass (create-if-absent) would otherwise
	// mint a NEW channel row next to the un-migrated "{}" one. Fail-fast: a
	// malformed channel flag aborts startup.
	if err := cache.MigrateChannelLayout(store); err != nil {
		return fmt.Errorf("cache migrate: %w", err)
	}

	// Load the declarative cache-target catalog (fail-fast): an operator
	// catalog.yaml if present, else the flag-derived default set. This reads
	// only files/flags (no DB access); it runs before NewReconciler so the
	// desired set is available to the reconciler's applyCatalog pass.
	catalog, err := cache.LoadCatalog()
	if err != nil {
		return fmt.Errorf("cache: load catalog: %w", err)
	}

	// P5: the registry always shows the Factory's vanilla schematic. Seeded by
	// its known constant ID — never a Factory call at startup (SGE I4).
	if err := bootyHTTP.SeedVanillaSchematic(store); err != nil {
		return fmt.Errorf("seed vanilla schematic: %w", err)
	}

	// Take ownership of the process lifecycle: a single signal context drives an
	// ordered graceful shutdown of every subsystem started below.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Single cache reconciler replaces the per-OS version-check crons. Start it
	// after the host store is loaded; it owns all target/version DB writes and
	// eager artifact caching. Serving (TFTP/HTTP below) may begin immediately —
	// boots before the first reconcile 404 the same way they did pre-first-sync.
	reconciler := cache.NewReconciler(
		store,
		viper.GetDuration(config.CacheInterval),
		viper.GetInt(config.CacheConcurrency),
		catalog,
	)
	reconciler.Start(ctx)

	tftpServer := tftp.StartTFTP()

	// Start the HTTP server (non-blocking; returns the running server).
	httpServer := bootyHTTP.StartHTTP(bootyHTTP.APIDeps{
		Store:   store,
		Trigger: reconciler.Trigger,
	})

	// Start the proxyDHCP responder when enabled. Best-effort: nil when not
	// started, in which case the shutdown step is skipped below.
	proxyDHCPServer := startProxyDHCP()

	slog.Info("Booty started")

	// Block until a signal arrives, then shut down in order:
	// stop proxyDHCP (if running) -> drain HTTP -> stop schedulers -> stop TFTP.
	<-ctx.Done()
	slog.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Prepend the proxyDHCP step (when running) so the responder stops
	// answering before the rest of the stack drains.
	steps := []shutdownStep{}
	if proxyDHCPServer != nil {
		steps = append(steps, shutdownStep{name: "proxydhcp", stop: proxyDHCPServer.Shutdown})
	}
	steps = append(steps,
		shutdownStep{name: "http", stop: func() {
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				slog.Error("HTTP shutdown failed", "err", err)
			}
		}},
		shutdownStep{name: "reconciler", stop: reconciler.Stop},
		// Bound the TFTP stop: in single-port mode pin/tftp's Shutdown() does
		// not close the listening socket and the serve loop blocks on ReadFrom
		// with no read deadline, so Shutdown() can hang until the next packet.
		// Cap the wait so run() returns within a bounded window regardless.
		shutdownStep{name: "tftp", stop: func() {
			if !stopWithTimeout(tftpServer.Shutdown, 5*time.Second) {
				slog.Warn("TFTP shutdown timed out; exiting anyway", "timeout", 5*time.Second)
			}
		}},
	)
	shutdown(slog.Default(), steps)

	slog.Info("Booty stopped")
	return nil
}

// startProxyDHCP starts the proxyDHCP responder when enabled. It is
// best-effort: any misconfiguration or bind failure is logged and nil is
// returned so booty keeps running (an opt-in subsystem must never crash a
// working deployment). Returns the running server, or nil if not started.
func startProxyDHCP() *proxydhcp.Server {
	if !viper.GetBool(config.ProxyDHCPEnabled) {
		return nil
	}
	ip := net.ParseIP(viper.GetString(config.ServerIP))
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		slog.Error("proxyDHCP enabled but serverIP is not a usable LAN address; not starting",
			"serverIP", viper.GetString(config.ServerIP))
		return nil
	}
	srv, err := proxydhcp.NewServer(proxydhcp.Config{
		ServerIP:      ip,
		BootfileBIOS:  viper.GetString(config.ProxyDHCPBootfileBIOS),
		BootfileUEFI:  viper.GetString(config.ProxyDHCPBootfileUEFI),
		BootfileARM64: viper.GetString(config.ProxyDHCPBootfileARM64),
	})
	if err != nil {
		slog.Error("proxyDHCP construct failed; not starting", "err", err)
		return nil
	}
	if err := srv.Start(); err != nil {
		slog.Error("proxyDHCP start failed; continuing without it", "err", err)
		return nil
	}
	return srv
}
