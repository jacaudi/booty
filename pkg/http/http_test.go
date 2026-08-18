package http

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestPartialSuffixExcluded asserts the /data/ predicate recognizes an
// in-flight staged download (T2/T9 write <artifact>.partial while downloading)
// so isPartialPath can gate the FileServer from ever serving unverified,
// in-flight bytes — while leaving normal, landed artifacts servable.
// The ".download" case is the resumable large-file path (isodownload.go),
// which deliberately does NOT use ".partial" so SweepPartials cannot delete a
// multi-GB ISO between reconcile ticks. That same choice means a .download
// file can legitimately sit on disk for hours, inside exactly the cache
// directory [[baseurl]] points at, so it needs the identical exclusion.
func TestPartialSuffixExcluded(t *testing.T) {
	if !isPartialPath("/cache/flatcar/stable/amd64/1/x.img.partial") {
		t.Error(".partial paths must be recognized for exclusion")
	}
	if !isPartialPath("/cache/tails/-/amd64/7.10/tails-amd64.iso.download") {
		t.Error(".download paths must be recognized for exclusion")
	}
	if isPartialPath("/cache/flatcar/stable/amd64/1/x.img") {
		t.Error("final artifacts must NOT be excluded")
	}
}

// TestDataFileHandler_ServesOnlyCacheSubtree pins the /data/ allowlist: the
// cache tree is the ONLY subtree of dataDir any served content references
// (every /data/ URL booty emits is built by cache.CacheURLPath, which hardcodes
// "/data/cache/"), so everything else under dataDir must 404.
//
// This matters because dataDir is not an artifact-only directory: it is also
// where config.DatabasePathValue puts booty.db, and deploy/docker-compose.yml
// mounts one volume for both. Serving dataDir wholesale therefore published the
// SQLite database — schema, hosts, and config_revisions.source_b64 (per-host
// boot configs, plaintext) — to anyone who could reach the HTTP port.
//
// The -wal case is not redundant with the .db case: on a freshly started
// instance booty.db is a near-empty ~4KB stub and the live rows are in the
// write-ahead log, so a guard (or a test) that considered only "booty.db"
// would miss the actual exposure.
func TestDataFileHandler_ServesOnlyCacheSubtree(t *testing.T) {
	dataDir := t.TempDir()
	dbBytes := []byte("SQLite format 3\x00SECRET-DB-BYTES")
	for _, name := range []string{"booty.db", "booty.db-wal", "booty.db-shm", "catalog.yaml"} {
		if err := os.WriteFile(filepath.Join(dataDir, name), dbBytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Mirrors the production layout cache.CacheURLPath addresses:
	// <dataDir>/cache/<os>/<schematic>/<arch>/<version>/<artifact>.
	artifactDir := filepath.Join(dataDir, "cache", "talos", "-", "amd64", "v1.9.0")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	kernelBytes := []byte("REAL-KERNEL-BYTES")
	if err := os.WriteFile(filepath.Join(artifactDir, "kernel-amd64"), kernelBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	handler := http.StripPrefix("/data/", dataFileHandler(dataDir))
	const kernelURL = "/data/cache/talos/-/amd64/v1.9.0/kernel-amd64"

	blocked := []struct {
		name string
		path string
	}{
		{"sqlite database", "/data/booty.db"},
		{"sqlite write-ahead log", "/data/booty.db-wal"},
		{"sqlite shared-memory index", "/data/booty.db-shm"},
		{"operator catalog", "/data/catalog.yaml"},
		{"dataDir root listing", "/data/"},
		{"traversal out of the cache tree", "/data/cache/../booty.db"},
		{"traversal above dataDir", "/data/../../etc/passwd"},
	}
	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			body, _ := io.ReadAll(rec.Result().Body)

			// 404 specifically, not merely "not 200": a traversal refusal must
			// be indistinguishable from a plain miss, and FileServer's own
			// dot-dot rejection is a 400 that would leak the difference.
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: got %d, want 404 (body %q)", tc.path, rec.Code, body)
			}
			if bytes.Contains(body, dbBytes) {
				t.Fatalf("%s: served dataDir bytes outside the cache subtree", tc.path)
			}
		})
	}

	t.Run("cache artifact still served", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, kernelURL, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Result().Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d, want 200 (body %q)", kernelURL, rec.Code, body)
		}
		if !bytes.Equal(body, kernelBytes) {
			t.Fatalf("%s: body = %q, want %q", kernelURL, body, kernelBytes)
		}
	})

	// iPXE and the large-file boot paths depend on ranged GETs, so the guard
	// must not swallow Range handling by reading/serving the file itself.
	t.Run("cache artifact still honors Range", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, kernelURL, nil)
		req.Header.Set("Range", "bytes=0-3")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Result().Body)
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("ranged %s: got %d, want 206 (body %q)", kernelURL, rec.Code, body)
		}
		if !bytes.Equal(body, kernelBytes[:4]) {
			t.Fatalf("ranged %s: body = %q, want %q", kernelURL, body, kernelBytes[:4])
		}
	})
}

