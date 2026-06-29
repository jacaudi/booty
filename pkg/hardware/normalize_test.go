package hardware

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestNormalizeMAC(t *testing.T) {
	const canonical = "aa:bb:cc:dd:ee:ff"
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "already canonical", in: "aa:bb:cc:dd:ee:ff", want: canonical},
		{name: "uppercase colon", in: "AA:BB:CC:DD:EE:FF", want: canonical},
		{name: "hyphen delimited", in: "AA-BB-CC-DD-EE-FF", want: canonical},
		{name: "cisco dotted", in: "aabb.ccdd.eeff", want: canonical},
		{name: "empty", in: "", wantErr: true},
		{name: "garbage", in: "does-not-exist", wantErr: true},
		{name: "too short", in: "aa:bb:cc", wantErr: true},
		{name: "non-hex", in: "zz:bb:cc:dd:ee:ff", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeMAC(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeMAC(%q): err = nil, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeMAC(%q): unexpected err = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeMAC(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A host written under one format must be found under a different format,
// because both normalize to the same canonical key.
func TestWriteThenGet_CrossFormatMatch(t *testing.T) {
	setupTempDB(t)

	if err := WriteMacAddress("AA-BB-CC-DD-EE-FF", Host{Hostname: "node-01"}); err != nil {
		t.Fatalf("WriteMacAddress: %v", err)
	}

	got, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("GetMacAddress (cross-format): %v", err)
	}
	if got == nil || got.Hostname != "node-01" {
		t.Errorf("cross-format lookup = %+v, want hostname node-01", got)
	}
}

// WriteMacAddress must store the canonical MAC in Host.MAC even if the caller
// supplied a non-canonical form.
func TestWriteMacAddress_CanonicalizesStoredMAC(t *testing.T) {
	setupTempDB(t)

	if err := WriteMacAddress("AA-BB-CC-DD-EE-FF", Host{MAC: "AA-BB-CC-DD-EE-FF", Hostname: "node-01"}); err != nil {
		t.Fatalf("WriteMacAddress: %v", err)
	}

	got, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("GetMacAddress: %v", err)
	}
	if got.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("stored Host.MAC = %q, want canonical aa:bb:cc:dd:ee:ff", got.MAC)
	}
}

func TestWriteMacAddress_RejectsInvalid(t *testing.T) {
	setupTempDB(t)

	if err := WriteMacAddress("not-a-mac", Host{Hostname: "garbage"}); err == nil {
		t.Fatalf("WriteMacAddress(invalid): err = nil, want error")
	}
	if err := WriteMacAddress("", Host{Hostname: "empty"}); err == nil {
		t.Fatalf("WriteMacAddress(empty): err = nil, want error")
	}

	// Nothing should have been stored.
	data, err := GetData()
	if err != nil {
		t.Fatalf("GetData: %v", err)
	}
	var bd BootyData
	if err := json.Unmarshal(data, &bd); err != nil {
		t.Fatalf("Unmarshal GetData: %v", err)
	}
	if len(bd.Hosts) != 0 {
		t.Errorf("invalid writes leaked into Hosts: %+v", bd.Hosts)
	}
}

// GetMacAddress on an invalid MAC must return an error (not ErrNotFound, not a
// panic) and must not pollute UnknownHosts with garbage.
func TestGetMacAddress_InvalidIsErrorNotMiss(t *testing.T) {
	setupTempDB(t)

	host, err := GetMacAddress("not-a-mac")
	if host != nil {
		t.Errorf("GetMacAddress(invalid): host = %+v, want nil", host)
	}
	if err == nil {
		t.Fatalf("GetMacAddress(invalid): err = nil, want error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("GetMacAddress(invalid): err = ErrNotFound, want validation error")
	}

	data, err := GetData()
	if err != nil {
		t.Fatalf("GetData: %v", err)
	}
	var bd BootyData
	if err := json.Unmarshal(data, &bd); err != nil {
		t.Fatalf("Unmarshal GetData: %v", err)
	}
	if len(bd.UnknownHosts) != 0 {
		t.Errorf("invalid MAC polluted UnknownHosts: %+v", bd.UnknownHosts)
	}
}

func TestRemoveMacAddress_RejectsInvalid(t *testing.T) {
	setupTempDB(t)

	if err := RemoveMacAddress("not-a-mac"); err == nil {
		t.Fatalf("RemoveMacAddress(invalid): err = nil, want error")
	}
}

// Load() must canonicalize legacy (non-canonical) on-disk keys so that
// post-upgrade lookups in canonical form still match old records.
func TestLoad_CanonicalizesLegacyKeys(t *testing.T) {
	viper.Reset()
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")

	// Legacy on-disk key: uppercase, hyphen-delimited. Host.MAC also legacy.
	writeFixture(t, dir, map[string]Host{
		"AA-BB-CC-DD-EE-FF": {MAC: "AA-BB-CC-DD-EE-FF", Hostname: "legacy-node"},
	})

	if err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("GetMacAddress (canonical) after legacy Load: %v", err)
	}
	if got == nil || got.Hostname != "legacy-node" {
		t.Fatalf("legacy lookup = %+v, want hostname legacy-node", got)
	}
	if got.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("Host.MAC after Load = %q, want canonical aa:bb:cc:dd:ee:ff", got.MAC)
	}
}

// Load() must skip (not crash on) invalid on-disk keys and keep the valid ones.
func TestLoad_SkipsInvalidLegacyKeys(t *testing.T) {
	viper.Reset()
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")

	writeFixture(t, dir, map[string]Host{
		"garbage-key":       {Hostname: "bad"},
		"aa:bb:cc:dd:ee:ff": {MAC: "aa:bb:cc:dd:ee:ff", Hostname: "good"},
	})

	if err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("GetMacAddress (valid key) after Load: %v", err)
	}
	if got == nil || got.Hostname != "good" {
		t.Errorf("valid key = %+v, want hostname good", got)
	}

	data, err := GetData()
	if err != nil {
		t.Fatalf("GetData: %v", err)
	}
	var bd BootyData
	if err := json.Unmarshal(data, &bd); err != nil {
		t.Fatalf("Unmarshal GetData: %v", err)
	}
	if len(bd.Hosts) != 1 {
		t.Errorf("expected only the valid host retained, got %+v", bd.Hosts)
	}
}
