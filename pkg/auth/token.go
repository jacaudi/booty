// Package auth owns booty's single shared API token and the stateless session
// cookie derived from it. It is a leaf package: it imports only the standard
// library, so both pkg/http (the middleware) and cmd (the CLI + SIGHUP wiring)
// can depend on it without a cycle.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// TokenFileName is the basename of the persisted token under dataDir. It is
// NOT under any subtree the /data/ file server allowlists (pkg/http/http.go
// serves only cache/ and public/), so it is unreachable over HTTP by
// construction rather than by a rule someone has to remember to add.
const TokenFileName = "api-token"

// sessionKeyLabel domain-separates the cookie-signing key from the token
// itself, so the key can never be replayed as a token or vice versa.
const sessionKeyLabel = "booty-session-key-v1"

// tokenBytes is the entropy of a generated token before base64 encoding.
const tokenBytes = 32

// ErrEmptyToken is returned whenever an empty or whitespace-only token would
// be installed. This is a SECURITY invariant, not input tidiness: the cookie
// key is HMAC-SHA256(token, sessionKeyLabel), so an empty token yields a
// CONSTANT key that anyone can compute from this repo and use to forge a
// session cookie. Refusing at install time is the only place that closes both
// the header path and the cookie path at once.
var ErrEmptyToken = errors.New("auth: token is empty")

// Secret wraps the API token so an accidental log, fmt, or JSON render prints
// a placeholder instead of the credential. Expose is deliberately verbose and
// grep-able: every real use of the value is visible in review.
type Secret string

func (s Secret) String() string               { return "****" }
func (s Secret) MarshalText() ([]byte, error) { return []byte("****"), nil }
func (s Secret) Expose() string               { return string(s) }

// credential pairs a token with the cookie key derived from it. The two are
// swapped together through one atomic.Pointer so a request can never verify a
// cookie with a post-rotation key against a pre-rotation token, or vice versa.
type credential struct {
	token Secret
	key   [32]byte
}

// newCredential derives the cookie key from tok. It REFUSES an empty token:
// see ErrEmptyToken. Callers must not install the returned zero credential.
func newCredential(tok string) (*credential, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return nil, ErrEmptyToken
	}
	mac := hmac.New(sha256.New, []byte(tok))
	mac.Write([]byte(sessionKeyLabel))
	return &credential{token: Secret(tok), key: [32]byte(mac.Sum(nil))}, nil
}

// Store holds the live credential and the path it persists to.
type Store struct {
	path string
	cur  atomic.Pointer[credential]
}

// NewStore returns a Store backed by path with NO token installed. No IO
// happens until Load or Rotate, and until one succeeds HasToken reports false
// and both credential paths refuse everything.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// HasToken reports whether a usable token is installed. Both the header and
// the cookie path must consult it before doing anything with the credential.
func (s *Store) HasToken() bool { return s.cur.Load() != nil }

// Path returns the token file path.
func (s *Store) Path() string { return s.path }

// Load installs the token from the file, generating and persisting a new one at
// mode 0600 when the file does not exist. generated reports whether a new token
// was minted, so the caller can log it exactly once (docker logs / journalctl
// is how an operator discovers it on first run).
func (s *Store) Load() (generated bool, err error) {
	raw, err := os.ReadFile(s.path)
	if err == nil {
		// An existing-but-blank file is a hard failure, NOT an empty token to
		// install. A 0-byte api-token is reachable in production (a crash
		// between Rotate's truncate and its write leaves exactly that), and
		// installing it would make the cookie key publicly derivable.
		cred, cerr := newCredential(string(raw))
		if cerr != nil {
			return false, fmt.Errorf("auth: token file %s is blank: %w", s.path, cerr)
		}
		s.cur.Store(cred)
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, fmt.Errorf("auth: read token %s: %w", s.path, err)
	}
	tok, err := generateToken()
	if err != nil {
		return false, err
	}
	if err := writeToken(s.path, tok); err != nil {
		return false, err
	}
	cred, err := newCredential(tok)
	if err != nil {
		return false, err // unreachable: generateToken never returns empty
	}
	s.cur.Store(cred)
	return true, nil
}

// SetExplicit installs tok without touching disk. This is the --apiToken /
// BOOTY_API_TOKEN path: an explicitly supplied token overrides the file and is
// never persisted, so a token supplied by flag or environment cannot leak into
// dataDir. An empty or whitespace-only value is refused (ErrEmptyToken) rather
// than installed -- `--apiToken " "` must fail startup, not open the API.
func (s *Store) SetExplicit(tok string) error {
	cred, err := newCredential(tok)
	if err != nil {
		return err
	}
	s.cur.Store(cred)
	return nil
}

// Reload re-reads the token file. On any error the live credential is left
// untouched and the error returned: a SIGHUP against an unreadable file must
// not silently drop booty into an unauthenticatable state.
func (s *Store) Reload() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("auth: reload token %s: %w", s.path, err)
	}
	cred, err := newCredential(string(raw))
	if err != nil {
		return fmt.Errorf("auth: token file %s is blank: %w", s.path, err)
	}
	s.cur.Store(cred)
	return nil
}

// Rotate generates a new token, persists it at 0600, and installs it. Because
// the cookie key is derived from the token, this invalidates every outstanding
// session with no session store to sweep.
func (s *Store) Rotate() (Secret, error) {
	tok, err := generateToken()
	if err != nil {
		return "", err
	}
	if err := writeToken(s.path, tok); err != nil {
		return "", err
	}
	cred, err := newCredential(tok)
	if err != nil {
		return "", err // unreachable: generateToken never returns empty
	}
	s.cur.Store(cred)
	return Secret(tok), nil
}

// Token returns the live token, or "" when none is installed.
func (s *Store) Token() Secret {
	c := s.cur.Load()
	if c == nil {
		return ""
	}
	return c.token
}

// Key returns the live cookie-signing key, and ok=false when no token is
// installed. Callers MUST check ok: a zero key is not a safe fallback, it is a
// publicly computable one.
func (s *Store) Key() ([32]byte, bool) {
	c := s.cur.Load()
	if c == nil {
		return [32]byte{}, false
	}
	return c.key, true
}

// Verify constant-time-compares presented against the live token. With no
// token installed it refuses everything, so a misconfigured store fails closed.
func (s *Store) Verify(presented string) bool {
	c := s.cur.Load()
	if c == nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(c.token)) == 1
}

func generateToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// writeToken creates the file with 0600 from the outset. os.WriteFile applies
// perm only on creation, so an existing file keeps its mode; Chmod afterwards
// makes rotation over a pre-existing wrong-moded file correct too.
func writeToken(path, tok string) error {
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return fmt.Errorf("auth: write token %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("auth: chmod token %s: %w", path, err)
	}
	return nil
}
