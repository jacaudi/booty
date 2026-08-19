package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
	"github.com/spf13/viper"
)

func writeFile(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func hexSHA(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// armorPublicKey serializes ent's public key into an armored PGP keyring block.
// go-crypto v1.4.1 has no top-level ArmorPublicKey helper: armoring is hand-rolled
// as ent.Serialize into an armor.Encode writer (closed before the buffer is read).
func armorPublicKey(t *testing.T, ent *openpgp.Entity) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ent.Serialize(w); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// gpgFixture generates a throwaway keypair, detach-signs body, and serves the
// binary signature at an httptest URL. Returns armored public keyring + sig URL.
func gpgFixture(t *testing.T, body []byte) (keyring []byte, sigURL string, closeFn func()) {
	t.Helper()
	ent, err := openpgp.NewEntity("test", "p3b", "t@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	var sig bytes.Buffer
	if err := openpgp.DetachSign(&sig, ent, bytes.NewReader(body), nil); err != nil {
		t.Fatal(err)
	}
	pub := armorPublicKey(t, ent)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(sig.Bytes())
	}))
	return pub, srv.URL + "/artifact.sig", srv.Close
}

func TestVerifyArtifact_SHA256(t *testing.T) {
	dir := t.TempDir()
	body := []byte("good-bytes")
	p := writeFile(t, dir, "rootfs.img", body)
	a := ostype.Artifact{Filename: "rootfs.img", URL: "https://ex/rootfs.img", SHA256: hexSHA(body)}

	if v := verifyArtifact(t.Context(), p, "", a); v.class != classPass {
		t.Errorf("matching sha256 must PASS, got class=%d err=%v", v.class, v.err)
	}
	bad := a
	bad.SHA256 = hexSHA([]byte("other"))
	if v := verifyArtifact(t.Context(), p, "", bad); v.class != classCorruption {
		t.Errorf("sha256 mismatch must be CORRUPTION, got class=%d", v.class)
	}
	// Streamed-hash path (land-path): no file read needed to detect mismatch.
	if v := verifyArtifact(t.Context(), p, hexSHA([]byte("other")), a); v.class != classCorruption {
		t.Errorf("streamed-hash mismatch must be CORRUPTION, got class=%d", v.class)
	}
}

func TestVerifyArtifact_NotVerifiable(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "kernel", []byte("x"))
	a := ostype.Artifact{Filename: "kernel", URL: "https://ex/kernel"} // no fields
	if v := verifyArtifact(t.Context(), p, "", a); v.class != classNotVerifiable {
		t.Errorf("empty fields must be NOT-VERIFIABLE, got class=%d", v.class)
	}
}

func TestVerifyArtifact_GPGPassForgeryUnknownKey(t *testing.T) {
	dir := t.TempDir()
	body := []byte("signed-artifact")
	p := writeFile(t, dir, "vmlinuz", body)
	keyring, sigURL, closeFn := gpgFixture(t, body)
	t.Cleanup(closeFn)

	pass := ostype.Artifact{Filename: "vmlinuz", URL: "https://ex/vmlinuz", SigURL: sigURL, GPGKey: keyring}
	if v := verifyArtifact(t.Context(), p, "", pass); v.class != classPass {
		t.Errorf("valid signature must PASS, got class=%d err=%v", v.class, v.err)
	}

	// Tamper the file → RSA verification failure → FORGERY.
	writeFile(t, dir, "vmlinuz", []byte("tampered!"))
	if v := verifyArtifact(t.Context(), p, "", pass); v.class != classForgery {
		t.Errorf("signature mismatch must be FORGERY, got class=%d err=%v", v.class, v.err)
	}

	// Verify against a DIFFERENT key → unknown issuer → CORRUPTION (benign).
	writeFile(t, dir, "vmlinuz", body)
	otherKeyring, _, closeFn2 := gpgFixture(t, body)
	t.Cleanup(closeFn2)
	wrongKey := pass
	wrongKey.GPGKey = otherKeyring
	if v := verifyArtifact(t.Context(), p, "", wrongKey); v.class != classCorruption {
		t.Errorf("unknown/expired key must be CORRUPTION, got class=%d err=%v", v.class, v.err)
	}
}

func TestVerifyArtifact_FailClosedOnUnobtainable(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "vmlinuz", []byte("x"))
	// Declared SigURL that 404s → CORRUPTION (fail-closed), never NULL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	t.Cleanup(srv.Close)
	a := ostype.Artifact{Filename: "vmlinuz", URL: "https://ex/vmlinuz", SigURL: srv.URL + "/x.sig", GPGKey: []byte("not-a-key")}
	if v := verifyArtifact(t.Context(), p, "", a); v.class != classCorruption {
		t.Errorf("unfetchable declared .sig must FAIL (corruption), got class=%d", v.class)
	}
}

