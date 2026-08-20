package ostype

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

const fixtureDoc = `
endpoints:
  systemrescue-amd64:
    path: /asset-mirror/releases/download/13.01-d20a63ac/
    files:
    - airootfs.sfs
    - initrd
    - vmlinuz
    - archiso_pxe_http
    os: systemrescue
    version: 13.01
    arch: amd64
  memtest86plus:
    path: /asset-mirror/releases/download/8.00-32a14678/
    files:
    - mt86p_x86_64
    os: memtest86-plus
    version: '8.00'
  tails:
    path: /asset-mirror/releases/download/7.10-17629562/
    files:
    - vmlinuz
    - initrd.img
    - 9990-misc-helpers.sh
    - tails-amd64.iso
    os: tails
    version: '7.10'
`

// liveAssetRisk reports why serveFixture must not be used to drive these
// registrations, or "" when they are safe. Split out from serveFixture so the
// guard is testable: serveFixture reports it with t.Fatal, which would fail the
// very test asserting the guard fires.
func liveAssetRisk(driven []netbootxyzOS) string {
	for _, o := range driven {
		if o.checksums != "" {
			return fmt.Sprintf(
				"use serveToolFixture, not serveFixture: %s declares checksums %q, and serveFixture points the asset base at the real github.com, so Artifacts would issue a LIVE network request",
				o.name, o.checksums)
		}
	}
	return ""
}

// fixtureFatal is the guard's reporting seam. It exists so
// TestServeFixtureGuardIsWired can prove serveFixture actually CONSULTS
// liveAssetRisk: t.Fatal cannot be observed from the test asserting it, and a
// predicate-only test would pass even with the guard never called — the
// inert-mechanism failure mode. Nothing else may reassign it.
var fixtureFatal = func(t *testing.T, msg string) { t.Fatal(msg) }

