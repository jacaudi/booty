package checksum

import "testing"

// The Tails sidecar is exactly one LF-terminated line: <64 hex> + TWO SPACES +
// filename. No "./" prefix, no binary-mode "*", no trailing blank line.
// Verified against five real netbootxyz/asset-mirror releases (7.10, 7.9.1,
// 7.3.1, 6.6, 4.22) on 2026-08-02 and re-verified by the design's Gate 1.
func TestParseSumsTailsSingleLine(t *testing.T) {
	const digest = "6dab23b2000000000000000000000000000000000000000000000000000d1743"
	body := []byte(digest + "  tails-amd64.iso\n")

	sums, err := ParseSums(body)
	if err != nil {
		t.Fatalf("ParseSums: %v", err)
	}
	if len(sums) != 1 {
		t.Fatalf("got %d entries, want 1: %#v", len(sums), sums)
	}
	if got := sums["tails-amd64.iso"]; got != digest {
		t.Errorf("digest = %q, want %q", got, digest)
	}
}

// Debian's cdimage SHA256SUMS is the same format with many lines. This is the
// shape verifyDVDChecksums has always fed the parser, so it must keep working.
func TestParseSumsDebianMultiLine(t *testing.T) {
	body := []byte(
		"aaaa  debian-13.1.0-amd64-DVD-1.iso\n" +
			"bbbb  debian-13.1.0-amd64-DVD-2.iso\n" +
			"cccc  debian-13.1.0-amd64-DVD-3.iso\n")

	sums, err := ParseSums(body)
	if err != nil {
		t.Fatalf("ParseSums: %v", err)
	}
	if len(sums) != 3 {
		t.Fatalf("got %d entries, want 3: %#v", len(sums), sums)
	}
	if sums["debian-13.1.0-amd64-DVD-2.iso"] != "bbbb" {
		t.Errorf("entry 2 = %q, want %q", sums["debian-13.1.0-amd64-DVD-2.iso"], "bbbb")
	}
}

// A malformed line is an ERROR, not a skipped line. A partially-parsed digest
// file would silently leave files unverified, which is the exact silent
// downgrade D2 exists to forbid.
func TestParseSumsMalformedLineIsAnError(t *testing.T) {
	if _, err := ParseSums([]byte("not-a-sums-line\n")); err == nil {
		t.Fatal("ParseSums accepted a line with no double-space separator, want error")
	}
}

// Blank lines are skipped (a trailing "\n\n" must not fail an otherwise valid
// file); an empty body yields an empty map and no error. The CALLER decides
// whether an empty map is acceptable — for Tails that is D2a's job, not the
// parser's.
func TestParseSumsBlankAndEmpty(t *testing.T) {
	sums, err := ParseSums([]byte("aaaa  x.iso\n\n"))
	if err != nil {
		t.Fatalf("trailing blank line must be tolerated: %v", err)
	}
	if len(sums) != 1 {
		t.Fatalf("got %d entries, want 1: %#v", len(sums), sums)
	}

	empty, err := ParseSums(nil)
	if err != nil {
		t.Fatalf("empty body must not error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty body must yield an empty map, got %#v", empty)
	}
}
