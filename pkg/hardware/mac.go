package hardware

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// Host describes a registered (or seen-but-not-registered) machine.
type Host struct {
	MAC          string `json:"mac"`
	Hostname     string `json:"hostname"`
	IP           string `json:"ip"`
	Booted       string `json:"booted"`
	IgnitionFile string `json:"ignitionFile,omitempty"`
	OS           string `json:"os,omitempty"`
	OSTreeImage  string `json:"ostreeImage,omitempty"`
	DoInstall    bool   `json:"doInstall,omitempty"`
}

// BootyData is the JSON shape returned by GetData and consumed by the UI.
type BootyData struct {
	Hosts        map[string]*Host `json:"hosts"`
	UnknownHosts map[string]*Host `json:"unknownHosts"`
}

// ErrNotFound is returned by GetMacAddress when the MAC isn't registered.
// Callers should distinguish this from real I/O errors and treat it as a
// "miss," not a failure.
var ErrNotFound = errors.New("hardware: host not found")

// Package-level state. fileMutex guards both maps and disk persistence.
// Callers must not mutate any *Host returned by package functions; the
// pointer aliases the live map entry.
var (
	fileMutex    sync.Mutex
	HostDB       map[string]*Host
	UnknownHosts map[string]*Host
)

func init() {
	HostDB = make(map[string]*Host)
	UnknownHosts = make(map[string]*Host)
}

// Load reads <DataDir>/<HardwareMap> from disk into HostDB. Must be called
// once at startup before any HTTP/TFTP server starts. UnknownHosts is also
// reset (it's a runtime-only tracker for the UI's "pending" list). If the
// file doesn't exist, Load treats the DB as empty (expected on first boot)
// and returns nil. Any other read or parse error is returned to the caller.
func Load() error {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	HostDB = make(map[string]*Host)
	UnknownHosts = make(map[string]*Host)

	path := dbPath()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		// fall through to unmarshal
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("hardware: read %s: %w", path, err)
	}

	if err := json.Unmarshal(data, &HostDB); err != nil {
		return fmt.Errorf("hardware: parse %s: %w", path, err)
	}
	return nil
}

// GetData returns the JSON-marshaled BootyData (registered + unknown hosts).
func GetData() ([]byte, error) {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	bd := BootyData{
		Hosts:        HostDB,
		UnknownHosts: UnknownHosts,
	}
	out, err := json.Marshal(bd)
	if err != nil {
		return nil, fmt.Errorf("hardware: marshal: %w", err)
	}
	return out, nil
}

// GetMacAddress returns the host registered for mac, or (nil, ErrNotFound)
// if the MAC isn't registered. On a miss, the MAC is added to UnknownHosts
// for the UI's pending list (existing behavior).
//
// The returned *Host aliases the live map entry; callers must not mutate it.
func GetMacAddress(mac string) (*Host, error) {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	if h, ok := HostDB[mac]; ok {
		delete(UnknownHosts, mac)
		return h, nil
	}
	UnknownHosts[mac] = &Host{}
	return nil, ErrNotFound
}

// WriteMacAddress upserts host for mac, persists atomically, and removes
// the MAC from UnknownHosts on success. On persist failure, in-memory
// state is rolled back so HostDB and disk remain consistent.
func WriteMacAddress(mac string, host Host) error {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	prev, existed := HostDB[mac]
	HostDB[mac] = &host

	payload, err := json.Marshal(HostDB)
	if err != nil {
		// Restore in-memory state.
		if existed {
			HostDB[mac] = prev
		} else {
			delete(HostDB, mac)
		}
		return fmt.Errorf("hardware: marshal: %w", err)
	}

	if err := persist(payload); err != nil {
		if existed {
			HostDB[mac] = prev
		} else {
			delete(HostDB, mac)
		}
		return err
	}

	// Success path: only now do we remove the MAC from UnknownHosts.
	delete(UnknownHosts, mac)
	return nil
}

// RemoveMacAddress removes the host record from HostDB and persists.
// On persist failure, in-memory state is restored. Idempotent: a remove
// against a not-present MAC is a no-op and returns nil.
func RemoveMacAddress(mac string) error {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	prev, existed := HostDB[mac]
	if !existed {
		return nil
	}
	delete(HostDB, mac)

	payload, err := json.Marshal(HostDB)
	if err != nil {
		HostDB[mac] = prev
		return fmt.Errorf("hardware: marshal: %w", err)
	}

	if err := persist(payload); err != nil {
		HostDB[mac] = prev
		return err
	}
	return nil
}

// dbPath returns the absolute path to the hardware database file.
func dbPath() string {
	return filepath.Join(viper.GetString(config.DataDir), viper.GetString(config.HardwareMap))
}

// persist writes payload atomically to dbPath() via temp file + rename.
// The temp file is created in the same directory so the rename is atomic
// at the filesystem level on POSIX. Sync is called before close so a
// crash between rename and the next write doesn't leave a renamed-but-
// empty file. Caller must hold fileMutex.
func persist(payload []byte) error {
	target := dbPath()
	dir := filepath.Dir(target)

	tmp, err := os.CreateTemp(dir, "hardware-*.json")
	if err != nil {
		return fmt.Errorf("hardware: create temp: %w", err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hardware: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hardware: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("hardware: close temp: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("hardware: rename temp -> %s: %w", target, err)
	}
	success = true
	return nil
}
