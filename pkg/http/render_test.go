package http

import (
	"strings"
	"testing"
)

func TestConfigKindForFamily(t *testing.T) {
	cases := map[string]string{
		"ignition":      "butane", // the only non-identity mapping
		"machineconfig": "machineconfig",
		"preseed":       "preseed",
	}
	for family, want := range cases {
		if got := configKindForFamily(family); got != want {
			t.Errorf("configKindForFamily(%q) = %q, want %q", family, got, want)
		}
	}
}

func TestRenderConfigButaneTranslatesToIgnition(t *testing.T) {
	src := []byte("variant: fcos\nversion: 1.5.0\n")
	out, ct, _, err := renderConfig("butane", src, TemplateVars{})
	if err != nil {
		t.Fatalf("renderConfig(butane): %v", err)
	}
	if ct != "application/json" {
		t.Errorf("contentType = %q, want application/json", ct)
	}
	if !strings.Contains(string(out), `"ignition"`) {
		t.Errorf("butane output not Ignition JSON: %s", out)
	}
}

func TestRenderConfigButaneFatalReportIsError(t *testing.T) {
	// A structurally-invalid config (non-absolute file path) → a FATAL translate
	// report AND a non-nil error, with a NON-empty report string (mirrors the live
	// handler's fatal-report path exactly).
	//
	// F3 note (verified empirically against vendored butane v0.19.0): the earlier
	// `version: 0.0.0` input does NOT work here — an unsupported version returns
	// `err = "No translator exists for variant fcos with version 0.0.0"` with an
	// EMPTY report (`report.String() == ""`, `IsFatal() == false`), so the
	// `report != ""` assertion would fail. A validation error such as a relative
	// file path instead yields `err = "config generated was invalid"` with a
	// non-empty fatal report `"error at $.storage.files.0.path ... path not
	// absolute"` — exercising the non-empty-report branch this test intends.
	src := []byte("variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: etc/x\n      contents:\n        inline: hi\n")
	_, _, report, err := renderConfig("butane", src, TemplateVars{})
	if err == nil {
		t.Fatal("bad butane must return an error")
	}
	if report == "" {
		t.Error("fatal butane must surface a non-empty report")
	}
}

func TestRenderConfigTemplateSubstitution(t *testing.T) {
	src := []byte("hostname: {{ .Hostname }} server={{ .ServerIP }}")
	out, ct, _, err := renderConfig("preseed", src, TemplateVars{Hostname: "node1", ServerIP: "10.0.0.1:80"})
	if err != nil {
		t.Fatalf("renderConfig(preseed): %v", err)
	}
	if ct != "text/plain" {
		t.Errorf("contentType = %q, want text/plain", ct)
	}
	if string(out) != "hostname: node1 server=10.0.0.1:80" {
		t.Errorf("rendered = %q", out)
	}
}

func TestRenderConfigMachineConfigPassthrough(t *testing.T) {
	src := []byte("version: v1alpha1\nmachine: {}\n")
	out, ct, _, err := renderConfig("machineconfig", src, TemplateVars{})
	if err != nil {
		t.Fatalf("renderConfig(machineconfig): %v", err)
	}
	if ct != "text/yaml" || string(out) != string(src) {
		t.Errorf("machineconfig passthrough = %q / %q", out, ct)
	}
}

func TestRenderConfigUnknownKindIsError(t *testing.T) {
	if _, _, _, err := renderConfig("bogus", []byte("x"), TemplateVars{}); err == nil {
		t.Fatal("unknown kind must return an error")
	}
}

func TestRenderConfigTemplateParseErrorIsError(t *testing.T) {
	if _, _, _, err := renderConfig("preseed", []byte("{{ .Bad "), TemplateVars{}); err == nil {
		t.Fatal("malformed template must return an error")
	}
}