// TestVerifyArtifact_FailClosedOnSig404 exercises the fail-closed guarantee on a
// DECLARED-but-unfetchable sidecar: a VALID keyring (so ReadArmoredKeyRing
// succeeds and control reaches the .sig fetch) plus a matching SHA256 (so the
// checksum arm passes) plus a SigURL that 404s. This forces the fetchBytes
// status>=400 → corruption branch, which must FAIL closed (classCorruption,
// non-nil err) — NEVER classNotVerifiable.
func TestVerifyArtifact_FailClosedOnSig404(t *testing.T) {
	dir := t.TempDir()
	body := []byte("declared-but-unfetchable")
	p := writeFile(t, dir, "vmlinuz", body)

	// Reuse gpgFixture's generated public keyring so keyring parse succeeds; we
	// only need a valid keyring here, not its (working) sig endpoint.
	keyring, _, closeFn := gpgFixture(t, body)
	t.Cleanup(closeFn)

	// A distinct sidecar endpoint that 404s. Matching SHA256 lets the checksum
	// arm pass so control reaches the .sig fetch rather than short-circuiting.
	sigSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(sigSrv.Close)

	a := ostype.Artifact{
		Filename: "vmlinuz",
		URL:      "https://ex/vmlinuz",
		SHA256:   hexSHA(body),
		SigURL:   sigSrv.URL + "/vmlinuz.sig",
		GPGKey:   keyring,
	}
	if v := verifyArtifact(t.Context(), p, "", a); v.class != classCorruption || v.err == nil {
		t.Errorf("declared .sig that 404s must FAIL closed (corruption, non-nil err), got class=%d err=%v", v.class, v.err)
	}
}

