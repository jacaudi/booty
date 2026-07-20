package http

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePreseedBasicDirective(t *testing.T) {
	got := parsePreseed([]byte("d-i debian-installer/locale string en_US\n"))
	want := []preseedDirective{{owner: "d-i", template: "debian-installer/locale", dtype: "string", value: "en_US", raw: "d-i debian-installer/locale string en_US"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePreseed = %#v, want %#v", got, want)
	}
}

func TestParsePreseedJoinsContinuations(t *testing.T) {
	src := "d-i partman-auto/expert_recipe string \\\n    boot-root :: \\\n    500 . \n"
	got := parsePreseed([]byte(src))
	if len(got) != 1 {
		t.Fatalf("want 1 joined directive, got %d: %#v", len(got), got)
	}
	if got[0].template != "partman-auto/expert_recipe" {
		t.Fatalf("template = %q", got[0].template)
	}
	if want := "boot-root :: 500 ."; !contains(got[0].value, "boot-root :: ") || !contains(got[0].value, "500 .") {
		t.Fatalf("value did not join continuations: %q (want to contain %q)", got[0].value, want)
	}
}

func TestParsePreseedDropsCommentsAndBlanks(t *testing.T) {
	src := "# a comment\n\nd-i time/zone string UTC\n   \n"
	got := parsePreseed([]byte(src))
	if len(got) != 1 || got[0].template != "time/zone" {
		t.Fatalf("comments/blanks not dropped: %#v", got)
	}
}

func TestParsePreseedPreservesUnparseableAsPassthrough(t *testing.T) {
	got := parsePreseed([]byte("this is not a directive\n"))
	if len(got) != 1 || got[0].template != "" || got[0].raw != "this is not a directive" {
		t.Fatalf("unparseable line not preserved: %#v", got)
	}
}

func TestParsePreseedPackageOwner(t *testing.T) {
	got := parsePreseed([]byte("keyboard-configuration keyboard-configuration/xkb-keymap select us\n"))
	if len(got) != 1 || got[0].owner != "keyboard-configuration" || got[0].template != "keyboard-configuration/xkb-keymap" || got[0].value != "us" {
		t.Fatalf("package-owned directive misparsed: %#v", got)
	}
}

// contains is a tiny local helper to keep assertions readable.
func contains(s, sub string) bool { return strings.Contains(s, sub) }
