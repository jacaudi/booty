package versions

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/jeefy/booty/pkg/config"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

func StartFlatcarCron() {
	log.Println("Starting CRON version check")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).Do(FlatcarVersionCheck)
	if err != nil {
		log.Fatalf("Error creating prune cronjob: %s", err.Error())
	}
	cron.StartAsync()
}

func FlatcarVersionCheck() {
	if viper.GetBool(config.Updating) {
		log.Println("Already updating, skipping version check")
		return
	}
	if viper.GetBool("debug") {
		log.Println("Checking remote flatcar version")
	}

	if viper.GetString(config.CurrentFlatcarVersion) == "" {
		versionPath := fmt.Sprintf("%s/version.txt", viper.GetString(config.DataDir))
		if oldVer, err := os.Open(versionPath); err == nil {
			defer oldVer.Close()
			data, parseErr := godotenv.Parse(oldVer)
			switch {
			case parseErr != nil:
				log.Printf("Error parsing %s: %s; defaulting to 0.0.0", versionPath, parseErr.Error())
				viper.Set(config.CurrentFlatcarVersion, "0.0.0")
			default:
				if v, ok := data["FLATCAR_VERSION"]; ok {
					viper.Set(config.CurrentFlatcarVersion, v)
					log.Printf("Flatcar version set to %s", v)
				} else {
					log.Printf("%s present but FLATCAR_VERSION key missing; defaulting to 0.0.0", versionPath)
					viper.Set(config.CurrentFlatcarVersion, "0.0.0")
				}
			}
		} else {
			if !os.IsNotExist(err) {
				log.Printf("Error opening %s: %s", versionPath, err.Error())
			} else {
				log.Printf("%s not found, setting current version to 0.0.0", versionPath)
			}
			viper.Set(config.CurrentFlatcarVersion, "0.0.0")
		}
	}

	LoadRemoteFlatcarVersion()
	if viper.GetString(config.RemoteFlatcarVersion) != viper.GetString(config.CurrentFlatcarVersion) {
		ctx := context.Background()
		viper.Set(config.Updating, true)
		log.Printf("Remote flatcar version %s is different than local version %s", viper.GetString(config.RemoteFlatcarVersion), viper.GetString(config.CurrentFlatcarVersion))

		if err := DownloadFlatcarFile(ctx, "version.txt"); err != nil {
			log.Printf("Error downloading version.txt: %s", err.Error())
		}
		if err := DownloadFlatcarFile(ctx, "flatcar_production_pxe_image.cpio.gz"); err != nil {
			log.Printf("Error downloading flatcar_production_pxe_image.cpio.gz: %s", err.Error())
		}
		if err := DownloadFlatcarFile(ctx, "flatcar_production_pxe.vmlinuz"); err != nil {
			log.Printf("Error downloading flatcar_production_pxe.vmlinuz: %s", err.Error())
		}

		viper.Set(config.CurrentFlatcarVersion, viper.GetString(config.RemoteFlatcarVersion))
		viper.Set(config.Updating, false)
	}

}

func LoadRemoteFlatcarVersion() {
	if resp, err := http.Get(RemoteFlatcarURL() + "/version.txt"); err == nil {
		defer resp.Body.Close()
		data, _ := godotenv.Parse(resp.Body)
		if _, ok := data["FLATCAR_VERSION"]; !ok {
			log.Printf("Error retrieving remote flatcar version from %s", resp.Request.URL.String())
			if err != nil {
				log.Println(err.Error())
			}
			return
		}
		viper.Set(config.RemoteFlatcarVersion, data["FLATCAR_VERSION"])
		if viper.GetBool("debug") {
			log.Printf("Remote flatcar version found: %s", data["FLATCAR_VERSION"])
		}
	} else {
		log.Printf("Error retrieving remote flatcar version from %s: %s", RemoteFlatcarURL(), err.Error())
	}
}

func RemoteFlatcarURL() string {
	return fmt.Sprintf(viper.GetString(config.FlatcarURL), viper.GetString(config.FlatcarChannel), viper.GetString(config.FlatcarArchitecture))
}

func DownloadFlatcarFile(ctx context.Context, filename string) error {
	return config.DownloadFile(ctx, fmt.Sprintf(RemoteFlatcarURL()+"/%s", filename))
}
