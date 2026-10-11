package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func seedHealthHost(t *testing.T, s *Store, mac string) {
	t.Helper()
	if err := s.UpsertHost(Host{MAC: mac}); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
}

func TestHostHealthRoundTripAndReplace(t *testing.T) {
	s := newTestStore(t)
	const mac = "aa:bb:cc:dd:ee:f0"
	seedHealthHost(t, s, mac)

	if _, _, err := s.GetHostHealth(mac); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetHostHealth miss: err = %v, want sql.ErrNoRows", err)
	}
	if err := s.UpsertHostHealth(mac, "2026-01-01T00:00:00Z", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("UpsertHostHealth: %v", err)
	}
	at, rep, err := s.GetHostHealth(mac)
	if err != nil {
		t.Fatalf("GetHostHealth: %v", err)
	}
	if at != "2026-01-01T00:00:00Z" || string(rep) != `{"a":1}` {
		t.Fatalf("got (%q, %q)", at, rep)
	}
	if err = s.UpsertHostHealth(mac, "2026-01-02T00:00:00Z", []byte(`{"a":2}`)); err != nil {
		t.Fatalf("second UpsertHostHealth: %v", err)
	}
	at, rep, err = s.GetHostHealth(mac)
	if err != nil {
		t.Fatalf("GetHostHealth: %v", err)
	}
	if at != "2026-01-02T00:00:00Z" || string(rep) != `{"a":2}` {
		t.Fatalf("after replace got (%q, %q)", at, rep)
	}
}

func TestUpsertHostHealthUnknownHost(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertHostHealth("aa:bb:cc:dd:ee:f9", "2026-01-01T00:00:00Z", []byte(`{}`)); err == nil {
		t.Fatal("expected an error for an absent host")
	}
}

func TestUpsertHostHealthTouchesLastSeen(t *testing.T) {
	s := newTestStore(t)
	const mac = "aa:bb:cc:dd:ee:f0"
	seedHealthHost(t, s, mac)
	if _, err := s.db.Exec(`UPDATE hosts SET last_seen = '2000-01-01 00:00:00' WHERE mac = ?`, mac); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertHostHealth(mac, "2026-01-01T00:00:00Z", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	var ls string
	if err := s.db.QueryRow(`SELECT last_seen FROM hosts WHERE mac = ?`, mac).Scan(&ls); err != nil {
		t.Fatal(err)
	}
	if ls == "2000-01-01 00:00:00" {
		t.Fatal("last_seen was not updated")
	}
}

func TestHostHealthCascadeOnHostDelete(t *testing.T) {
	s := newTestStore(t)
	const mac = "aa:bb:cc:dd:ee:f0"
	seedHealthHost(t, s, mac)
	if err := s.UpsertHostHealth(mac, "2026-01-01T00:00:00Z", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHost(mac); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetHostHealth(mac); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows after host delete", err)
	}
}

func TestHostReportTokenGen(t *testing.T) {
	s := newTestStore(t)
	const mac = "aa:bb:cc:dd:ee:f0"
	seedHealthHost(t, s, mac)

	if g, err := s.HostReportTokenGen(mac); err != nil || g != 0 {
		t.Fatalf("gen = %d, %v; want 0, nil", g, err)
	}
	for want := int64(1); want <= 2; want++ {
		g, err := s.BumpHostReportTokenGen(mac)
		if err != nil || g != want {
			t.Fatalf("Bump = %d, %v; want %d", g, err, want)
		}
	}
	if g, err := s.HostReportTokenGen(mac); err != nil || g != 2 {
		t.Fatalf("gen = %d, %v; want 2", g, err)
	}
	const absent = "aa:bb:cc:dd:ee:99"
	if _, err := s.HostReportTokenGen(absent); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("HostReportTokenGen absent: %v", err)
	}
	if _, err := s.BumpHostReportTokenGen(absent); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Bump absent: %v", err)
	}
}

func TestListHostHealth(t *testing.T) {
	s := newTestStore(t)
	for _, m := range []string{"aa:bb:cc:dd:ee:f0", "aa:bb:cc:dd:ee:f1"} {
		seedHealthHost(t, s, m)
		if err := s.UpsertHostHealth(m, "2026-01-01T00:00:00Z", []byte(`{"m":"`+m+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListHostHealth()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["aa:bb:cc:dd:ee:f1"]) != `{"m":"aa:bb:cc:dd:ee:f1"}` {
		t.Fatalf("got %v", got)
	}
}

func TestMigrate0010UpgradesV9WithHosts(t *testing.T) {
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
	} {
		stmt, rerr := migrationsFS.ReadFile("migrations/" + m)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, err = raw.Exec(string(stmt)); err != nil {
			t.Fatalf("apply %s: %v", m, err)
		}
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 9`,
		`INSERT INTO hosts (mac, hostname) VALUES ('aa:bb:cc:dd:ee:f0', 'old')`,
	} {
		if _, err = raw.Exec(stmt); err != nil {
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
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatal(err)
	}
	if uv != 10 {
		t.Fatalf("user_version = %d, want 10", uv)
	}
	h, err := s.GetHost("aa:bb:cc:dd:ee:f0")
	if err != nil || h == nil || h.Hostname != "old" {
		t.Fatalf("host = %+v, %v; want hostname old", h, err)
	}
	if g, gerr := s.HostReportTokenGen("aa:bb:cc:dd:ee:f0"); gerr != nil || g != 0 {
		t.Fatalf("gen = %d, %v; want 0", g, gerr)
	}
	var n int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM host_health`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("host_health count = %d, %v; want 0", n, err)
	}
}