// serveFixture stands up the endpoints manifest and points the asset base at
// the REAL github.com, which is what lets TestNetbootxyzOSArtifactURLs pin the
// production URL shape.
//
// driven is the registrations the test will call Artifacts on — nil for tests
// that only fetch the manifest. It is a REQUIRED parameter, not variadic, on
// purpose: a sidecar-declaring registration driven through here issues a live
// network request, and a parameter that can be omitted is a convention rather
// than a guard. Requiring it makes the next author confront the question at
// compile time.
func serveFixture(t *testing.T, body string, hits *int32, driven []netbootxyzOS) {
	t.Helper()
	if risk := liveAssetRisk(driven); risk != "" {
		fixtureFatal(t, risk)
		return
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	viper.Set(config.NetbootxyzEndpointsURL, srv.URL)
	// The asset base MUST be set too: its default lives in config.LoadConfig(cmd),
	// which no unit test calls, so it would otherwise be "" and every composed
	// artifact URL would come out relative.
	viper.Set(config.NetbootxyzAssetBase, "https://github.com/netbootxyz")
	t.Cleanup(func() {
		viper.Set(config.NetbootxyzEndpointsURL, "")
		viper.Set(config.NetbootxyzAssetBase, "")
	})
	ResetNetbootxyzCache()
	t.Cleanup(ResetNetbootxyzCache)
}

// serveFixture points the asset base at the REAL github.com, so any test that
// drives a sidecar-declaring registration through it issues a live network
// request. Task 4 created that hazard by giving tails a sidecar; this pins the
// guard that closes it. A live request would 404 fast rather than hang, so the
// "watch for a non-0.00s test" convention would not reliably catch it —
// convention is not a guard.
//
// The predicate is tested rather than serveFixture itself because the guard
// calls t.Fatal, which would fail the very test asserting it fires.
func TestServeFixtureRejectsASidecarDeclaringRegistration(t *testing.T) {
	o, ok := Lookup("tails")
	if !ok {
		t.Fatal("tails not registered")
	}
	tails := o.(netbootxyzOS)

	risk := liveAssetRisk([]netbootxyzOS{tails})
	if risk == "" {
		t.Fatal("a registration declaring checksums must be rejected by serveFixture")
	}
	for _, want := range []string{"serveToolFixture", "tails", "sha256-checksums.txt"} {
		if !strings.Contains(risk, want) {
			t.Errorf("the message must name %q so the failure explains itself, got: %s", want, risk)
		}
	}

	// The other seven tools publish no sidecar and must stay usable, or this
	// guard would break twelve existing call sites instead of protecting them.
	if got := liveAssetRisk([]netbootxyzOS{testSysrescue, testMemtest86Plus}); got != "" {
		t.Errorf("registrations with no sidecar must be allowed, got: %s", got)
	}
	if got := liveAssetRisk(nil); got != "" {
		t.Errorf("a test driving no registration must be allowed, got: %s", got)
	}
}

// TestServeFixtureRejectsASidecarDeclaringRegistration proves the PREDICATE is
// right; it would still pass if serveFixture never called it. This proves the
// WIRING, so the guard cannot be left inert.
func TestServeFixtureGuardIsWired(t *testing.T) {
	o, _ := Lookup("tails")

	var got string
	orig := fixtureFatal
	fixtureFatal = func(_ *testing.T, msg string) { got = msg }
	t.Cleanup(func() { fixtureFatal = orig })

	serveFixture(t, fixtureDoc, nil, []netbootxyzOS{o.(netbootxyzOS)})

	if got == "" {
		t.Fatal("serveFixture did not consult liveAssetRisk; the guard is inert")
	}
	if !strings.Contains(got, "serveToolFixture") {
		t.Errorf("guard message = %q, must point at the right helper", got)
	}
}

func TestFetchNetbootxyzDocPreservesVersionText(t *testing.T) {
	serveFixture(t, fixtureDoc, nil, nil)
	doc, err := fetchNetbootxyzDoc(context.Background())
	if err != nil {
		t.Fatalf("fetchNetbootxyzDoc: %v", err)
	}
	if got := doc["systemrescue-amd64"].Version; got != "13.01" {
		t.Errorf("version = %q, want \"13.01\"", got)
	}
	if got := len(doc["systemrescue-amd64"].Files); got != 4 {
		t.Errorf("files = %d, want 4", got)
	}
}

func TestFetchNetbootxyzDocToleratesUnknownKeys(t *testing.T) {
	serveFixture(t, fixtureDoc+"\n  extra-thing:\n    path: /x/\n    files: []\n    surprise: yes\n", nil, nil)
	if _, err := fetchNetbootxyzDoc(context.Background()); err != nil {
		t.Fatalf("unknown keys must be tolerated, got %v", err)
	}
}

func TestFetchNetbootxyzDocMemoizes(t *testing.T) {
	var hits int32
	serveFixture(t, fixtureDoc, &hits, nil)
	for range 3 {
		if _, err := fetchNetbootxyzDoc(context.Background()); err != nil {
			t.Fatalf("fetch: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream hits = %d, want 1", got)
	}
	ResetNetbootxyzCache()
	if _, err := fetchNetbootxyzDoc(context.Background()); err != nil {
		t.Fatalf("fetch after reset: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits after reset = %d, want 2", got)
	}
}

func TestReleaseTag(t *testing.T) {
	cases := map[string]string{
		"/asset-mirror/releases/download/13.01-d20a63ac/":       "13.01-d20a63ac",
		"/asset-mirror/releases/download/8.00-32a14678":         "8.00-32a14678",
		"/debian-squash/releases/download/0.72-beta8-2568400c/": "0.72-beta8-2568400c",
		"":                                   "",
		"/":                                  "",
		"/asset-mirror/releases/download/.":  "", // guard: final segment "."
		"/asset-mirror/releases/download/..": "", // guard: final segment ".."
	}
	for in, want := range cases {
		if got := releaseTag(in); got != want {
			t.Errorf("releaseTag(%q) = %q, want %q", in, got, want)
		}
	}
}

var testSysrescue = netbootxyzOS{
	name:      "systemrescue",
	endpoints: map[string]string{"amd64": "systemrescue-amd64"},
	files:     []string{"vmlinuz", "initrd", "archiso_pxe_http", "airootfs.sfs"},
}

func TestNetbootxyzOSDiscoverReturnsReleaseTag(t *testing.T) {
	serveFixture(t, fixtureDoc, nil, []netbootxyzOS{testSysrescue})
	got, err := testSysrescue.DiscoverVersions(context.Background(), nil)
	if err != nil {
		t.Fatalf("DiscoverVersions: %v", err)
	}
	if len(got) != 1 || got[0] != "13.01-d20a63ac" {
		t.Errorf("DiscoverVersions = %#v, want [13.01-d20a63ac]", got)
	}
}

func TestNetbootxyzOSArtifactURLs(t *testing.T) {
	serveFixture(t, fixtureDoc, nil, []netbootxyzOS{testSysrescue})
	arts, err := testSysrescue.Artifacts(context.Background(), "13.01-d20a63ac", "amd64", nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(arts) != 4 {
		t.Fatalf("got %d artifacts, want 4", len(arts))
	}
	want := "https://github.com/netbootxyz/asset-mirror/releases/download/13.01-d20a63ac/vmlinuz"
	var found bool
	for _, a := range arts {
		if a.URL == want {
			found = true
		}
		if a.SHA256 != "" || a.SigURL != "" {
			t.Errorf("%s: netboot.xyz publishes no verification material", a.Filename)
		}
	}
	if !found {
		t.Errorf("no artifact with URL %q; got %+v", want, arts)
	}
}

func TestNetbootxyzOSArtifactsRefusesStaleVersion(t *testing.T) {
	serveFixture(t, fixtureDoc, nil, []netbootxyzOS{testSysrescue})
	if _, err := testSysrescue.Artifacts(context.Background(), "12.00-deadbeef", "amd64", nil); err == nil {
		t.Fatal("Artifacts accepted a stale version, want error")
	}
}

func TestNetbootxyzOSUnknownArch(t *testing.T) {
	serveFixture(t, fixtureDoc, nil, []netbootxyzOS{testSysrescue})
	if _, err := testSysrescue.Artifacts(context.Background(), "13.01-d20a63ac", "arm64", nil); err == nil {
		t.Fatal("Artifacts accepted an unregistered arch, want error")
	}
}

func TestNetbootxyzOSRejectsUnsafeTag(t *testing.T) {
	serveFixture(t, `
endpoints:
  systemrescue-amd64:
    path: /asset-mirror/releases/download/..%2f..%2fetc/
    files: [vmlinuz]
    version: evil
`, nil, []netbootxyzOS{testSysrescue})
	if _, err := testSysrescue.DiscoverVersions(context.Background(), nil); err == nil {
		t.Fatal("DiscoverVersions accepted an unsafe release tag, want error")
	}
}

// The guard validates the manifest-supplied PATH, not the composed URL. A
// composed-URL host check is structurally incapable of failing here, because the
// authority always comes from assetBase — see the comment on artifactURL.
func TestArtifactURLRejectsUnsafePath(t *testing.T) {
	for _, bad := range []string{
		"https://evil.example/x/", // absolute URL
		"//evil.example/x/",       // protocol-relative
		"asset-mirror/x/",         // not rooted
	} {
		if _, err := artifactURL("https://github.com/netbootxyz", bad, "f"); err == nil {
			t.Errorf("artifactURL accepted unsafe path %q, want error", bad)
		}
	}
}

// An unset asset base must FAIL, not silently produce a relative URL.
func TestArtifactURLRequiresAssetBase(t *testing.T) {
	if _, err := artifactURL("", "/asset-mirror/x/", "f"); err == nil {
		t.Fatal("artifactURL accepted an empty asset base, want error")
	}
}

func TestNetbootxyzOSInterfaceBasics(t *testing.T) {
	if testSysrescue.Name() != "systemrescue" {
		t.Errorf("Name() = %q", testSysrescue.Name())
	}
	if testSysrescue.Family().Name != "tool" {
		t.Errorf("Family() = %+v, want tool", testSysrescue.Family())
	}
	if len(testSysrescue.RequiredParams()) != 0 {
		t.Errorf("RequiredParams() = %#v, want empty", testSysrescue.RequiredParams())
	}
	if testSysrescue.CompareVersions("a", "b") >= 0 {
		t.Error("CompareVersions should order lexicographically")
	}
}

// fixtureMemtestWithBogusFiles reproduces the live lab defect: netboot.xyz's
// own endpoints.yml lists 7 files for memtest86plus, but its asset mirror only
// ever published 3 of them (mt86p_i586, mt86p_la64, mt86p_x86_64) at release
// 8.00-32a14678 — the other four 404. Only mt86p_x86_64 is ever booted.
const fixtureMemtestWithBogusFiles = `
endpoints:
  memtest86plus:
    path: /asset-mirror/releases/download/8.00-32a14678/
    files:
    - mt86p_i586
    - mt86p_la64
    - mt86p_x86_64
    - memtest32.bin
    - memtest32.efi
    - memtest64.bin
    - memtest64.efi
    os: memtest86-plus
    version: '8.00'
`

var testMemtest86Plus = netbootxyzOS{
	name:      "memtest86plus",
	endpoints: map[string]string{"amd64": "memtest86plus"},
	files:     []string{"mt86p_x86_64"},
}

// TestNetbootxyzOSArtifactsAppliesFileAllowlist is the regression test for the
// live lab defect: a manifest entry listing files upstream does not actually
// publish must not make Artifacts return downloadables for them.
func TestNetbootxyzOSArtifactsAppliesFileAllowlist(t *testing.T) {
	serveFixture(t, fixtureMemtestWithBogusFiles, nil, []netbootxyzOS{testMemtest86Plus})
	arts, err := testMemtest86Plus.Artifacts(context.Background(), "8.00-32a14678", "amd64", nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(arts) != 1 || arts[0].Filename != "mt86p_x86_64" {
		t.Fatalf("Artifacts = %#v, want exactly one artifact for mt86p_x86_64", arts)
	}
}

// TestNetbootxyzOSArtifactsFailsLoudOnMissingAllowlistedFile covers the other
// direction: if upstream renames or drops the exact file the boot script
// needs, Artifacts must fail loudly rather than silently caching nothing
// useful.
func TestNetbootxyzOSArtifactsFailsLoudOnMissingAllowlistedFile(t *testing.T) {
	serveFixture(t, `
endpoints:
  memtest86plus:
    path: /asset-mirror/releases/download/8.00-32a14678/
    files:
    - mt86p_i586
    os: memtest86-plus
    version: '8.00'
`, nil, []netbootxyzOS{testMemtest86Plus})
	_, err := testMemtest86Plus.Artifacts(context.Background(), "8.00-32a14678", "amd64", nil)
	if err == nil {
		t.Fatal("Artifacts silently accepted a manifest missing the allowlisted file, want error")
	}
	if !strings.Contains(err.Error(), "mt86p_x86_64") {
		t.Errorf("error must name the missing file %q, got: %v", "mt86p_x86_64", err)
	}
}

func TestTailsISOIsMarkedLarge(t *testing.T) {
	o, ok := Lookup("tails")
	if !ok {
		t.Fatal("tails not registered")
	}
	tool, ok := o.(netbootxyzOS)
	if !ok {
		t.Fatal("tails is not a netbootxyzOS")
	}
	if !tool.large["tails-amd64.iso"] {
		t.Error("the 1.9 GB ISO must be marked Large or it cannot land (D13)")
	}
	if tool.large["vmlinuz"] {
		t.Error("small files must NOT take the untimed path")
	}
}

// The registration data above is inert unless Artifacts actually copies it onto
// the Artifact. Without this test, omitting that one field leaves every test
// green while Tails silently reverts to the 5-minute ceiling — the exact failure
// D13 exists to prevent.
func TestTailsArtifactsCarryLarge(t *testing.T) {
	// serveFixture points the asset base at the real github.com; now that tails
	// declares a sidecar, Artifacts would issue a LIVE network request. Use the
	// local asset host instead.
	serveTailsSidecar(t, tailsDigest+"  tails-amd64.iso\n")
	o, _ := Lookup("tails")
	arts, err := o.Artifacts(context.Background(), "7.10-17629562", "amd64", nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	got := map[string]bool{}
	for _, a := range arts {
		got[a.Filename] = a.Large
	}
	if !got["tails-amd64.iso"] {
		t.Error("the ISO artifact must carry Large=true")
	}
	if got["vmlinuz"] {
		t.Error("vmlinuz must carry Large=false")
	}
}

// tailsSidecarBody is the exact shape netbootxyz/asset-mirror publishes: ONE
// LF-terminated line, <64 hex> + TWO SPACES + filename. No "./", no binary-mode
// "*", no trailing blank. Verified against five real releases.
const tailsDigest = "6dab23b2000000000000000000000000000000000000000000000000000d1743"

// serveToolFixture stands up BOTH servers a sidecar test needs: the endpoints
// manifest and a local ASSET host. serveFixture points the asset base at the
// real github.com, which makes any test that fetches a sidecar issue a live
// network request — so sidecar tests must use this helper instead.
//
// assetBody is called for every asset request and returns (body, status).
// Returns the counter of requests whose path ends in "/sha256-checksums.txt".
func serveToolFixture(t *testing.T, doc string, assetBody func(path string) (string, int)) *atomic.Int32 {
	t.Helper()
	sidecarHits := new(atomic.Int32)

	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(doc))
	}))
	t.Cleanup(manifest.Close)

	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sha256-checksums.txt") {
			sidecarHits.Add(1)
		}
		body, status := assetBody(r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(assets.Close)

	viper.Set(config.NetbootxyzEndpointsURL, manifest.URL)
	viper.Set(config.NetbootxyzAssetBase, assets.URL)
	t.Cleanup(func() {
		viper.Set(config.NetbootxyzEndpointsURL, "")
		viper.Set(config.NetbootxyzAssetBase, "")
	})
	ResetNetbootxyzCache()
	t.Cleanup(ResetNetbootxyzCache)
	return sidecarHits
}

// serveTailsSidecar is the common case: a valid sidecar covering the ISO.
func serveTailsSidecar(t *testing.T, sidecar string) *atomic.Int32 {
	t.Helper()
	return serveToolFixture(t, fixtureDoc, func(path string) (string, int) {
		if strings.HasSuffix(path, "/sha256-checksums.txt") {
			if sidecar == "" {
				return "not found", http.StatusNotFound
			}
			return sidecar, http.StatusOK
		}
		return "BINARY", http.StatusOK
	})
}

// D1 — the digest is resolved into the existing Artifact.SHA256 field, and ONLY
// for the file the sidecar actually lists. The Tails sidecar legitimately lists
// only the ISO, so vmlinuz/initrd.img/9990-misc-helpers.sh stay not-verifiable.
func TestTailsArtifactsPopulateISOSHA256Only(t *testing.T) {
	serveTailsSidecar(t, tailsDigest+"  tails-amd64.iso\n")

	o, ok := Lookup("tails")
	if !ok {
		t.Fatal("tails not registered")
	}
	arts, err := o.Artifacts(context.Background(), "7.10-17629562", "amd64", nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	got := map[string]string{}
	for _, a := range arts {
		got[a.Filename] = a.SHA256
		if a.SigURL != "" {
			t.Errorf("%s: GPG verification is out of scope for #76; SigURL must stay empty", a.Filename)
		}
	}
	if got["tails-amd64.iso"] != tailsDigest {
		t.Errorf("ISO SHA256 = %q, want %q", got["tails-amd64.iso"], tailsDigest)
	}
	for _, f := range []string{"vmlinuz", "initrd.img", "9990-misc-helpers.sh"} {
		if got[f] != "" {
			t.Errorf("%s: SHA256 = %q, want \"\" (the sidecar lists only the ISO)", f, got[f])
		}
	}
}

// D2 — an unfetchable sidecar fails LOUD. Falling back to not-verifiable is a
// silent security downgrade. reconcile.go turns this error into "log a warning
// and skip this version this tick" without touching the row, so fail-loud costs
// the availability of UPDATES, never of the existing cache.
func TestTailsArtifactsFailLoudOnSidecar404(t *testing.T) {
	serveTailsSidecar(t, "") // 404

	o, _ := Lookup("tails")
	_, err := o.Artifacts(context.Background(), "7.10-17629562", "amd64", nil)
	if err == nil {
		t.Fatal("an unfetchable sidecar must be an error, not a silent downgrade to not-verifiable")
	}
	// Assert the MECHANISM, not merely non-nil. With sidecar support disabled
	// entirely, `sums` is nil, the ISO looks uncovered, and D2a returns an error
	// too — so an err != nil check alone passes in a world where D2's fail-loud
	// fetch path does not exist at all.
	if !strings.Contains(err.Error(), "fetch sha256-checksums.txt") {
		t.Errorf("the error must name the failed FETCH, got: %v", err)
	}
}

// D2 — a malformed sidecar fails loud for the same reason.
func TestTailsArtifactsFailLoudOnMalformedSidecar(t *testing.T) {
	serveTailsSidecar(t, "this is not a sums line\n")

	o, _ := Lookup("tails")
	_, err := o.Artifacts(context.Background(), "7.10-17629562", "amd64", nil)
	if err == nil {
		t.Fatal("a malformed sidecar must be an error")
	}
	// Same reason as the 404 test: D2a would also error here if parsing were
	// skipped, so only the message separates the two mechanisms.
	if !strings.Contains(err.Error(), "parse sha256-checksums.txt") {
		t.Errorf("the error must name the failed PARSE, got: %v", err)
	}
}

// D2a — the SIDECAR-ONLY DESYNC. Manifest and release asset still say
// tails-amd64.iso, but the sidecar drops or re-keys that line. Nothing else in
// the pipeline notices — the manifest-membership check passes, the URL composes,
// the download succeeds — so the 1.94 GB ISO would silently land
// classNotVerifiable, the exact failure D2 exists to forbid.
//
// Note the fixture shape: e.Files still lists tails-amd64.iso while the sidecar
// keys a different name. That is ALSO what a rename the manifest has not yet
// tracked looks like from here, which is why this branch catches both.
// TestUpstreamRenameSurfacesTheAllowlistError below pins the other rename
// branch, where the manifest HAS tracked it and the membership check fires
// first.
func TestTailsArtifactsFailLoudWhenSidecarOmitsACoveredFile(t *testing.T) {
	serveTailsSidecar(t, tailsDigest+"  tails-amd64-6.6.iso\n") // re-keyed, ISO line absent

	o, _ := Lookup("tails")
	_, err := o.Artifacts(context.Background(), "7.10-17629562", "amd64", nil)
	if err == nil {
		t.Fatal("a declared checksum-covered file absent from the sidecar must be an error")
	}
	if !strings.Contains(err.Error(), "tails-amd64.iso") {
		t.Errorf("the error must name the uncovered file, got: %v", err)
	}
}

// A tool that declares no sidecar must issue NO sidecar request and behave
// exactly as it does today — seven of the eight tools are in that state and
// netboot.xyz publishes nothing for them.
func TestToolWithoutChecksumsIssuesNoSidecarRequest(t *testing.T) {
	hits := serveToolFixture(t, fixtureDoc, func(string) (string, int) { return "BINARY", http.StatusOK })

	o, ok := Lookup("systemrescue")
	if !ok {
		t.Fatal("systemrescue not registered")
	}
	arts, err := o.Artifacts(context.Background(), "13.01-d20a63ac", "amd64", nil)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("a tool with no checksums declared issued %d sidecar requests, want 0", n)
	}
	for _, a := range arts {
		if a.SHA256 != "" {
			t.Errorf("%s: SHA256 = %q, want \"\"", a.Filename, a.SHA256)
		}
	}
}

// A digest that is not exactly 64 lowercase hex characters must fail LOUD at
// discovery time rather than be resolved into Artifact.SHA256.
//
// This is about the CONSEQUENCE of a malformed declared digest, not cosmetics.
// landArtifact compares byte-exactly against lowercase hex.EncodeToString
// output, and a Large artifact's mismatch is classCorruption — rejected under
// warn AND strict (D4a). So one uppercase character upstream would delete the
// completed 1.94 GB file, RemoveAll the version directory, and re-download from
// zero on the next reconcile tick, forever.
//
// It does NOT loosen the comparison: strings.EqualFold was rejected because it
// would also change FCOS and Flatcar. Comparison stays case-SENSITIVE; this
// rejects malformed INPUT at the point it enters the pipeline. It lives in
// pkg/ostype rather than pkg/checksum.ParseSums because that parser is shared
// with the Debian DVD path, whose behaviour is out of scope here.
func TestTailsArtifactsRejectMalformedDigest(t *testing.T) {
	for name, digest := range map[string]string{
		"uppercase":  strings.ToUpper(tailsDigest),
		"too short":  tailsDigest[:63],
		"too long":   tailsDigest + "a",
		"non hex":    "g" + tailsDigest[1:],
		"whitespace": " " + tailsDigest[1:],
	} {
		t.Run(name, func(t *testing.T) {
			serveTailsSidecar(t, digest+"  tails-amd64.iso\n")

			o, _ := Lookup("tails")
			arts, err := o.Artifacts(context.Background(), "7.10-17629562", "amd64", nil)
			if err == nil {
				t.Fatalf("a %s digest must be an error, got artifacts %+v", name, arts)
			}
			if !strings.Contains(err.Error(), "tails-amd64.iso") {
				t.Errorf("the error must name the offending file, got: %v", err)
			}
			if !strings.Contains(err.Error(), digest) {
				t.Errorf("the error must quote the bad value %q, got: %v", digest, err)
			}
		})
	}
}

// The rename branch WHERE THE MANIFEST HAS TRACKED THE RENAME, pinned so nobody
// re-attributes it to D2a. Say which branch: this is only the tracked one —
// e.Files below lists the NEW name while booty's own registration allowlist
// still names the old one, so the MANIFEST-MEMBERSHIP check runs first and the
// allowlist error is what surfaces; checksumCovers is never reached.
//
// The OTHER rename branch, where the manifest LAGS (path advanced to the new
// release, files still naming the old ISO), passes membership and is caught by
// checksumCovers instead — pinned by
// TestTailsArtifactsFailLoudWhenSidecarOmitsACoveredFile above, whose fixture
// has exactly that shape. Neither branch can silently land unverified bytes.
func TestUpstreamRenameSurfacesTheAllowlistError(t *testing.T) {
	renamed := `
endpoints:
  tails:
    path: /asset-mirror/releases/download/7.10-17629562/
    files:
    - vmlinuz
    - initrd.img
    - 9990-misc-helpers.sh
    - tails-amd64-7.10.iso
    os: tails
    version: '7.10'
`
	serveToolFixture(t, renamed, func(path string) (string, int) {
		if strings.HasSuffix(path, "/sha256-checksums.txt") {
			return tailsDigest + "  tails-amd64-7.10.iso\n", http.StatusOK
		}
		return "BINARY", http.StatusOK
	})

	o, _ := Lookup("tails")
	_, err := o.Artifacts(context.Background(), "7.10-17629562", "amd64", nil)
	if err == nil {
		t.Fatal("an upstream rename must fail loudly")
	}
	if !strings.Contains(err.Error(), "not in the manifest entry") {
		t.Errorf("a rename must surface the MANIFEST-MEMBERSHIP error, not D2a's; got: %v", err)
	}
}

func TestNetbootxyzOSRequiresAllowlist(t *testing.T) {
	noFiles := netbootxyzOS{
		name:      "systemrescue",
		endpoints: map[string]string{"amd64": "systemrescue-amd64"},
		// files deliberately unset
	}
	serveFixture(t, fixtureDoc, nil, []netbootxyzOS{noFiles})
	if _, err := noFiles.Artifacts(context.Background(), "13.01-d20a63ac", "amd64", nil); err == nil {
		t.Fatal("Artifacts accepted an empty allowlist; every tool must declare files (D14)")
	}
}
