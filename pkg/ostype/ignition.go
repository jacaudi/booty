package ostype

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/buger/jsonparser"
	"github.com/jeefy/booty/pkg/config"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"golang.org/x/mod/semver"
)

func init() {
	register(flatcar{})
	register(fedoraCoreOS{})
}

// ---- flatcar ----

type flatcar struct{}

func (flatcar) Name() string   { return "flatcar" }
func (flatcar) Family() Family { return families["ignition"] }

func (flatcar) RequiredParams() []string { return []string{"channel"} }

// ValidateVersion accepts a bare Flatcar version (e.g. 3815.2.0) by validating
// it as semver once a leading "v" is supplied.
func (flatcar) ValidateVersion(v string) error {
	if !semver.IsValid("v" + v) {
		return fmt.Errorf("ostype: flatcar: invalid version %q", v)
	}
	return nil
}

// CompareVersions orders bare Flatcar versions by semver.
func (flatcar) CompareVersions(a, b string) int {
	return semver.Compare("v"+a, "v"+b)
}

// DiscoverVersions fetches the channel's current version.txt and returns the
// single FLATCAR_VERSION it advertises. The channel comes from target params
// (flag fallback); arch stays a flag.
func (flatcar) DiscoverVersions(ctx context.Context, params map[string]string) ([]string, error) {
	base := flatcarBaseURL(params["channel"])
	body, err := fetchMetadata(ctx, base+"/version.txt")
	if err != nil {
		return nil, err
	}
	data, err := godotenv.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ostype: flatcar: parse version.txt: %w", err)
	}
	v, ok := data["FLATCAR_VERSION"]
	if !ok {
		return nil, fmt.Errorf("ostype: flatcar: version.txt missing FLATCAR_VERSION")
	}
	return []string{v}, nil
}

// Artifacts intentionally ignores version: Flatcar release URLs are
// channel-relative to /current, not version-scoped, so a retained-but-no-
// longer-advertised version's URL resolves to whatever upstream currently
// serves. Harmless in practice because ensureArtifact only downloads when
// the on-disk file is absent — already-cached older versions are never
// overwritten. P3b replaces this hand-built derivation with streams JSON.
func (flatcar) Artifacts(ctx context.Context, version, arch string, params map[string]string) ([]Artifact, error) {
	base := flatcarBaseURL(params["channel"])
	files := []string{"flatcar_production_pxe.vmlinuz", "flatcar_production_pxe_image.cpio.gz"}
	out := make([]Artifact, 0, len(files))
	for _, f := range files {
		u := base + "/" + f
		out = append(out, Artifact{Filename: f, URL: u, SigURL: u + ".sig", GPGKey: flatcarKeyring})
	}
	return out, nil
}

// ---- fedora-coreos ----

type fedoraCoreOS struct{}

func (fedoraCoreOS) Name() string   { return "fedora-coreos" }
func (fedoraCoreOS) Family() Family { return families["ignition"] }

func (fedoraCoreOS) RequiredParams() []string { return []string{"channel"} }

// ValidateVersion accepts a dotted-numeric FCOS build id (e.g. 39.20231101.3.0).
func (fedoraCoreOS) ValidateVersion(v string) error {
	if v == "" {
		return fmt.Errorf("ostype: fedora-coreos: empty version")
	}
	for part := range strings.SplitSeq(v, ".") {
		if _, err := strconv.Atoi(part); err != nil {
			return fmt.Errorf("ostype: fedora-coreos: non-numeric field in %q", v)
		}
	}
	return nil
}

// CompareVersions orders FCOS build ids field-by-field numerically (they are
// NOT semver). Shorter ids sort before longer ones when otherwise equal.
func (fedoraCoreOS) CompareVersions(a, b string) int {
	return compareDottedNumeric(a, b)
}

// DiscoverVersions fetches the channel streams JSON and returns its metal build
// release for the target's channel (params, flag fallback) and the arch flag. It
// routes through the pass-scoped streams memo (D17), the SAME one Artifacts uses,
// so discovery + artifact resolution for a channel cost one streams GET per pass.
func (fedoraCoreOS) DiscoverVersions(ctx context.Context, params map[string]string) ([]string, error) {
	body, err := fetchStreams(ctx, coreosStreamsURL(params["channel"]))
	if err != nil {
		return nil, err
	}
	rel, err := jsonparser.GetString(body, "architectures", coreosArch(), "artifacts", "metal", "release")
	if err != nil {
		return nil, fmt.Errorf("ostype: fedora-coreos: read release: %w", err)
	}
	return []string{rel}, nil
}

