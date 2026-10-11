package nodereport

import (
	"bytes"
	"os"
	"path/filepath"
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
