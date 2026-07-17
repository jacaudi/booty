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
// download, so it is rejected up front (design M2/M3). debian is intentionally
// absent: Debian *target* support does not exist yet (design B1) — add it here
// (and revisit spec typing) when it lands.
var catalogArches = map[string][]string{
	"flatcar":       {"amd64", "arm64"},
	"fedora-coreos": {"x86_64"},
	"talos":         {"amd64", "arm64"},
}

// CatalogEntry is one declared cache target. Enabled/Retain are pointers so an
// omitted field is distinguishable from an explicit zero (default true / 1).
// Spec holds the OS-specific path-discriminating params (channel or schematic)
// and maps 1:1 to the target's RequiredParams.
type CatalogEntry struct {
	OS      string            `yaml:"os"`
	Arch    string            `yaml:"arch"`
	Enabled *bool             `yaml:"enabled"`
	Retain  *int              `yaml:"retain"`
	Spec    map[string]string `yaml:"spec"`
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
			return fmt.Errorf("cache: catalog[%d]: unsupported os %q (supported: flatcar, fedora-coreos, talos)", i, e.OS)
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
// lts) and Talos; Fedora CoreOS is intentionally NOT in the default (operators
// add it via catalog.yaml or the API). retain 1 for flatcar is a code constant
// (no flag exists); talos retain follows --talosRetainMinors.
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
