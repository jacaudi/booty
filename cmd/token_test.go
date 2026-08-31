package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/auth"
)

func TestTokenPrintShowsThePersistedToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, auth.TokenFileName), []byte("persisted-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newTokenCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"print", "--dataDir", dir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("token print: %v", err)
	}
	if !strings.Contains(out.String(), "persisted-token") {
		t.Fatalf("token print output = %q, want it to contain the token", out.String())
	}
}

func TestTokenPrintFailsLoudlyWhenNoTokenExists(t *testing.T) {
	cmd := newTokenCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"print", "--dataDir", t.TempDir()})

	if err := cmd.Execute(); err == nil {
		t.Fatal("token print with no token file must return an error, not print an empty token")
	}
}

func TestTokenRotateReplacesTheFileAtMode600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, auth.TokenFileName)
	if err := os.WriteFile(path, []byte("old-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newTokenCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"rotate", "--yes", "--dataDir", dir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("token rotate: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "old-token" {
		t.Fatal("rotate must replace the token on disk")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("rotated token mode = %o, want 600", perm)
	}
	if !strings.Contains(out.String(), string(raw)) {
		t.Fatal("rotate must print the new token so the operator can copy it")
	}
}

func TestTokenRotateRefusesWithoutConfirmation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, auth.TokenFileName)
	if err := os.WriteFile(path, []byte("old-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newTokenCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"rotate", "--dataDir", dir})

	if err := cmd.Execute(); err == nil {
		t.Fatal("rotate without --yes must refuse: it logs out every session")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "old-token" {
		t.Fatal("a refused rotate must not touch the token file")
	}
}
