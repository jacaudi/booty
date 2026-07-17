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
