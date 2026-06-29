package ostype

import (
	"context"
	"fmt"
	"regexp"
	"slices"
)

func init() { register(debian{}) }

type debian struct{}

func (debian) Name() string             { return "debian" }
func (debian) Family() Family           { return families["debian"] }
func (debian) RequiredParams() []string { return []string{"channel"} }

var debianVersionRE = regexp.MustCompile(`^\d+(\.\d+)?$`)

func (debian) ValidateVersion(v string) error {
	if !debianVersionRE.MatchString(v) {
		return fmt.Errorf("ostype: debian: invalid version %q (want e.g. 12 or 12.5)", v)
	}
	return nil
}

// CompareVersions orders Debian point releases numerically (major then point).
func (debian) CompareVersions(a, b string) int { return compareDottedNumeric(a, b) }

// debianFixedReleases is the documented supported set returned by discovery in
// P1a (newest first). FLAGGED: a deliberate fixed set, not a placeholder — to be
// replaced by real release-source discovery when Debian caching is wired.
var debianFixedReleases = []string{"12.5", "11.9"}

// DiscoverVersions returns the fixed supported set (see debianFixedReleases).
// ctx is accepted to satisfy the OS interface and for the future real impl.
func (debian) DiscoverVersions(ctx context.Context) ([]string, error) {
	return slices.Clone(debianFixedReleases), nil
}

// debianCodenames maps a channel to the netboot codename used in artifact URLs.
var debianCodenames = map[string]string{
	"stable":    "bookworm",
	"oldstable": "bullseye",
}

func (debian) Artifacts(version, arch string, params map[string]string) []Artifact {
	codename := debianCodenames[params["channel"]]
	if codename == "" {
		codename = debianCodenames["stable"]
	}
	base := fmt.Sprintf(
		"https://deb.debian.org/debian/dists/%s/main/installer-%s/current/images/netboot/debian-installer/%s",
		codename, arch, arch)
	return []Artifact{
		{Filename: "linux", URL: base + "/linux"},
		{Filename: "initrd.gz", URL: base + "/initrd.gz"},
	}
}
