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

// TestCreateConfigRejectsPreseedKind: the 'preseed' config kind was removed
// (#59) — huma's enum validation on the create-config Body.Kind field must
// reject it before any handler logic runs. Asserting only resp.Code==422
// would also pass BEFORE this task's fix (Task 3's DB CHECK constraint
// already 422s once the request reaches CreateConfig), so this additionally
// pins the huma-level "validation failed" body — the schema-validation
// response huma emits for an enum miss — to prove the enum, not the DB
// constraint, is what rejected it.
func TestCreateConfigRejectsPreseedKind(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/configs", map[string]any{"name": "np", "kind": "preseed", "source": "x"})
	if resp.Code != 422 {
		t.Fatalf("create preseed = %d, want 422 (huma enum rejects it)", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), `"detail":"validation failed"`) {
		t.Fatalf("create preseed body = %s, want huma schema-validation rejection", resp.Body.String())
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
	api.Post("/api/v1/configs", map[string]any{"name": "p", "kind": "debianconfig", "source": "raw_preseed: |\n  host={{ .ServerIP }}\n"})
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
	api.Post("/api/v1/configs", map[string]any{"name": "r", "kind": "debianconfig", "source": "hostname: v1\n"})
	api.Put("/api/v1/configs/1", map[string]any{"source": "hostname: v2\n"})
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
	api.Post("/api/v1/configs", map[string]any{"name": "d", "kind": "debianconfig", "source": "{}\n"})
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

// TestConfigPreviewUsesPerKindVars pins previewVars' per-kind dispatch: a
// machineconfig preview must use the machineconfig-family vars (HOST-ONLY
// .ServerIP, port carried separately in .ServerHTTPPort) — the same vars the
// boot path would use — not the ignition-family host:port .ServerIP. Contrast
// with a preseed preview (ignition family), which DOES get host:port.
func TestConfigPreviewUsesPerKindVars(t *testing.T) {
	deps := hostsTestDeps(t) // seeds host aa:bb:cc:dd:ee:40, OS=flatcar
	api := newTestAPI(t, deps)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ServerIP, "10.0.0.1")
	viper.Set(config.ServerHttpPort, "8080")

	mcResp := api.Post("/api/v1/configs", map[string]any{
		"name": "mc", "kind": "machineconfig", "source": "server={{ .ServerIP }}",
	})
	if mcResp.Code != 201 {
		t.Fatalf("create machineconfig config: %d %s", mcResp.Code, mcResp.Body.String())
	}
	psResp := api.Post("/api/v1/configs", map[string]any{
		"name": "ps", "kind": "debianconfig", "source": "raw_preseed: |\n  server={{ .ServerIP }}\n",
	})
	if psResp.Code != 201 {
		t.Fatalf("create preseed config: %d %s", psResp.Code, psResp.Body.String())
	}

	mcPreview := api.Post("/api/v1/configs/1/preview", map[string]any{"mac": "aa:bb:cc:dd:ee:40"})
	if mcPreview.Code != 200 {
		t.Fatalf("preview machineconfig = %d: %s", mcPreview.Code, mcPreview.Body.String())
	}
	if !strings.Contains(mcPreview.Body.String(), "server=10.0.0.1") ||
		strings.Contains(mcPreview.Body.String(), "server=10.0.0.1:8080") {
		t.Fatalf("machineconfig preview must render HOST-ONLY ServerIP, got: %s", mcPreview.Body.String())
	}

	psPreview := api.Post("/api/v1/configs/2/preview", map[string]any{"mac": "aa:bb:cc:dd:ee:40"})
	if psPreview.Code != 200 {
		t.Fatalf("preview preseed = %d: %s", psPreview.Code, psPreview.Body.String())
	}
	if !strings.Contains(psPreview.Body.String(), "server=10.0.0.1:8080") {
		t.Fatalf("preseed preview must render host:port ServerIP, got: %s", psPreview.Body.String())
	}
}
