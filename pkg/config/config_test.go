package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func TestLoadConfig_ProxyDHCPDefaults(t *testing.T) {
	viper.Reset()
	viper.Set(DataDir, t.TempDir())

	LoadConfig(&cobra.Command{})

	if got := viper.GetBool(ProxyDHCPEnabled); got != false {
		t.Errorf("ProxyDHCPEnabled = %v, want false", got)
	}
	if got := viper.GetString(ProxyDHCPBootfileBIOS); got != "undionly.kpxe" {
		t.Errorf("ProxyDHCPBootfileBIOS = %q, want %q", got, "undionly.kpxe")
	}
	if got := viper.GetString(ProxyDHCPBootfileUEFI); got != "ipxe.efi" {
		t.Errorf("ProxyDHCPBootfileUEFI = %q, want %q", got, "ipxe.efi")
	}
	if got := viper.GetString(ProxyDHCPBootfileARM64); got != "ipxe-arm64.efi" {
		t.Errorf("ProxyDHCPBootfileARM64 = %q, want %q", got, "ipxe-arm64.efi")
	}
}

func TestDownloadFile_TimesOut(t *testing.T) {
	viper.Reset()
	dir := t.TempDir()
	viper.Set(DataDir, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := DownloadFile(ctx, dir, srv.URL+"/foo.bin")
	if err == nil {
		t.Fatalf("DownloadFile: err = nil, want timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("DownloadFile: err = %v, want wrap of context.DeadlineExceeded", err)
	}
}

func TestDownloadFile_StripsQueryStringFromFilename(t *testing.T) {
	viper.Reset()
	dir := t.TempDir()
	viper.Set(DataDir, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	if err := DownloadFile(context.Background(), dir, srv.URL+"/foo.bin?token=secret"); err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}

	want := filepath.Join(dir, "foo.bin")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected file at %s, stat err: %v", want, err)
	}

	bad := filepath.Join(dir, "foo.bin?token=secret")
	if _, err := os.Stat(bad); err == nil {
		t.Errorf("query-tainted filename %s should not exist", bad)
	}
}

func TestDownloadFile_SuccessRoundTrip(t *testing.T) {
	viper.Reset()
	dir := t.TempDir()
	viper.Set(DataDir, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	if err := DownloadFile(context.Background(), dir, srv.URL+"/greeting.txt"); err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "greeting.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("body = %q, want %q", string(got), "hello")
	}
}

func TestLoadConfig_CacheDefaults(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(DataDir, t.TempDir())

	LoadConfig(&cobra.Command{})

	if got := viper.GetDuration(CacheInterval); got != 5*time.Minute {
		t.Errorf("CacheInterval = %v, want 5m", got)
	}
	if got := viper.GetInt(CacheConcurrency); got != 4 {
		t.Errorf("CacheConcurrency = %d, want 4", got)
	}
	if got := viper.GetString(CoreOSStreamsURL); got != "https://builds.coreos.fedoraproject.org/streams/%s.json" {
		t.Errorf("CoreOSStreamsURL = %q, want the Fedora streams URL", got)
	}
}

func TestDownloadStagedHashesToPartial(t *testing.T) {
	body := []byte("artifact-bytes")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	partial, gotSHA, err := DownloadStaged(t.Context(), dir, srv.URL+"/flatcar_production_pxe.vmlinuz")
	if err != nil {
		t.Fatalf("DownloadStaged: %v", err)
	}
	if want := filepath.Join(dir, "flatcar_production_pxe.vmlinuz.partial"); partial != want {
		t.Errorf("partialPath = %q, want %q", partial, want)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Errorf(".partial must exist after staging: %v", err)
	}
	// The FINAL name must NOT exist yet (caller owns the rename).
	if _, err := os.Stat(filepath.Join(dir, "flatcar_production_pxe.vmlinuz")); !os.IsNotExist(err) {
		t.Error("final-named file must not exist after staging")
	}
	if gotSHA != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %q, want %q", gotSHA, hex.EncodeToString(sum[:]))
	}
}

func TestDownloadStagedRejects404AndLeavesNoPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if _, _, err := DownloadStaged(t.Context(), dir, srv.URL+"/missing.img"); err == nil {
		t.Fatal("a 404 must return an error")
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.img.partial")); !os.IsNotExist(err) {
		t.Error("a rejected download must leave no .partial behind")
	}
}

func TestDownloadFile_RejectsErrorStatus(t *testing.T) {
	viper.Reset()
	dir := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := DownloadFile(context.Background(), dir, srv.URL+"/boom.bin"); err == nil {
		t.Fatalf("DownloadFile: err = nil, want error for 500 status")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "boom.bin")); statErr == nil {
		t.Errorf("rejected download must not create %s", filepath.Join(dir, "boom.bin"))
	}
}
