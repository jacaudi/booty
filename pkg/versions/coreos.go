package versions

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/buger/jsonparser"
	"github.com/go-co-op/gocron"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// StartCoreOSCron starts the CoreOS version-check scheduler and returns it so
// the caller can Stop() it during graceful shutdown.
func StartCoreOSCron() *gocron.Scheduler {
	slog.Info("starting CoreOS CRON version check")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).Do(CoreOSVersionCheck)
	if err != nil {
		slog.Error("error creating cronjob", "err", err)
		os.Exit(1)
	}
	cron.StartAsync()
	return cron
}

func CoreOSVersionCheck() {
	if viper.GetBool(config.UpdatingCoreOS) {
		slog.Info("already updating, skipping version check")
		return
	}
	slog.Debug("checking remote coreos version")

	if viper.GetString(config.CurrentCoreOSVersion) == "" {
		// Check for an existing coreos.json file
		jsonPath := fmt.Sprintf("%s/%s.json", viper.GetString(config.DataDir), viper.GetString(config.CoreOSChannel))
		if b, err := os.ReadFile(jsonPath); err == nil {
			slog.Info("found old coreos json, setting current version to that")
			oldVersion, err := jsonparser.GetString(b, "architectures", viper.GetString(config.CoreOSArchitecture), "artifacts", "metal", "release")
			if err != nil {
				slog.Warn("old coreos json file is invalid", "channel", viper.GetString(config.CoreOSChannel), "err", err)
			}
			viper.Set(config.CurrentCoreOSVersion, oldVersion)
			slog.Info("coreos version set", "version", oldVersion)
		} else {
			slog.Info("coreos json not found, setting current version to 0.0.0", "path", jsonPath)
			viper.Set(config.CurrentCoreOSVersion, "0.0.0")
		}
	}

	LoadRemoteCoreOSVersion()
	oldVersion := viper.GetString(config.CurrentCoreOSVersion)
	if viper.GetString(config.RemoteCoreOSVersion) != viper.GetString(config.CurrentCoreOSVersion) {
		ctx := context.Background()
		viper.Set(config.UpdatingCoreOS, true)
		slog.Info("remote coreos version differs from local", "remote", viper.GetString(config.RemoteCoreOSVersion), "local", oldVersion)

		if err := DownloadCoreOSJSON(ctx); err != nil {
			slog.Warn("error downloading coreos json", "err", err)
		}
		toDownload := ""

		toDownload = fmt.Sprintf("fedora-coreos-%s-live-initramfs.%s.img", viper.GetString(config.RemoteCoreOSVersion), viper.GetString(config.CoreOSArchitecture))
		if err := DownloadCoreOSFile(ctx, toDownload); err != nil {
			slog.Warn("error downloading coreos file", "file", toDownload, "err", err)
		}

		toDownload = fmt.Sprintf("fedora-coreos-%s-live-kernel-%s", viper.GetString(config.RemoteCoreOSVersion), viper.GetString(config.CoreOSArchitecture))
		if err := DownloadCoreOSFile(ctx, toDownload); err != nil {
			slog.Warn("error downloading coreos file", "file", toDownload, "err", err)
		}

		toDownload = fmt.Sprintf("fedora-coreos-%s-live-rootfs.%s.img", viper.GetString(config.RemoteCoreOSVersion), viper.GetString(config.CoreOSArchitecture))
		if err := DownloadCoreOSFile(ctx, toDownload); err != nil {
			slog.Warn("error downloading coreos file", "file", toDownload, "err", err)
		}

		viper.Set(config.CurrentCoreOSVersion, viper.GetString(config.RemoteCoreOSVersion))

		if oldVersion != "" && oldVersion != "0.0.0" {
			if err := removeVersionDir("coreos", "-", viper.GetString(config.CoreOSArchitecture), oldVersion); err != nil {
				slog.Warn("coreos cleanup: remove old version dir failed", "version", oldVersion, "err", err)
			}
		}

		viper.Set(config.UpdatingCoreOS, false)
	}

}

func LoadRemoteCoreOSVersion() {
	url := RemoteCoreOSJSONURL()
	b, err := fetchVersionMetadata(url)
	if err != nil {
		slog.Warn("error retrieving remote coreos version", "url", url, "err", err)
		return
	}

	remoteVersion, err := jsonparser.GetString(b, "architectures", viper.GetString(config.CoreOSArchitecture), "artifacts", "metal", "release")
	if err != nil {
		slog.Warn("error retrieving remote coreos version", "url", url, "err", err)
		return
	}
	viper.Set(config.RemoteCoreOSVersion, remoteVersion)
	slog.Debug("remote coreos version found", "version", remoteVersion)
}

// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/39.20231101.3.0/x86_64/fedora-coreos-39.20231101.3.0-live-kernel-x86_64
// https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/0.0.0/x86_64//fedora-coreos-39.20231101.3.0-live-kernel-x86_64
func RemoteCoreOSURL() string {
	return fmt.Sprintf(viper.GetString(config.CoreOSURL), viper.GetString(config.CoreOSChannel), viper.GetString(config.RemoteCoreOSVersion), viper.GetString(config.CoreOSArchitecture))
}

func DownloadCoreOSFile(ctx context.Context, filename string) error {
	dir := cacheDir("coreos", "-", viper.GetString(config.CoreOSArchitecture), viper.GetString(config.RemoteCoreOSVersion))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return config.DownloadFile(ctx, dir, fmt.Sprintf(RemoteCoreOSURL()+"/%s", filename))
}

func RemoteCoreOSJSONURL() string {
	return fmt.Sprintf("https://builds.coreos.fedoraproject.org/streams/%s.json", viper.GetString(config.CoreOSChannel))
}

func DownloadCoreOSJSON(ctx context.Context) error {
	return config.DownloadFile(ctx, viper.GetString(config.DataDir), RemoteCoreOSJSONURL())
}
