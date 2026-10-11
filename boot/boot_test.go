package boot

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	for _, name := range Names {
		b, ok := Lookup(name)
		if !ok || len(b) == 0 {
			t.Errorf("Lookup(%q) = %d bytes, %v; want non-empty, true", name, len(b), ok)
		}
	}
	for _, name := range []string{"booty.ipxe", "embed.ipxe", "../go.mod", ""} {
		if b, ok := Lookup(name); ok || b != nil {
			t.Errorf("Lookup(%q) = %d bytes, %v; want nil, false", name, len(b), ok)
		}
	}
}

func TestEmbeddedMatchesSHA256SUMS(t *testing.T) {
	data, err := os.ReadFile("SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("SHA256SUMS has %d lines, want 3", len(lines))
	}
	for _, line := range lines {
		want, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("malformed SHA256SUMS line %q", line)
		}
		b, found := Lookup(name)
		if !found {
			t.Fatalf("SHA256SUMS names %q, which is not embedded", name)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: embedded sha256 %s, SHA256SUMS says %s (rebuild with go generate ./boot/)", name, got, want)
		}
	}
}
