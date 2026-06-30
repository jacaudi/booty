package cache

import "testing"

func TestEncodeParamsCanonical(t *testing.T) {
	a, err := EncodeParams(map[string]string{"schematic": "x", "a": "1"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	b, _ := EncodeParams(map[string]string{"a": "1", "schematic": "x"})
	if a != b {
		t.Fatalf("non-canonical: %q != %q", a, b)
	}
	if s, _ := EncodeParams(nil); s != "{}" {
		t.Fatalf("nil params = %q, want {}", s)
	}
	m, err := DecodeParams(a)
	if err != nil || m["schematic"] != "x" {
		t.Fatalf("decode roundtrip failed: %v / %v", m, err)
	}
}
