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

	Approved       bool   `json:"approved,omitzero"`
	BootMode       string `json:"bootMode,omitzero"`
	AssignedOS     string `json:"assignedOS,omitzero"`
	AssignedArch   string `json:"assignedArch,omitzero"`
	AssignedParams string `json:"assignedParams,omitzero"`
	UUID           string `json:"uuid,omitzero"`
	Serial         string `json:"serial,omitzero"`
	ConfigID       *int64 `json:"configId,omitzero"` // hosts.config_id (P4); omitzero matches adjacent P1c fields
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
	storeMu       sync.RWMutex
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
// Passing nil resets the injection so a subsequent Load() opens its own store;
// this is used by tests to restore package state after a test-injected store is
// closed.
func SetStore(s *db.Store) {
	storeMu.Lock()
	store = s
	storeInjected = (s != nil)
	storeMu.Unlock()
}

// withRLockedStore runs fn with the current store under a read lock, so Load
// (which takes the write lock to close+reopen) cannot swap the handle out from
// under an in-flight DB op. fn must not call Load. A nil store yields the
// supplied zero behavior via the bool.
func withRLockedStore(fn func(st *db.Store) error) (bool, error) {
	storeMu.RLock()
	defer storeMu.RUnlock()
	if store == nil {
		return false, nil
	}
	return true, fn(store)
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
//
// Once an import (or a no-file steady-state) is recorded, the meta flag
// "hardware_import_done"="1" gates all subsequent calls so a stale
// hardware.json that reappears (e.g. due to a failed rename) is never
// re-imported and cannot resurrect a host deleted from SQLite between restarts.
func importLegacyJSON(st *db.Store) error {
	// Skip entirely (don't even read the file) once a prior import completed, so
	// a failed .migrated rename can't resurrect a host deleted between restarts.
	if v, err := st.GetMeta("hardware_import_done"); err == nil && v == "1" {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("hardware: read import flag: %w", err)
	}

	path := legacyJSONPath()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Nothing to import, but record that we've reached steady state so a file
		// that appears later (stale copy) is never imported.
		if err := st.SetMeta("hardware_import_done", "1"); err != nil {
			return fmt.Errorf("hardware: set import flag: %w", err)
		}
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

	// Set the flag before the rename: if the rename fails the flag still
	// prevents a resurrection on the next start. A crash between here and
	// the rename leaves the flag set and the file present — the next start
	// sees the flag and skips the file (correct). A crash mid-loop (before
	// this point) leaves the flag unset so the next start re-imports
	// idempotently via UpsertHost.
	if err := st.SetMeta("hardware_import_done", "1"); err != nil {
		return fmt.Errorf("hardware: set import flag: %w", err)
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
		Approved: h.Approved, BootMode: h.BootMode, AssignedOS: h.AssignedOS,
		AssignedArch: h.AssignedArch, AssignedParams: h.AssignedParams, UUID: h.UUID, Serial: h.Serial,
		ConfigID: h.ConfigID,
	}
}

func fromDBHost(d db.Host) *Host {
	return &Host{
		MAC: d.MAC, Hostname: d.Hostname, IP: d.IP, Booted: d.Booted,
		IgnitionFile: d.IgnitionFile, OS: d.OS, DoInstall: d.DoInstall, Schematic: d.Schematic,
		Approved: d.Approved, BootMode: d.BootMode, AssignedOS: d.AssignedOS,
		AssignedArch: d.AssignedArch, AssignedParams: d.AssignedParams, UUID: d.UUID, Serial: d.Serial,
		ConfigID: d.ConfigID,
	}
}

// GetData returns the JSON-marshaled BootyData (registered + unknown hosts).
func GetData() ([]byte, error) {
	hosts := map[string]*Host{}
	if _, err := withRLockedStore(func(st *db.Store) error {
		list, err := st.ListHosts()
		if err != nil {
			return fmt.Errorf("hardware: list hosts: %w", err)
		}
		for _, d := range list {
			hosts[d.MAC] = fromDBHost(d)
		}
		return nil
	}); err != nil {
		return nil, err
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
	var out *Host
	had, err := withRLockedStore(func(st *db.Store) error {
		d, gerr := st.GetHost(key)
		if errors.Is(gerr, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		if gerr != nil {
			return gerr
		}
		out = fromDBHost(*d)
		return nil
	})
	if !had || errors.Is(err, sql.ErrNoRows) {
		trackUnknown(key)
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hardware: get host %s: %w", key, err)
	}
	clearUnknown(key)
	return out, nil
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
	had, err := withRLockedStore(func(st *db.Store) error {
		return st.UpsertHost(toDBHost(host))
	})
	if !had {
		return errors.New("hardware: store not initialized")
	}
	if err != nil {
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
	_, err = withRLockedStore(func(st *db.Store) error {
		return st.DeleteHost(key)
	})
	return err
}

// Approve marks the canonicalized mac approved.
func Approve(mac string) error { return mutateHost(mac, (*db.Store).ApproveHost) }

// Revoke clears approval for the canonicalized mac.
func Revoke(mac string) error { return mutateHost(mac, (*db.Store).RevokeHost) }

func mutateHost(mac string, op func(*db.Store, string) error) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	had, err := withRLockedStore(func(st *db.Store) error { return op(st, key) })
	if !had {
		return errors.New("hardware: store not initialized")
	}
	return err
}

// SetAssignment sets the assigned target for the canonicalized mac. params must
// be the canonical encoding (cache.EncodeParams).
func SetAssignment(mac, os, arch, params string) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	had, err := withRLockedStore(func(st *db.Store) error {
		return st.SetAssignment(key, os, arch, params)
	})
	if !had {
		return errors.New("hardware: store not initialized")
	}
	return err
}

// SetHostConfig sets (or clears, when configID is nil) the per-host config
// binding for the canonicalized mac, through the store.
func SetHostConfig(mac string, configID *int64) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	had, err := withRLockedStore(func(st *db.Store) error {
		return st.SetHostConfig(key, configID)
	})
	if !had {
		return errors.New("hardware: store not initialized")
	}
	return err
}

// SetHostRoles replaces the canonicalized mac's role set through the store.
func SetHostRoles(mac string, roleIDs []int64) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	had, err := withRLockedStore(func(st *db.Store) error {
		return st.SetHostRoles(key, roleIDs)
	})
	if !had {
		return errors.New("hardware: store not initialized")
	}
	return err
}

