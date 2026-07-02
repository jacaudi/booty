package ostype

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
	"golang.org/x/mod/semver"
)

func init() { register(talos{}) }

type talos struct{}

func (talos) Name() string             { return "talos" }
func (talos) Family() Family           { return families["talos"] }
func (talos) RequiredParams() []string { return []string{"schematic"} }

func (talos) ValidateVersion(v string) error {
	if !semver.IsValid(v) {
		return fmt.Errorf("ostype: talos: invalid version %q", v)
	}
	return nil
}

func (talos) CompareVersions(a, b string) int { return semver.Compare(a, b) }

// DiscoverVersions GETs <factory>/versions and decodes the JSON tag array
// (mirrors pkg/versions/talos.go's fetchTalosVersions).
func (talos) DiscoverVersions(ctx context.Context, _ map[string]string) ([]string, error) {
	body, err := fetchMetadata(ctx, talosFactoryURL()+"/versions")
	if err != nil {
		return nil, err
	}
	var tags []string
	if err := json.Unmarshal(body, &tags); err != nil {
		return nil, fmt.Errorf("ostype: talos: decode versions: %w", err)
	}
	return tags, nil
}

func (talos) Artifacts(version, arch string, params map[string]string) []Artifact {
	schematic := params["schematic"]
	base := talosFactoryURL() + "/image/" + schematic + "/" + version + "/"
	return []Artifact{
		{Filename: "kernel-" + arch, URL: base + "kernel-" + arch},
		{Filename: "initramfs-" + arch + ".xz", URL: base + "initramfs-" + arch + ".xz"},
	}
}

func talosFactoryURL() string { return viper.GetString(config.TalosFactoryURL) }
