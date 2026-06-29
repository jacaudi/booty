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
