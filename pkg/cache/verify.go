package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
)

// verifyClass separates a forgery signal (never boots) from a corruption signal
// (warn lands it) so the policy (§5, D15) can treat them differently.
type verifyClass int

const (
	classPass          verifyClass = iota // verified OK
	classNotVerifiable                    // no mechanism declared (empty fields)
	classCorruption                       // sha256 mismatch / bad-or-unfetchable sidecar / unknown-or-expired key
	classForgery                          // GPG signature does not validate — tamper
)

// artifactVerdict is one artifact's verification outcome. err carries the
// failure-class message ("checksum mismatch", "signature mismatch", …) for
// corruption/forgery; it is nil for pass/not-verifiable.
type artifactVerdict struct {
	class verifyClass
	err   error
}

// sidecarClient bounds the small detached-signature fetch.
var sidecarClient = &http.Client{Timeout: 30 * time.Second}

// verifyArtifact checks one file against its Artifact's declared material and
// classifies the outcome. It is the SINGLE per-file check shared by the land-
// path (streamedSHA256 = the hash DownloadStaged computed while streaming) and
// VerifyVersion (streamedSHA256 = "" → hash the on-disk file). Fail-closed: a
// DECLARED sha256/.sig that cannot be evaluated is corruption, never NULL.
func verifyArtifact(ctx context.Context, filePath, streamedSHA256 string, a ostype.Artifact) artifactVerdict {
	if a.SHA256 == "" && a.SigURL == "" {
		return artifactVerdict{class: classNotVerifiable}
	}
	if a.SHA256 != "" {
		got := streamedSHA256
		if got == "" {
			h, err := hashFile(filePath)
			if err != nil {
				return artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: checksum unavailable: %w", a.Filename, err)}
			}
			got = h
		}
		if got != a.SHA256 {
			return artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: checksum mismatch", a.Filename)}
		}
	}
	if a.SigURL != "" {
		if v := verifyDetachedGPG(ctx, filePath, a); v.class != classPass {
			return v
		}
	}
	return artifactVerdict{class: classPass}
}

// landArtifact stages one artifact to <dir>/<file>.partial, verifies it per
// policy + failure class (D15), then renames it into place (land) or deletes it
// (reject). Returns whether bytes now sit at the final path and the verdict for
// version-level aggregation + recording. err != nil is a transport/IO failure
// (nothing landed; retry next tick). Under `off`, verification does not run and
// the verdict is not-verifiable (verified stays NULL). This is the download-side
// twin of VerifyVersion: it shares verifyArtifact + aggregateVerdicts, using the
// hash DownloadStaged computed while streaming (no second read on the hot path).
func landArtifact(ctx context.Context, dir string, a ostype.Artifact, policy string) (bool, artifactVerdict, error) {
	// DownloadStaged only os.Creates the .partial; it does not create the
	// version dir. The retired ensureArtifact used to MkdirAll before every
	// fetch, so replicate that here (idempotent, safe under concurrent calls).
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, artifactVerdict{}, fmt.Errorf("cache: mkdir %s: %w", dir, err)
	}
	partial, streamedSHA, err := config.DownloadStaged(ctx, dir, a.URL)
	if err != nil {
		return false, artifactVerdict{}, err
	}
	final := strings.TrimSuffix(partial, ".partial")

	land := func(v artifactVerdict) (bool, artifactVerdict, error) {
		if err := os.Rename(partial, final); err != nil {
			return false, artifactVerdict{}, fmt.Errorf("cache: land %s: %w", final, err)
		}
		return true, v, nil
	}
	reject := func(v artifactVerdict) (bool, artifactVerdict, error) {
		_ = os.Remove(partial)
		return false, v, nil
	}

	if policy == "off" {
		return land(artifactVerdict{class: classNotVerifiable})
	}
	v := verifyArtifact(ctx, partial, streamedSHA, a)
	switch v.class {
	case classPass, classNotVerifiable:
		return land(v)
	case classForgery:
		return reject(v) // never boots — refused under warn AND strict
	case classCorruption:
		if policy == "warn" {
			return land(v) // availability trade-off warn exists for
		}
		return reject(v) // strict
	default:
		return reject(v)
	}
}

