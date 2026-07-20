package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunConvertPreseedWritesYAMLToOutAndWarningsToErr(t *testing.T) {
	// Unknown directive → preserved in raw_preseed (clean round-trip, no warnings).
	in := strings.NewReader("d-i netcfg/get_hostname string web01\nd-i some/unknown boolean true\n")
	var out, errOut bytes.Buffer
	if err := runConvertPreseed(in, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hostname: web01") {
		t.Fatalf("stdout should carry the YAML:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "raw_preseed:") {
		t.Fatalf("unknown directive should be in raw_preseed:\n%s", out.String())
	}
}

func TestRunConvertPreseedEmitsWarningsToStderr(t *testing.T) {
	// Invalid username → re-render error → warning on stderr, YAML still on stdout (B4).
	in := strings.NewReader("d-i passwd/make-user boolean true\nd-i passwd/username string BAD_UPPER\nd-i passwd/user-password-crypted password $6$x\n")
	var out, errOut bytes.Buffer
	if err := runConvertPreseed(in, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatalf("must still emit YAML")
	}
	if !strings.Contains(errOut.String(), "did not re-render") {
		t.Fatalf("expected a re-render warning on stderr:\n%s", errOut.String())
	}
}
