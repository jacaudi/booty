package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// factoryStub stands in for the Image Factory: each POST /schematics answers
// with the next id in ids (the last repeats) and status; it counts builds and
// points --talosFactoryURL at itself. Call AFTER viper.Reset.
func factoryStub(t *testing.T, status int, ids ...string) *int {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/schematics" {
			http.NotFound(w, r)
			return
		}
		calls++
		if status >= 300 {
			w.WriteHeader(status)
			fmt.Fprint(w, "schematic parse failed")
			return
		}
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"id":%q}`, ids[min(calls-1, len(ids)-1)])
	}))
	t.Cleanup(srv.Close)
	viper.Set(config.TalosFactoryURL, srv.URL)
	return &calls
}

// schematicTestEnv resets viper and sets everything the schematic save path
// reads (unit tests never run config.LoadConfig, so defaults must be explicit).
func schematicTestEnv(t *testing.T) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ConfigRevisionsKeep, 10)
	viper.Set(config.TalosArchitecture, "amd64")
	viper.Set(config.TalosRetainMinors, 3)
}

const iscsiSource = "customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/iscsi-tools\n"

func TestSchematicCreateBuildsStoresAndPreCaches(t *testing.T) {
	deps, trigger := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	schematicTestEnv(t)
	calls := factoryStub(t, 201, "a1b2c3d4")

	resp := api.Post("/api/v1/configs", map[string]any{
		"name": "iscsi", "kind": "schematic", "source": iscsiSource,
	})
	if resp.Code != 201 {
		t.Fatalf("create = %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"derivedSchematicId":"a1b2c3d4"`) {
		t.Fatalf("create body missing derived id: %s", resp.Body.String())
	}
	if *calls != 1 {
		t.Fatalf("factory builds = %d, want 1", *calls)
	}
	// D4: the save ensured a discovery target and kicked a reconcile.
	targets, err := deps.Store.ListTargets()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(targets, func(tg db.Target) bool {
		return tg.OS == "talos" && tg.Params == `{"schematic":"a1b2c3d4"}` && tg.Mode == "discovery" && !tg.Predefined
	}) {
		t.Fatalf("schematic cache target not ensured: %+v", targets)
	}
	if *trigger != 1 {
		t.Fatalf("reconcile trigger = %d, want 1", *trigger)
	}
	// The catalog lists it with the derived id.
	if resp := api.Get("/api/v1/configs"); !strings.Contains(resp.Body.String(), `"derivedSchematicId":"a1b2c3d4"`) {
		t.Fatalf("list missing derived id: %s", resp.Body.String())
	}
}

func TestSchematicCreateFactoryErrorIs422NoRowNoTarget(t *testing.T) {
	deps, trigger := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	schematicTestEnv(t)
	factoryStub(t, 500)

	resp := api.Post("/api/v1/configs", map[string]any{
		"name": "broken", "kind": "schematic", "source": "customization: [",
	})
	if resp.Code != 422 {
		t.Fatalf("factory 500 → create = %d, want 422: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "schematic build failed") {
		t.Fatalf("422 detail missing: %s", resp.Body.String())
	}
	if list, _ := deps.Store.ListConfigs(); len(list) != 0 {
		t.Fatalf("failed build must leave no config row, got %+v", list)
	}
	if targets, _ := deps.Store.ListTargets(); len(targets) != 0 {
		t.Fatalf("failed build must leave no target, got %+v", targets)
	}
	if *trigger != 0 {
		t.Fatalf("failed build must not trigger reconcile, got %d", *trigger)
	}
}

func TestSchematicEditMintsNewIDRollbackNoRebuild(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	schematicTestEnv(t)
	calls := factoryStub(t, 201, "a1b2c3d4", "e5f6a7b8")

	if resp := api.Post("/api/v1/configs", map[string]any{
		"name": "cp-min", "kind": "schematic", "source": "customization: {}\n",
	}); resp.Code != 201 {
		t.Fatalf("create = %d: %s", resp.Code, resp.Body.String())
	}
	// Edit → new revision, new content-addressed ID.
	resp := api.Put("/api/v1/configs/1", map[string]any{"source": iscsiSource})
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"derivedSchematicId":"e5f6a7b8"`) {
		t.Fatalf("edit = %d: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Get("/api/v1/configs/1/revisions"); !strings.Contains(resp.Body.String(), `"revision":2`) {
		t.Fatalf("edit must mint revision 2: %s", resp.Body.String())
	}
	// Rollback re-points to revision 1's STORED id — no Factory re-POST.
	before := *calls
	resp = api.Post("/api/v1/configs/1/rollback", map[string]any{"revision": 1})
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"derivedSchematicId":"a1b2c3d4"`) {
		t.Fatalf("rollback = %d: %s", resp.Code, resp.Body.String())
	}
	if *calls != before {
		t.Fatalf("rollback must not rebuild: factory calls %d → %d", before, *calls)
	}
}

func TestSchematicPreviewIs422(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	schematicTestEnv(t)
	factoryStub(t, 201, "a1b2c3d4")
	if resp := api.Post("/api/v1/configs", map[string]any{
		"name": "s", "kind": "schematic", "source": "customization: {}\n",
	}); resp.Code != 201 {
		t.Fatalf("create = %d", resp.Code)
	}
	if resp := api.Post("/api/v1/configs/1/preview", map[string]any{}); resp.Code != 422 {
		t.Fatalf("preview(schematic) = %d, want 422", resp.Code)
	}
}
