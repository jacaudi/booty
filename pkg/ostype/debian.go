package ostype

import (
	"context"
	"fmt"
	"regexp"
	"slices"
)

func init() { register(debian{}) }

// DefaultDebianChannel is the release channel used when a Debian target has
// no per-deployment default to read from (no config.DebianChannel flag exists
// — targets are catalog-declared, potentially multiple channels at once) and
// no host-assigned channel either. Single-sourced here so pkg/tftp's
// bootTokens and pkg/http's debianDVDMirrorDir — which both need this same
// fallback — can't drift from each other or from this package's own internal
// default (the "13" stable/current suite).
const DefaultDebianChannel = "13"

type debian struct{}

func (debian) Name() string             { return "debian" }
func (debian) Family() Family           { return families["debian"] }
func (debian) RequiredParams() []string { return []string{"channel"} }

var debianVersionRE = regexp.MustCompile(`^\d+(\.\d+){0,2}$`)

func (debian) ValidateVersion(v string) error {
	if !debianVersionRE.MatchString(v) {
		return fmt.Errorf("ostype: debian: invalid version %q (want e.g. 12, 12.5, or 12.5.0)", v)
	}
	return nil
}

// CompareVersions orders Debian point releases numerically (major then point).
func (debian) CompareVersions(a, b string) int { return compareDottedNumeric(a, b) }

// debianCodenames maps a numeric release channel to its netboot codename.
var debianCodenames = map[string]string{"13": "trixie", "12": "bookworm", "11": "bullseye"}

// debianIndexFetcher fetches a directory index (HTML autoindex or a structured
// list) for point-release resolution. Overridable in tests.
var debianIndexFetcher = fetchDebianIndex

// fetchDebianIndex retrieves a cdimage directory index as text, reusing the
// package's shared HTTP client (fetchMetadata) — these are small text bodies,
// so its 30s ceiling is fine.
func fetchDebianIndex(ctx context.Context, url string) (string, error) {
	body, err := fetchMetadata(ctx, url)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// debianPointReleaseRE precompiles, once at init, the point-release matcher
// for each known Debian major channel (the same set debianCodenames
// validates) — a bare "<major>.x.y" token anywhere in a directory index body
// — instead of calling regexp.MustCompile on every newestDebianPointRelease
// call.
var debianPointReleaseRE = func() map[string]*regexp.Regexp {
	m := make(map[string]*regexp.Regexp, len(debianCodenames))
	for major := range debianCodenames {
		m[major] = regexp.MustCompile(`\b` + regexp.QuoteMeta(major) + `\.\d+\.\d+\b`)
	}
	return m
}()

// newestDebianPointRelease returns the highest <major>.x.y token found in
// body, or "" if none match (including an unrecognized major).
func newestDebianPointRelease(body, major string) string {
	re, ok := debianPointReleaseRE[major]
	if !ok {
		return ""
	}
	matches := re.FindAllString(body, -1)
	if len(matches) == 0 {
		return ""
	}
	return slices.MaxFunc(matches, compareDottedNumeric)
}

// DiscoverVersions resolves the newest point release for the target's
// channel: "13" (stable) reads the newest ISO under debian-cd/current/;
// "12"/"11" read the highest archived point release under cdimage/archive/.
func (debian) DiscoverVersions(ctx context.Context, params map[string]string) ([]string, error) {
	major := params["channel"]
	if debianCodenames[major] == "" {
		return nil, fmt.Errorf("ostype: debian: unknown release channel %q (want 11/12/13)", major)
	}
	var indexURL string
	switch major {
	case "13":
		indexURL = "https://cdimage.debian.org/debian-cd/current/amd64/iso-dvd/"
	default:
		indexURL = "https://cdimage.debian.org/cdimage/archive/"
	}
	body, err := debianIndexFetcher(ctx, indexURL)
	if err != nil {
		return nil, fmt.Errorf("ostype: debian: fetch index %s: %w", indexURL, err)
	}
	v := newestDebianPointRelease(body, major)
	if v == "" {
		return nil, fmt.Errorf("ostype: debian: no point release for %s in %s", major, indexURL)
	}
	return []string{v}, nil
}

func (debian) Artifacts(ctx context.Context, version, arch string, params map[string]string) ([]Artifact, error) {
	codename := debianCodenames[params["channel"]]
	if codename == "" {
		codename = "trixie"
	}
	base := fmt.Sprintf(
		"https://deb.debian.org/debian/dists/%s/main/installer-%s/current/images/netboot/debian-installer/%s",
		codename, arch, arch)
	return []Artifact{
		{Filename: "linux", URL: base + "/linux"},
		{Filename: "initrd.gz", URL: base + "/initrd.gz"},
	}, nil
}
