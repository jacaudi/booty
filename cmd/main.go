package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	bootyHTTP "github.com/jeefy/booty/pkg/http"
	"github.com/jeefy/booty/pkg/proxydhcp"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/jeefy/booty/pkg/versions"
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
	maxCacheAge         int
	cronSchedule        string
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
	flags.StringVar(
		&args.cronSchedule,
		"updateSchedule",
		"*/5 * * * *",
		"Cron schedule to use for cleaning up cache files",
	)

	flags.StringVar(
		&args.dataDir,
		"dataDir",
		"/data",
		"Directory to store stateful data",
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
		80,
		"Alternative HTTP port to use for clients",
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

	Cmd.AddCommand(newVersionCmd())

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

func run(cmd *cobra.Command, argv []string) error {
	setupLogging(viper.GetBool(config.Debug))
	slog.Info("Starting Booty!")
	config.LoadConfig(cmd)

	// Open the authoritative SQLite store (fail-fast: pragmas + migrations run
	// here) and share it with the host store before Load runs its one-time
	// hardware.json import.
	store, err := db.Open(config.DatabasePathValue())
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer store.Close()
	hardware.SetStore(store)

	if err := hardware.Load(); err != nil {
		return fmt.Errorf("hardware: %w", err)
	}

	// Take ownership of the process lifecycle: a single signal context drives an
	// ordered graceful shutdown of every subsystem started below.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	versions.FlatcarVersionCheck()
	versions.CoreOSVersionCheck()
	versions.TalosSync()

	flatcarCron := versions.StartFlatcarCron()
	coreOSCron := versions.StartCoreOSCron()
	talosCron := versions.StartTalosCron()

	tftpServer := tftp.StartTFTP()

	// Start the HTTP server (non-blocking; returns the running server).
	httpServer := bootyHTTP.StartHTTP()

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
		shutdownStep{name: "flatcar-cron", stop: flatcarCron.Stop},
		shutdownStep{name: "coreos-cron", stop: coreOSCron.Stop},
		shutdownStep{name: "talos-cron", stop: talosCron.Stop},
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
