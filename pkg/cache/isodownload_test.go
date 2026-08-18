package cache

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// downloadLargeFile is the only artifact path with no timeout, moving the
// largest payloads booty handles. Silence here is indistinguishable from a
// hang, so the start/complete pair is load-bearing operator-facing behaviour,
// not decoration. The completion size must be the WHOLE file, not just the
// bytes this attempt copied — a resumed transfer that reports only its delta
// reads as a truncated download.
// TestDownloadLargeIntoLeavesBytesUnrenamed is the load-bearing property Task 3
// depends on: the resumable downloader must hand back COMPLETE bytes at the
// in-progress path WITHOUT renaming, so landArtifact can verify them before an
// unverified 1.94 GB ever occupies the final path that /data/ serves.
func TestDownloadLargeIntoLeavesBytesUnrenamed(t *testing.T) {
	payload := bytes.Repeat([]byte("unrenamed-bytes\n"), 512)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.iso", time.Unix(0, 0), bytes.NewReader(payload))
	}))
	defer srv.Close()

	dir := t.TempDir()
	final := filepath.Join(dir, "x.iso")
	inProgress := final + DownloadSuffix

	if err := downloadLargeInto(t.Context(), srv.URL+"/x.iso", inProgress); err != nil {
		t.Fatalf("downloadLargeInto: %v", err)
	}
	got, err := os.ReadFile(inProgress)
	if err != nil {
		t.Fatalf("in-progress file must hold the complete bytes: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("in-progress content = %d bytes, want %d", len(got), len(payload))
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatal("downloadLargeInto must NOT rename; the caller verifies first, then lands")
	}
}

// The 416 branch means a prior attempt already wrote every byte and this
// attempt streams nothing. downloadLargeInto must still return nil with the
// complete bytes in place and still not rename.
func TestDownloadLargeInto416LeavesBytesUnrenamed(t *testing.T) {
	payload := bytes.Repeat([]byte("already-complete\n"), 512)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	final := filepath.Join(dir, "x.iso")
	inProgress := final + DownloadSuffix
	if err := os.WriteFile(inProgress, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := downloadLargeInto(t.Context(), srv.URL+"/x.iso", inProgress); err != nil {
		t.Fatalf("416 with complete bytes on disk must succeed: %v", err)
	}
	got, err := os.ReadFile(inProgress)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("416 path must leave the complete bytes at the in-progress path (err=%v)", err)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatal("downloadLargeInto must NOT rename on the 416 path either")
	}
}

// The completion log reports offset+n. On the 200 branch the server ignored
// Range and the file was truncated back to zero, so `offset` is STALE and the
// logged size over-reports by the discarded prefix. isodownload_test's existing
// log test exercises only the 206 path, so nothing caught this.
func TestDownloadLargeFileLogsTrueSizeAfter200Restart(t *testing.T) {
	payload := bytes.Repeat([]byte("iso\n"), 4096) // 16384 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload) // ignores Range: always 200 with the full body
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "x.iso")
	const seeded = 100
	if err := os.WriteFile(dest+DownloadSuffix, bytes.Repeat([]byte{0xFF}, seeded), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := downloadLargeFile(t.Context(), srv.URL+"/x.iso", dest); err != nil {
		t.Fatal(err)
	}

	out := logs.String()
	if want := fmt.Sprintf("bytes=%d", len(payload)); !strings.Contains(out, want) {
		t.Errorf("completion log must report the TRUE file size %q; got:\n%s", want, out)
	}
	if bad := fmt.Sprintf("bytes=%d", seeded+len(payload)); strings.Contains(out, bad) {
		t.Errorf("completion log over-reports by the discarded prefix (%q); got:\n%s", bad, out)
	}
}

func TestDownloadLargeFile_LogsStartAndCompletionWithResumeOffset(t *testing.T) {
	payload := bytes.Repeat([]byte("iso\n"), 4096) // 16384 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.iso", time.Unix(0, 0), bytes.NewReader(payload))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "x.iso")
	const seeded = 100
	if err := os.WriteFile(dest+".download", payload[:seeded], 0o644); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := downloadLargeFile(t.Context(), srv.URL+"/x.iso", dest); err != nil {
		t.Fatal(err)
	}

	out := logs.String()
	for _, want := range []string{
		`msg="downloading (resumable)"`,
		"resumeOffset=100",
		`msg="resumable download complete"`,
		fmt.Sprintf("bytes=%d", len(payload)),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q\ngot:\n%s", want, out)
		}
	}
}
