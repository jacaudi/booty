package cache

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadLargeFile_ResumesFromPartial(t *testing.T) {
	payload := bytes.Repeat([]byte("debian-iso-bytes\n"), 4096) // ~68KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.iso", time.Unix(0, 0), bytes.NewReader(payload)) // honors Range
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "x.iso")
	// pre-seed the in-progress file with the first 100 bytes to exercise resume
	if err := os.WriteFile(dest+".download", payload[:100], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := downloadLargeFile(t.Context(), srv.URL+"/x.iso", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded %d bytes, want %d (resume corrupted the file)", len(got), len(payload))
	}
	if _, err := os.Stat(dest + ".download"); !os.IsNotExist(err) {
		t.Fatal(".download should be removed after rename")
	}
}

func TestDownloadLargeFile_ServerIgnoresRangeRestartsClean(t *testing.T) {
	payload := bytes.Repeat([]byte("correct-payload\n"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload) // ignores Range: always 200 OK with the full body
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "x.iso")
	// pre-seed the in-progress file with WRONG stale bytes; a clean restart
	// must discard these, not append the fresh body after them.
	if err := os.WriteFile(dest+".download", bytes.Repeat([]byte{0xFF}, 50), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := downloadLargeFile(t.Context(), srv.URL+"/x.iso", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded %d bytes, want %d (stale prefix not discarded on 200 restart)", len(got), len(payload))
	}
	if _, err := os.Stat(dest + ".download"); !os.IsNotExist(err) {
		t.Fatal(".download should be removed after rename")
	}
}

// TestDownloadLargeFile_416AlreadyCompleteRenamesAndReturns covers #3/NEW-1's
// sibling: a crash between io.Copy finishing and os.Rename running on a prior
// attempt leaves a FULL-SIZE .download file. The next attempt sends
// Range: bytes=<size>- past EOF, and a compliant server replies 416. That must
// be treated as "already fully downloaded" — rename and return nil — not a
// permanent per-tick failure (isoVerify's checksum step is the correctness
// gate for a truly corrupt file).
func TestDownloadLargeFile_416AlreadyCompleteRenamesAndReturns(t *testing.T) {
	payload := bytes.Repeat([]byte("finished-iso-bytes\n"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "x.iso")
	// pre-seed the in-progress file with the FULL payload, simulating a crash
	// between io.Copy completing and os.Rename running on a prior attempt.
	if err := os.WriteFile(dest+".download", payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := downloadLargeFile(t.Context(), srv.URL+"/x.iso", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("416 path corrupted the file: got %d bytes, want %d", len(got), len(payload))
	}
	if _, err := os.Stat(dest + ".download"); !os.IsNotExist(err) {
		t.Fatal(".download should be removed after rename on 416")
	}
}

func TestDownloadLargeFile_CancelStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never responds
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := downloadLargeFile(ctx, srv.URL, filepath.Join(t.TempDir(), "y.iso")); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}
