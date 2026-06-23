package hardware

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	DoInstall    bool   `json:"doInstall,omitempty"`
	Schematic    string `json:"schematic,omitempty"`
}

// BootyData is the JSON shape returned by GetData and consumed by the UI.
type BootyData struct {
	Hosts        map[string]*Host `json:"hosts"`
	UnknownHosts map[string]*Host `json:"unknownHosts"`
}

// NormalizeMAC parses mac in any form net.ParseMAC accepts (colon-, hyphen-,
// or Cisco dot-delimited) and returns the single canonical representation used
// as the host-store key: lowercase, colon-delimited (net.HardwareAddr.String()).
// It is the one source of truth for MAC canonicalization in booty: every DB
// key boundary funnels through it so that, e.g., "AA-BB-CC-DD-EE-FF" and
// "aa:bb:cc:dd:ee:ff" address the same record. An empty or invalid MAC returns
// a wrapped error and is never stored.
func NormalizeMAC(mac string) (string, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return "", fmt.Errorf("hardware: invalid MAC %q: %w", mac, err)
	}
	return hw.String(), nil
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

	// Unmarshal into a local map keyed by whatever is on disk, then rebuild
	// HostDB with canonical keys. Existing files may hold non-canonical keys
	// (e.g. "AA-BB-CC-DD-EE-FF") from before MAC normalization; canonicalizing
	// on read keeps post-upgrade lookups (which arrive canonical) matching old
	// records. This is best-effort: valid keys are migrated and their host.MAC
	// is canonicalized to match; invalid keys are logged and skipped rather
	// than failing the whole load. Migration is in-memory only — the canonical
	// form is rewritten to disk on the next WriteMacAddress.
	var onDisk map[string]*Host
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return fmt.Errorf("hardware: parse %s: %w", path, err)
	}

	for rawKey, host := range onDisk {
		key, err := NormalizeMAC(rawKey)
		if err != nil {
			slog.Warn("hardware: skipping host with invalid MAC key", "rawKey", rawKey, "path", path, "err", err)
			continue
		}
		if host == nil {
			host = &Host{}
		}
		if _, dup := HostDB[key]; dup {
			slog.Warn("hardware: duplicate MAC after normalization; keeping last", "mac", key, "path", path)
		}
		host.MAC = key
		HostDB[key] = host
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
// if the MAC isn't registered. The MAC is canonicalized first, so a lookup in
// any accepted form matches a record stored in any other form. On a miss, the
// canonical MAC is added to UnknownHosts for the UI's pending list (existing
// behavior). An invalid/empty MAC returns a wrapped validation error distinct
// from ErrNotFound and does NOT pollute UnknownHosts.
//
// The returned *Host aliases the live map entry; callers must not mutate it.
func GetMacAddress(mac string) (*Host, error) {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return nil, err
	}

	fileMutex.Lock()
	defer fileMutex.Unlock()

	if h, ok := HostDB[key]; ok {
		delete(UnknownHosts, key)
		return h, nil
	}
	UnknownHosts[key] = &Host{}
	return nil, ErrNotFound
}

// WriteMacAddress upserts host for mac, persists atomically, and removes
// the MAC from UnknownHosts on success. The MAC is canonicalized first and
// used both as the store key and as host.MAC, so an invalid/empty MAC is
// rejected with a wrapped error and never stored. On persist failure,
// in-memory state is rolled back so HostDB and disk remain consistent.
func WriteMacAddress(mac string, host Host) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	host.MAC = key

	fileMutex.Lock()
	defer fileMutex.Unlock()

	prev, existed := HostDB[key]
	HostDB[key] = &host

	payload, err := json.Marshal(HostDB)
	if err != nil {
		// Restore in-memory state.
		if existed {
			HostDB[key] = prev
		} else {
			delete(HostDB, key)
		}
		return fmt.Errorf("hardware: marshal: %w", err)
	}

	if err := persist(payload); err != nil {
		if existed {
			HostDB[key] = prev
		} else {
			delete(HostDB, key)
		}
		return err
	}

	// Success path: only now do we remove the MAC from UnknownHosts.
	delete(UnknownHosts, key)
	return nil
}

// RemoveMacAddress removes the host record from HostDB and persists. The MAC
// is canonicalized first, so a record stored under any accepted form is removed
// regardless of the form supplied here; an invalid/empty MAC is rejected with a
// wrapped error. On persist failure, in-memory state is restored. Idempotent: a
// remove against a valid-but-absent MAC is a no-op and returns nil.
func RemoveMacAddress(mac string) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}

	fileMutex.Lock()
	defer fileMutex.Unlock()

	prev, existed := HostDB[key]
	if !existed {
		return nil
	}
	delete(HostDB, key)

	payload, err := json.Marshal(HostDB)
	if err != nil {
		HostDB[key] = prev
		return fmt.Errorf("hardware: marshal: %w", err)
	}

	if err := persist(payload); err != nil {
		HostDB[key] = prev
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
