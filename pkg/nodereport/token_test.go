package nodereport

import (
	"bytes"
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
