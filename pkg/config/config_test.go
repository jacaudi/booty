package config

import (
	"context"
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

func TestLoadConfig_VersionFile(t *testing.T) {
	cases := []struct {
		name        string
		writeFile   bool
		fileContent string
		wantVersion string // empty => CurrentFlatcarVersion should be unset/empty
	}{
		{
			name:        "present with key",
			writeFile:   true,
			fileContent: "FLATCAR_VERSION=1.2.3\n",
			wantVersion: "1.2.3",
		},
		{
			name:        "present without key",
			writeFile:   true,
			fileContent: "OTHER_KEY=value\n",
			wantVersion: "",
		},
		{
			name:        "malformed file",
			writeFile:   true,
			fileContent: "this is not = valid = dotenv = at all\x00",
			wantVersion: "",
		},
		{
			name:        "absent file",
			writeFile:   false,
			wantVersion: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			dir := t.TempDir()
			viper.Set(DataDir, dir)

			if tc.writeFile {
				path := filepath.Join(dir, "version.txt")
				if err := os.WriteFile(path, []byte(tc.fileContent), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
			}

			LoadConfig(&cobra.Command{})

			got := viper.GetString(CurrentFlatcarVersion)
			if got != tc.wantVersion {
				t.Errorf("CurrentFlatcarVersion = %q, want %q", got, tc.wantVersion)
			}
		})
	}
}

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
