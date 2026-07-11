package http

import (
	"strings"
	"testing"
)

// TestConfigCreateDebianConfigAdmitted: the huma enum tag (I2) plus the DB
// CHECK (0006) both admit the new kind; validation flows through
// validateConfigSource's default arm (stub-var render) with NO new case.
func TestConfigCreateDebianConfigAdmitted(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/configs", map[string]any{
		"name": "deb", "kind": "debianconfig", "source": "hostname: \"{{ .Hostname }}\"\n",
	})
	if resp.Code != 201 {
		t.Fatalf("create debianconfig = %d, want 201: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"kind":"debianconfig"`) {
		t.Fatalf("create body = %s, want kind debianconfig", resp.Body.String())
	}
}

// TestConfigCreateDebianConfigIncoherentIs422: a coherence violation (mirror
// with one device) is a 422 through the normal validation path — proving no
// bespoke debianconfig admission code exists.
func TestConfigCreateDebianConfigIncoherentIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/configs", map[string]any{
		"name": "deb-bad", "kind": "debianconfig",
		"source": "disk:\n  devices: [/dev/sda]\n  raid: mirror\n",
	})
	if resp.Code != 422 {
		t.Fatalf("incoherent debianconfig create = %d, want 422: %s", resp.Code, resp.Body.String())
	}
}
