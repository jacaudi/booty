package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

func TestCacheAPIListPinAndDelete403(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)

	tid, _ := deps.Store.CreateTarget(db.Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"abc"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	_ = deps.Store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "v1.13.5", Source: "discovered", Cached: true})
	tvID, _ := deps.Store.TargetVersionID(tid, "v1.13.5")
	_ = deps.Store.UpsertCacheEntry(tvID, 100)

	resp := api.Get("/api/v1/cache")
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"version":"v1.13.5"`) || !strings.Contains(resp.Body.String(), `"state":"in-cycle"`) {
		t.Fatalf("cache list = %d: %s", resp.Code, resp.Body.String())
	}

	rows, _ := deps.Store.ListCacheEntries(db.CacheFilter{})
	id := rows[0].ID
	if r := api.Post(fmt.Sprintf("/api/v1/cache/%d/pin", id)); r.Code != 200 {
		t.Fatalf("pin = %d: %s", r.Code, r.Body.String())
	}
	rows, _ = deps.Store.ListCacheEntries(db.CacheFilter{})
	if !rows[0].Pinned {
		t.Fatal("pin endpoint should set pinned")
	}

	if r := api.Delete(fmt.Sprintf("/api/v1/cache/%d", id)); r.Code != 403 {
		t.Fatalf("DELETE = %d, want 403", r.Code)
	}

	// unpin: should return 200 and clear pinned flag
	if r := api.Post(fmt.Sprintf("/api/v1/cache/%d/unpin", id)); r.Code != 200 {
		t.Fatalf("unpin = %d: %s", r.Code, r.Body.String())
	}
	rows, _ = deps.Store.ListCacheEntries(db.CacheFilter{})
	if rows[0].Pinned {
		t.Fatal("unpin endpoint should clear pinned")
	}

	// scan: should return 200 with a summary body that includes the "scanned" field
	if r := api.Post("/api/v1/cache/scan"); r.Code != 200 || !strings.Contains(r.Body.String(), `"scanned"`) {
		t.Fatalf("scan = %d: %s", r.Code, r.Body.String())
	}
}

func hexSHAHTTP(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// fcosStreamsHandler serves the FCOS channel streams document whose current metal
// release (44.20260607.3.1) references pxe kernel/initramfs/rootfs files with the
// given sha256. Only the streams JSON is served: the current-build artifacts
// declare sha256 (no .sig), so VerifyVersion hashes the on-disk FINAL files the
// test pre-writes and never fetches the artifact URLs.
func fcosStreamsHandler(t *testing.T, sum string) http.Handler {
	t.Helper()
	streams := `{
  "architectures": { "x86_64": { "artifacts": { "metal": {
    "release": "44.20260607.3.1",
    "formats": { "pxe": {
      "kernel":    { "location": "https://ex/44/kernel",    "sha256": "` + sum + `" },
      "initramfs": { "location": "https://ex/44/initramfs", "sha256": "` + sum + `" },
      "rootfs":    { "location": "https://ex/44/rootfs",    "sha256": "` + sum + `" }
    } } } } } }
}`
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(streams))
	})
}

func TestReverifyRecomputesVerdict(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)

	body := []byte("rootfs-bytes")
	sum := hexSHAHTTP(body)
	srv := httptest.NewServer(fcosStreamsHandler(t, sum))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	dataDir := t.TempDir()
	viper.Set(config.DataDir, dataDir)
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.CoreOSChannel, "stable")

	tid, err := deps.Store.CreateTarget(db.Target{OS: "fedora-coreos", Arch: "x86_64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := deps.Store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "44.20260607.3.1", Source: "discovered", Cached: true}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}
	tvID, err := deps.Store.TargetVersionID(tid, "44.20260607.3.1")
	if err != nil {
		t.Fatalf("TargetVersionID: %v", err)
	}
	if err := deps.Store.UpsertCacheEntry(tvID, 100); err != nil {
		t.Fatalf("UpsertCacheEntry: %v", err)
	}
	dir := filepath.Join(dataDir, "cache", "coreos", "stable", "x86_64", "44.20260607.3.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"kernel", "initramfs", "rootfs"} { // match streams basenames
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	rows, err := deps.Store.ListCacheEntries(db.CacheFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListCacheEntries: %v (rows=%d)", err, len(rows))
	}
	id := rows[0].ID

	resp := api.Post("/api/v1/cache/"+itoa(id)+"/reverify", struct{}{})
	if resp.Code != 200 {
		t.Fatalf("reverify = %d: %s", resp.Code, resp.Body.String())
	}
	after, err := deps.Store.ListCacheEntries(db.CacheFilter{})
	if err != nil {
		t.Fatalf("ListCacheEntries after: %v", err)
	}
	if after[0].Verified == nil || !*after[0].Verified {
		t.Fatalf("matching sha256 must set verified=true, got %+v", after[0])
	}
	_ = cache.VerifyVersion // referenced so the intent is explicit
}

// TestReverifyRecordsFailureVerdict closes the gap left by
// TestReverifyRecomputesVerdict: it drives reverify's headline recourse — a
// version whose on-disk bytes FAIL verification — through the actual HTTP
// endpoint, not just VerifyVersion/aggregateVerdicts directly. Mirrors the
// passing-verdict test's setup exactly; the only difference is the on-disk
// bytes do NOT match the streams-declared sha256, so VerifyVersion's hashFile
// comparison mismatches (classCorruption) and the version must land as
// verified=false with a non-empty verify_err.
func TestReverifyRecordsFailureVerdict(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)

	body := []byte("rootfs-bytes")
	sum := hexSHAHTTP(body)
	srv := httptest.NewServer(fcosStreamsHandler(t, sum))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	dataDir := t.TempDir()
	viper.Set(config.DataDir, dataDir)
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.CoreOSChannel, "stable")

	tid, err := deps.Store.CreateTarget(db.Target{OS: "fedora-coreos", Arch: "x86_64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := deps.Store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "44.20260607.3.1", Source: "discovered", Cached: true}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}
	tvID, err := deps.Store.TargetVersionID(tid, "44.20260607.3.1")
	if err != nil {
		t.Fatalf("TargetVersionID: %v", err)
	}
	if err := deps.Store.UpsertCacheEntry(tvID, 100); err != nil {
		t.Fatalf("UpsertCacheEntry: %v", err)
	}
	dir := filepath.Join(dataDir, "cache", "coreos", "stable", "x86_64", "44.20260607.3.1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Deliberately does NOT match sum: proves the failure path, not the pass path.
	onDisk := []byte("tampered-bytes-not-matching-sum")
	for _, name := range []string{"kernel", "initramfs", "rootfs"} { // match streams basenames
		if err := os.WriteFile(filepath.Join(dir, name), onDisk, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	rows, err := deps.Store.ListCacheEntries(db.CacheFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListCacheEntries: %v (rows=%d)", err, len(rows))
	}
	id := rows[0].ID

	resp := api.Post("/api/v1/cache/"+itoa(id)+"/reverify", struct{}{})
	if resp.Code != 200 {
		t.Fatalf("reverify = %d: %s", resp.Code, resp.Body.String())
	}
	after, err := deps.Store.ListCacheEntries(db.CacheFilter{})
	if err != nil {
		t.Fatalf("ListCacheEntries after: %v", err)
	}
	if after[0].Verified == nil || *after[0].Verified {
		t.Fatalf("mismatching sha256 must set verified=false (tri-state false, not null/true), got %+v", after[0])
	}
	if after[0].VerifyErr == "" {
		t.Fatalf("a failing verdict must record a non-empty verify_err, got %+v", after[0])
	}
}

func TestReverifyMissingEntryIs404(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	if resp := api.Post("/api/v1/cache/9999/reverify", struct{}{}); resp.Code != 404 {
		t.Fatalf("reverify of a missing entry = %d, want 404", resp.Code)
	}
}

// TestCacheDTOVerifiedTriState locks the wire contract: verified must marshal as
// three distinct JSON states — true, false, and absent (NULL) — so a warn-landed
// corrupt version (false) is never conflated with a not-yet-verified one (absent).
func TestCacheDTOVerifiedTriState(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name  string
		v     *bool
		wantS string // substring that must be present
		wantA string // substring that must be ABSENT
	}{
		{"true present", &yes, `"verified":true`, ""},
		{"false present", &no, `"verified":false`, ""},
		{"nil absent", nil, "", `"verified"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(CacheEntryDTO{Verified: tc.v})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			s := string(b)
			if tc.wantS != "" && !strings.Contains(s, tc.wantS) {
				t.Fatalf("want %s in %s", tc.wantS, s)
			}
			if tc.wantA != "" && strings.Contains(s, tc.wantA) {
				t.Fatalf("want %s ABSENT from %s", tc.wantA, s)
			}
		})
	}
}
