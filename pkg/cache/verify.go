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
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
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

// verifyDetachedGPG fetches the detached BINARY signature at a.SigURL and checks
// it over filePath against a.GPGKey (armored keyring). Unfetchable/unparseable
// material and an unknown/expired key are CORRUPTION (benign / fail-closed); a
// genuine verification failure is FORGERY (tamper). The .sig is binary →
// CheckDetachedSignature (not the Armored variant); the key file is armored →
// ReadArmoredKeyRing (spike §9).
func verifyDetachedGPG(ctx context.Context, filePath string, a ostype.Artifact) artifactVerdict {
	keyring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(a.GPGKey))
	if err != nil {
		return artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: keyring parse: %w", a.Filename, err)}
	}
	sig, err := fetchBytes(ctx, a.SigURL)
	if err != nil {
		return artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: signature material unavailable: %w", a.Filename, err)}
	}
	signed, err := os.Open(filePath)
	if err != nil {
		return artifactVerdict{class: classCorruption, err: fmt.Errorf("%s: open for verify: %w", a.Filename, err)}
	}
	defer signed.Close()

	_, err = openpgp.CheckDetachedSignature(keyring, signed, bytes.NewReader(sig), nil)
	switch {
	case err == nil:
		return artifactVerdict{class: classPass}
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
