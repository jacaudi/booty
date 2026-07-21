package http

import (
	"strings"
	"testing"
)

func TestCatalogListsOS(t *testing.T) {
	api := newTestAPI(t, APIDeps{})
	resp := api.Get("/api/v1/os")
	if resp.Code != 200 {
		t.Fatalf("GET /os = %d", resp.Code)
	}
	body := resp.Body.String()
	for _, want := range []string{"talos", "flatcar", "fedora-coreos"} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET /os missing %q: %s", want, body)
		}
	}
}

func TestCatalogListsFamilies(t *testing.T) {
	api := newTestAPI(t, APIDeps{})
	resp := api.Get("/api/v1/families")
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), "ignition") {
		t.Fatalf("GET /families = %d: %s", resp.Code, resp.Body.String())
	}
}

func TestCatalogFamiliesExposeAuthoringKinds(t *testing.T) {
	api := newTestAPI(t, APIDeps{})
	resp := api.Get("/api/v1/families")
	if resp.Code != 200 {
		t.Fatalf("GET /families = %d", resp.Code)
	}
	body := resp.Body.String()
	// ignition family authors butane; preseed family authors debianconfig (not preseed).
	for _, want := range []string{`"authoringKinds"`, `"butane"`, `"debianconfig"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET /families missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `"authoringKinds":["preseed"]`) {
		t.Fatalf("preseed must not appear as an authoring kind: %s", body)
	}
}
