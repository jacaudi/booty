package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/ostype"
	"github.com/spf13/viper"
	yaml "go.yaml.in/yaml/v4"
)

// catalogSchemaVersion is the only schemaVersion this build accepts.
const catalogSchemaVersion = 1

// catalogArches is the per-OS arch allowlist AND the set of OSes a catalog may
// declare. A mismatched arch yields a valid-looking cache segment that 404s on
// download, so it is rejected up front (design M2/M3).
var catalogArches = map[string][]string{
	"flatcar":       {"amd64", "arm64"},
	"fedora-coreos": {"x86_64"},
	"talos":         {"amd64", "arm64"},
	"debian":        {"amd64", "arm64"},
}

// CatalogEntry is one declared cache target. Enabled/Retain are pointers so an
// omitted field is distinguishable from an explicit zero (default true / 1).
// Spec holds the OS-specific path-discriminating params (channel or schematic)
// and maps 1:1 to the target's RequiredParams. SourceMode/DvdCount are
// top-level (not Spec keys) so they carry Debian's netinst/dvd serving-mode
// intent without perturbing ValidateTargetParams, which requires Spec's keys
// to equal the OS's RequiredParams exactly.
type CatalogEntry struct {
	OS         string            `yaml:"os"`
	Arch       string            `yaml:"arch"`
	Enabled    *bool             `yaml:"enabled"`
	Retain     *int              `yaml:"retain"`
	Spec       map[string]string `yaml:"spec"`
	SourceMode string            `yaml:"sourceMode,omitempty"` // debian only: "" (netinst) | "netinst" | "dvd"
	DvdCount   int               `yaml:"dvdCount,omitempty"`   // debian dvd only: number of DVD images to mirror
}

func (e CatalogEntry) enabledOrDefault() bool {
	return e.Enabled == nil || *e.Enabled
}

func (e CatalogEntry) retainOrDefault() int {
	if e.Retain == nil {
		return 1
	}
	return *e.Retain
}

type catalogFile struct {
	SchemaVersion int            `yaml:"schemaVersion"`
	Entries       []CatalogEntry `yaml:"catalog"`
}

// parseCatalog decodes and fully validates catalog YAML. WithKnownFields makes
// an unknown/misspelled key a hard error (fail-fast).
func parseCatalog(data []byte) ([]CatalogEntry, error) {
	var c catalogFile
	if err := yaml.Load(data, &c, yaml.WithKnownFields()); err != nil {
		return nil, fmt.Errorf("cache: parse catalog: %w", err)
	}
	if err := validateCatalog(c); err != nil {
		return nil, err
	}
	return c.Entries, nil
}

func validateCatalog(c catalogFile) error {
	if c.SchemaVersion != catalogSchemaVersion {
		return fmt.Errorf("cache: catalog: unsupported schemaVersion %d (want %d)", c.SchemaVersion, catalogSchemaVersion)
	}
	seen := map[string]bool{}
	for i, e := range c.Entries {
		arches, ok := catalogArches[e.OS]
		if !ok {
			return fmt.Errorf("cache: catalog[%d]: unsupported os %q (supported: flatcar, fedora-coreos, talos, debian)", i, e.OS)
		}
		if !slices.Contains(arches, e.Arch) {
			return fmt.Errorf("cache: catalog[%d]: os %q does not support arch %q (want one of %v)", i, e.OS, e.Arch, arches)
		}
		o, ok := ostype.Lookup(e.OS) // guaranteed by catalogArches membership; checked to avoid a nil-interface panic if the two ever drift
		if !ok {
			return fmt.Errorf("cache: catalog[%d]: os %q is not registered", i, e.OS)
		}
		if err := ValidateTargetParams(o, e.Spec); err != nil {
			return fmt.Errorf("cache: catalog[%d] (%s/%s): %w", i, e.OS, e.Arch, err)
		}
		if r := e.retainOrDefault(); r < 0 {
			return fmt.Errorf("cache: catalog[%d]: retain must be >= 0, got %d", i, r)
		}
		if e.OS == "debian" {
			if err := validateDebianEntry(i, e); err != nil {
				return err
			}
		}
		params, err := encodeParams(e.Spec)
		if err != nil {
			return fmt.Errorf("cache: catalog[%d]: %w", i, err)
		}
		key := e.OS + "|" + e.Arch + "|" + params
		if seen[key] {
			return fmt.Errorf("cache: catalog[%d]: duplicate entry (%s/%s/%s)", i, e.OS, e.Arch, params)
		}
		seen[key] = true
	}
	return nil
}

// validateDebianEntry enforces the Debian-specific catalog rules that don't
// fit ValidateTargetParams' generic key-presence/path-safety contract: the
// channel must be a supported numeric release, sourceMode must be a known
// value, and dvd mode is amd64-only (Debian ships no arm64 DVD ISOs).
func validateDebianEntry(i int, e CatalogEntry) error {
	if !slices.Contains([]string{"11", "12", "13"}, e.Spec["channel"]) {
		return fmt.Errorf("cache: catalog[%d]: debian channel must be one of 11, 12, 13, got %q", i, e.Spec["channel"])
	}
	if !slices.Contains([]string{"", "netinst", "dvd"}, e.SourceMode) {
		return fmt.Errorf("cache: catalog[%d]: debian sourceMode must be \"netinst\" or \"dvd\", got %q", i, e.SourceMode)
	}
	if e.SourceMode == "dvd" && e.Arch != "amd64" {
		return fmt.Errorf("cache: catalog[%d]: debian dvd sourceMode requires arch amd64 (DVDs are amd64-only), got %q", i, e.Arch)
	}
	if e.DvdCount < 0 {
		return fmt.Errorf("cache: catalog[%d]: debian dvdCount must be >= 0, got %d", i, e.DvdCount)
	}
	return nil
}

