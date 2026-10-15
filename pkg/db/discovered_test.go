package db

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
)

// insertDiscovered inserts a discovered host and fails the test on error.
func insertDiscovered(t *testing.T, s *Store, mac, ip, arch string, maxRows int) bool {
	t.Helper()
	ok, err := s.InsertDiscoveredHost(t.Context(), mac, ip, arch, maxRows)
	if err != nil {
		t.Fatalf("InsertDiscoveredHost(%s): %v", mac, err)
	}
	return ok
}

// discoveredMACs lists the discovered hosts and returns their MACs in list order.
func discoveredMACs(t *testing.T, s *Store) []string {
	t.Helper()
	list, err := s.ListDiscoveredHosts(t.Context())
	if err != nil {
		t.Fatalf("ListDiscoveredHosts: %v", err)
	}
	var macs []string
	for _, d := range list {
		macs = append(macs, d.MAC)
	}
	return macs
}

func countDiscovered(t *testing.T, s *Store) int {
	t.Helper()
	n, err := s.CountDiscoveredHosts(t.Context())
	if err != nil {
		t.Fatalf("CountDiscoveredHosts: %v", err)
	}
	return n
}

// discoveredIPArch reads the stored ip and arch for mac straight from the table.
func discoveredIPArch(t *testing.T, s *Store, mac string) (ip, arch string) {
	t.Helper()
	err := s.db.QueryRowContext(t.Context(),
		`SELECT ip, arch FROM discovered_hosts WHERE mac = ?`, mac).Scan(&ip, &arch)
	if err != nil {
		t.Fatalf("read discovered %s: %v", mac, err)
	}
	return ip, arch
}

func TestDiscoveredInsertAndList(t *testing.T) {
	s := newTestStore(t)
	const mac = "aa:bb:cc:dd:ee:01"

	if !insertDiscovered(t, s, mac, "10.0.0.5", "amd64", 1024) {
		t.Fatal("InsertDiscoveredHost = false, want true")
	}
	list, err := s.ListDiscoveredHosts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	got := list[0]
	if got.MAC != mac || got.IP != "10.0.0.5" || got.Arch != "amd64" {
		t.Errorf("row = %+v, want mac %s ip 10.0.0.5 arch amd64", got, mac)
	}
	if got.FirstSeen == "" || got.LastSeen == "" {
		t.Errorf("times empty: %+v", got)
	}
}

func TestDiscoveredInsertConflictReturnsFalse(t *testing.T) {
	s := newTestStore(t)
	const mac = "aa:bb:cc:dd:ee:01"

	if !insertDiscovered(t, s, mac, "10.0.0.5", "amd64", 1024) {
		t.Fatal("first insert = false, want true")
	}
	if insertDiscovered(t, s, mac, "10.0.0.6", "arm64", 1024) {
		t.Error("second insert of the same MAC = true, want false")
	}
	if n := countDiscovered(t, s); n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}

func TestDiscoveredInsertRefusesWhenFull(t *testing.T) {
	s := newTestStore(t)
	first := []string{"02:00:00:00:00:01", "02:00:00:00:00:02", "02:00:00:00:00:03"}

	for _, mac := range first {
		if !insertDiscovered(t, s, mac, "", "", 3) {
			t.Errorf("insert %s = false, want true", mac)
		}
	}
	if insertDiscovered(t, s, "02:00:00:00:00:04", "", "", 3) {
		t.Error("insert past the cap = true, want false")
	}
	if n := countDiscovered(t, s); n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
	got := discoveredMACs(t, s)
	slices.Sort(got)
	if !slices.Equal(got, first) {
		t.Errorf("macs = %v, want %v", got, first)
	}
}

