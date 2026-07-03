package http

import (
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
func TestPartialSuffixExcluded(t *testing.T) {
	if !isPartialPath("/cache/flatcar/stable/amd64/1/x.img.partial") {
		t.Error(".partial paths must be recognized for exclusion")
	}
	if isPartialPath("/cache/flatcar/stable/amd64/1/x.img") {
		t.Error("final artifacts must NOT be excluded")
	}
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
func TestDataFileHandler_BlocksPartial(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "os", "arch", "ver")
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

	handler := http.StripPrefix("/data/", dataFileHandler(dataDir))

	cases := []struct {
		name        string
		path        string
		wantBlocked bool
	}{
		{"lowercase .partial blocked", "/data/os/arch/ver/kernel.partial", true},
		{"percent-encoded dot blocked", "/data/os/arch/ver/kernel%2Epartial", true},
		{"uppercase .PARTIAL blocked", "/data/os/arch/ver/kernel.PARTIAL", true},
		{"real file still served", "/data/os/arch/ver/kernel", false},
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
