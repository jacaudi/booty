package config

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestLoadConfig_SignaturePolicyDefault(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(DataDir, t.TempDir())
	LoadConfig(&cobra.Command{})
	if got := viper.GetString(SignaturePolicy); got != "warn" {
		t.Errorf("SignaturePolicy default = %q, want %q", got, "warn")
	}
}

func TestValidateSignaturePolicy(t *testing.T) {
	t.Cleanup(viper.Reset)
	for _, ok := range []string{"strict", "warn", "off"} {
		viper.Set(SignaturePolicy, ok)
		if err := ValidateSignaturePolicy(); err != nil {
			t.Errorf("%q must be valid: %v", ok, err)
		}
	}
	viper.Set(SignaturePolicy, "loose")
	if err := ValidateSignaturePolicy(); err == nil {
		t.Error("an unknown policy must fail startup")
	}
}
