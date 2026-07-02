package ostype

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	versions, err := o.DiscoverVersions(t.Context(), nil)
	if err != nil {
		t.Fatalf("DiscoverVersions error: %v", err)
	}
	if len(versions) != 1 || versions[0] != "3815.2.0" {
		t.Errorf("DiscoverVersions = %v, want [3815.2.0]", versions)
	}
}

func TestFlatcar_DiscoverVersions_PerChannelParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/beta/"):
			_, _ = w.Write([]byte("FLATCAR_VERSION=4300.1.0\n"))
		default:
			_, _ = w.Write([]byte("FLATCAR_VERSION=4230.2.2\n"))
		}
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.FlatcarURL, srv.URL+"/%s/%s")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")

	o, _ := Lookup("flatcar")
	got, err := o.DiscoverVersions(t.Context(), map[string]string{"channel": "beta"})
	if err != nil {
		t.Fatalf("DiscoverVersions: %v", err)
	}
	if len(got) != 1 || got[0] != "4300.1.0" {
		t.Errorf("channel=beta discovery = %v, want [4300.1.0] (params must beat the flag)", got)
	}
}

func TestFlatcar_Artifacts_ChannelInURL(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.FlatcarURL, "https://%s.release.flatcar-linux.net/%s-usr/current")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")

	o, _ := Lookup("flatcar")
	for _, a := range o.Artifacts("4230.2.2", "amd64", map[string]string{"channel": "beta"}) {
		if !strings.Contains(a.URL, "https://beta.release.flatcar-linux.net/") {
			t.Errorf("artifact URL %q must use the params channel (beta)", a.URL)
		}
	}
	// Empty channel: defensive fallback to the flag (a pre-migration row must
	// not build a %!s(MISSING) URL).
	for _, a := range o.Artifacts("4230.2.2", "amd64", nil) {
		if !strings.Contains(a.URL, "https://stable.release.flatcar-linux.net/") {
			t.Errorf("nil-params artifact URL %q must fall back to the flag channel", a.URL)
		}
	}
}

func TestFlatcar_RequiredParams(t *testing.T) {
	o, _ := Lookup("flatcar")
	if got := o.RequiredParams(); len(got) != 1 || got[0] != "channel" {
		t.Errorf("flatcar RequiredParams = %v, want [channel]", got)
	}
	fc, _ := Lookup("fedora-coreos")
	if got := fc.RequiredParams(); len(got) != 1 || got[0] != "channel" {
		t.Errorf("fedora-coreos RequiredParams = %v, want [channel]", got)
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

func TestFedoraCoreOS_CompareVersions_LengthTiebreak(t *testing.T) {
	o, _ := Lookup("fedora-coreos")
	if o.CompareVersions("39.20231101.3", "39.20231101.3.0") >= 0 {
		t.Error("shorter id should sort before longer when shared fields are equal")
	}
	if o.CompareVersions("39.20231101.3.0", "39.20231101.3") <= 0 {
		t.Error("longer id should sort after shorter when shared fields are equal")
	}
}

func TestFedoraCoreOS_DiscoverHonorsStreamsURLOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// streams JSON with one metal release for the configured arch.
		_, _ = w.Write([]byte(`{"architectures":{"x86_64":{"artifacts":{"metal":{"release":"40.20240101.3.0"}}}}}`))
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/streams/%s.json")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")

	o, _ := Lookup("fedora-coreos")
	got, err := o.DiscoverVersions(t.Context(), nil)
	if err != nil {
		t.Fatalf("DiscoverVersions: %v", err)
	}
	if len(got) != 1 || got[0] != "40.20240101.3.0" {
		t.Errorf("DiscoverVersions = %v, want [40.20240101.3.0] from the override", got)
	}
}

func TestFedoraCoreOS_DiscoverVersions_PerChannelParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/next.json") {
			_, _ = w.Write([]byte(`{"architectures":{"x86_64":{"artifacts":{"metal":{"release":"45.20260601.1.0"}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"architectures":{"x86_64":{"artifacts":{"metal":{"release":"44.20260607.3.1"}}}}}`))
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/streams/%s.json")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")

	o, _ := Lookup("fedora-coreos")
	got, err := o.DiscoverVersions(t.Context(), map[string]string{"channel": "next"})
	if err != nil {
		t.Fatalf("DiscoverVersions: %v", err)
	}
	if len(got) != 1 || got[0] != "45.20260601.1.0" {
		t.Errorf("channel=next discovery = %v, want [45.20260601.1.0]", got)
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

func TestFedoraCoreOS_Artifacts_DotKernelAndChannel(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.CoreOSURL, "https://builds.coreos.fedoraproject.org/prod/streams/%s/builds/%s/%s")
	viper.Set(config.CoreOSChannel, "stable")

	o, _ := Lookup("fedora-coreos")
	got := o.Artifacts("44.20260607.3.1", "x86_64", map[string]string{"channel": "testing"})
	if len(got) != 3 {
		t.Fatalf("fcos artifacts = %d, want 3", len(got))
	}
	// LIVE BUG fix: FCOS renamed live-kernel-<arch> (dash) to live-kernel.<arch>
	// (dot) between FCOS 39 and 44 — the dash form 404s for every current build.
	wantKernel := "fedora-coreos-44.20260607.3.1-live-kernel.x86_64"
	if got[0].Filename != wantKernel {
		t.Errorf("kernel filename = %q, want %q (dot form)", got[0].Filename, wantKernel)
	}
	for _, a := range got {
		if !strings.Contains(a.URL, "/streams/testing/builds/") {
			t.Errorf("artifact URL %q must use the params channel (testing)", a.URL)
		}
		if strings.Contains(a.URL, "live-kernel-x86_64") {
			t.Errorf("artifact URL %q still uses the dash kernel form", a.URL)
		}
	}
}
