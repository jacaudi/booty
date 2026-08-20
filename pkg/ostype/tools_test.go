package ostype

import (
	"slices"
	"testing"
)

func TestToolsAreRegistered(t *testing.T) {
	for _, name := range []string{"systemrescue", "uefi-shell", "memtest86plus"} {
		o, ok := Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q) not registered", name)
			continue
		}
		if o.Family().Name != "tool" {
			t.Errorf("%s: family = %q, want tool", name, o.Family().Name)
		}
		if len(o.RequiredParams()) != 0 {
			t.Errorf("%s: RequiredParams = %#v, want empty", name, o.RequiredParams())
		}
	}
}

// DiscoverVersions gets no arch, so it returns the union of tags across arches.
// That is unambiguous only while every tool is single-arch. If this fails, read
// the Interface Limitation section of the plan before "fixing" it: with a
// multi-arch tool one arch would be entirely broken, not merely misordered.
func TestEveryToolIsSingleArch(t *testing.T) {
	for name, arches := range ToolArches() {
		if len(arches) != 1 {
			t.Errorf("%s declares %v; multi-arch tools need the DiscoverVersions arch question answered first", name, arches)
		}
	}
}

// A registration whose checksumCovers names a file outside its own files
// allowlist can never be satisfied: Artifacts only ever looks up names from
// files, so the covers entry would be inert and the ISO could silently land
// not-verifiable. That is a registration bug, asserted here rather than
// discovered in production.
func TestToolChecksumCoversIsSubsetOfFiles(t *testing.T) {
	for _, o := range All() {
		tool, ok := o.(netbootxyzOS)
		if !ok {
			continue
		}
		for _, c := range tool.checksumCovers {
			if !slices.Contains(tool.files, c) {
				t.Errorf("%s: checksumCovers entry %q is not in files %v", tool.name, c, tool.files)
			}
		}
		if len(tool.checksumCovers) > 0 && tool.checksums == "" {
			t.Errorf("%s: declares checksumCovers %v but no checksums sidecar to satisfy them",
				tool.name, tool.checksumCovers)
		}
	}
}

// The registration data is inert unless it is actually declared. Without this,
// dropping either field leaves every other test green while Tails silently
// reverts to caching 1.94 GB unverified.
func TestTailsDeclaresItsSidecar(t *testing.T) {
	o, ok := Lookup("tails")
	if !ok {
		t.Fatal("tails not registered")
	}
	tool, ok := o.(netbootxyzOS)
	if !ok {
		t.Fatal("tails is not a netbootxyzOS")
	}
	if tool.checksums != "sha256-checksums.txt" {
		t.Errorf("checksums = %q, want \"sha256-checksums.txt\"", tool.checksums)
	}
	if !slices.Contains(tool.checksumCovers, "tails-amd64.iso") {
		t.Errorf("checksumCovers = %v, must declare tails-amd64.iso", tool.checksumCovers)
	}
	if slices.Contains(tool.files, "sha256-checksums.txt") {
		t.Error("the sidecar is verification material, never cached and never served — it must NOT be in files")
	}
}

func TestToolArchesMatchRegistry(t *testing.T) {
	for name, arches := range ToolArches() {
		o, ok := Lookup(name)
		if !ok {
			t.Fatalf("ToolArches lists unregistered %q", name)
		}
		tool, ok := o.(netbootxyzOS)
		if !ok {
			t.Fatalf("%s is not a netbootxyzOS", name)
		}
		var want []string
		for a := range tool.endpoints {
			want = append(want, a)
		}
		slices.Sort(want)
		got := slices.Clone(arches)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: ToolArches %v != registration %v", name, got, want)
		}
	}
}
