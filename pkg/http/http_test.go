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

// dataMux mounts the /data/ handler exactly as StartHTTP does (http.go:40):
// on a real ServeMux, behind StripPrefix. Testing the StripPrefix handler
// alone misrepresents production — ServeMux is not passive on this route. It
// path-cleans before dispatch, which is the only reason a literal "…/." spelling
// never reaches the handler, and it answers escaping paths with a 307 rather
// than the handler's 404.
func dataMux(dataDir string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/data/", http.StripPrefix("/data/", dataFileHandler(dataDir)))
	return mux
}

// getData issues target against the mux and returns status, body and Location.
func getData(mux *http.ServeMux, target string) (int, []byte, string) {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, body, res.Header.Get("Location")
}

// getDataFollow follows ServeMux's path-cleaning redirects to the status a real
// client would end on, returning that status and every body seen along the way.
//
// Refusals are NOT uniformly 404 in production: ServeMux path-cleans before
// dispatch, so an escaping path is answered with a 307 whose Location names the
// cleaned target, and only the followed request reaches the handler. The
// invariant worth asserting is therefore not the first status code but that no
// chain ever terminates in served bytes.
func getDataFollow(t *testing.T, mux *http.ServeMux, target string) (int, [][]byte) {
	t.Helper()
	var bodies [][]byte
	for hop := 0; hop < 5; hop++ {
		code, body, loc := getData(mux, target)
		bodies = append(bodies, body)
		if code != http.StatusMovedPermanently && code != http.StatusTemporaryRedirect {
			return code, bodies
		}
		if loc == "" {
			t.Fatalf("%s: redirect %d with no Location", target, code)
		}
		target = loc
	}
	t.Fatalf("%s: redirect loop", target)
	return 0, bodies
}

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

// TestIsAllowedDataPathCleansItsInput pins isAllowedDataPath's defence in
// depth. Its only caller already cleans, so nothing end-to-end notices if the
// predicate stops cleaning too — mutation-testing found exactly that hole.
//
// The clean stays, because B2 was caused by two predicates and the file server
// disagreeing about what "the path" was: a predicate that silently trusts its
// caller to have normalised is the same footgun re-armed for the next caller.
// This test is what makes the clean load-bearing rather than decorative.
func TestIsAllowedDataPathCleansItsInput(t *testing.T) {
	escapes := []string{
		"cache/../booty.db",
		"/cache/../booty.db",
		"public/../config/ignition.yaml",
		"cache/talos/../../booty.db",
	}
	for _, p := range escapes {
		if isAllowedDataPath(p) {
			t.Errorf("isAllowedDataPath(%q) = true, want false: an unnormalised path that escapes the subtree must be refused", p)
		}
	}
	for _, p := range []string{"cache/talos/kernel", "/public/cni.sh"} {
		if !isAllowedDataPath(p) {
			t.Errorf("isAllowedDataPath(%q) = false, want true", p)
		}
	}
}

// TestDataFileHandler_ServesOnlyAllowedSubtrees pins the /data/ allowlist:
// cache/ (boot artifacts, addressed by cache.CacheURLPath) and public/
// (operator assets the booted node fetches) are served; everything else under
// dataDir 404s.
//
// dataDir is not an artifact-only directory — config.DatabasePathValue puts
// booty.db there and deploy/docker-compose.yml mounts one volume for the lot —
// so serving it wholesale published the SQLite database, including
// config_revisions.source_b64, the plaintext per-host boot configs.
//
// The -wal case is not redundant with the .db case: on a freshly started
// instance booty.db is a near-empty ~4KB stub and the live rows are in the
// write-ahead log, so a guard (or a test) considering only "booty.db" would
// miss the actual exposure.
func TestDataFileHandler_ServesOnlyAllowedSubtrees(t *testing.T) {
	dataDir := t.TempDir()
	dbBytes := []byte("SQLite format 3\x00SECRET-DB-BYTES")
	for _, name := range []string{"booty.db", "booty.db-wal", "booty.db-shm", "catalog.yaml"} {
		if err := os.WriteFile(filepath.Join(dataDir, name), dbBytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A real file, so the prefix-boundary case below 404s because the guard
	// rejects it and not merely because nothing is there to serve.
	sibling := filepath.Join(dataDir, "cache.bak")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "booty.db"), dbBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	// <dataDir>/config/ holds the TEMPLATES booty renders server-side
	// (config.go:81,82,98 default IgnitionFile/TalosConfigFile/PreseedFile
	// there). They are inputs, never served, and may carry secrets.
	configDir := filepath.Join(dataDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ignition.yaml", "machineconfig.yaml", "preseed.cfg"} {
		if err := os.WriteFile(filepath.Join(configDir, name), dbBytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// <dataDir>/public/ holds operator assets the BOOTED NODE fetches over
	// HTTP — the ignition example's cni.sh/join.sh and friends. Serving these
	// is a shipped, documented boot path, so it is allowlisted alongside cache.
	publicDir := filepath.Join(dataDir, "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptBytes := []byte("#!/bin/sh\necho cni\n")
	if err := os.WriteFile(filepath.Join(publicDir, "cni.sh"), scriptBytes, 0o644); err != nil {
		t.Fatal(err)
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

	mux := dataMux(dataDir)
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
		{"cache root listing", "/data/cache/"},
		{"cache root, no trailing slash", "/data/cache"},
		{"traversal out of the cache tree", "/data/cache/../booty.db"},
		{"sibling directory sharing the prefix", "/data/cache.bak/booty.db"},
		{"ignition template", "/data/config/ignition.yaml"},
		{"talos machineconfig template", "/data/config/machineconfig.yaml"},
		{"debian preseed template", "/data/config/preseed.cfg"},
		{"config directory listing", "/data/config/"},
		{"traversal from public into config", "/data/public/../config/ignition.yaml"},
	}
	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			code, bodies := getDataFollow(t, mux, tc.path)
			if code != http.StatusNotFound {
				t.Fatalf("%s: final status %d, want 404 (bodies %q)", tc.path, code, bodies)
			}
			for _, body := range bodies {
				if bytes.Contains(body, dbBytes) {
					t.Fatalf("%s: served dataDir bytes outside an allowed subtree", tc.path)
				}
			}
		})
	}

	// The booted node fetches these over HTTP (examples/config/ignition.yaml,
	// examples/k8s.yaml). Blocking them breaks a documented, shipped boot path.
	t.Run("public operator asset served", func(t *testing.T) {
		const scriptURL = "/data/public/cni.sh"
		code, body, _ := getData(mux, scriptURL)
		if code != http.StatusOK {
			t.Fatalf("%s: got %d, want 200 (body %q)", scriptURL, code, body)
		}
		if !bytes.Equal(body, scriptBytes) {
			t.Fatalf("%s: body = %q, want %q", scriptURL, body, scriptBytes)
		}
	})

	t.Run("cache artifact still served", func(t *testing.T) {
		code, body, _ := getData(mux, kernelURL)
		if code != http.StatusOK {
			t.Fatalf("%s: got %d, want 200 (body %q)", kernelURL, code, body)
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
		mux.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Result().Body)
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("ranged %s: got %d, want 206 (body %q)", kernelURL, rec.Code, body)
		}
		if !bytes.Equal(body, kernelBytes[:4]) {
			t.Fatalf("ranged %s: body = %q, want %q", kernelURL, body, kernelBytes[:4])
		}
	})
}

