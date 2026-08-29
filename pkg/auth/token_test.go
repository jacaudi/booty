package auth

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadGeneratesPersistsAndReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), TokenFileName)
	s := NewStore(path)

	generated, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !generated {
		t.Fatal("first Load must report generated=true so the caller logs the token once")
	}
	if s.Token().Expose() == "" {
		t.Fatal("Load must install a non-empty token")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file mode = %o, want 600", perm)
	}
}

func TestLoadReadsExistingWithoutRegenerating(t *testing.T) {
	path := filepath.Join(t.TempDir(), TokenFileName)
	if err := os.WriteFile(path, []byte("preexisting-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)

	generated, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if generated {
		t.Fatal("Load must report generated=false when the file already exists")
	}
	if got := s.Token().Expose(); got != "preexisting-token" {
		t.Fatalf("token = %q, want %q (trailing newline must be trimmed)", got, "preexisting-token")
	}
}

func TestSecretNeverRendersItsValue(t *testing.T) {
	s := Secret("hunter2")
	if got := s.String(); got == "hunter2" {
		t.Fatal("Secret.String must not render the token")
	}
	text, err := s.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if string(text) == "hunter2" {
		t.Fatal("Secret.MarshalText must not render the token")
	}
	if s.Expose() != "hunter2" {
		t.Fatal("Expose must return the real value")
	}
}

// TestEmptyTokenIsRefusedEverywhere closes an authentication BYPASS, not a
// tidiness issue. The cookie-signing key is HMAC-SHA256(token, label); with an
// empty token that key is a CONSTANT any reader of this open-source repo can
// compute offline, so anyone could forge a session cookie for the whole
// /api/v1 surface. Verify() alone is not enough, because the cookie path never
// consults the token -- only the key. So the empty token must never be
// INSTALLED in the first place, and HasToken must report the failure.
//
// Every one of these is a reachable production state: a 0-byte api-token file,
// a whitespace-only one, a crash between Rotate's truncate and write, and
// --apiToken " ".
func TestEmptyTokenIsRefusedEverywhere(t *testing.T) {
	dir := t.TempDir()

	t.Run("SetExplicit refuses empty and whitespace", func(t *testing.T) {
		s := NewStore(filepath.Join(dir, TokenFileName))
		for _, bad := range []string{"", "   ", "\t\n"} {
			if err := s.SetExplicit(bad); !errors.Is(err, ErrEmptyToken) {
				t.Fatalf("SetExplicit(%q) err = %v, want ErrEmptyToken", bad, err)
			}
			if s.HasToken() {
				t.Fatalf("SetExplicit(%q) installed a usable token", bad)
			}
		}
	})

	t.Run("Load refuses an empty token file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), TokenFileName)
		if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s := NewStore(path)
		if _, err := s.Load(); !errors.Is(err, ErrEmptyToken) {
			t.Fatalf("Load on a blank token file err = %v, want ErrEmptyToken", err)
		}
		if s.HasToken() {
			t.Fatal("a blank token file must not yield a usable token")
		}
	})

	t.Run("Reload refuses a blanked file and keeps the live token", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), TokenFileName)
		s := NewStore(path)
		if _, err := s.Load(); err != nil {
			t.Fatal(err)
		}
		live := s.Token().Expose()
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.Reload(); !errors.Is(err, ErrEmptyToken) {
			t.Fatalf("Reload on a truncated file err = %v, want ErrEmptyToken", err)
		}
		if s.Token().Expose() != live {
			t.Fatal("a refused Reload must leave the live token untouched")
		}
	})

	t.Run("a store with no token verifies nothing and has no derivable key", func(t *testing.T) {
		s := NewStore(filepath.Join(t.TempDir(), TokenFileName))
		if s.HasToken() {
			t.Fatal("a fresh store must report no token")
		}
		if s.Verify("") || s.Verify("anything") {
			t.Fatal("a store with no token must verify nothing")
		}
	})
}

func TestVerifyAcceptsOnlyTheLiveToken(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), TokenFileName))
	if err := s.SetExplicit("right"); err != nil {
		t.Fatal(err)
	}

	if !s.Verify("right") {
		t.Fatal("Verify must accept the live token")
	}
	for _, wrong := range []string{"", "wrong", "right ", "RIGHT", "righ"} {
		if s.Verify(wrong) {
			t.Fatalf("Verify(%q) = true, want false", wrong)
		}
	}
}

func TestRotateChangesTokenKeyAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), TokenFileName)
	s := NewStore(path)
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	before := s.Token().Expose()
	beforeKey, ok := s.Key()
	if !ok {
		t.Fatal("Load must install a usable key")
	}

	rotated, err := s.Rotate()
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotated.Expose() == before {
		t.Fatal("Rotate must mint a different token")
	}
	afterKey, ok := s.Key()
	if !ok {
		t.Fatal("Rotate must leave a usable key installed")
	}
	if afterKey == beforeKey {
		t.Fatal("Rotate must change the derived cookie key (this is what invalidates sessions)")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != rotated.Expose() {
		t.Fatalf("file = %q, want the rotated token %q", string(onDisk), rotated.Expose())
	}
	if s.Verify(before) {
		t.Fatal("the pre-rotation token must no longer verify")
	}
}

func TestReloadPicksUpAnExternallyRewrittenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), TokenFileName)
	s := NewStore(path)
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("rotated-out-of-band"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := s.Token().Expose(); got != "rotated-out-of-band" {
		t.Fatalf("after Reload token = %q, want %q", got, "rotated-out-of-band")
	}
}

func TestReloadLeavesTheLiveTokenIntactOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), TokenFileName)
	s := NewStore(path)
	if err := s.SetExplicit("still-good"); err != nil {
		t.Fatal(err)
	}

	// The file was never created, so Reload must fail loudly and change nothing.
	if err := s.Reload(); err == nil {
		t.Fatal("Reload on a missing file must return an error, not silently succeed")
	}
	if got := s.Token().Expose(); got != "still-good" {
		t.Fatalf("a failed Reload changed the live token to %q", got)
	}
}

// TestConcurrentVerifyAndRotate is the -race guard for the SIGHUP path: the
// middleware reads the token on every request while a signal goroutine rotates
// it. It fails under -race if the credential is not swapped atomically.
func TestConcurrentVerifyAndRotate(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), TokenFileName))
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				_ = s.Verify("probe")
				_, _ = s.Key()
			}
		})
	}
	wg.Go(func() {
		for range 50 {
			if _, err := s.Rotate(); err != nil {
				t.Errorf("Rotate: %v", err)
				return
			}
		}
	})
	wg.Wait()
}
