package ostype

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestFlatcar_CompareVersions(t *testing.T) {
	o, _ := Lookup("flatcar")
	if o.CompareVersions("3815.2.0", "3602.2.3") <= 0 {
		t.Error("flatcar: 3815.2.0 should sort after 3602.2.3")
	}
	if o.CompareVersions("3602.2.3", "3602.2.3") != 0 {
		t.Error("flatcar: equal versions should compare 0")
	}
}

func TestFlatcar_ValidateVersion(t *testing.T) {
	o, _ := Lookup("flatcar")
	if err := o.ValidateVersion("3815.2.0"); err != nil {
		t.Errorf("valid flatcar version rejected: %v", err)
	}
	if err := o.ValidateVersion("not-a-version"); err == nil {
		t.Error("invalid flatcar version accepted")
	}
}

func TestFlatcar_Artifacts(t *testing.T) {
	o, _ := Lookup("flatcar")
	got := o.Artifacts("3815.2.0", "amd64", nil)
	wantFiles := map[string]bool{
		"flatcar_production_pxe.vmlinuz":       false,
		"flatcar_production_pxe_image.cpio.gz": false,
	}
	for _, a := range got {
		if _, ok := wantFiles[a.Filename]; ok {
			wantFiles[a.Filename] = true
		}
		if a.URL == "" {
			t.Errorf("artifact %s has empty URL", a.Filename)
		}
	}
	for f, seen := range wantFiles {
		if !seen {
			t.Errorf("flatcar artifact %s missing", f)
		}
	}
}

// TestFlatcar_DiscoverVersions is a hermetic httptest-based check that
// DiscoverVersions parses FLATCAR_VERSION from a version.txt body.
func TestFlatcar_DiscoverVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("FLATCAR_VERSION=3815.2.0\n"))
	}))
	t.Cleanup(srv.Close)

	// Point config at httptest server; restore after test.
	viper.Set(config.FlatcarURL, srv.URL+"/%s/%s")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")
	t.Cleanup(func() {
		viper.Set(config.FlatcarURL, "")
		viper.Set(config.FlatcarChannel, "")
		viper.Set(config.FlatcarArchitecture, "")
	})

	o, ok := Lookup("flatcar")
	if !ok {
		t.Fatal("flatcar not registered")
	}
	versions, err := o.DiscoverVersions(t.Context())
	if err != nil {
		t.Fatalf("DiscoverVersions error: %v", err)
	}
	if len(versions) != 1 || versions[0] != "3815.2.0" {
		t.Errorf("DiscoverVersions = %v, want [3815.2.0]", versions)
	}
}

func TestFedoraCoreOS_CompareVersions(t *testing.T) {
	o, _ := Lookup("fedora-coreos")
	// Date-ish ordering, not semver: newer build sorts after.
	if o.CompareVersions("40.20240101.3.0", "39.20231101.3.0") <= 0 {
		t.Error("fcos: 40.* should sort after 39.*")
	}
	if o.CompareVersions("39.20231101.3.1", "39.20231101.3.0") <= 0 {
		t.Error("fcos: higher trailing field should sort after")
	}
}

func TestFedoraCoreOS_Artifacts(t *testing.T) {
	o, _ := Lookup("fedora-coreos")
	got := o.Artifacts("39.20231101.3.0", "x86_64", nil)
	if len(got) != 3 {
		t.Fatalf("fcos artifacts = %d, want 3 (kernel, initramfs, rootfs)", len(got))
	}
	for _, a := range got {
		if a.URL == "" || a.Filename == "" {
			t.Errorf("incomplete fcos artifact: %+v", a)
		}
	}
}