// SetBootMode sets boot_mode for the canonicalized mac (e.g. "menu"). Follows
// SetAssignment's wrapper shape (its own withRLockedStore call) because it takes
// a second argument, unlike the single-arg Approve/Revoke mutateHost helper.
func SetBootMode(mac, mode string) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	had, err := withRLockedStore(func(st *db.Store) error {
		return st.SetBootMode(key, mode)
	})
	if !had {
		return errors.New("hardware: store not initialized")
	}
	return err
}

// SetSchematic writes the canonicalized mac's Talos schematic ID through the
// store (P5 bind). Binding only changes WHERE host.Schematic gets its value —
// everything downstream (approve's params["schematic"], the cache segment,
// the factory URL, tftp bootTokens) reads the field exactly as before
// (design §5: boot path byte-identical).
func SetSchematic(mac, schematic string) error {
	key, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	had, err := withRLockedStore(func(st *db.Store) error {
		return st.SetHostSchematic(key, schematic)
	})
	if !had {
		return errors.New("hardware: store not initialized")
	}
	return err
}

// ListHosts returns every registered host (fresh copies).
func ListHosts() ([]*Host, error) {
	var out []*Host
	had, err := withRLockedStore(func(st *db.Store) error {
		list, lerr := st.ListHosts()
		if lerr != nil {
			return lerr
		}
		for _, d := range list {
			out = append(out, fromDBHost(d))
		}
		return nil
	})
	if !had {
		return nil, errors.New("hardware: store not initialized")
	}
	return out, err
}

// PreserveBoot runs the one-time upgrade backfill (see db.PreserveExistingHostBoot).
func PreserveBoot() (int64, error) {
	var n int64
	had, err := withRLockedStore(func(st *db.Store) error {
		var perr error
		n, perr = st.PreserveExistingHostBoot()
		return perr
	})
	if !had {
		return 0, errors.New("hardware: store not initialized")
	}
	return n, err
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
