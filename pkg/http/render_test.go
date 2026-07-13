package http

import (
	"strings"
	"testing"
)

func TestFamilyAllowsKind(t *testing.T) {
	cases := []struct {
		family, kind string
		want         bool
	}{
		{"ignition", "butane", true}, // author butane, serve ignition
		{"ignition", "ignition", false},
		{"ignition", "preseed", false},
		{"machineconfig", "machineconfig", true},
		{"machineconfig", "butane", false},
		{"preseed", "preseed", true},      // raw preseed still allowed
		{"preseed", "debianconfig", true}, // the only 1:many entry
		{"preseed", "butane", false},
		{"machineconfig", "debianconfig", false},
	}
	for _, c := range cases {
		if got := familyAllowsKind(c.family, c.kind); got != c.want {
			t.Errorf("familyAllowsKind(%q, %q) = %v, want %v", c.family, c.kind, got, c.want)
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

// TestRenderConfigDebianConfigTranslates: the debianconfig arm — template
// substitution runs FIRST (shared step), so {{ .Hostname }} lands in the spec
// before translation, exactly as it does for butane (design §5).
func TestRenderConfigDebianConfigTranslates(t *testing.T) {
	src := []byte("hostname: \"{{ .Hostname }}\"\nlocale: en_US.UTF-8\n")
	out, ct, report, err := renderConfig("debianconfig", src, TemplateVars{Hostname: "node1"})
	if err != nil {
		t.Fatalf("renderConfig(debianconfig): %v", err)
	}
	if ct != "text/plain" {
		t.Errorf("contentType = %q, want text/plain", ct)
	}
	if report != "" {
		t.Errorf("report = %q, want empty", report)
	}
	want := "d-i debian-installer/locale string en_US.UTF-8\nd-i netcfg/get_hostname string node1\n"
	if string(out) != want {
		t.Errorf("rendered:\ngot:  %q\nwant: %q", out, want)
	}
}

// TestRenderConfigDebianConfigIncoherentIsError: coherence errors propagate
// through the arm (validateConfigSource's default arm turns this into a 422).
func TestRenderConfigDebianConfigIncoherentIsError(t *testing.T) {
	src := []byte("disk:\n  devices: [/dev/sda]\n  raid: mirror\n")
	if _, _, _, err := renderConfig("debianconfig", src, TemplateVars{}); err == nil {
		t.Fatal("incoherent debianconfig must return an error")
	}
}