// ValidateTargetParams enforces the target param contract shared by the catalog
// loader and the /targets create API: params keys must be exactly the OS's
// RequiredParams, each non-empty and path-safe (they become cache dir + URL
// segments). Single knowledge site (was inline in pkg/http/api_targets.go).
func ValidateTargetParams(o ostype.OS, params map[string]string) error {
	required := o.RequiredParams()
	for k := range params {
		if !slices.Contains(required, k) {
			return fmt.Errorf("unexpected param: %s", k)
		}
	}
	for _, p := range required {
		v := params[p]
		if v == "" {
			return fmt.Errorf("missing required param: %s", p)
		}
		if err := ValidatePathParam(v); err != nil {
			return fmt.Errorf("invalid param %s: %w", p, err)
		}
	}
	return nil
}

// defaultCatalog builds the shipped default desired set from the existing flags
// when no catalog.yaml is present. Flag-derived (not a static embed) so an
// operator's --flatcarChannel / --talosSchematic / etc. overrides still drive
// the default set (design §5). The *set* is curated: Flatcar (primary channel +
// lts), Talos, and Debian (13 netinst amd64+arm64, enabled; 12 and 11 dvd
// amd64, seeded but DISABLED so a fresh install downloads nothing until an
// operator opts in by setting enabled:true for that entry in catalog.yaml —
// a full DVD set is ~44 GB, design §6.1. promote-dvd does NOT apply to these:
// it promotes an already-enabled netinst target, not a disabled dvd one).
// Fedora CoreOS is intentionally NOT in the default (operators add it via
// catalog.yaml or the API). retain 1 for flatcar/debian is a code constant (no
// flag exists); talos retain follows --talosRetainMinors.
func defaultCatalog() []CatalogEntry {
	arch := viper.GetString(config.FlatcarArchitecture)
	primary := viper.GetString(config.FlatcarChannel)

	entries := []CatalogEntry{
		{OS: "flatcar", Arch: arch, Retain: new(1), Spec: map[string]string{"channel": primary}},
	}
	// Always ship the Flatcar LTS channel — unless the primary flatcar channel
	// flag is already "lts", which would collide on identity (os,arch,params).
	if primary != "lts" {
		entries = append(entries, CatalogEntry{OS: "flatcar", Arch: arch, Retain: new(1),
			Spec: map[string]string{"channel": "lts"}})
	}
	entries = append(entries, CatalogEntry{OS: "talos", Arch: viper.GetString(config.TalosArchitecture),
		Retain: new(viper.GetInt(config.TalosRetainMinors)), Spec: map[string]string{"schematic": viper.GetString(config.TalosSchematic)}})

	// Debian: 13 (trixie) netinst on both arches, enabled by default. 12 and 11
	// dvd amd64, seeded but disabled — an operator opts in by setting
	// enabled:true for that entry in their own catalog.yaml (the catalog is
	// authoritative for enabled, so a PATCH would revert). promote-dvd is a
	// SEPARATE mechanism: it promotes an already-enabled netinst target (like
	// the seeded 13 entries) to dvd, and does not apply to these — they start
	// life already in dvd mode.
	entries = append(entries,
		CatalogEntry{OS: "debian", Arch: "amd64", Enabled: new(true), Retain: new(1), Spec: map[string]string{"channel": "13"}},
		CatalogEntry{OS: "debian", Arch: "arm64", Enabled: new(true), Retain: new(1), Spec: map[string]string{"channel": "13"}},
		CatalogEntry{OS: "debian", Arch: "amd64", Enabled: new(false), Retain: new(1), Spec: map[string]string{"channel": "12"}, SourceMode: "dvd", DvdCount: 1},
		CatalogEntry{OS: "debian", Arch: "amd64", Enabled: new(false), Retain: new(1), Spec: map[string]string{"channel": "11"}, SourceMode: "dvd", DvdCount: 1},
	)
	return entries
}

// LoadCatalog resolves the desired catalog at startup (fail-fast). Precedence:
//   - --catalogFile set and present    -> parse it
//   - --catalogFile set and missing    -> error (operator asked for a file)
//   - default <dataDir>/catalog.yaml present -> parse it
//   - default path missing             -> defaultCatalog() (flag-derived)
//
// The flag-derived default is validated too, so a malformed flag value fails
// startup exactly as an invalid file would.
func LoadCatalog() ([]CatalogEntry, error) {
	explicit := viper.GetString(config.CatalogFile)
	path := explicit
	if path == "" {
		path = filepath.Join(viper.GetString(config.DataDir), "catalog.yaml")
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if explicit != "" {
			return nil, fmt.Errorf("cache: catalog file %q not found", path)
		}
		entries := defaultCatalog()
		if err := validateCatalog(catalogFile{SchemaVersion: catalogSchemaVersion, Entries: entries}); err != nil {
			return nil, fmt.Errorf("cache: default catalog invalid (check channel/schematic/arch flags): %w", err)
		}
		return entries, nil
	case err != nil:
		return nil, fmt.Errorf("cache: read catalog %q: %w", path, err)
	default:
		return parseCatalog(data)
	}
}
