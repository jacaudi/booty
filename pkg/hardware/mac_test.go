package hardware

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// setupTempDB sets viper to use a fresh tempdir as DataDir, with hardware.json
// as the map filename, and clears the in-memory state by calling Load against
// the (likely empty) tempdir.
func setupTempDB(t *testing.T) string {
	t.Helper()
	viper.Reset()
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	if err := Load(); err != nil {
		t.Fatalf("setupTempDB: initial Load failed: %v", err)
	}
	return dir
}

// writeFixture writes hardware.json directly to disk, bypassing the package.
// Use before calling Load() to seed a known on-disk state.
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

func TestLoad_RoundTripsRegisteredHost(t *testing.T) {
	viper.Reset()
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
}

func TestLoad_MissingFileIsNoOp(t *testing.T) {
	viper.Reset()
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	// No file at dir/hardware.json — Load should treat as empty.

	if err := Load(); err != nil {
		t.Errorf("Load on missing file: err = %v, want nil", err)
	}
	// HostDB should be empty: any GetMacAddress should return ErrNotFound.
	_, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMacAddress on empty DB: err = %v, want ErrNotFound", err)
	}
}

func TestLoad_MalformedFileErrors(t *testing.T) {
	viper.Reset()
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	if err := os.WriteFile(filepath.Join(dir, "hardware.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := Load(); err == nil {
		t.Errorf("Load on malformed file: err = nil, want error")
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

func TestWriteMacAddress_PersistsAcrossLoad(t *testing.T) {
	dir := setupTempDB(t)

	if err := WriteMacAddress("aa:bb:cc:dd:ee:ff", Host{
		MAC:      "aa:bb:cc:dd:ee:ff",
		Hostname: "node-01",
	}); err != nil {
		t.Fatalf("WriteMacAddress: %v", err)
	}

	// Simulate a restart by re-Loading from disk.
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

	// Sanity: file actually exists at dir.
	if _, err := os.Stat(filepath.Join(dir, "hardware.json")); err != nil {
		t.Errorf("hardware.json not at expected path: %v", err)
	}
}

func TestWriteMacAddress_ConcurrentWritesAllLand(t *testing.T) {
	setupTempDB(t)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			mac := fakeMAC(i)
			err := WriteMacAddress(mac, Host{MAC: mac, Hostname: "node"})
			if err != nil {
				t.Errorf("WriteMacAddress(%s): %v", mac, err)
			}
		}()
	}
	wg.Wait()

	// Re-load from disk and verify all N hosts present and the file is valid.
	if err := Load(); err != nil {
		t.Fatalf("Load after concurrent writes: %v", err)
	}
	for i := 0; i < n; i++ {
		mac := fakeMAC(i)
		got, err := GetMacAddress(mac)
		if err != nil {
			t.Errorf("GetMacAddress(%s): %v", mac, err)
			continue
		}
		if got == nil {
			t.Errorf("GetMacAddress(%s): nil", mac)
		}
	}
}

func fakeMAC(i int) string {
	const hex = "0123456789abcdef"
	hi := hex[(i>>4)&0xf]
	lo := hex[i&0xf]
	return "aa:bb:cc:dd:" + string([]byte{hi, lo}) + ":01"
}

func TestWriteMacAddress_RollsBackOnPersistFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based rollback test is POSIX-only")
	}

	dir := setupTempDB(t)
	// Make the data dir read-only so CreateTemp/Rename fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() {
		// Restore so t.TempDir() can clean up.
		_ = os.Chmod(dir, 0o700)
	})

	err := WriteMacAddress("aa:bb:cc:dd:ee:ff", Host{
		MAC:      "aa:bb:cc:dd:ee:ff",
		Hostname: "should-not-persist",
	})
	if err == nil {
		t.Fatalf("WriteMacAddress: err = nil, want error from read-only dir")
	}

	// Verify in-memory state was rolled back.
	got, lookupErr := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if !errors.Is(lookupErr, ErrNotFound) {
		t.Errorf("after rollback, GetMacAddress: got=%+v err=%v, want ErrNotFound", got, lookupErr)
	}
}

func TestRemoveMacAddress_RemovesAndPersists(t *testing.T) {
	setupTempDB(t)

	if err := WriteMacAddress("aa:bb:cc:dd:ee:ff", Host{MAC: "aa:bb:cc:dd:ee:ff", Hostname: "node-01"}); err != nil {
		t.Fatalf("WriteMacAddress: %v", err)
	}
	if err := RemoveMacAddress("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatalf("RemoveMacAddress: %v", err)
	}

	_, err := GetMacAddress("aa:bb:cc:dd:ee:ff")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("after remove, GetMacAddress: err = %v, want ErrNotFound", err)
	}

	// Persists across reload.
	if err := Load(); err != nil {
		t.Fatalf("Load after remove: %v", err)
	}
	_, err = GetMacAddress("aa:bb:cc:dd:ee:ff")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("after reload+remove, GetMacAddress: err = %v, want ErrNotFound", err)
	}
}

func TestRemoveMacAddress_OnMissingMacIsIdempotent(t *testing.T) {
	setupTempDB(t)

	// A valid-but-absent MAC: remove is idempotent and returns nil.
	if err := RemoveMacAddress("11:22:33:44:55:66"); err != nil {
		t.Errorf("RemoveMacAddress on missing: err = %v, want nil", err)
	}
}

func TestGetData_IncludesRegisteredAndUnknown(t *testing.T) {
	setupTempDB(t)

	if err := WriteMacAddress("aa:bb:cc:dd:ee:ff", Host{MAC: "aa:bb:cc:dd:ee:ff", Hostname: "node-01"}); err != nil {
		t.Fatalf("WriteMacAddress: %v", err)
	}
	_, _ = GetMacAddress("11:22:33:44:55:66") // miss → tracked as unknown

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