// TestVerifyArtifact_UnparseableKeyIsCorruption pins the classification of an
// unparseable armored keyring: with a REACHABLE served .sig (so fetchBytes and
// the file open both succeed and control actually reaches
// ReadArmoredKeyRing — unlike FailClosedOnUnobtainable, which 404s the sig so
// the parse is never hit), a malformed GPGKey must classify as CORRUPTION, NOT
// forgery. Corruption warn-lands while forgery is always rejected
// (TestLandArtifact_PolicyTable), so misclassifying a bad key as forgery would
// silently change warn-policy availability — the exact regression this guards.
// It matches verifyDetachedGPG's doc comment ("unparseable material … is
// CORRUPTION").
func TestVerifyArtifact_UnparseableKeyIsCorruption(t *testing.T) {
	dir := t.TempDir()
	body := []byte("signed-artifact")
	p := writeFile(t, dir, "vmlinuz", body)
	// gpgFixture serves a real detached sig over body at a reachable 200 URL; we
	// discard its keyring and substitute garbage so the parse is what fails.
	_, sigURL, closeFn := gpgFixture(t, body)
	t.Cleanup(closeFn)

	a := ostype.Artifact{Filename: "vmlinuz", URL: "https://ex/vmlinuz", SigURL: sigURL, GPGKey: []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\nnot a real key\n-----END PGP PUBLIC KEY BLOCK-----")}
	if v := verifyArtifact(t.Context(), p, "", a); v.class != classCorruption || v.err == nil {
		t.Errorf("unparseable keyring must be CORRUPTION (non-nil err), got class=%d err=%v", v.class, v.err)
	}
}

// TestArtifactVerdictZeroValueIsNotAPass pins WHY classUnset exists, not merely
// that it does. reconcile.go pre-allocates `verdicts := make([]artifactVerdict,
// len(arts))` alongside `landedFlags := make([]bool, len(arts))` — two parallel
// slices whose zero values default in OPPOSITE safety directions. A zero-valued
// bool reads as "rejected", which is safe; before classUnset a zero-valued
// artifactVerdict read as classPass, i.e. "verified OK", which is not.
//
// No partially-filled slice reaches aggregateVerdicts today, because
// `vg.Wait() != nil` abandons the whole version first. But D4b makes an error
// return from landArtifact a NORMAL path for Large artifacts, so that
// short-circuit now carries weight it did not carry before, and the zero value
// should fail closed on its own rather than by depending on it.
//
// This test goes red if someone folds classUnset away and puts classPass back
// at iota 0.
func TestArtifactVerdictZeroValueIsNotAPass(t *testing.T) {
	var zero artifactVerdict
	if zero.class == classPass {
		t.Fatal("artifactVerdict{} must not read as classPass: an unassigned verdict would count as verified OK")
	}
	if zero.class != classUnset {
		t.Fatalf("artifactVerdict{}.class = %d, want classUnset (%d): classUnset must hold the zero value", zero.class, classUnset)
	}
}

func TestAggregateVerdicts(t *testing.T) {
	// none verifiable → NULL
	if verified, _ := aggregateVerdicts([]artifactVerdict{{class: classNotVerifiable}, {class: classNotVerifiable}}); verified != nil {
		t.Errorf("all-not-verifiable must aggregate to NULL")
	}
	// all pass (≥1 verifiable) → true
	if verified, _ := aggregateVerdicts([]artifactVerdict{{class: classPass}, {class: classNotVerifiable}}); verified == nil || !*verified {
		t.Errorf("pass + not-verifiable must aggregate to true")
	}
	// any fail → false, errors.Join of all messages
	verified, msg := aggregateVerdicts([]artifactVerdict{
		{class: classCorruption, err: errString("checksum mismatch: kernel")},
		{class: classForgery, err: errString("signature mismatch: rootfs")},
	})
	if verified == nil || *verified {
		t.Fatalf("any failure must aggregate to false")
	}
	if !bytes.Contains([]byte(msg), []byte("checksum mismatch: kernel")) || !bytes.Contains([]byte(msg), []byte("signature mismatch: rootfs")) {
		t.Errorf("verify_err must join ALL failing messages, got %q", msg)
	}
	// Failure is driven by the verdict CLASS, not by err != nil: a forgery class
	// with a nil err must still land as false, never silently counted as a pass.
	if verified, _ := aggregateVerdicts([]artifactVerdict{{class: classForgery}}); verified == nil || *verified {
		t.Errorf("forgery class with nil err must aggregate to false")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// TestVerifyVersion_AbsentFinalWithPartialIsNull pins the split at VerifyVersion's
// absent-final handling (verify.go, re-review #8): a verifiable artifact whose
// FINAL file is absent yields NULL (no verdict) when a sibling <final>.partial
// exists (a re-download is in flight), but a FAILURE when no .partial exists. The
// FCOS current build declares sha256 (so the artifacts are verifiable and the
// absent-final branch is reached); the two phases share one seeding so the NULL is
// attributable to the .partial sibling, not to non-verifiable artifacts — Phase 2
// removes the partials and asserts FAILURE, discriminating the split.
func TestVerifyVersion_AbsentFinalWithPartialIsNull(t *testing.T) {
	ostype.ResetStreamsCache()
	t.Cleanup(ostype.ResetStreamsCache)

	// Current-build streams doc (release == the cached version) so Artifacts returns
	// three sha256-bearing (verifiable) artifacts with basenames kernel/initramfs/
	// rootfs. The sha256 values are never checked here — the absent-final short
	// circuit fires before any hashing.
	streams := `{
  "architectures": { "x86_64": { "artifacts": { "metal": {
    "release": "44.0.0.0",
    "formats": { "pxe": {
      "kernel":    { "location": "https://ex/44/kernel",    "sha256": "aaa" },
      "initramfs": { "location": "https://ex/44/initramfs", "sha256": "bbb" },
      "rootfs":    { "location": "https://ex/44/rootfs",    "sha256": "ccc" }
    } } } } } }
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(streams))
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.CoreOSStreamsURL, srv.URL+"/%s.json")
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")

	store := newReconcileStore(t)
	tid, err := store.CreateTarget(db.Target{OS: "fedora-coreos", Arch: "x86_64", Params: `{"channel":"stable"}`, Mode: "discovery", RetainN: 1, Source: "api", Enabled: true})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "44.0.0.0", Source: "discovered", Cached: true}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}
	tvID, err := store.TargetVersionID(tid, "44.0.0.0")
	if err != nil {
		t.Fatalf("TargetVersionID: %v", err)
	}
	if err := store.UpsertCacheEntry(tvID, 100); err != nil {
		t.Fatalf("UpsertCacheEntry: %v", err)
	}
	rows, err := store.ListCacheEntries(db.CacheFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListCacheEntries: %v (rows=%d)", err, len(rows))
	}
	id := rows[0].ID

	dir := cacheDir("coreos", "stable", "x86_64", "44.0.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	partials := []string{
		filepath.Join(dir, "kernel.partial"),
		filepath.Join(dir, "initramfs.partial"),
		filepath.Join(dir, "rootfs.partial"),
	}

	// Phase 1: finals absent, a sibling .partial present → re-download in flight → NULL.
	for _, p := range partials {
		if err := os.WriteFile(p, []byte("in-flight"), 0o644); err != nil {
			t.Fatalf("write partial: %v", err)
		}
	}
	verified, verifyErr, err := VerifyVersion(t.Context(), store, id)
	if err != nil {
		t.Fatalf("VerifyVersion (partial present): %v", err)
	}
	if verified != nil {
		t.Fatalf("absent final WITH sibling .partial must be NULL (no verdict), got verified=%v verifyErr=%q", *verified, verifyErr)
	}
	if verifyErr != "" {
		t.Fatalf("NULL verdict must carry no verify_err, got %q", verifyErr)
	}

	// Phase 2 (proves the split): identical seeding, no .partial → absent final FAILS.
	for _, p := range partials {
		if err := os.Remove(p); err != nil {
			t.Fatalf("remove partial: %v", err)
		}
	}
	verified, verifyErr, err = VerifyVersion(t.Context(), store, id)
	if err != nil {
		t.Fatalf("VerifyVersion (no partial): %v", err)
	}
	if verified == nil || *verified {
		t.Fatalf("absent final with NO .partial must FAIL (verified=false), got verified=%v", verified)
	}
	if verifyErr == "" {
		t.Fatalf("failure verdict must carry a non-empty verify_err")
	}
}

// TestLandArtifactLargeUsesResumableDownloader proves the Large artifact is
// actually routed through downloadLargeInto (pkg/cache/isodownload.go), not
// merely that the outcome LOOKS the same as the ordinary staged path would
// produce. A plain single-shot 200 response can't discriminate the two: with
// no SHA256/SigURL declared, config.DownloadStaged would ALSO land the file at
// the final name with no .partial left behind and classNotVerifiable — an
// observably identical result. The discriminator is the Range header:
// downloadLargeInto resumes from an existing ".download" file with
// `Range: bytes=<offset>-`; config.DownloadStaged never sends one. So this
// test pre-seeds a ".download" prefix (as a prior, interrupted attempt would
// leave behind) and asserts the server actually received a Range request —
// an assertion only satisfiable by the Large routing firing — plus that the
// landed file is the full prefix+remainder, proving the resume reassembled
// it rather than truncating.
func TestLandArtifactLargeUsesResumableDownloader(t *testing.T) {
	full := []byte("LARGE-PAYLOAD-CONTENT-FOR-RESUME-TEST")
	prefix := full[:8]    // what a prior, interrupted attempt already wrote
	remainder := full[8:] // what the server must supply on resume

	gotRange := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			gotRange <- true
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(remainder)
			return
		}
		gotRange <- false
		_, _ = w.Write(full)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	// Pre-seed the in-progress file downloadLargeInto resumes from. The
	// ".download" suffix (not ".partial") is what lets it survive
	// SweepPartials between reconcile passes.
	if err := os.WriteFile(filepath.Join(dir, "big.iso.download"), prefix, 0o644); err != nil {
		t.Fatalf("seed .download: %v", err)
	}

	a := ostype.Artifact{Filename: "big.iso", URL: srv.URL + "/big.iso", Large: true}
	landed, v, err := landArtifact(t.Context(), dir, a, "strict")
	if err != nil {
		t.Fatalf("landArtifact: %v", err)
	}
	if !landed {
		t.Fatal("large artifact did not land")
	}
	if v.class != classNotVerifiable {
		t.Errorf("class = %v, want classNotVerifiable", v.class)
	}
	// It must land at the FINAL name with no .partial left behind: the large
	// path writes via a ".download" file that survives SweepPartials.
	if _, err := os.Stat(filepath.Join(dir, "big.iso")); err != nil {
		t.Errorf("final file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.iso.partial")); err == nil {
		t.Error("a .partial was left behind; the large path must not use .partial")
	}
	// Discriminator: only downloadLargeInto ever sends a Range header. If the
	// Large routing were dropped and landArtifact fell through to
	// config.DownloadStaged instead, the request would arrive with no Range
	// header and this assertion would fail.
	if !<-gotRange {
		t.Error("server did not receive a Range header; Large artifact was not routed through the resumable downloader")
	}
	got, err := os.ReadFile(filepath.Join(dir, "big.iso"))
	if err != nil {
		t.Fatalf("read landed file: %v", err)
	}
	if string(got) != string(full) {
		t.Errorf("landed content = %q, want %q (resume must reassemble prefix+remainder, not truncate)", got, full)
	}
}

// TestVerifyVersionToolWithoutMaterialIsNoVerdict: a tool that declares no
// checksums has nothing to check, so reverify aggregates zero verifiable
// artifacts and records NULL. Before D5 this was delivered by a family
// short-circuit; now it falls out of aggregateVerdicts, which is the more
// honest mechanism — but it must still hold.
func TestVerifyVersionToolWithoutMaterialIsNoVerdict(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())

	const tag = "8.00-32a14678"
	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("BINARY"))
	}))
	t.Cleanup(assets.Close)
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("endpoints:\n" +
			"  memtest86plus:\n" +
			"    path: /asset-mirror/releases/download/" + tag + "/\n" +
			"    files:\n" +
			"    - mt86p_x86_64\n" +
			"    os: memtest86-plus\n" +
			"    version: '8.00'\n"))
	}))
	t.Cleanup(manifest.Close)
	viper.Set(config.NetbootxyzEndpointsURL, manifest.URL)
	viper.Set(config.NetbootxyzAssetBase, assets.URL)
	ostype.ResetNetbootxyzCache()
	t.Cleanup(ostype.ResetNetbootxyzCache)

	store := newReconcileStore(t)
	tid, err := store.CreateTarget(db.Target{
		OS: "memtest86plus", Arch: "amd64", Params: "{}", Enabled: true, RetainN: 1,
		Mode: "discovery", Source: "api",
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := store.UpsertTargetVersion(db.TargetVersion{
		TargetID: tid, Version: tag, Source: "discovered", Cached: true,
	}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}
	tvID, err := store.TargetVersionID(tid, tag)
	if err != nil {
		t.Fatalf("TargetVersionID: %v", err)
	}
	if err := store.UpsertCacheEntry(tvID, 10); err != nil {
		t.Fatalf("UpsertCacheEntry: %v", err)
	}
	rows, err := store.ListCacheEntries(db.CacheFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListCacheEntries: %v (rows=%d)", err, len(rows))
	}

	verified, verifyErr, err := VerifyVersion(t.Context(), store, rows[0].ID)
	if err != nil {
		t.Fatalf("VerifyVersion: %v", err)
	}
	if verified != nil {
		t.Fatalf("a tool with no verification material must record NULL, got %v (%q)", *verified, verifyErr)
	}
	if verifyErr != "" {
		t.Errorf("verifyErr = %q, want empty: no verdict must carry no failure message", verifyErr)
	}
}

// tailsReverifyFixture stands up a manifest + asset host for tails and returns
// the release tag they publish.
func tailsReverifyFixture(t *testing.T, isoBody []byte) string {
	t.Helper()
	const tag = "7.10-17629562"
	sidecar := hexSHA(isoBody) + "  tails-amd64.iso\n"

	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sha256-checksums.txt") {
			_, _ = w.Write([]byte(sidecar))
			return
		}
		_, _ = w.Write([]byte("BINARY"))
	}))
	t.Cleanup(assets.Close)

	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("endpoints:\n" +
			"  tails:\n" +
			"    path: /asset-mirror/releases/download/" + tag + "/\n" +
			"    files:\n" +
			"    - vmlinuz\n" +
			"    - initrd.img\n" +
			"    - 9990-misc-helpers.sh\n" +
			"    - tails-amd64.iso\n" +
			"    os: tails\n" +
			"    version: '7.10'\n"))
	}))
	t.Cleanup(manifest.Close)

	viper.Set(config.NetbootxyzEndpointsURL, manifest.URL)
	viper.Set(config.NetbootxyzAssetBase, assets.URL)
	ostype.ResetNetbootxyzCache()
	t.Cleanup(ostype.ResetNetbootxyzCache)
	return tag
}

// seedTailsVersion creates a tails target + version + cache_entries row and
// returns (cache_entries.id, version dir).
func seedTailsVersion(t *testing.T, store *db.Store, tag string) (int64, string) {
	t.Helper()
	tid, err := store.CreateTarget(db.Target{
		OS: "tails", Arch: "amd64", Params: "{}", Enabled: true, RetainN: 1,
		Mode: "discovery", Source: "catalog",
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := store.UpsertTargetVersion(db.TargetVersion{
		TargetID: tid, Version: tag, Source: "discovered", Cached: true,
	}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}
	tvID, err := store.TargetVersionID(tid, tag)
	if err != nil {
		t.Fatalf("TargetVersionID: %v", err)
	}
	if err := store.UpsertCacheEntry(tvID, 100); err != nil {
		t.Fatalf("UpsertCacheEntry: %v", err)
	}
	rows, err := store.ListCacheEntries(db.CacheFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListCacheEntries: %v (rows=%d)", err, len(rows))
	}
	return rows[0].ID, cacheDir("tails", "-", "amd64", tag)
}

// D5 — reverify now reaches Artifacts for a tool that declares material, so a
// Tails version on disk gets a real verdict instead of a permanent NULL.
func TestVerifyVersionTailsProducesAVerdict(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())

	iso := []byte("GOOD-ISO-BYTES")
	tag := tailsReverifyFixture(t, iso)
	store := newReconcileStore(t)
	id, dir := seedTailsVersion(t, store, tag)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"vmlinuz": []byte("k"), "initrd.img": []byte("i"),
		"9990-misc-helpers.sh": []byte("h"), "tails-amd64.iso": iso,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	verified, verifyErr, err := VerifyVersion(t.Context(), store, id)
	if err != nil {
		t.Fatalf("VerifyVersion: %v", err)
	}
	if verified == nil || !*verified {
		t.Fatalf("a good Tails version must reverify to verified=true, got %v (err %q)", verified, verifyErr)
	}

	// And a corrupted ISO must FAIL rather than pass or return no verdict.
	if err := os.WriteFile(filepath.Join(dir, "tails-amd64.iso"), []byte("CORRUPTED"), 0o644); err != nil {
		t.Fatal(err)
	}
	verified, verifyErr, err = VerifyVersion(t.Context(), store, id)
	if err != nil {
		t.Fatalf("VerifyVersion (corrupted): %v", err)
	}
	if verified == nil || *verified {
		t.Fatalf("a corrupted ISO must reverify to verified=false, got %v", verified)
	}
	if verifyErr == "" {
		t.Error("a failure verdict must carry a non-empty verify_err")
	}
}

// §6.2 — a Large artifact's in-flight sibling is DownloadSuffix, never
// ".partial". Checking only ".partial" returns "artifact absent" ->
// classCorruption: a FALSE FAILURE VERDICT on a perfectly healthy system that
// is simply mid-resume.
func TestVerifyVersionTailsInFlightResumeIsNoVerdict(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())

	iso := []byte("GOOD-ISO-BYTES")
	tag := tailsReverifyFixture(t, iso)
	store := newReconcileStore(t)
	id, dir := seedTailsVersion(t, store, tag)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vmlinuz", "initrd.img", "9990-misc-helpers.sh"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The ISO's FINAL file is absent and a resumable in-progress sibling exists.
	if err := os.WriteFile(filepath.Join(dir, "tails-amd64.iso"+DownloadSuffix), iso[:4], 0o644); err != nil {
		t.Fatal(err)
	}

	verified, verifyErr, err := VerifyVersion(t.Context(), store, id)
	if err != nil {
		t.Fatalf("VerifyVersion: %v", err)
	}
	if verified != nil {
		t.Fatalf("a live resume must yield NO VERDICT, got verified=%v verifyErr=%q", *verified, verifyErr)
	}
}

// §6.3 — Artifacts refuses any version that is not the entry's current tag.
// That refusal must map to "no verdict" (what an archived tool returned before
// D5 lifted the short-circuit), not to a permanent HTTP 500 on reverify.
func TestVerifyVersionSupersededToolTagIsNoVerdictNotAnError(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())

	tailsReverifyFixture(t, []byte("GOOD-ISO-BYTES"))
	store := newReconcileStore(t)
	// Seed an ARCHIVED tag — deliberately NOT what the manifest publishes.
	id, _ := seedTailsVersion(t, store, "6.6-cfd50f75")

	verified, verifyErr, err := VerifyVersion(t.Context(), store, id)
	if err != nil {
		t.Fatalf("a superseded tag must be NO VERDICT, not an error (reverify would 500): %v", err)
	}
	if verified != nil {
		t.Fatalf("superseded tag must yield no verdict, got verified=%v verifyErr=%q", *verified, verifyErr)
	}
}

// §6.3 maps ErrVersionSuperseded — and ONLY that — to "no verdict". Every other
// Artifacts error still propagates, so a transient sidecar or manifest failure
// surfaces as a reverify 500 rather than a reassuring NULL. That is new for
// tools and consistent with how FCOS already behaves.
func TestVerifyVersionArtifactsErrorPropagates(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())

	// A manifest server that always 500s: entryFor fails before any tag compare,
	// so the error is emphatically NOT ErrVersionSuperseded.
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(manifest.Close)
	viper.Set(config.NetbootxyzEndpointsURL, manifest.URL)
	viper.Set(config.NetbootxyzAssetBase, "http://127.0.0.1:1")
	ostype.ResetNetbootxyzCache()
	t.Cleanup(ostype.ResetNetbootxyzCache)

	store := newReconcileStore(t)
	id, _ := seedTailsVersion(t, store, "7.10-17629562")

	_, _, err := VerifyVersion(t.Context(), store, id)
	if err == nil {
		t.Fatal("a manifest failure is NOT a superseded tag and must propagate; " +
			"swallowing it would report a clean no-verdict for a version nobody could check")
	}
}

// largeServer serves body, honouring Range so a pre-seeded in-progress file
// resumes rather than restarting. Returns the URL for "big.iso".
func largeServer(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "big.iso", time.Unix(0, 0), bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/big.iso"
}

// D4a — THE LOAD-BEARING TEST. A Large artifact whose digest does not match
// must be REJECTED under `warn` as well as `strict`.
//
// Without this, the default policy lands a corruption verdict, and
// reconcile.go's settled-skip (cached=1 + all final files present, which
// deliberately does NOT consult `verified`) then skips that version FOREVER —
// so a truncated 1.94 GB anonymity distro lands, is served to netboot clients,
// and is never re-downloaded, not even after switching to `strict`, because
// policy tightening is non-retroactive. `warn` has no availability to trade
// here: D2 rejects an unfetchable or malformed sidecar before the download, and
// D4b routes a local read failure out as an error rather than a verdict, so
// classCorruption on a Large artifact means a definitive MISMATCH.
func TestLandArtifactLargeChecksumFailureRejectedUnderWarnAndStrict(t *testing.T) {
	body := []byte("REAL-ISO-BYTES-THAT-WILL-NOT-MATCH")
	url := largeServer(t, body)

	for _, policy := range []string{"warn", "strict"} {
		t.Run(policy, func(t *testing.T) {
			dir := t.TempDir()
			a := ostype.Artifact{
				Filename: "big.iso",
				URL:      url,
				Large:    true,
				SHA256:   hexSHA([]byte("completely-different-bytes")),
			}
			landed, v, err := landArtifact(t.Context(), dir, a, policy)
			if err != nil {
				t.Fatalf("a digest MISMATCH is a verdict, not a transport error: %v", err)
			}
			if landed {
				t.Fatalf("policy=%s: a Large artifact failing its checksum must NOT land", policy)
			}
			if v.class != classCorruption {
				t.Errorf("policy=%s: class = %d, want classCorruption", policy, v.class)
			}
			if _, err := os.Stat(filepath.Join(dir, "big.iso")); !os.IsNotExist(err) {
				t.Errorf("policy=%s: bytes are sitting at the FINAL path where /data/ would serve them", policy)
			}
			if _, err := os.Stat(filepath.Join(dir, "big.iso"+DownloadSuffix)); !os.IsNotExist(err) {
				t.Errorf("policy=%s: the rejected in-progress file must be removed — resuming a file "+
					"known to hash wrong appends good bytes to bad ones forever", policy)
			}
		})
	}
}

// The happy path: a Large artifact whose digest matches lands, and the verdict
// aggregates to verified=1 (an improvement on today's permanent NULL for tools).
func TestLandArtifactLargeCorrectChecksumLandsVerified(t *testing.T) {
	body := []byte("REAL-ISO-BYTES")
	url := largeServer(t, body)

	dir := t.TempDir()
	a := ostype.Artifact{Filename: "big.iso", URL: url, Large: true, SHA256: hexSHA(body)}

	landed, v, err := landArtifact(t.Context(), dir, a, "strict")
	if err != nil {
		t.Fatalf("landArtifact: %v", err)
	}
	if !landed || v.class != classPass {
		t.Fatalf("landed=%v class=%d, want landed=true classPass", landed, v.class)
	}
	got, err := os.ReadFile(filepath.Join(dir, "big.iso"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("final file wrong (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.iso"+DownloadSuffix)); !os.IsNotExist(err) {
		t.Error("the in-progress file must be gone after a successful land")
	}
	verified, verifyErr := aggregateVerdicts([]artifactVerdict{v})
	if verified == nil || !*verified {
		t.Fatalf("version verdict = %v (err %q), want verified=true", verified, verifyErr)
	}
}

// D4 — the fail-closed guard is NARROWED to SigURL, never deleted. SigURL's
// existing path expects a signature over a CHECKSUM FILE against a known
// embedded keyring, not a detached signature over a multi-GB ISO whose key
// booty does not ship. Declaring one must still hard-fail.
func TestLandArtifactLargeWithSigURLStillHardFails(t *testing.T) {
	dir := t.TempDir()
	a := ostype.Artifact{
		Filename: "big.iso",
		URL:      "https://ex/big.iso",
		Large:    true,
		SigURL:   "https://ex/big.iso.sig",
	}
	_, _, err := landArtifact(t.Context(), dir, a, "strict")
	if err == nil {
		t.Fatal("a Large artifact declaring SigURL must hard-fail; detached-sig verification is unsupported there")
	}
	if !strings.Contains(err.Error(), "detached-signature") {
		t.Errorf("error must name the unsupported mechanism, got: %v", err)
	}
}

// D4b — "I could not evaluate the material" is INFRASTRUCTURE, not a verdict.
//
// verifyArtifact classifies its own hashFile read failure as classCorruption,
// and hashFile is reachable on the land path ONLY for Large artifacts (the
// staged path always carries a streamed digest). Under D4a that would convert a
// transient local read error — a NAS blip, fd exhaustion under CacheConcurrency,
// an operator clearing the cache dir mid-pass — into: delete the completed
// 1.94 GB file, RemoveAll the version directory, and arm the retry guard for an
// hour. So landArtifact hashes the Large file ITSELF and returns the read
// failure as an error, which routes to reconcile.go's `vg.Wait() != nil ->
// continue`: no row written, no version dir removed, and the resumable bytes
// left on disk to resume next tick.
func TestLandArtifactLargeHashReadFailureIsAnErrorNotAVerdict(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses the mode bits this test relies on")
	}
	body := []byte("COMPLETE-ISO-BYTES-ALREADY-ON-DISK")

	// 416: a prior attempt already wrote every byte, so this attempt streams
	// nothing and the in-progress file is exactly what we seeded. That lets the
	// test control the file's mode without racing the downloader.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	inProgress := filepath.Join(dir, "big.iso"+DownloadSuffix)
	// 0o200 = write-only: downloadLargeInto's O_WRONLY|O_APPEND open succeeds,
	// hashFile's read-only open does not.
	if err := os.WriteFile(inProgress, body, 0o200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inProgress, 0o644) })

	a := ostype.Artifact{Filename: "big.iso", URL: srv.URL + "/big.iso", Large: true, SHA256: hexSHA(body)}
	landed, v, err := landArtifact(t.Context(), dir, a, "warn")

	if err == nil {
		t.Fatalf("an unreadable in-progress file is INFRASTRUCTURE and must return an error; got landed=%v class=%d", landed, v.class)
	}
	// Naming the mechanism is what makes this test fail in the RED phase. The
	// pre-existing hard-fail guard ALSO returns an error, leaves the file
	// untouched and lands nothing — so every other assertion here is satisfied
	// by the code this task replaces. Only the message discriminates.
	if !strings.Contains(err.Error(), "cache: hash") {
		t.Fatalf("the error must come from landArtifact's own hashFile call, got: %v", err)
	}
	if landed {
		t.Error("nothing may land when the digest could not be evaluated")
	}
	if _, serr := os.Stat(inProgress); serr != nil {
		t.Error("the resumable bytes must SURVIVE a read failure so the next tick resumes rather than re-downloading 1.94 GB")
	}
	if _, serr := os.Stat(filepath.Join(dir, "big.iso")); !os.IsNotExist(serr) {
		t.Error("unevaluated bytes must not reach the final path")
	}
}

// §5.2 pins an ACKNOWLEDGED reachable path rather than claiming it away:
// policy "off" short-circuits before verification, exactly as it does for
// staged artifacts. "off" means off. This test exists so that stays deliberate
// instead of becoming an accident.
func TestLandArtifactLargeUnderPolicyOffLandsUnverified(t *testing.T) {
	body := []byte("UNVERIFIED-BUT-OFF")
	url := largeServer(t, body)

	dir := t.TempDir()
	a := ostype.Artifact{Filename: "big.iso", URL: url, Large: true, SHA256: hexSHA([]byte("wrong"))}

	landed, v, err := landArtifact(t.Context(), dir, a, "off")
	if err != nil {
		t.Fatalf("landArtifact: %v", err)
	}
	if !landed || v.class != classNotVerifiable {
		t.Fatalf("landed=%v class=%d, want landed=true classNotVerifiable under policy=off", landed, v.class)
	}
}

// D3 — hash the COMPLETED file, never the stream. downloadLargeInto resumes via
// Range, so a resumed transfer's stream carries only the DELTA. Hashing the
// stream would compute the digest of the remainder and reject a perfectly good
// ISO. Pre-seeding a prefix and declaring the digest of the WHOLE payload is
// the only assertion that separates the two.
// The Range assertion is load-bearing, not decoration. Without it the test
// passes in a world where the resume never happens at all: if the in-progress
// path were wrong, the server would serve the FULL body from offset 0, the
// digest would still match hexSHA(full), and the file would still land. Only
// "the server actually received a Range header" separates a real resume from a
// silent restart-from-scratch.
func TestLandArtifactLargeHashesWholeFileNotTheResumeDelta(t *testing.T) {
	full := []byte("PREFIX-BYTES-AND-THEN-THE-REMAINDER-OF-THE-ISO")
	prefix := full[:12]

	gotRange := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange <- r.Header.Get("Range")
		http.ServeContent(w, r, "big.iso", time.Unix(0, 0), bytes.NewReader(full))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.iso"+DownloadSuffix), prefix, 0o644); err != nil {
		t.Fatal(err)
	}

	a := ostype.Artifact{Filename: "big.iso", URL: srv.URL + "/big.iso", Large: true, SHA256: hexSHA(full)}
	landed, v, err := landArtifact(t.Context(), dir, a, "strict")
	if err != nil {
		t.Fatalf("landArtifact: %v", err)
	}
	if !landed || v.class != classPass {
		t.Fatalf("landed=%v class=%d — the digest must cover the WHOLE file, not the resume delta", landed, v.class)
	}
	if rng := <-gotRange; rng != "bytes=12-" {
		t.Fatalf("server saw Range %q, want \"bytes=12-\" — the pre-seeded prefix was not resumed from, "+
			"so this test would pass even against a restart-from-zero", rng)
	}
	got, err := os.ReadFile(filepath.Join(dir, "big.iso"))
	if err != nil || !bytes.Equal(got, full) {
		t.Fatalf("landed content must be prefix+remainder (err=%v)", err)
	}
}

