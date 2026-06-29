package hardware

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// setupTempDB points DataDir at a fresh tempdir and Loads (lazy-opening a fresh
// SQLite store at <dir>/booty.db). No store is injected, so Load reopens.
func setupTempDB(t *testing.T) string {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	if err := Load(); err != nil {
		t.Fatalf("setupTempDB: Load failed: %v", err)
	}
	return dir
}

// writeFixture writes a legacy hardware.json so Load() imports it.
func writeFixture(t *testing.T, dir string, hosts map[string]Host) {
	t.Helper()
	m := make(map[string]*Host, len(hosts))
	for k, v := range hosts {
		v := v
		m[k] = &v
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("writeFixture: marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hardware.json"), data, 0o644); err != nil {
		t.Fatalf("writeFixture: write: %v", err)
	}
}

func TestLoad_ImportsLegacyJSON(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	writeFixture(t, dir, map[string]Host{
		"aa:bb:cc:dd:ee:ff": {MAC: "aa:bb:cc:dd:ee:ff", Hostname: "node-01"},
	})

	if err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("GetMacAddress: %v", err)
	}
	if got == nil || got.Hostname != "node-01" {
		t.Errorf("GetMacAddress = %+v, want hostname node-01", got)
	}
	// The legacy file is renamed so the import runs once.
	if _, err := os.Stat(filepath.Join(dir, "hardware.json")); !os.IsNotExist(err) {
		t.Errorf("hardware.json should have been renamed after import; stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hardware.json.migrated")); err != nil {
		t.Errorf("expected hardware.json.migrated marker: %v", err)
	}
}

func TestLoad_MissingFileIsNoOp(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")

	if err := Load(); err != nil {
		t.Errorf("Load on missing file: err = %v, want nil", err)
	}
	_, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMacAddress on empty DB: err = %v, want ErrNotFound", err)
	}
}

func TestLoad_MalformedLegacyJSONErrors(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	if err := os.WriteFile(filepath.Join(dir, "hardware.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Load(); err == nil {
		t.Errorf("Load on malformed legacy file: err = nil, want error")
	}
}

func TestGetMacAddress_MissingMacReturnsErrNotFound(t *testing.T) {
	setupTempDB(t)
	host, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if host != nil {
		t.Errorf("GetMacAddress: host = %+v, want nil", host)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMacAddress: err = %v, want ErrNotFound", err)
	}
}

func TestGetMacAddress_TracksMissAsUnknownHost(t *testing.T) {
	setupTempDB(t)
	_, _ = GetMacAddress("aa:bb:cc:dd:ee:ff")

	data, err := GetData()
	if err != nil {
		t.Fatalf("GetData: %v", err)
	}
	var bd BootyData
	if err := json.Unmarshal(data, &bd); err != nil {
		t.Fatalf("Unmarshal GetData: %v", err)
	}
	if _, ok := bd.UnknownHosts["aa:bb:cc:dd:ee:ff"]; !ok {
		t.Errorf("expected unknown MAC in UnknownHosts, got %+v", bd.UnknownHosts)
	}
}

func TestWriteMacAddress_PersistsAcrossReload(t *testing.T) {
	dir := setupTempDB(t)
	if err := WriteMacAddress("aa:bb:cc:dd:ee:ff", Host{MAC: "aa:bb:cc:dd:ee:ff", Hostname: "node-01"}); err != nil {
		t.Fatalf("WriteMacAddress: %v", err)
	}
	// Reload (reopens the same booty.db); the row persists in SQLite.
	if err := Load(); err != nil {
		t.Fatalf("Load after write: %v", err)
	}
	got, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("GetMacAddress after reload: %v", err)
	}
	if got == nil || got.Hostname != "node-01" {
		t.Errorf("after reload: got %+v, want hostname node-01", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "booty.db")); err != nil {
		t.Errorf("booty.db not at expected path: %v", err)
	}
}

