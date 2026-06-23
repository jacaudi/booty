package versions

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
	"golang.org/x/mod/semver"
)

// selectRetained returns the highest-patch tag for each of the newest n minor
// lines (e.g. v1.10.x, v1.9.x, v1.8.x), newest line first. Invalid tags and
// prereleases are dropped — the factory list is an untrusted boundary, so this
// uses semver validation rather than a regex. Pure function; table-tested.
func selectRetained(tags []string, n int) []string {
	best := map[string]string{} // MajorMinor -> highest patch tag
	for _, tag := range tags {
		if !semver.IsValid(tag) || semver.Prerelease(tag) != "" {
			continue
		}
		mm := semver.MajorMinor(tag)
		if cur, ok := best[mm]; !ok || semver.Compare(tag, cur) > 0 {
			best[mm] = tag
		}
	}
	lines := make([]string, 0, len(best))
	for mm := range best {
		lines = append(lines, mm)
	}
	slices.SortFunc(lines, func(a, b string) int { return semver.Compare(b, a) })

	// Explicit []string{} (not slices.Collect / nil) so the empty-input case
	// returns a non-nil empty slice, matching the test's reflect.DeepEqual.
	out := []string{}
	for i, mm := range lines {
		if i >= n {
			break
		}
		out = append(out, best[mm])
	}
	return out
}

func talosFactoryURL() string { return viper.GetString(config.TalosFactoryURL) }

// fetchTalosVersions GETs <factory>/versions and decodes the JSON array of tags.
func fetchTalosVersions() ([]string, error) {
	b, err := fetchVersionMetadata(talosFactoryURL() + "/versions")
	if err != nil {
		return nil, err
	}
	var tags []string
	if err := json.Unmarshal(b, &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// talosImageURL builds the factory asset URL for one artifact file.
func talosImageURL(schematic, version, file string) string {
	return talosFactoryURL() + "/image/" + schematic + "/" + version + "/" + file
}

// NewestCachedTalos returns the highest semver version directory currently
// present under cache/talos/<schematic>/<arch>/, or "" if none. Exported so
// pkg/tftp can resolve the boot version at serve time (the cache dir is the
// source of truth — there is no currentTalosVersion viper key).
func NewestCachedTalos(schematic, arch string) string {
	base := filepath.Join(cacheRoot(), "talos", schematic, arch)
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	newest := ""
	for _, e := range entries {
		if !e.IsDir() || !semver.IsValid(e.Name()) {
			continue
		}
		if newest == "" || semver.Compare(e.Name(), newest) > 0 {
			newest = e.Name()
		}
	}
	return newest
}

// talosSchematics returns the default schematic plus every distinct schematic
// configured on a Talos host.
func talosSchematics() []string {
	set := map[string]struct{}{viper.GetString(config.TalosSchematic): {}}
	data, err := hardware.GetData()
	if err == nil {
		var bd hardware.BootyData
		if json.Unmarshal(data, &bd) == nil {
			for _, h := range bd.Hosts {
				if h.OS == "talos" && h.Schematic != "" {
					set[h.Schematic] = struct{}{}
				}
			}
		}
	} else {
		slog.Debug("talos: could not read host data for schematic set; using default only", "err", err)
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

// TalosSync caches kernel + initramfs for each (schematic x retained version)
// at the configured arch. Never prunes (operator's choice; latest-N is a floor).
func TalosSync() {
	// Guard against an overlapping run. This is safe (not a mutex) because gocron
	// serializes this job against itself and it is the only writer of UpdatingTalos;
	// viper.Set/Get is not goroutine-safe, so TalosSync must stay single-threaded.
	if viper.GetBool(config.UpdatingTalos) {
		slog.Info("already updating talos, skipping")
		return
	}
	tags, err := fetchTalosVersions()
	if err != nil {
		slog.Warn("error fetching talos versions", "err", err)
		return
	}
	retained := selectRetained(tags, viper.GetInt(config.TalosRetainMinors))
	if len(retained) == 0 {
		slog.Warn("no retained talos versions selected")
		return
	}

	viper.Set(config.UpdatingTalos, true)
	defer viper.Set(config.UpdatingTalos, false)

	ctx := context.Background()
	arch := viper.GetString(config.TalosArchitecture)
	files := []string{"kernel-" + arch, "initramfs-" + arch + ".xz"}

	for _, schematic := range talosSchematics() {
		for _, version := range retained {
			dir := cacheDir("talos", schematic, arch, version)
			for _, file := range files {
				if err := ensureArtifact(ctx, dir, talosImageURL(schematic, version, file)); err != nil {
					slog.Warn("error caching talos artifact", "schematic", schematic, "version", version, "file", file, "err", err)
				}
			}
		}
	}
}

// StartTalosCron starts the Talos version-check scheduler and returns it so the
// caller can Stop() it during graceful shutdown.
func StartTalosCron() *gocron.Scheduler {
	slog.Info("starting Talos CRON version check")
	cron := gocron.NewScheduler(time.UTC)
	_, err := cron.Cron(viper.GetString(config.UpdateSchedule)).Do(TalosSync)
	if err != nil {
		slog.Error("error creating talos cronjob", "err", err)
		os.Exit(1)
	}
	cron.StartAsync()
	return cron
}
