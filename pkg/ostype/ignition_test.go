package ostype

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestFlatcar_Artifacts_CarriesSigAndKey(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.FlatcarURL, "https://%s.release.flatcar-linux.net/%s-usr/current")
	viper.Set(config.FlatcarChannel, "stable")
	viper.Set(config.FlatcarArchitecture, "amd64")

	o, _ := Lookup("flatcar")
	arts, err := o.Artifacts(t.Context(), "4230.2.2", "amd64", map[string]string{"channel": "stable"})
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(arts) != 2 {
		t.Fatalf("flatcar artifacts = %d, want 2", len(arts))
	}
	for _, a := range arts {
		if a.SigURL != a.URL+".sig" {
			t.Errorf("SigURL = %q, want %q", a.SigURL, a.URL+".sig")
		}
		if len(a.GPGKey) == 0 {
			t.Errorf("flatcar artifact %s must carry the embedded GPG keyring", a.Filename)
		}
		if a.SHA256 != "" {
			t.Errorf("flatcar uses GPG only, not SHA256; got %q", a.SHA256)
		}
	}
}

func TestTalos_Artifacts_NoVerificationFields(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	o, _ := Lookup("talos")
	arts, err := o.Artifacts(t.Context(), "v1.10.5", "amd64", map[string]string{"schematic": "abc"})
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	for _, a := range arts {
		if a.SHA256 != "" || a.SigURL != "" || a.GPGKey != nil {
			t.Errorf("talos must be pass-through (no verify fields): %+v", a)
		}
	}
}

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
	got, err := o.Artifacts(t.Context(), "3815.2.0", "amd64", nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
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
	gotBeta, err := o.Artifacts(t.Context(), "4230.2.2", "amd64", map[string]string{"channel": "beta"})
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	for _, a := range gotBeta {
		if !strings.Contains(a.URL, "https://beta.release.flatcar-linux.net/") {
			t.Errorf("artifact URL %q must use the params channel (beta)", a.URL)
		}
	}
	// Empty channel: defensive fallback to the flag (a pre-migration row must
	// not build a %!s(MISSING) URL).
	gotNil, err := o.Artifacts(t.Context(), "4230.2.2", "amd64", nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	for _, a := range gotNil {
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
	// Post-D17: Artifacts always consults the streams doc first to decide
	// current-vs-pinned. This test's version is deliberately NOT the streams
	// "current" release, so it exercises the pattern-fallback branch (same
	// behavior this test asserted before P3b's streams-JSON rewrite).
	ResetStreamsCache()
	t.Cleanup(ResetStreamsCache)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"architectures":{"x86_64":{"artifacts":{"metal":{"release":"99.0.0.0"}}}}}`))
	}))
	t.Cleanup(srv.Close)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
	viper.Set(config.CoreOSArchitecture, "x86_64")

	o, _ := Lookup("fedora-coreos")
	got, err := o.Artifacts(t.Context(), "39.20231101.3.0", "x86_64", nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
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
	// Post-D17: same pattern-fallback rationale as TestFedoraCoreOS_Artifacts
	// above — the tested version is pinned/older relative to the injected
	// streams "current" release, so the dot-form pattern-built URL path (this
	// test's actual subject) still fires.
	ResetStreamsCache()
	t.Cleanup(ResetStreamsCache)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"architectures":{"x86_64":{"artifacts":{"metal":{"release":"99.0.0.0"}}}}}`))
	}))
	t.Cleanup(srv.Close)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	viper.Set(config.CoreOSURL, "https://builds.coreos.fedoraproject.org/prod/streams/%s/builds/%s/%s")
	viper.Set(config.CoreOSChannel, "stable")

	o, _ := Lookup("fedora-coreos")
	got, err := o.Artifacts(t.Context(), "44.20260607.3.1", "x86_64", map[string]string{"channel": "testing"})
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
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

const fcosStreamJSON = `{
  "architectures": { "x86_64": { "artifacts": { "metal": {
    "release": "44.20260607.3.1",
    "formats": { "pxe": {
      "kernel":    { "location": "https://ex/44/kernel",    "sha256": "aaa" },
      "initramfs": { "location": "https://ex/44/initramfs", "sha256": "bbb" },
      "rootfs":    { "location": "https://ex/44/rootfs",    "sha256": "ccc" }
    } } } } } }
}`

