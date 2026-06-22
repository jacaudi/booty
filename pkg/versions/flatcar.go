package versions

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/jeefy/booty/pkg/config"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

func StartFlatcarCron() {
	slog.Info("starting Flatcar CRON version check")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).Do(FlatcarVersionCheck)
	if err != nil {
		slog.Error("error creating prune cronjob", "err", err)
		os.Exit(1)
	}
	cron.StartAsync()
}

func FlatcarVersionCheck() {
	if viper.GetBool(config.UpdatingFlatcar) {
		slog.Info("already updating, skipping version check")
		return
	}
	slog.Debug("checking remote flatcar version")

	if viper.GetString(config.CurrentFlatcarVersion) == "" {
		versionPath := fmt.Sprintf("%s/version.txt", viper.GetString(config.DataDir))
		if oldVer, err := os.Open(versionPath); err == nil {
			defer oldVer.Close()
			data, parseErr := godotenv.Parse(oldVer)
			switch {
			case parseErr != nil:
				slog.Warn("error parsing version file; defaulting to 0.0.0", "path", versionPath, "err", parseErr)
				viper.Set(config.CurrentFlatcarVersion, "0.0.0")
			default:
				if v, ok := data["FLATCAR_VERSION"]; ok {
					viper.Set(config.CurrentFlatcarVersion, v)
					slog.Info("flatcar version set", "version", v)
				} else {
					slog.Warn("version file present but FLATCAR_VERSION key missing; defaulting to 0.0.0", "path", versionPath)
					viper.Set(config.CurrentFlatcarVersion, "0.0.0")
				}
			}
		} else {
			if !os.IsNotExist(err) {
				slog.Warn("error opening version file", "path", versionPath, "err", err)
			} else {
				slog.Info("version file not found, setting current version to 0.0.0", "path", versionPath)
			}
			viper.Set(config.CurrentFlatcarVersion, "0.0.0")
		}
	}

	LoadRemoteFlatcarVersion()
	if viper.GetString(config.RemoteFlatcarVersion) != viper.GetString(config.CurrentFlatcarVersion) {
		ctx := context.Background()
		viper.Set(config.UpdatingFlatcar, true)
		slog.Info("remote flatcar version differs from local", "remote", viper.GetString(config.RemoteFlatcarVersion), "local", viper.GetString(config.CurrentFlatcarVersion))

		if err := DownloadFlatcarFile(ctx, "version.txt"); err != nil {
			slog.Warn("error downloading flatcar file", "file", "version.txt", "err", err)
		}
		if err := DownloadFlatcarFile(ctx, "flatcar_production_pxe_image.cpio.gz"); err != nil {
			slog.Warn("error downloading flatcar file", "file", "flatcar_production_pxe_image.cpio.gz", "err", err)
		}
		if err := DownloadFlatcarFile(ctx, "flatcar_production_pxe.vmlinuz"); err != nil {
			slog.Warn("error downloading flatcar file", "file", "flatcar_production_pxe.vmlinuz", "err", err)
		}

		viper.Set(config.CurrentFlatcarVersion, viper.GetString(config.RemoteFlatcarVersion))
		viper.Set(config.UpdatingFlatcar, false)
	}

}

func LoadRemoteFlatcarVersion() {
	url := RemoteFlatcarURL() + "/version.txt"
	b, err := fetchVersionMetadata(url)
	if err != nil {
		slog.Warn("error retrieving remote flatcar version", "url", url, "err", err)
		return
	}

	data, err := godotenv.Parse(bytes.NewReader(b))
	if err != nil {
		slog.Warn("error parsing remote flatcar version", "url", url, "err", err)
		return
	}
	if _, ok := data["FLATCAR_VERSION"]; !ok {
		slog.Warn("error retrieving remote flatcar version", "url", url)
		return
	}
	viper.Set(config.RemoteFlatcarVersion, data["FLATCAR_VERSION"])
	slog.Debug("remote flatcar version found", "version", data["FLATCAR_VERSION"])
}

func RemoteFlatcarURL() string {
	return fmt.Sprintf(viper.GetString(config.FlatcarURL), viper.GetString(config.FlatcarChannel), viper.GetString(config.FlatcarArchitecture))
}

func DownloadFlatcarFile(ctx context.Context, filename string) error {
	return config.DownloadFile(ctx, fmt.Sprintf(RemoteFlatcarURL()+"/%s", filename))
}
