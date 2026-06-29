package hardware

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
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

// NormalizeMAC parses mac in any form net.ParseMAC accepts and returns the
// canonical lowercase colon-delimited key. It is the one source of truth for
// MAC canonicalization in booty. An empty/invalid MAC returns a wrapped error.
func NormalizeMAC(mac string) (string, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return "", fmt.Errorf("hardware: invalid MAC %q: %w", mac, err)
	}
	return hw.String(), nil
}

// ErrNotFound is returned by GetMacAddress when the MAC isn't registered.
var ErrNotFound = errors.New("hardware: host not found")

// store is the SQLite-backed host store. In production cmd/main.go injects a
// shared *db.Store via SetStore; in tests Load lazily opens one (see Load).
var (
	storeMu       sync.Mutex
	store         *db.Store
	storeInjected bool
)

// unknownHosts tracks MACs that contacted booty without a record — a runtime
// only "pending" list for the UI, reset on each Load (never persisted).
var (
	unknownMu    sync.Mutex
	unknownHosts = map[string]*Host{}
)

// SetStore injects the shared database store. Call it once at startup before
// Load. When set, Load uses this store rather than opening its own.
func SetStore(s *db.Store) {
	storeMu.Lock()
	store = s
	storeInjected = true
	storeMu.Unlock()
}

func currentStore() *db.Store {
	storeMu.Lock()
	defer storeMu.Unlock()
	return store
}

// Load prepares the host store and performs the one-time hardware.json import.
// If no store was injected (tests / standalone), it (re)opens a fresh store at
// config.DatabasePathValue(), preserving the pre-SQLite reset-per-Load
// semantics. The in-memory pending tracker is reset.
func Load() error {
	storeMu.Lock()
	if !storeInjected {
		if store != nil {
			_ = store.Close()
		}
		s, err := db.Open(config.DatabasePathValue())
		if err != nil {
			storeMu.Unlock()
			return fmt.Errorf("hardware: open db: %w", err)
		}
		store = s
	}
	st := store
	storeMu.Unlock()

	unknownMu.Lock()
	unknownHosts = map[string]*Host{}
	unknownMu.Unlock()

	return importLegacyJSON(st)
}

// legacyJSONPath is the old on-disk host DB location (<DataDir>/<HardwareMap>).
func legacyJSONPath() string {
	return filepath.Join(viper.GetString(config.DataDir), viper.GetString(config.HardwareMap))
}

// importLegacyJSON imports a pre-SQLite hardware.json exactly once: each host is
// canonicalized (invalid MACs logged and skipped, as before) and upserted, then
// the file is renamed to <name>.migrated so a later startup skips it. A missing
// file is the steady state (already migrated / fresh install) and is not an
// error; a malformed file IS an error (matching the old Load contract).
func importLegacyJSON(st *db.Store) error {
	path := legacyJSONPath()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("hardware: read %s: %w", path, err)
	}

	var onDisk map[string]*Host
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return fmt.Errorf("hardware: parse %s: %w", path, err)
	}
	for rawKey, host := range onDisk {
		key, err := NormalizeMAC(rawKey)
		if err != nil {
			slog.Warn("hardware: skipping host with invalid MAC key during migration", "rawKey", rawKey, "path", path, "err", err)
			continue
		}
		if host == nil {
			host = &Host{}
		}
		host.MAC = key
		if err := st.UpsertHost(toDBHost(*host)); err != nil {
			return fmt.Errorf("hardware: migrate host %s: %w", key, err)
		}
	}

	migrated := path + ".migrated"
	if err := os.Rename(path, migrated); err != nil {
		slog.Warn("hardware: could not rename migrated hardware.json", "path", path, "err", err)
	} else {
		slog.Info("hardware: imported hardware.json into SQLite", "from", path, "to", migrated)
	}
	return nil
}

func toDBHost(h Host) db.Host {
	return db.Host{
		MAC: h.MAC, Hostname: h.Hostname, IP: h.IP, Booted: h.Booted,
		IgnitionFile: h.IgnitionFile, OS: h.OS, DoInstall: h.DoInstall, Schematic: h.Schematic,
	}
}

func fromDBHost(d db.Host) *Host {
	return &Host{
		MAC: d.MAC, Hostname: d.Hostname, IP: d.IP, Booted: d.Booted,
		IgnitionFile: d.IgnitionFile, OS: d.OS, DoInstall: d.DoInstall, Schematic: d.Schematic,
	}
}

// GetData returns the JSON-marshaled BootyData (registered + unknown hosts).
func GetData() ([]byte, error) {
	hosts := map[string]*Host{}
	if st := currentStore(); st != nil {
		list, err := st.ListHosts()
		if err != nil {
			return nil, fmt.Errorf("hardware: list hosts: %w", err)
		}
		for _, d := range list {
			hosts[d.MAC] = fromDBHost(d)
		}
	}

	unknownMu.Lock()
	unknown := make(map[string]*Host, len(unknownHosts))
	maps.Copy(unknown, unknownHosts)
	unknownMu.Unlock()

	out, err := json.Marshal(BootyData{Hosts: hosts, UnknownHosts: unknown})
	if err != nil {
		return nil, fmt.Errorf("hardware: marshal: %w", err)
	}
	return out, nil
}

// GetMacAddress returns a fresh *Host for the canonicalized mac, or ErrNotFound.
// On a miss the canonical MAC is tracked in the pending list. Unlike the old
// map-backed store, the returned *Host does NOT alias shared state, so callers
// may mutate the copy freely. An invalid/empty MAC returns a validation error
// distinct from ErrNotFound and does not pollute the pending list.
func GetMacAddress(mac string) (*Host, error) {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return nil, err
	}
	st := currentStore()
	if st == nil {
		trackUnknown(key)
		return nil, ErrNotFound
	}
	d, err := st.GetHost(key)
	if errors.Is(err, sql.ErrNoRows) {
		trackUnknown(key)
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hardware: get host %s: %w", key, err)
	}
	clearUnknown(key)
	return fromDBHost(*d), nil
}

// WriteMacAddress upserts host for the canonicalized mac and clears it from the
// pending list. An invalid/empty MAC is rejected and never stored. The write is
// transactional in SQLite, so there is no in-memory state to roll back.
func WriteMacAddress(mac string, host Host) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	host.MAC = key
	st := currentStore()
	if st == nil {
		return errors.New("hardware: store not initialized")
	}
	if err := st.UpsertHost(toDBHost(host)); err != nil {
		return err
	}
	clearUnknown(key)
	return nil
}

// RemoveMacAddress deletes the host for the canonicalized mac. Idempotent: a
// valid-but-absent MAC is a no-op returning nil. An invalid MAC is rejected.
func RemoveMacAddress(mac string) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	st := currentStore()
	if st == nil {
		return nil
	}
	return st.DeleteHost(key)
}

func trackUnknown(key string) {
	unknownMu.Lock()
	unknownHosts[key] = &Host{}
	unknownMu.Unlock()
}

func clearUnknown(key string) {
	unknownMu.Lock()
	delete(unknownHosts, key)
	unknownMu.Unlock()
}
