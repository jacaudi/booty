package nodereport

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenDeterministicAndDistinct(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	base := Token(key, "aa:bb:cc:dd:ee:01", 1)
	if len(base) != 64 {
		t.Fatalf("len = %d, want 64", len(base))
	}
	if Token(key, "aa:bb:cc:dd:ee:01", 1) != base {
		t.Error("same inputs gave different tokens")
	}
	if Token(key, "aa:bb:cc:dd:ee:02", 1) == base {
		t.Error("different MAC gave same token")
	}
	if Token(key, "aa:bb:cc:dd:ee:01", 2) == base {
		t.Error("different gen gave same token")
	}
	if Token(bytes.Repeat([]byte{2}, 32), "aa:bb:cc:dd:ee:01", 1) == base {
		t.Error("different key gave same token")
	}
}

func TestValidToken(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	mac := "aa:bb:cc:dd:ee:01"
	good := Token(key, mac, 3)
	tests := []struct {
		name, presented string
		want            bool
	}{
		{"correct", good, true},
		{"wrong", Token(key, mac, 4), false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		if got := ValidToken(key, mac, 3, tt.presented); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node-report-key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 {
		t.Fatalf("len = %d", len(k1))
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Errorf("second call: %x, %v; want %x", k2, err, k1)
	}

	bad := filepath.Join(t.TempDir(), "short")
	if werr := os.WriteFile(bad, make([]byte, 31), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if _, kerr := LoadOrCreateKey(bad); kerr == nil {
		t.Error("31-byte key file: want error")
	}
}

func TestBadKeyLength(t *testing.T) {
	good := bytes.Repeat([]byte{1}, 32)
	mac := "aa:bb:cc:dd:ee:01"
	for _, key := range [][]byte{nil, make([]byte, 31), make([]byte, 33)} {
		if tok := Token(key, mac, 1); tok != "" {
			t.Errorf("len %d: Token = %q, want empty", len(key), tok)
		}
		if ValidToken(key, mac, 1, "") || ValidToken(key, mac, 1, Token(good, mac, 1)) {
			t.Errorf("len %d: ValidToken must reject", len(key))
		}
	}
}

func TestLoadOrCreateKeyLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateKey(filepath.Join(dir, "k")); err != nil {
		t.Fatal(err)
	}
	// Rename onto a directory fails; the error must not mention a closed file.
	bad := filepath.Join(dir, "d")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := writeKey(bad, make([]byte, 32))
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "already closed") {
		t.Errorf("spurious close error: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".node-report-key-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