func TestFedoraCoreOS_Artifacts_CurrentVersionFromStreams(t *testing.T) {
	ResetStreamsCache()
	t.Cleanup(ResetStreamsCache)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(fcosStreamJSON))
	}))
	t.Cleanup(srv.Close)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")

	o, _ := Lookup("fedora-coreos")
	arts, err := o.Artifacts(t.Context(), "44.20260607.3.1", "x86_64", map[string]string{"channel": "stable"})
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(arts) != 3 {
		t.Fatalf("want 3 artifacts, got %d", len(arts))
	}
	// Assert URL + sha256 PER artifact against the expected maps (keyed by the
	// streams basename, which is Artifact.Filename = path.Base(location)).
	wantURL := map[string]string{"kernel": "https://ex/44/kernel", "initramfs": "https://ex/44/initramfs", "rootfs": "https://ex/44/rootfs"}
	wantSHA := map[string]string{"kernel": "aaa", "initramfs": "bbb", "rootfs": "ccc"}
	for _, a := range arts {
		wu, ok := wantURL[a.Filename]
		if !ok {
			t.Errorf("unexpected artifact %q", a.Filename)
			continue
		}
		if a.URL != wu {
			t.Errorf("%s URL = %q, want %q", a.Filename, a.URL, wu)
		}
		if a.SHA256 != wantSHA[a.Filename] {
			t.Errorf("%s SHA256 = %q, want %q", a.Filename, a.SHA256, wantSHA[a.Filename])
		}
	}
	// Ordering is deterministic (kernel, initramfs, rootfs).
	if arts[0].URL != "https://ex/44/kernel" || arts[0].SHA256 != "aaa" {
		t.Errorf("kernel = {%q,%q}, want {kernel,aaa}", arts[0].URL, arts[0].SHA256)
	}

	// D17: a second Artifacts call for the same channel (same pass) must NOT
	// re-fetch the streams doc.
	if _, err := o.Artifacts(t.Context(), "44.20260607.3.1", "x86_64", map[string]string{"channel": "stable"}); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("streams JSON fetched %d times, want 1 (pass-scoped memoization)", hits)
	}

	// D17: ResetStreamsCache must force the next pass to refetch the streams JSON.
	ResetStreamsCache()
	if _, err := o.Artifacts(t.Context(), "44.20260607.3.1", "x86_64", map[string]string{"channel": "stable"}); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Fatalf("post-reset fetch count = %d, want 2 (ResetStreamsCache must force a refetch)", hits)
	}
}

func TestFedoraCoreOS_Artifacts_OlderVersionPatternFallbackNoSHA(t *testing.T) {
	ResetStreamsCache()
	t.Cleanup(ResetStreamsCache)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fcosStreamJSON)) // current release is 44.*
	}))
	t.Cleanup(srv.Close)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
	viper.Set(config.CoreOSURL, "https://builds.example/prod/streams/%s/builds/%s/%s")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")

	o, _ := Lookup("fedora-coreos")
	arts, err := o.Artifacts(t.Context(), "39.20231101.3.0", "x86_64", map[string]string{"channel": "stable"})
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(arts) != 3 {
		t.Fatalf("want 3, got %d", len(arts))
	}
	for _, a := range arts {
		if a.SHA256 != "" {
			t.Errorf("pinned older version must fall back to pattern URLs with NO sha256 (NULL verified): %+v", a)
		}
	}
	if arts[0].Filename != "fedora-coreos-39.20231101.3.0-live-kernel.x86_64" {
		t.Errorf("fallback kernel filename = %q, want dot-form", arts[0].Filename)
	}
}

// TestFedoraCoreOS_Artifacts_MissingArchIsError is the design §10-mandated case:
// a streams doc that parses but LACKS the configured architecture key must make
// Artifacts return a non-nil error (fail-closed), not silently yield zero/empty
// artifacts. The real code hits this at jsonparser.GetString on the metal
// `release` path (architectures.<arch>.artifacts.metal.release), which errors
// when <arch> is absent.
func TestFedoraCoreOS_Artifacts_MissingArchIsError(t *testing.T) {
	ResetStreamsCache()
	t.Cleanup(ResetStreamsCache)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Well-formed JSON, but only aarch64 is present; x86_64 (configured) is absent.
		_, _ = w.Write([]byte(`{"architectures":{"aarch64":{"artifacts":{"metal":{"release":"44.0.0","formats":{"pxe":{}}}}}}}`))
	}))
	t.Cleanup(srv.Close)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64") // configured arch is missing from the doc

	o, _ := Lookup("fedora-coreos")
	if _, err := o.Artifacts(t.Context(), "44.0.0", "x86_64", map[string]string{"channel": "stable"}); err == nil {
		t.Fatal("streams JSON missing the configured architecture must return a non-nil error (fail-closed)")
	}
}
