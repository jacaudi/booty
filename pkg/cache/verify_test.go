package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
// actually routed through downloadLargeFile (pkg/cache/isodownload.go), not
// merely that the outcome LOOKS the same as the ordinary staged path would
// produce. A plain single-shot 200 response can't discriminate the two: with
// no SHA256/SigURL declared, config.DownloadStaged would ALSO land the file at
// the final name with no .partial left behind and classNotVerifiable — an
// observably identical result. The discriminator is the Range header:
// downloadLargeFile resumes from an existing ".download" file with
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
	// Pre-seed the in-progress file downloadLargeFile resumes from. The
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
	// Discriminator: only downloadLargeFile ever sends a Range header. If the
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

// TestVerifyVersionToolIsNotVerifiable pins the short-circuit that keeps
// reverify from ever reaching Artifacts for a tool: tools carry no
// verification material at all, and Artifacts refuses any version that is
// not upstream's current tag — so calling it on an archived tool version
// would surface as a permanent 500 on POST /api/v1/cache/{id}/reverify
// instead of the "no verdict" this test requires.
//
// Adaptation note: the task brief's literal test called
// store.UpsertCacheEntry(db.CacheEntry{TargetVersionID: vid}), but the real
// pkg/db API (pkg/db/cache.go:47) is
// UpsertCacheEntry(targetVersionID, size int64) error — there is no
// db.CacheEntry input type. Adapted to the real signature below.
func TestVerifyVersionToolIsNotVerifiable(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())

	store := newReconcileStore(t)
	tid, err := store.CreateTarget(db.Target{
		OS: "memtest86plus", Arch: "amd64", Params: "{}", Enabled: true, RetainN: 1,
		Mode: "discovery", Source: "api",
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	// An ARCHIVED tag — deliberately NOT whatever upstream currently publishes.
	const staleTag = "7.00-deadbeef"
	if err := store.UpsertTargetVersion(db.TargetVersion{
		TargetID: tid, Version: staleTag, Source: "discovered",
	}); err != nil {
		t.Fatalf("UpsertTargetVersion: %v", err)
	}
	vid, err := store.TargetVersionID(tid, staleTag)
	if err != nil {
		t.Fatalf("TargetVersionID: %v", err)
	}
	if err := store.UpsertCacheEntry(vid, 0); err != nil {
		t.Fatalf("UpsertCacheEntry: %v", err)
	}
	rows, err := store.ListCacheEntries(db.CacheFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListCacheEntries: %v (%d rows)", err, len(rows))
	}

	// Must NOT error, and must NOT reach Artifacts (which would refuse the stale
	// tag and surface as a permanent 500 on reverify).
	verified, verifyErr, err := VerifyVersion(t.Context(), store, rows[0].ID)
	if err != nil {
		t.Fatalf("VerifyVersion on a tool = %v, want nil error", err)
	}
	if verified != nil {
		t.Errorf("verified = %v, want nil (no verdict)", verified)
	}
	if verifyErr != "" {
		t.Errorf("verifyErr = %q, want empty", verifyErr)
	}
}
