package ostype

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestDebian_Basics(t *testing.T) {
	o, _ := Lookup("debian")
	if o.Family().Name != "debian" || o.Family().ConfigKind != "preseed" {
		t.Errorf("family = %+v, want debian/preseed", o.Family())
	}
	if !slices.Equal(o.RequiredParams(), []string{"channel"}) {
		t.Errorf("RequiredParams = %v, want [channel]", o.RequiredParams())
	}
}

func TestDebian_ValidateAndCompare(t *testing.T) {
	o, _ := Lookup("debian")
	if err := o.ValidateVersion("12.5"); err != nil {
		t.Errorf("12.5 rejected: %v", err)
	}
	if err := o.ValidateVersion("12"); err != nil {
		t.Errorf("12 rejected: %v", err)
	}
	if err := o.ValidateVersion("bookworm"); err == nil {
		t.Error("non-numeric version accepted")
	}
	if o.CompareVersions("12.5", "12.4") <= 0 {
		t.Error("12.5 should sort after 12.4")
	}
	if o.CompareVersions("12.0", "11.9") <= 0 {
		t.Error("12.0 should sort after 11.9")
	}
}

// swapIndexFetcher replaces debianIndexFetcher for the duration of a test and
// returns a func to restore the original.
func swapIndexFetcher(f func(ctx context.Context, url string) (string, error)) func() {
	orig := debianIndexFetcher
	debianIndexFetcher = f
	return func() { debianIndexFetcher = orig }
}

func TestDebianDiscover_StableFromCurrent(t *testing.T) {
	restore := swapIndexFetcher(func(ctx context.Context, url string) (string, error) {
		if !strings.Contains(url, "/debian-cd/current/") {
			t.Fatalf("stable must resolve via debian-cd/current, got %s", url)
		}
		return "debian-13.6.0-amd64-DVD-1.iso\ndebian-13.6.0-amd64-netinst.iso\n", nil
	})
	defer restore()
	got, err := debian{}.DiscoverVersions(t.Context(), map[string]string{"channel": "13"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "13.6.0" {
		t.Fatalf("DiscoverVersions(13) = %v, want [13.6.0]", got)
	}
}

func TestDebianDiscover_OldstableFromArchive_PicksHighest(t *testing.T) {
	restore := swapIndexFetcher(func(ctx context.Context, url string) (string, error) {
		if !strings.Contains(url, "/cdimage/archive/") {
			t.Fatalf("12 must resolve via cdimage/archive, got %s", url)
		}
		return "12.5.0/\n12.15.0/\n12.9.0/\n", nil // archive lists point-release dirs
	})
	defer restore()
	got, err := debian{}.DiscoverVersions(t.Context(), map[string]string{"channel": "12"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "12.15.0" {
		t.Fatalf("DiscoverVersions(12) = %v, want [12.15.0] (highest)", got)
	}
}

func TestDebianCodename_Trixie(t *testing.T) {
	for ch, want := range map[string]string{"13": "trixie", "12": "bookworm", "11": "bullseye"} {
		if got := debianCodenames[ch]; got != want {
			t.Errorf("codename[%s]=%q want %q", ch, got, want)
		}
	}
}

func TestDebianArtifacts_NetinstURLs(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		arts, err := debian{}.Artifacts(t.Context(), "13.6.0", arch, map[string]string{"channel": "13"})
		if err != nil {
			t.Fatal(err)
		}
		if len(arts) != 2 || arts[0].Filename != "linux" || arts[1].Filename != "initrd.gz" {
			t.Fatalf("arch=%s artifacts=%v", arch, arts)
		}
		want := "installer-" + arch
		if !strings.Contains(arts[0].URL, "trixie") || !strings.Contains(arts[0].URL, want) {
			t.Fatalf("arch=%s linux URL=%q missing trixie/%s", arch, arts[0].URL, want)
		}
	}
}

// TestDebianArtifacts_DefaultsToTrixieWhenChannelUnset covers the fallback
// branch the numeric lookup doesn't reach: an unset/unrecognized channel
// param must default to "trixie", not silently resolve to an empty codename
// (the old code fell back to the dead debianCodenames["stable"] key, which
// doesn't exist in the numeric-keyed map).
func TestDebianArtifacts_DefaultsToTrixieWhenChannelUnset(t *testing.T) {
	arts, err := debian{}.Artifacts(t.Context(), "13.6.0", "amd64", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(arts[0].URL, "trixie") {
		t.Fatalf("unset channel URL=%q, want default trixie codename", arts[0].URL)
	}
}

// TestDebian_Artifacts covers channel "12" (bookworm) — a numeric channel
// distinct from TestDebianArtifacts_NetinstURLs's "13" (trixie) — and asserts
// the resolved codename actually appears in the URL. Originally passed
// channel "stable", which isn't a key in the numeric-keyed debianCodenames
// map (rekeyed in Task 2) and so silently produced a malformed
// empty-codename URL without the test catching it; reconciled here to a
// real channel with a codename assertion per Task 3.
func TestDebian_Artifacts(t *testing.T) {
	o, _ := Lookup("debian")
	got, err := o.Artifacts(t.Context(), "12.5", "amd64", map[string]string{"channel": "12"})
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("debian artifacts = %d, want 2 (linux, initrd.gz)", len(got))
	}
	for _, a := range got {
		if a.URL == "" || a.Filename == "" {
			t.Errorf("incomplete debian artifact: %+v", a)
		}
		if !slices.Contains([]string{"linux", "initrd.gz"}, a.Filename) {
			t.Errorf("unexpected debian artifact filename %q", a.Filename)
		}
		if !strings.Contains(a.URL, "bookworm") {
			t.Errorf("channel 12 URL=%q missing bookworm codename", a.URL)
		}
	}
}