// The 416 branch streams NOTHING at all, so a stream-derived digest would be the
// hash of the empty string. Both arms: correct digest lands, wrong digest is
// rejected rather than renamed unverified.
func TestLandArtifactLarge416VerifiesBeforeLanding(t *testing.T) {
	body := []byte("FULL-ISO-FROM-A-PRIOR-ATTEMPT")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	t.Cleanup(srv.Close)

	t.Run("correct digest lands", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "big.iso"+DownloadSuffix), body, 0o644); err != nil {
			t.Fatal(err)
		}
		a := ostype.Artifact{Filename: "big.iso", URL: srv.URL + "/big.iso", Large: true, SHA256: hexSHA(body)}
		landed, v, err := landArtifact(t.Context(), dir, a, "strict")
		if err != nil || !landed || v.class != classPass {
			t.Fatalf("landed=%v class=%d err=%v, want landed=true classPass", landed, v.class, err)
		}
	})

	t.Run("wrong digest is rejected", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "big.iso"+DownloadSuffix), body, 0o644); err != nil {
			t.Fatal(err)
		}
		a := ostype.Artifact{Filename: "big.iso", URL: srv.URL + "/big.iso", Large: true, SHA256: hexSHA([]byte("nope"))}
		landed, v, err := landArtifact(t.Context(), dir, a, "warn")
		if err != nil {
			t.Fatalf("a mismatch is a verdict, not an error: %v", err)
		}
		if landed || v.class != classCorruption {
			t.Fatalf("landed=%v class=%d — the 416 path must verify BEFORE renaming", landed, v.class)
		}
		if _, err := os.Stat(filepath.Join(dir, "big.iso")); !os.IsNotExist(err) {
			t.Error("the 416 path renamed an unverified file into the final path")
		}
	})
}
