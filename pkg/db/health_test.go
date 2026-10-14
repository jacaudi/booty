package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeefy/booty/pkg/nodereport"
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

func TestListHostHealthSummaries(t *testing.T) {
	s := newTestStore(t)
	full := nodereport.Health{
		ReceivedAt: "2026-10-11T12:00:00Z", BootID: "boot-1", Running: "4152.2.0",
		FailedUnits: []string{"a.service", "b.service"}, JournalErrors: []string{"x", "y", "z"},
		DMI:      nodereport.DMI{Vendor: `Dell "Inc"`, Product: "Proé ✓", BIOSVersion: "1.2", ProductUUID: "uuid"},
		Firmware: nodereport.FirmwareUEFI, Kernel: "6.12.1",
	}
	sparse := nodereport.Health{ReceivedAt: "2026-10-12T00:00:00Z", Running: "1.0.0"} // nil units marshal to null
	tests := []struct {
		name   string
		raw    []byte // stored report; nil = host never reported
		want   *nodereport.Summary
		stored bool // present in the map (nil value = unreadable)
	}{
		{name: "full report", raw: mustJSON(t, full), want: new(full.Summary()), stored: true},
		{name: "sparse report", raw: mustJSON(t, sparse), want: new(sparse.Summary()), stored: true},
		{name: "empty object", raw: []byte(`{}`), want: new(nodereport.Summary{}), stored: true},
		{name: "never reported"},
		{name: "malformed JSON", raw: []byte(`{"running":`), stored: true},
		{name: "non-object JSON", raw: []byte(`[1,2]`), stored: true},
	}
	macs := make([]string, len(tests))
	for i, tc := range tests {
		macs[i] = fmt.Sprintf("aa:bb:cc:dd:ee:%02x", i)
		seedHealthHost(t, s, macs[i])
		if tc.raw != nil {
			if err := s.UpsertHostHealth(macs[i], "2026-01-01T00:00:00Z", tc.raw); err != nil {
				t.Fatalf("%s: UpsertHostHealth: %v", tc.name, err)
			}
		}
	}

	got, err := s.ListHostHealthSummaries()
	if err != nil {
		t.Fatalf("ListHostHealthSummaries: %v", err)
	}
	for i, tc := range tests {
		sum, ok := got[macs[i]]
		if ok != tc.stored {
			t.Errorf("%s: present = %v, want %v", tc.name, ok, tc.stored)
			continue
		}
		if !reflect.DeepEqual(sum, tc.want) {
			t.Errorf("%s: summary = %+v, want %+v", tc.name, sum, tc.want)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
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