// TestDataFileHandler_NoDirectoryListing covers directories the allowlist
// ALLOWS — inside cache/ — where suppression is noListingDir's job alone.
//
// The listing of a cached version directory names the in-flight .partial and
// .download files sitting in it, which is the discovery half of fetching one.
// The directory-vs-miss distinction (301 vs 404) was also an existence oracle.
// Both spellings must look like a plain miss.
func TestDataFileHandler_NoDirectoryListing(t *testing.T) {
	dataDir := t.TempDir()
	verDir := filepath.Join(dataDir, "cache", "talos", "-", "amd64", "v1.9.0")
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "kernel-amd64"), []byte("K"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "big.iso.download"), []byte("X"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := dataMux(dataDir)

	for _, target := range []string{
		"/data/cache/talos/-/amd64/v1.9.0/",
		"/data/cache/talos/-/amd64/v1.9.0",
		"/data/cache/talos/",
		"/data/cache/talos",
	} {
		t.Run(target, func(t *testing.T) {
			code, bodies := getDataFollow(t, mux, target)
			if code != http.StatusNotFound {
				t.Fatalf("%s: final status %d, want 404 (bodies %q)", target, code, bodies)
			}
			for _, body := range bodies {
				if bytes.Contains(body, []byte("big.iso.download")) {
					t.Fatalf("%s: listing disclosed an in-flight filename", target)
				}
				if bytes.Contains(body, []byte("kernel-amd64")) {
					t.Fatalf("%s: listing disclosed cache contents", target)
				}
			}
		})
	}

	// The guard must not have cost us the artifacts themselves.
	t.Run("file in that directory still served", func(t *testing.T) {
		const u = "/data/cache/talos/-/amd64/v1.9.0/kernel-amd64"
		if code, body, _ := getData(mux, u); code != http.StatusOK || string(body) != "K" {
			t.Fatalf("%s: got %d %q, want 200 %q", u, code, body, "K")
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

	mux := dataMux(dataDir)

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
		// The predicate and the file FileServer opens must agree on the path.
		// FileServer serves path.Clean(r.URL.Path) (net/http/fs.go:995), so a
		// trailing "/%2e" names the same file while defeating a raw suffix test.
		// ServeMux path-cleans the literal "/." spelling away before dispatch,
		// but not the encoded one: cleanPath runs on the escaped path, where
		// "%2e" is not a dot. The .download case is the exploitable one — it is
		// the long-lived multi-GB ISO that SweepPartials deliberately spares.
		{"in-flight via encoded-dot suffix", "/data/cache/os/arch/ver/big.iso.download/%2e", true},
		{"in-flight via uppercase encoded-dot", "/data/cache/os/arch/ver/big.iso.download/%2E", true},
		{"partial via encoded-dot suffix", "/data/cache/os/arch/ver/kernel.partial/%2e", true},
		{"in-flight via encoded dot-dot rejoin", "/data/cache/os/arch/ver/x/%2e%2e/big.iso.download", true},
		{"real file still served", "/data/cache/os/arch/ver/kernel", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body, _ := getData(mux, tc.path)

			if tc.wantBlocked {
				if code == http.StatusOK {
					t.Fatalf("%s: expected the request to be blocked, got 200 with body %q", tc.path, body)
				}
				if string(body) == string(partialBytes) {
					t.Fatalf("%s: served in-flight partial bytes", tc.path)
				}
				return
			}
			if code != http.StatusOK {
				t.Fatalf("%s: expected 200, got %d (body %q)", tc.path, code, body)
			}
			if string(body) != string(realBytes) {
				t.Fatalf("%s: body mismatch: got %q want %q", tc.path, body, realBytes)
			}
		})
	}
}