func hashFile(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// errKeyringParse tags a failure to parse the armored keyring in the shared
// checkDetachedSignature core, so verifyDetachedGPG can classify it as
// CORRUPTION (benign / fail-closed, per its doc comment) rather than letting it
// fall through to the FORGERY default arm. Without this, moving the parse into
// the shared helper would silently reclassify a malformed key as forgery —
// changing warn-policy availability (forgery is always rejected; corruption
// warn-lands).
var errKeyringParse = errors.New("keyring parse")

// pgpArmorHeader is the leading line of an ASCII-armored OpenPGP signature
// block. checkDetachedSignature sniffs for it to pick the armored vs binary
// verifier — Flatcar's detached .sig sidecars are binary, but Debian's
// cdimage SHA256SUMS.sign is ASCII-armored, and CheckDetachedSignature
// rejects armored input outright (fails on the leading '-' byte).
var pgpArmorHeader = []byte("-----BEGIN PGP")

// checkDetachedSignature is the shared openpgp core for detached-signature
// verification: parse the armored keyring, then check sig (armored or binary,
// detected by sniffing for the ASCII-armor header) over signed. Both
// verifyDetachedGPG (fetches the signature over HTTP) and
// verifyDetachedGPGLocal (reads it from disk) call this single helper so "how
// we check a detached sig" stays single-sourced (DRY). A keyring-parse failure
// is wrapped with errKeyringParse so callers can distinguish it from a genuine
// signature-verification failure.
func checkDetachedSignature(key []byte, signed io.Reader, sig []byte) error {
	keyring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(key))
	if err != nil {
		return fmt.Errorf("%w: %v", errKeyringParse, err)
	}
	if bytes.HasPrefix(bytes.TrimSpace(sig), pgpArmorHeader) {
		_, err = openpgp.CheckArmoredDetachedSignature(keyring, signed, bytes.NewReader(sig), nil)
		return err
	}
	_, err = openpgp.CheckDetachedSignature(keyring, signed, bytes.NewReader(sig), nil)
	return err
}

// verifyDetachedGPG fetches the detached BINARY signature at a.SigURL and checks
// it over filePath against a.GPGKey (armored keyring). Unfetchable/unparseable
// material and an unknown/expired key are CORRUPTION (benign / fail-closed); a
// genuine verification failure is FORGERY (tamper). The .sig is binary →
// CheckDetachedSignature (not the Armored variant); the key file is armored →
// ReadArmoredKeyRing (spike §9).
func verifyDetachedGPG(ctx context.Context, filePath string, a ostype.Artifact) artifactVerdict {
	sig, err := fetchBytes(ctx, a.SigURL)
	if err != nil {
		return artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: signature material unavailable: %w", a.Filename, err)}
	}
	signed, err := os.Open(filePath)
	if err != nil {
		return artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: open for verify: %w", a.Filename, err)}
	}
	defer signed.Close()

	err = checkDetachedSignature(a.GPGKey, signed, sig)
	switch {
	case err == nil:
		return artifactVerdict{class: classPass}
	case errors.Is(err, errKeyringParse):
		// An unparseable/malformed armored key is CORRUPTION (benign / fail-
		// closed), NOT forgery — matching this function's doc comment and the
		// pre-DRY-refactor behavior. Guards warn-policy availability (§5).
		return artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: keyring parse: %w", a.Filename, err)}
	case errors.Is(err, pgperrors.ErrUnknownIssuer), errors.Is(err, pgperrors.ErrKeyExpired), errors.Is(err, pgperrors.ErrSignatureExpired):
		// ErrSignatureExpired (a signature-packet expiry, distinct from key
		// expiry) joins the same benign arm as ErrKeyExpired — matching the
		// design's "expiry is a benign availability trade-off" classification
		// (§5). Inert for Flatcar's current non-expiring SHA-256 sigs; folding it
		// here prevents a future warn-brick were an expiring signature adopted.
		return artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: unknown or expired signing key", a.Filename)}
	default:
		return artifactVerdict{class: classForgery, err: fmt.Errorf("%s: signature mismatch: %w", a.Filename, err)}
	}
}