func TestWriteMacAddress_ConcurrentWritesAllLand(t *testing.T) {
	setupTempDB(t)
	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			mac := fakeMAC(i)
			if err := WriteMacAddress(mac, Host{MAC: mac, Hostname: "node"}); err != nil {
				t.Errorf("WriteMacAddress(%s): %v", mac, err)
			}
		})
	}
	wg.Wait()

	if err := Load(); err != nil {
		t.Fatalf("Load after concurrent writes: %v", err)
	}
	for i := range n {
		mac := fakeMAC(i)
		got, err := GetMacAddress(mac)
		if err != nil || got == nil {
			t.Errorf("GetMacAddress(%s): got=%v err=%v", mac, got, err)
		}
	}
}

func fakeMAC(i int) string {
	const hex = "0123456789abcdef"
	hi := hex[(i>>4)&0xf]
	lo := hex[i&0xf]
	return "aa:bb:cc:dd:" + string([]byte{hi, lo}) + ":01"
}

func TestRemoveMacAddress_RemovesAndPersists(t *testing.T) {
	setupTempDB(t)
	if err := WriteMacAddress("aa:bb:cc:dd:ee:ff", Host{MAC: "aa:bb:cc:dd:ee:ff", Hostname: "node-01"}); err != nil {
		t.Fatalf("WriteMacAddress: %v", err)
	}
	if err := RemoveMacAddress("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatalf("RemoveMacAddress: %v", err)
	}
	if _, err := GetMacAddress("aa:bb:cc:dd:ee:ff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after remove: err = %v, want ErrNotFound", err)
	}
	if err := Load(); err != nil {
		t.Fatalf("Load after remove: %v", err)
	}
	if _, err := GetMacAddress("aa:bb:cc:dd:ee:ff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after reload+remove: err = %v, want ErrNotFound", err)
	}
}

func TestRemoveMacAddress_OnMissingMacIsIdempotent(t *testing.T) {
	setupTempDB(t)
	if err := RemoveMacAddress("11:22:33:44:55:66"); err != nil {
		t.Errorf("RemoveMacAddress on missing: err = %v, want nil", err)
	}
}

func TestLoad_ConcurrentWithDBOpsIsRaceClean(t *testing.T) {
	dir := setupTempDB(t) // existing helper: DataDir set, store lazily opened (not injected)
	_ = dir

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers/writers hammering the store while Load reopens it repeatedly.
	for i := range 8 {
		wg.Go(func() {
			mac := fakeMAC(i)
			for {
				select {
				case <-stop:
					return
				default:
					if err := WriteMacAddress(mac, Host{MAC: mac, Hostname: "n"}); err != nil {
						t.Errorf("WriteMacAddress concurrent with Load: %v", err)
					}
					if _, err := GetMacAddress(mac); err != nil && !errors.Is(err, ErrNotFound) {
						t.Errorf("GetMacAddress unexpected error concurrent with Load: %v", err)
					}
					if _, err := GetData(); err != nil {
						t.Errorf("GetData concurrent with Load: %v", err)
					}
				}
			}
		})
	}
	for range 20 {
		if err := Load(); err != nil {
			t.Errorf("Load during concurrent ops: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestGetData_IncludesRegisteredAndUnknown(t *testing.T) {
	setupTempDB(t)
	if err := WriteMacAddress("aa:bb:cc:dd:ee:ff", Host{MAC: "aa:bb:cc:dd:ee:ff", Hostname: "node-01"}); err != nil {
		t.Fatalf("WriteMacAddress: %v", err)
	}
	_, _ = GetMacAddress("11:22:33:44:55:66") // miss → tracked unknown

	data, err := GetData()
	if err != nil {
		t.Fatalf("GetData: %v", err)
	}
	var bd BootyData
	if err := json.Unmarshal(data, &bd); err != nil {
		t.Fatalf("Unmarshal GetData: %v", err)
	}
	if _, ok := bd.Hosts["aa:bb:cc:dd:ee:ff"]; !ok {
		t.Errorf("registered host missing from GetData; got %+v", bd.Hosts)
	}
	if _, ok := bd.UnknownHosts["11:22:33:44:55:66"]; !ok {
		t.Errorf("unknown host missing from GetData; got %+v", bd.UnknownHosts)
	}
}
