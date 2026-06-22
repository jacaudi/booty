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

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	CurrentFlatcarVersion = "currentFlatcarVersion"
	RemoteFlatcarVersion  = "remoteFlatcarVersion"
	FlatcarChannel        = "flatcarChannel"
	CurrentCoreOSVersion  = "currentCoreOSVersion"
	RemoteCoreOSVersion   = "remoteCoreOSVersion"
	CoreOSChannel         = "coreOSChannel"
	IgnitionFile          = "ignitionFile"
	HardwareMap           = "hardwareMap"
	CoreOSArchitecture    = "coreOSArchitecture"
	FlatcarArchitecture   = "flatcarArchitecture"
	Debug                 = "debug"
	UpdateSchedule        = "updateSchedule"
	HttpPort              = "httpPort"
	DataDir               = "dataDir"
	FlatcarURL            = "flatcarURL"
	CoreOSURL             = "coreOSURL"
	ServerIP              = "serverIP"
	ServerHttpPort        = "serverHttpPort"
	JoinString            = "joinString"
	UpdatingFlatcar       = "updatingFlatcar"
	UpdatingCoreOS        = "updatingCoreOS"
)

// httpClient is the package-level HTTP client used for DownloadFile.
// The 5-minute Timeout is a hard ceiling covering the entire request
// lifecycle (connect + headers + body); ctx-driven cancellation
// composes on top, so whichever fires first wins.
var httpClient = &http.Client{Timeout: 5 * time.Minute}

func LoadConfig(cmd *cobra.Command) {
	viper.SetDefault(Debug, false)
	viper.SetDefault(UpdatingFlatcar, false)
	viper.SetDefault(UpdatingCoreOS, false)
	viper.SetDefault(FlatcarURL, "https://%s.release.flatcar-linux.net/%s-usr/current")
	viper.SetDefault(CoreOSURL, "https://builds.coreos.fedoraproject.org/prod/streams/%s/builds/%s/%s")
	// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/39.20231101.3.0/x86_64/fedora-coreos-39.20231101.3.0-live-kernel-x86_64
	// https://stable.release.flatcar-linux.net/amd64-usr/current/version.txt

	versionPath := fmt.Sprintf("%s/version.txt", viper.GetString(DataDir))
	if file, err := os.Open(versionPath); err == nil {
		defer file.Close()
		data, parseErr := godotenv.Parse(file)
		switch {
		case parseErr != nil:
			slog.Warn("error parsing version file", "path", versionPath, "err", parseErr)
		default:
			if v, ok := data["FLATCAR_VERSION"]; ok {
				viper.Set(CurrentFlatcarVersion, v)
				slog.Info("local version found", "version", v)
			} else {
				slog.Warn("version file present but FLATCAR_VERSION key missing", "path", versionPath)
			}
		}
	} else if !os.IsNotExist(err) {
		slog.Warn("error opening version file", "path", versionPath, "err", err)
	}

	viper.BindEnv(IgnitionFile, "IGNITION_FILE")
	viper.SetDefault(IgnitionFile, "config/ignition.yaml")

	viper.BindEnv(HardwareMap, "HARDWARE_MAP")
	viper.SetDefault(HardwareMap, "hardware.json")
}

// DownloadFile streams the body at rawURL into <DataDir>/<filename>,
// where filename is the trailing path segment of rawURL (query
// strings stripped). The request honors ctx cancellation and the
// package-level httpClient.Timeout (5 minutes); whichever fires
// first wins.
func DownloadFile(ctx context.Context, rawURL string) error {
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
	filename := filepath.Join(viper.GetString(DataDir), base)
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
