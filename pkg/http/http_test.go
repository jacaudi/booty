package http

import "testing"

// TestPartialSuffixExcluded asserts the /data/ predicate recognizes an
// in-flight staged download (T2/T9 write <artifact>.partial while downloading)
// so isPartialPath can gate the FileServer from ever serving unverified,
// in-flight bytes — while leaving normal, landed artifacts servable.
func TestPartialSuffixExcluded(t *testing.T) {
	if !isPartialPath("/cache/flatcar/stable/amd64/1/x.img.partial") {
		t.Error(".partial paths must be recognized for exclusion")
	}
	if isPartialPath("/cache/flatcar/stable/amd64/1/x.img") {
		t.Error("final artifacts must NOT be excluded")
	}
}