// verifyDetachedGPGLocal checks the detached BINARY signature at sigPath over
// signedPath against key (armored keyring), reading both from disk — no HTTP
// fetch. Used offline for material already downloaded to the cache (e.g. a
// DVD ISO's SHA256SUMS/SHA256SUMS.sign pair), sharing checkDetachedSignature's
// openpgp core with verifyDetachedGPG rather than re-parsing/re-checking
// independently.
func verifyDetachedGPGLocal(signedPath, sigPath string, key []byte) error {
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("%s: read signature: %w", sigPath, err)
	}
	signed, err := os.Open(signedPath)
	if err != nil {
		return fmt.Errorf("%s: open for verify: %w", signedPath, err)
	}
	defer signed.Close()

	if err := checkDetachedSignature(key, signed, sig); err != nil {
		return fmt.Errorf("%s: signature verification failed: %w", signedPath, err)
	}
	return nil
}

func fetchBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := sidecarClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get %s: status %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// aggregateVerdicts folds per-artifact verdicts into a version-level verdict:
// verified=true iff every verifiable artifact passed AND at least one was
// verifiable; verified=false if any failed; NULL (nil) if none were verifiable.
// verify_err is the errors.Join of every failing artifact's message (design §5,
// re-review #12) — this exact definition also appears in DATABASE.md/API.md.
func aggregateVerdicts(vs []artifactVerdict) (*bool, string) {
	verifiable := 0
	failed := false
	var errs []error
	for _, v := range vs {
		// Disposition is driven by the verdict CLASS, not by err != nil: a
		// pass/not-verifiable lands, a corruption/forgery fails. Keying failure
		// on class (not the err field) means a future failure verdict that ever
		// carries a nil err still fails closed instead of being silently counted
		// as a pass. err is retained only to carry the failure message.
		switch v.class {
		case classNotVerifiable:
			continue
		case classPass:
			verifiable++
		case classCorruption, classForgery:
			verifiable++
			failed = true
			if v.err != nil {
				errs = append(errs, v.err)
			} else {
				// Failure class with no message (should not happen for current
				// producers, which always attach err) — synthesize one so the
				// verdict still fails closed with a non-empty verify_err.
				errs = append(errs, fmt.Errorf("verification failed (class %d)", v.class))
			}
		}
	}
	if verifiable == 0 {
		return nil, ""
	}
	if failed {
		no := false
		return &no, errors.Join(errs...).Error()
	}
	yes := true
	return &yes, ""
}

// VerifyVersion recomputes a cached version's verdict from its on-disk FINAL
// files — the reverify-facing half of the D16 single-source (the land-path uses
// verifyArtifact + aggregateVerdicts on .partial files). It NEVER writes the DB
// or moves files; the caller owns disposition. A verifiable artifact whose final
// file is absent is a failure ("artifact absent") UNLESS a sibling .partial
// exists (a re-download is in flight) — then the whole version records NULL
// (re-review #8). id must exist (caller checks first / handles the error).
func VerifyVersion(ctx context.Context, store *db.Store, id int64) (*bool, string, error) {
	row, err := store.GetCacheEntry(id)
	if err != nil {
		return nil, "", err
	}
	o, ok := ostype.Lookup(row.OS)
	if !ok {
		return nil, "", fmt.Errorf("cache: verify: unknown OS %q", row.OS)
	}
	params, err := decodeParams(row.Params)
	if err != nil {
		return nil, "", fmt.Errorf("cache: verify params: %w", err)
	}
	dir := cacheDir(canonicalToCacheName(row.OS), paramSegment(params), row.Arch, row.Version)
	arts, err := o.Artifacts(ctx, row.Version, row.Arch, params)
	if err != nil {
		return nil, "", fmt.Errorf("cache: verify artifacts: %w", err)
	}

	verdicts := make([]artifactVerdict, 0, len(arts))
	for _, a := range arts {
		if a.SHA256 == "" && a.SigURL == "" {
			verdicts = append(verdicts, artifactVerdict{class: classNotVerifiable})
			continue
		}
		final, perr := artifactPath(dir, a.URL)
		if perr != nil {
			return nil, "", perr
		}
		if _, serr := os.Stat(final); serr != nil {
			if _, perr := os.Stat(final + ".partial"); perr == nil {
				return nil, "", nil // re-download in flight → no verdict
			}
			verdicts = append(verdicts, artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: artifact absent", a.Filename)})
			continue
		}
		verdicts = append(verdicts, verifyArtifact(ctx, final, "", a))
	}
	verified, verifyErr := aggregateVerdicts(verdicts)
	return verified, verifyErr, nil
}