func TestDiscoveredInsertRefusesRegisteredMAC(t *testing.T) {
	s := newTestStore(t)
	const mac = "aa:bb:cc:dd:ee:02"

	if err := s.UpsertHost(Host{MAC: mac}); err != nil {
		t.Fatal(err)
	}
	if insertDiscovered(t, s, mac, "10.0.0.5", "amd64", 1024) {
		t.Error("insert of a registered MAC = true, want false")
	}
	if n := countDiscovered(t, s); n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

func TestDiscoveredInsertRejectsBadArch(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.InsertDiscoveredHost(t.Context(), "aa:bb:cc:dd:ee:01", "10.0.0.5", "sparc", 1024); err == nil {
		t.Error("InsertDiscoveredHost with arch sparc: err = nil, want an error")
	}
}

func TestDiscoveredTouchDebounce(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	const mac = "aa:bb:cc:dd:ee:03"

	ok, err := s.TouchDiscoveredHost(ctx, mac, "10.0.0.9", "")
	if err != nil || ok {
		t.Fatalf("Touch on an absent MAC = (%v, %v), want (false, nil)", ok, err)
	}

	insertDiscovered(t, s, mac, "10.0.0.5", "amd64", 1024)
	ok, err = s.TouchDiscoveredHost(ctx, mac, "10.0.0.9", "")
	if err != nil || !ok {
		t.Fatalf("Touch on a fresh row = (%v, %v), want (true, nil)", ok, err)
	}
	if ip, _ := discoveredIPArch(t, s, mac); ip != "10.0.0.5" {
		t.Errorf("ip after a debounced touch = %q, want 10.0.0.5", ip)
	}

	if _, err = s.db.ExecContext(ctx,
		`UPDATE discovered_hosts SET last_seen = datetime('now','-21 seconds') WHERE mac = ?`, mac); err != nil {
		t.Fatal(err)
	}
	ok, err = s.TouchDiscoveredHost(ctx, mac, "10.0.0.9", "")
	if err != nil || !ok {
		t.Fatalf("Touch on a stale row = (%v, %v), want (true, nil)", ok, err)
	}
	if ip, arch := discoveredIPArch(t, s, mac); ip != "10.0.0.9" || arch != "amd64" {
		t.Errorf("after a stale touch ip = %q arch = %q, want 10.0.0.9 and amd64", ip, arch)
	}
}

func TestDiscoveredListExcludesRegisteredAndSorts(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	const (
		a = "aa:00:00:00:00:01"
		b = "aa:00:00:00:00:02"
		c = "aa:00:00:00:00:03"
	)

	insertDiscovered(t, s, a, "", "", 1024)
	insertDiscovered(t, s, b, "", "", 1024)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE discovered_hosts SET last_seen = datetime('now','+1 minute') WHERE mac = ?`, a); err != nil {
		t.Fatal(err)
	}
	insertDiscovered(t, s, c, "", "", 1024)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO hosts (mac) VALUES (?)`, c); err != nil {
		t.Fatal(err)
	}

	got := discoveredMACs(t, s)
	if want := []string{a, b}; !slices.Equal(got, want) {
		t.Errorf("macs = %v, want %v", got, want)
	}
}

func TestDiscoveredDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	const mac = "aa:bb:cc:dd:ee:01"

	insertDiscovered(t, s, mac, "", "", 1024)
	ok, err := s.DeleteDiscoveredHost(ctx, mac)
	if err != nil || !ok {
		t.Fatalf("Delete of a present row = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = s.DeleteDiscoveredHost(ctx, mac)
	if err != nil || ok {
		t.Fatalf("Delete of an absent row = (%v, %v), want (false, nil)", ok, err)
	}

	insertDiscovered(t, s, "aa:bb:cc:dd:ee:02", "", "", 1024)
	insertDiscovered(t, s, "aa:bb:cc:dd:ee:03", "", "", 1024)
	n, err := s.DeleteAllDiscoveredHosts(ctx)
	if err != nil || n != 2 {
		t.Fatalf("DeleteAll = (%d, %v), want (2, nil)", n, err)
	}
	if got := countDiscovered(t, s); got != 0 {
		t.Errorf("count after DeleteAll = %d, want 0", got)
	}
}

func TestUpsertHostClearsDiscovered(t *testing.T) {
	s := newTestStore(t)
	const mac = "aa:bb:cc:dd:ee:04"

	insertDiscovered(t, s, mac, "10.0.0.5", "amd64", 1024)
	if err := s.UpsertHost(Host{MAC: mac}); err != nil {
		t.Fatal(err)
	}
	if n := countDiscovered(t, s); n != 0 {
		t.Errorf("discovered count after UpsertHost = %d, want 0", n)
	}
	if _, err := s.GetHost(mac); err != nil {
		t.Errorf("GetHost(%s): %v", mac, err)
	}
}

func TestDiscoveredSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	insertDiscovered(t, s, "aa:bb:cc:dd:ee:01", "10.0.0.5", "amd64", 1024)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })
	if n := countDiscovered(t, s2); n != 1 {
		t.Errorf("count after reopen = %d, want 1", n)
	}
}

func TestMigrate0011UpgradesV10RewritesFedoraCoreOS(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "b.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	for _, m := range []string{
		"0001_init.sql", "0002_cache_entries.sql", "0003_configs_roles.sql",
		"0004_schematic_id.sql", "0005_clusters.sql", "0006_debianconfig.sql",
		"0007_target_source.sql", "0008_debian_source_mode.sql", "0009_drop_preseed_kind.sql",
		"0010_host_health.sql",
	} {
		stmt, rerr := migrationsFS.ReadFile("migrations/" + m)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, err = raw.ExecContext(t.Context(), string(stmt)); err != nil {
			t.Fatalf("apply %s: %v", m, err)
		}
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 10`,
		`INSERT INTO hosts (mac, os, assigned_os) VALUES ('aa:bb:cc:dd:ee:f1', 'fedora-coreos', 'fedora-coreos')`,
	} {
		if _, err = raw.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	var uv int
	if err = s.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatal(err)
	}
	if uv != 11 {
		t.Fatalf("user_version = %d, want 11", uv)
	}
	h, err := s.GetHost("aa:bb:cc:dd:ee:f1")
	if err != nil {
		t.Fatal(err)
	}
	if h.OS != "coreos" || h.AssignedOS != "coreos" {
		t.Errorf("os = %q assigned_os = %q, want coreos for both", h.OS, h.AssignedOS)
	}
}
