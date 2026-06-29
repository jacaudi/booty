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
