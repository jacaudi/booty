package cache

import (
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestParseCatalog_ShippedDefaultRoundTrips(t *testing.T) {
	src := []byte(`schemaVersion: 1
catalog:
  - os: flatcar
    arch: amd64
    retain: 1
    spec: {channel: stable}
  - os: fedora-coreos
    arch: x86_64
    retain: 1
    spec: {channel: stable}
  - os: talos
    arch: amd64
    retain: 3
    spec: {schematic: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba}
`)
	entries, err := parseCatalog(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[2].OS != "talos" || entries[2].Spec["schematic"] == "" || *entries[2].Retain != 3 {
		t.Errorf("talos entry = %+v", entries[2])
	}
}

func TestParseCatalog_RejectsUnknownSchemaVersion(t *testing.T) {
	_, err := parseCatalog([]byte("schemaVersion: 2\ncatalog: []\n"))
	if err == nil {
		t.Fatal("want error for schemaVersion 2")
	}
}

func TestParseCatalog_RejectsUnknownEntryKey(t *testing.T) {
	// WithKnownFields must reject a misspelled field.
	_, err := parseCatalog([]byte("schemaVersion: 1\ncatalog:\n  - os: talos\n    arch: amd64\n    retian: 1\n    spec: {schematic: abc}\n"))
	if err == nil {
		t.Fatal("want error for unknown key 'retian'")
	}
}

func TestParseCatalog_RejectsDebian(t *testing.T) {
	_, err := parseCatalog([]byte("schemaVersion: 1\ncatalog:\n  - os: debian\n    arch: amd64\n    spec: {channel: stable}\n"))
	if err == nil {
		t.Fatal("want error: debian not yet supported in catalog")
	}
}

func TestParseCatalog_RejectsWrongArchForOS(t *testing.T) {
	// fedora-coreos uses x86_64, not amd64.
	_, err := parseCatalog([]byte("schemaVersion: 1\ncatalog:\n  - os: fedora-coreos\n    arch: amd64\n    spec: {channel: stable}\n"))
	if err == nil {
		t.Fatal("want error for fedora-coreos/amd64")
	}
}

func TestParseCatalog_RejectsUnexpectedSpecKey(t *testing.T) {
	_, err := parseCatalog([]byte("schemaVersion: 1\ncatalog:\n  - os: talos\n    arch: amd64\n    spec: {channel: stable}\n"))
	if err == nil {
		t.Fatal("want error: talos requires 'schematic', not 'channel'")
	}
}

func TestParseCatalog_RejectsUnsafeParam(t *testing.T) {
	_, err := parseCatalog([]byte("schemaVersion: 1\ncatalog:\n  - os: flatcar\n    arch: amd64\n    spec: {channel: \"../etc\"}\n"))
	if err == nil {
		t.Fatal("want error: channel not path-safe")
	}
}

func TestDefaultCatalog_CuratedSet(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, config.DefaultTalosSchematic)
	viper.Set(config.TalosRetainMinors, 3)

	got := defaultCatalog()
	// Flatcar stable + Flatcar lts + Talos; NO fedora-coreos.
	if len(got) != 3 {
		t.Fatalf("defaultCatalog() = %d entries, want 3: %+v", len(got), got)
	}
	channels := map[string]bool{}
	for _, e := range got {
		if e.OS == "fedora-coreos" {
			t.Errorf("fedora-coreos must NOT be in the default set: %+v", e)
		}
		if e.OS == "flatcar" {
			channels[e.Spec["channel"]] = true
		}
	}
	if !channels["stable"] || !channels["lts"] {
		t.Errorf("flatcar default channels = %v, want stable+lts", channels)
	}
	if err := validateCatalog(catalogFile{SchemaVersion: 1, Entries: got}); err != nil {
		t.Fatalf("default catalog must validate: %v", err)
	}
}

func TestDefaultCatalog_PrimaryLtsDoesNotDuplicate(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.FlatcarChannel, "lts") // primary already lts
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosSchematic, config.DefaultTalosSchematic)
	viper.Set(config.TalosRetainMinors, 3)

	got := defaultCatalog()
	// One flatcar (lts) + one talos; the lts entry is not duplicated.
	if len(got) != 2 {
		t.Fatalf("defaultCatalog() = %d entries, want 2 (no lts dup): %+v", len(got), got)
	}
	if err := validateCatalog(catalogFile{SchemaVersion: 1, Entries: got}); err != nil {
		t.Fatalf("must validate (no duplicate-identity error): %v", err)
	}
}