// Artifacts resolves FCOS artifacts from the channel streams JSON. When version
// equals the stream's current metal release, it returns the pxe kernel/
// initramfs/rootfs location URLs + their sha256 (verifiable). For any other
// (manually pinned older) version it falls back to the hand-built dot-form
// URLs with NO sha256 — a documented limitation: those versions verify NULL
// (design §3, D10). The streams doc is fetched at most once per pass (D17).
func (fedoraCoreOS) Artifacts(ctx context.Context, version, arch string, params map[string]string) ([]Artifact, error) {
	body, err := fetchStreams(ctx, coreosStreamsURL(params["channel"]))
	if err != nil {
		return nil, err
	}
	metal := []string{"architectures", coreosArch(), "artifacts", "metal"}
	release, err := jsonparser.GetString(body, append(metal, "release")...)
	if err != nil {
		return nil, fmt.Errorf("ostype: fedora-coreos: read release: %w", err)
	}

	// Older pinned build: pattern fallback, no sha256.
	if version != release {
		base := coreosBuildBaseURL(params["channel"], version, arch)
		files := []string{
			// Dot-form kernel: FCOS renamed live-kernel-<arch> → live-kernel.<arch>
			// between FCOS 39 and 44 (verified 2026-07-01: dash 404s, dot 200s).
			// initramfs/rootfs already used dots.
			fmt.Sprintf("fedora-coreos-%s-live-kernel.%s", version, arch),
			fmt.Sprintf("fedora-coreos-%s-live-initramfs.%s.img", version, arch),
			fmt.Sprintf("fedora-coreos-%s-live-rootfs.%s.img", version, arch),
		}
		out := make([]Artifact, 0, len(files))
		for _, f := range files {
			out = append(out, Artifact{Filename: f, URL: base + "/" + f})
		}
		return out, nil
	}

	// Current build: location + sha256 from the streams pxe formats.
	pxe := append(metal, "formats", "pxe")
	out := make([]Artifact, 0, 3)
	for _, key := range []string{"kernel", "initramfs", "rootfs"} {
		loc, err := jsonparser.GetString(body, append(pxe, key, "location")...)
		if err != nil {
			return nil, fmt.Errorf("ostype: fedora-coreos: %s location: %w", key, err)
		}
		sha, err := jsonparser.GetString(body, append(pxe, key, "sha256")...)
		if err != nil {
			return nil, fmt.Errorf("ostype: fedora-coreos: %s sha256: %w", key, err)
		}
		out = append(out, Artifact{Filename: path.Base(loc), URL: loc, SHA256: sha})
	}
	return out, nil
}

// compareDottedNumeric compares two dotted-numeric strings field by field.
// Defined here; Task 9 (debian) reuses it from this file — do not duplicate.
func compareDottedNumeric(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var av, bv int
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	if len(as) < len(bs) {
		return -1
	}
	if len(as) > len(bs) {
		return 1
	}
	return 0
}

// URL helpers derive per-target URLs: channel comes from target params (flag
// fallback via cmp.Or); the URL templates and arch remain flags.
func flatcarBaseURL(channel string) string {
	return fmt.Sprintf(viper.GetString(config.FlatcarURL),
		cmp.Or(channel, viper.GetString(config.FlatcarChannel)),
		viper.GetString(config.FlatcarArchitecture))
}

func coreosArch() string { return viper.GetString(config.CoreOSArchitecture) }

func coreosStreamsURL(channel string) string {
	return fmt.Sprintf(viper.GetString(config.CoreOSStreamsURL),
		cmp.Or(channel, viper.GetString(config.CoreOSChannel)))
}

func coreosBuildBaseURL(channel, version, arch string) string {
	return fmt.Sprintf(viper.GetString(config.CoreOSURL),
		cmp.Or(channel, viper.GetString(config.CoreOSChannel)), version, arch)
}