// TestDataFileHandler_BlocksPartial exercises the /data/ wrapper end-to-end
// via httptest (not just the isPartialPath predicate in isolation), proving:
//   - the predicate is actually wired into the FileServer response, not just
//     correct on its own;
//   - a normal, landed file is still served correctly;
//   - a percent-encoded ".partial" (kernel%2Epartial) is blocked, since
//     net/url decodes it into r.URL.Path before isPartialPath ever runs;
//   - an uppercase ".PARTIAL" is blocked. On a case-insensitive dev
//     filesystem (macOS/APFS) http.Dir would resolve "kernel.PARTIAL" to the
//     on-disk "kernel.partial" and serve unverified in-flight bytes unless
//     isPartialPath itself is case-insensitive.
//
// The fixtures live under cache/ deliberately: that is where the downloaders
// actually stage, and it is the one subtree isAllowedDataPath permits. Placing
// them anywhere else would let every case here pass on the allowlist alone,
// proving nothing about the in-flight guard.
func TestDataFileHandler_BlocksPartial(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "cache", "os", "arch", "ver")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	realBytes := []byte("REAL-KERNEL-BYTES")
	if err := os.WriteFile(filepath.Join(dir, "kernel"), realBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	partialBytes := []byte("IN-FLIGHT-UNVERIFIED-BYTES")
	if err := os.WriteFile(filepath.Join(dir, "kernel.partial"), partialBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.iso.download"), partialBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	handler := http.StripPrefix("/data/", dataFileHandler(dataDir))

	cases := []struct {
		name        string
		path        string
		wantBlocked bool
	}{
		{"lowercase .partial blocked", "/data/cache/os/arch/ver/kernel.partial", true},
		{"percent-encoded dot blocked", "/data/cache/os/arch/ver/kernel%2Epartial", true},
		{"uppercase .PARTIAL blocked", "/data/cache/os/arch/ver/kernel.PARTIAL", true},
		{"lowercase .download blocked", "/data/cache/os/arch/ver/big.iso.download", true},
		{"uppercase .DOWNLOAD blocked", "/data/cache/os/arch/ver/big.iso.DOWNLOAD", true},
		{"real file still served", "/data/cache/os/arch/ver/kernel", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			body, _ := io.ReadAll(rec.Result().Body)

			if tc.wantBlocked {
				if rec.Code == http.StatusOK {
					t.Fatalf("%s: expected the request to be blocked, got 200 with body %q", tc.path, body)
				}
				if string(body) == string(partialBytes) {
					t.Fatalf("%s: served in-flight partial bytes", tc.path)
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: expected 200, got %d (body %q)", tc.path, rec.Code, body)
			}
			if string(body) != string(realBytes) {
				t.Fatalf("%s: body mismatch: got %q want %q", tc.path, body, realBytes)
			}
		})
	}
}
