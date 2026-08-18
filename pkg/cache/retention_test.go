package cache

import (
	"reflect"
	"testing"
)

func TestSelectRetained_NewestPatchPerMinorLine(t *testing.T) {
	tags := []string{"v1.10.1", "v1.10.5", "v1.9.0", "v1.9.3", "v1.8.7", "bad", "v1.7.0-alpha.1"}
	got := selectRetained(tags, 2)
	want := []string{"v1.10.5", "v1.9.3"} // newest 2 minor lines, highest patch each
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectRetained = %v, want %v", got, want)
	}
}

func TestSelectRetained_EmptyInputIsNonNilEmpty(t *testing.T) {
	got := selectRetained(nil, 3)
	if got == nil || len(got) != 0 {
		t.Errorf("selectRetained(nil) = %v, want non-nil empty slice", got)
	}
}

func TestRetentionFor_TalosUsesMinorLines(t *testing.T) {
	tags := []string{"v1.10.1", "v1.10.5", "v1.9.0", "v1.9.3", "v1.8.7"}
	got := retentionFor("talos", tags, 2)
	want := []string{"v1.10.5", "v1.9.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("retentionFor(talos) = %v, want %v", got, want)
	}
}

func TestRetentionFor_NonTalosNewestNByCompare(t *testing.T) {
	// fedora-coreos: dotted-numeric ordering, plain newest-N.
	got := retentionFor("fedora-coreos", []string{"39.20231101.3.0", "40.20240101.3.0", "38.20230901.3.0"}, 2)
	want := []string{"40.20240101.3.0", "39.20231101.3.0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("retentionFor(fedora-coreos) = %v, want %v", got, want)
	}
}

func TestRetentionForToolKeepsDiscoveredNotLexicalMax(t *testing.T) {
	// Upstream moved 9.05 -> 10.00. Lexicographically "9.05..." > "10.00...",
	// so a plain descending sort would keep the OLD tag forever.
	got := retentionFor("memtest86plus", []string{"10.00-bbbbbbbb", "9.05-aaaaaaaa"}, 1)
	if len(got) != 1 || got[0] != "10.00-bbbbbbbb" {
		t.Errorf("retentionFor = %v, want [10.00-bbbbbbbb]", got)
	}
}

// A negative n must never panic. retentionFor's truncation is `out[:n]`, which
// on a negative n is a slice-bounds panic that takes the whole process down —
// and it runs on the reconcile goroutine, reachable from any persisted target
// whose RetainN went negative. Clamping to zero (keep nothing) is the only
// defensible reading of "retain -5 versions" and matches retain: 0's meaning.
func TestRetentionForNegativeRetainClampsInsteadOfPanicking(t *testing.T) {
	cases := []struct{ os, name string }{
		{"talos", "talos minor-line branch"},
		{"clonezilla", "tool branch"},
		{"flatcar", "default compare branch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retentionFor(tc.os, []string{"v1.2.3", "v1.1.0"}, -5)
			if len(got) != 0 {
				t.Fatalf("retentionFor(%q, …, -5) = %v, want empty", tc.os, got)
			}
		})
	}
}
