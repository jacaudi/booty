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
	"github.com/jeefy/booty/pkg/ostype"
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
