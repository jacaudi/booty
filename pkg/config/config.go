package config

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	FlatcarChannel         = "flatcarChannel"
	CoreOSChannel          = "coreOSChannel"
	IgnitionFile           = "ignitionFile"
	HardwareMap            = "hardwareMap"
	CoreOSArchitecture     = "coreOSArchitecture"
	FlatcarArchitecture    = "flatcarArchitecture"
	Debug                  = "debug"
	HttpPort               = "httpPort"
	DataDir                = "dataDir"
	FlatcarURL             = "flatcarURL"
	CoreOSURL              = "coreOSURL"
	ServerIP               = "serverIP"
	ServerHttpPort         = "serverHttpPort"
	JoinString             = "joinString"
	ProxyDHCPEnabled       = "proxyDHCPEnabled"
	ProxyDHCPBootfileBIOS  = "proxyDHCPBootfileBIOS"
	ProxyDHCPBootfileUEFI  = "proxyDHCPBootfileUEFI"
	ProxyDHCPBootfileARM64 = "proxyDHCPBootfileARM64"
	TalosArchitecture      = "talosArchitecture"
	TalosSchematic         = "talosSchematic"
	TalosRetainMinors      = "talosRetainMinors"
	TalosConfigFile        = "talosConfigFile"
	TalosFactoryURL        = "talosFactoryURL"
	DatabasePath           = "databasePath"
	CacheInterval          = "cacheInterval"
	CacheConcurrency       = "cacheConcurrency"
	CoreOSStreamsURL       = "coreOSStreamsURL"
)

// httpClient is the package-level HTTP client used for DownloadFile.
// The 5-minute Timeout is a hard ceiling covering the entire request
// lifecycle (connect + headers + body); ctx-driven cancellation
// composes on top, so whichever fires first wins.
var httpClient = &http.Client{Timeout: 5 * time.Minute}

func LoadConfig(cmd *cobra.Command) {
	viper.SetDefault(Debug, false)
	viper.SetDefault(ProxyDHCPEnabled, false)
	viper.SetDefault(ProxyDHCPBootfileBIOS, "undionly.kpxe")
	viper.SetDefault(ProxyDHCPBootfileUEFI, "ipxe.efi")
	viper.SetDefault(ProxyDHCPBootfileARM64, "ipxe-arm64.efi")
	viper.SetDefault(TalosArchitecture, "amd64")
	viper.SetDefault(TalosSchematic, "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba")
	viper.SetDefault(TalosRetainMinors, 3)
	viper.SetDefault(TalosConfigFile, "config/machineconfig.yaml")
	viper.SetDefault(TalosFactoryURL, "https://factory.talos.dev")
	viper.SetDefault(CacheInterval, 5*time.Minute)
	viper.SetDefault(CacheConcurrency, 4)
	viper.SetDefault(CoreOSStreamsURL, "https://builds.coreos.fedoraproject.org/streams/%s.json")
	viper.SetDefault(FlatcarURL, "https://%s.release.flatcar-linux.net/%s-usr/current")
	viper.SetDefault(CoreOSURL, "https://builds.coreos.fedoraproject.org/prod/streams/%s/builds/%s/%s")
	// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/39.20231101.3.0/x86_64/fedora-coreos-39.20231101.3.0-live-kernel-x86_64
	// https://stable.release.flatcar-linux.net/amd64-usr/current/version.txt

	viper.BindEnv(IgnitionFile, "IGNITION_FILE")
	viper.SetDefault(IgnitionFile, "config/ignition.yaml")

	viper.BindEnv(HardwareMap, "HARDWARE_MAP")
	viper.SetDefault(HardwareMap, "hardware.json")

	viper.BindEnv(DatabasePath, "DATABASE_PATH")
}

// DownloadFile streams the body at rawURL into <destDir>/<filename>, where
// filename is the trailing path segment of rawURL (query strings stripped).
// The request honors ctx cancellation and httpClient.Timeout (5 minutes);
// whichever fires first wins. A >=400 status is rejected before any file is
// created. Callers are responsible for ensuring destDir exists.
func DownloadFile(ctx context.Context, destDir, rawURL string) error {
	slog.Info("downloading", "url", rawURL)

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("config: parse url %q: %w", rawURL, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("config: build request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("config: get %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("config: get %s: status %s", rawURL, resp.Status)
	}

	base := path.Base(u.Path)
	if base == "." || base == ".." || base == "/" {
		return fmt.Errorf("config: url %q yields unsafe filename %q", rawURL, base)
	}
	filename := filepath.Join(destDir, base)
	slog.Info("creating file", "file", filename)

	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("config: create %s: %w", filename, err)
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("config: write %s: %w", filename, err)
	}

	slog.Info("download complete", "url", rawURL, "bytes", n)
	return nil
}

// DatabasePathValue resolves the SQLite database path: the explicit
// databasePath/DATABASE_PATH value if set, otherwise <DataDir>/booty.db. It is
// the single source of truth for that default so cmd/main.go and pkg/hardware
// agree without LoadConfig having to be called first (tests set DataDir
// directly).
func DatabasePathValue() string {
	if p := viper.GetString(DatabasePath); p != "" {
		return p
	}
	return filepath.Join(viper.GetString(DataDir), "booty.db")
}
