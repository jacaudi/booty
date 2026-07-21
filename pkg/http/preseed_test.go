package http

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// TestAppendDVDMirror pins appendDVDMirror's contract: it appends exactly the
// three d-i mirror directives (single-sourcing the directive strings) without
// disturbing what was already rendered.
func TestAppendDVDMirror(t *testing.T) {
	base := []byte("d-i debian-installer/locale string en_US\n")
	out := string(appendDVDMirror(base, "10.0.0.1:8080", "/data/cache/debian/12/amd64/12.15.0"))
	for _, want := range []string{
		"d-i mirror/country string manual",
		"d-i mirror/http/hostname string 10.0.0.1:8080",
		"d-i mirror/http/directory string /data/cache/debian/12/amd64/12.15.0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasPrefix(out, "d-i debian-installer/locale string en_US\n") {
		t.Fatalf("appendDVDMirror must not disturb the existing bytes:\n%s", out)
	}
}

// mkDVDTarget creates a debian/amd64 target for the given suite (channel)
// with source_mode=dvd, and lands a fake version dir on disk so NewestCached
// resolves a real version for the mirror directory. Returns the target id.
func mkDVDTarget(t *testing.T, s *db.Store, suite, arch, version string) int64 {
	t.Helper()
	params, err := cache.EncodeParams(map[string]string{"channel": suite})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateTarget(db.Target{
		OS: "debian", Arch: arch, Params: params,
		Mode: "manual", RetainN: 1, Source: "api", Enabled: true,
		SourceMode: "dvd",
	})
	if err != nil {
		t.Fatalf("create dvd target: %v", err)
	}
	dir := filepath.Join(viper.GetString(config.DataDir), "cache", "debian", suite, arch, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestPreseedDVDModeHostGetsMirrorAppended: a debian host whose resolved
// target is dvd-mode gets the three d-i mirror directives appended to its
// served preseed, pointing at booty's local extracted tree.
func TestPreseedDVDModeHostGetsMirrorAppended(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.PreseedFile, "config/preseed.cfg")
	writeFile(t, "config/preseed.cfg", "d-i fallback-must-not-serve")
	mkDVDTarget(t, s, "13", "amd64", "13.9.0")

	const mac = "aa:bb:cc:dd:ee:70"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "debian", Hostname: "deb-dvd", Approved: true}); err != nil {
		t.Fatal(err)
	}
	cid, err := s.CreateConfig("dvd-preseed", "debianconfig")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	rid, _, err := s.AddConfigRevision(cid, base64.StdEncoding.EncodeToString([]byte("locale: en_US\n")), "sha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetActiveRevision(cid, rid); err != nil {
		t.Fatal(err)
	}
	if err := hardware.SetHostConfig(mac, &cid); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preseed?mac="+mac, nil)
	handlePreseedRequest(s)(rr, req)

	body := rr.Body.String()
	if rr.Code != 200 {
		t.Fatalf("preseed dvd mode: %d %s", rr.Code, body)
	}
	if !strings.HasPrefix(body, "d-i debian-installer/locale string en_US") {
		t.Fatalf("rendered body must be preserved, got: %s", body)
	}
	for _, want := range []string{
		"d-i mirror/country string manual",
		"d-i mirror/http/hostname string 10.0.0.1:8080",
		"d-i mirror/http/directory string /data/cache/debian/13/amd64/13.9.0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in dvd-mode preseed:\n%s", want, body)
		}
	}
}

// TestDebianDVDMirrorDir_GuardsNonDebianAndNil pins the two guard clauses
// directly: a nil host and a non-debian host both return ok=false without
// touching the store, so a non-debian preseed request (were one ever made)
// is never affected by the dvd-mirror override.
func TestDebianDVDMirrorDir_GuardsNonDebianAndNil(t *testing.T) {
	s := servingStore(t)
	if _, ok := debianDVDMirrorDir(s, nil); ok {
		t.Fatal("nil host must return ok=false")
	}
	if _, ok := debianDVDMirrorDir(s, &hardware.Host{OS: "flatcar"}); ok {
		t.Fatal("non-debian host must return ok=false")
	}
}

// TestPreseedNetinstModeHostUnchanged: a debian host resolving to an explicit
// netinst-mode target gets NO mirror override — its preseed must stay
// byte-identical to the unbound/no-target case (regression guard for the
// hard requirement that netinst hosts are unaffected).
func TestPreseedNetinstModeHostUnchanged(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.PreseedFile, "config/preseed.cfg")
	writeFile(t, "config/preseed.cfg", "d-i fallback-must-not-serve")
	id := mkDVDTarget(t, s, "13", "amd64", "13.9.0") // create then flip to netinst
	if err := s.SetTargetSourceMode(id, "netinst"); err != nil {
		t.Fatal(err)
	}

	const mac = "aa:bb:cc:dd:ee:71"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "debian", Hostname: "deb-netinst", Approved: true}); err != nil {
		t.Fatal(err)
	}
	cid, err := s.CreateConfig("netinst-preseed", "debianconfig")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	// src is a debianconfig source (translated, not passed through verbatim
	// like raw preseed); want is the exact translateDebianConfig output for a
	// lone locale field — one line plus its trailing newline (debiangen.go's
	// emit-only-what-is-set template terminates every emitted line).
	const src = "locale: en_US\n"
	const want = "d-i debian-installer/locale string en_US\n"
	rid, _, err := s.AddConfigRevision(cid, base64.StdEncoding.EncodeToString([]byte(src)), "sha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetActiveRevision(cid, rid); err != nil {
		t.Fatal(err)
	}
	if err := hardware.SetHostConfig(mac, &cid); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preseed?mac="+mac, nil)
	handlePreseedRequest(s)(rr, req)

	if rr.Code != 200 || rr.Body.String() != want {
		t.Fatalf("netinst-mode host must be byte-identical: %d\ngot:  %q\nwant: %q", rr.Code, rr.Body.String(), want)
	}
}
