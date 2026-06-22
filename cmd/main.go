package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	bootyHTTP "github.com/jeefy/booty/pkg/http"
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

	if err := hardware.Load(); err != nil {
		return fmt.Errorf("hardware: %w", err)
	}

	// Take ownership of the process lifecycle: a single signal context drives an
	// ordered graceful shutdown of every subsystem started below.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	versions.FlatcarVersionCheck()
	versions.CoreOSVersionCheck()

	flatcarCron := versions.StartFlatcarCron()
	coreOSCron := versions.StartCoreOSCron()

	// The OCI image sync pre-syncs once before starting its scheduler, after a
	// delay so the HTTP registry is up. The scheduler is created late, so hand
	// it back over a buffered channel; skip starting it if we are already
	// shutting down so we never leak a scheduler past shutdown.
	ostreeCronCh := make(chan *gocron.Scheduler, 1)
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}

		// Pre-sync the images
		versions.OSTreeImageSync()

		// Then start the CRON job
		ostreeCronCh <- versions.StartOSTreeImageSync()
	}()

	tftpServer := tftp.StartTFTP()

	// Start the HTTP server (non-blocking; returns the running server).
	httpServer := bootyHTTP.StartHTTP()

	slog.Info("Booty started")

	// Block until a signal arrives, then shut down in order:
	// drain HTTP -> stop schedulers -> stop TFTP.
	<-ctx.Done()
	slog.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var ostreeCron *gocron.Scheduler
	select {
	case ostreeCron = <-ostreeCronCh:
	default:
	}

	shutdown(slog.Default(), []shutdownStep{
		{name: "http", stop: func() {
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				slog.Error("HTTP shutdown failed", "err", err)
			}
		}},
		{name: "flatcar-cron", stop: flatcarCron.Stop},
		{name: "coreos-cron", stop: coreOSCron.Stop},
		{name: "ostree-cron", stop: schedulerStop(ostreeCron)},
		// Bound the TFTP stop: in single-port mode pin/tftp's Shutdown() does
		// not close the listening socket and the serve loop blocks on ReadFrom
		// with no read deadline, so Shutdown() can hang until the next packet.
		// Cap the wait so run() returns within a bounded window regardless.
		{name: "tftp", stop: func() {
			if !stopWithTimeout(tftpServer.Shutdown, 5*time.Second) {
				slog.Warn("TFTP shutdown timed out; exiting anyway", "timeout", 5*time.Second)
			}
		}},
	})

	slog.Info("Booty stopped")
	return nil
}

// schedulerStop returns the scheduler's Stop method, or nil if the scheduler
// was never created (so shutdown skips it).
func schedulerStop(s *gocron.Scheduler) func() {
	if s == nil {
		return nil
	}
	return s.Stop
}
