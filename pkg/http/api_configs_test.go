package http

import (
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestConfigsCRUDAndRevision(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	// PruneRevisions reads config.ConfigRevisionsKeep via viper; unit tests never
	// call config.LoadConfig() (that only runs in main), so the flag's default
	// must be set explicitly here or PUT prunes with keep=0 (clamped to 1).
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ConfigRevisionsKeep, 10)

	// Create → 201, revision 1 active.
	resp := api.Post("/api/v1/configs", map[string]any{
		"name": "prod", "kind": "butane", "source": "variant: fcos\nversion: 1.5.0\n",
	})
	if resp.Code != 201 {
		t.Fatalf("create = %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"activeRevision":1`) {
		t.Fatalf("create body = %s, want activeRevision 1", resp.Body.String())
	}

	// List → one config.
	if resp := api.Get("/api/v1/configs"); resp.Code != 200 || !strings.Contains(resp.Body.String(), `"prod"`) {
		t.Fatalf("list = %d: %s", resp.Code, resp.Body.String())
	}

	// PUT appends revision 2 and advances active.
	if resp := api.Put("/api/v1/configs/1", map[string]any{"source": "variant: fcos\nversion: 1.5.0\n# v2\n"}); resp.Code != 200 {
		t.Fatalf("put = %d: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Get("/api/v1/configs/1/revisions"); !strings.Contains(resp.Body.String(), `"revision":2`) {
		t.Fatalf("revisions = %s, want revision 2", resp.Body.String())
	}
}

func TestConfigCreateBadKindIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	if resp := api.Post("/api/v1/configs", map[string]any{"name": "x", "kind": "nope", "source": "y"}); resp.Code != 422 {
		t.Fatalf("bad kind = %d, want 422", resp.Code)
	}
}

func TestConfigCreateBadButaneIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	// Stub-var validation on create must reject fatal butane.
	if resp := api.Post("/api/v1/configs", map[string]any{"name": "x", "kind": "butane", "source": "variant: fcos\nversion: 0.0.0\n"}); resp.Code != 422 {
		t.Fatalf("fatal butane create = %d, want 422: %s", resp.Code, resp.Body.String())
	}
}

func TestConfigPreviewStubVarValidation(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/configs", map[string]any{"name": "p", "kind": "preseed", "source": "host={{ .ServerIP }}"})
	// No mac = stub-var validation; returns rendered + report.
	resp := api.Post("/api/v1/configs/1/preview", map[string]any{})
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), "host=") {
		t.Fatalf("preview (no mac) = %d: %s", resp.Code, resp.Body.String())
	}
}

func TestConfigRollbackPointerMove(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	// See TestConfigsCRUDAndRevision: PUT prunes via viper.GetInt(config.ConfigRevisionsKeep).
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ConfigRevisionsKeep, 10)
	api.Post("/api/v1/configs", map[string]any{"name": "r", "kind": "preseed", "source": "v1"})
	api.Put("/api/v1/configs/1", map[string]any{"source": "v2"})
	// Roll back to revision 1.
	if resp := api.Post("/api/v1/configs/1/rollback", map[string]any{"revision": 1}); resp.Code != 200 {
		t.Fatalf("rollback = %d: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Get("/api/v1/configs/1"); !strings.Contains(resp.Body.String(), `"activeRevision":1`) {
		t.Fatalf("after rollback active = %s, want revision 1", resp.Body.String())
	}
	// Rollback to an absent revision → 422.
	if resp := api.Post("/api/v1/configs/1/rollback", map[string]any{"revision": 99}); resp.Code != 422 {
		t.Fatalf("rollback absent = %d, want 422", resp.Code)
	}
}

func TestConfigDeleteIs403(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	api.Post("/api/v1/configs", map[string]any{"name": "d", "kind": "preseed", "source": "x"})
	if resp := api.Delete("/api/v1/configs/1"); resp.Code != 403 {
		t.Fatalf("delete = %d, want 403", resp.Code)
	}
}

func TestConfigGetNotFoundIs404(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	if resp := api.Get("/api/v1/configs/999"); resp.Code != 404 {
		t.Fatalf("missing config = %d, want 404", resp.Code)
	}
}
