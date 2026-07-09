package http

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestBuildSchematicSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/schematics" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "siderolabs/iscsi-tools") {
			t.Errorf("factory did not receive the customization source: %q", body)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"a1b2c3d4"}`)
	}))
	defer srv.Close()

	id, err := buildSchematic(t.Context(), srv.URL,
		[]byte("customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/iscsi-tools\n"))
	if err != nil {
		t.Fatalf("buildSchematic: %v", err)
	}
	if id != "a1b2c3d4" {
		t.Fatalf("id = %q, want a1b2c3d4", id)
	}
}

func TestBuildSchematicFactoryErrorSurfacesDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "yaml: line 2: mapping values are not allowed")
	}))
	defer srv.Close()

	_, err := buildSchematic(t.Context(), srv.URL, []byte("customization: ["))
	if err == nil {
		t.Fatal("want error for factory 400")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "yaml: line 2") {
		t.Fatalf("error must carry status + factory detail, got: %v", err)
	}
}

func TestBuildSchematicRejectsUnusableID(t *testing.T) {
	for name, payload := range map[string]string{
		"path-unsafe": `{"id":"../evil"}`,
		"empty":       `{"id":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, payload)
			}))
			defer srv.Close()
			if _, err := buildSchematic(t.Context(), srv.URL, []byte("customization: {}\n")); err == nil {
				t.Fatal("want error for an unusable factory id")
			}
		})
	}
}

func TestSeedVanillaSchematicIdempotentNoFactoryCall(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	// A live stub proves the seed NEVER calls the Factory (SGE I4): startup
	// must not depend on Factory reachability (disposability / air-gap).
	calls := factoryStub(t, 201, "must-never-be-built")

	if err := SeedVanillaSchematic(deps.Store); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if err := SeedVanillaSchematic(deps.Store); err != nil {
		t.Fatalf("second seed (restart): %v", err)
	}

	list, err := deps.Store.ListConfigs()
	if err != nil || len(list) != 1 {
		t.Fatalf("configs after two seeds = %d (err %v), want exactly 1", len(list), err)
	}
	v := list[0]
	if v.Name != "vanilla" || v.Kind != "schematic" {
		t.Fatalf("seeded config = %+v, want vanilla/schematic", v)
	}
	if v.DerivedSchematicID != config.DefaultTalosSchematic {
		t.Fatalf("seeded id = %q, want the vanilla constant %q", v.DerivedSchematicID, config.DefaultTalosSchematic)
	}
	if n, _ := deps.Store.CountRevisions(v.ID); n != 1 {
		t.Fatalf("revisions after two seeds = %d, want 1 (idempotent)", n)
	}
	if *calls != 0 {
		t.Fatalf("startup seed called the Factory %d times, want 0 (SGE I4)", *calls)
	}
}

func TestSeedVanillaSchematicRespectsOperatorName(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	// An operator-created config already named "vanilla" (any kind) makes the
	// seed a no-op — create-if-absent is keyed on the UNIQUE name.
	if _, err := deps.Store.CreateConfig("vanilla", "butane"); err != nil {
		t.Fatal(err)
	}
	if err := SeedVanillaSchematic(deps.Store); err != nil {
		t.Fatalf("seed with existing name: %v", err)
	}
	list, _ := deps.Store.ListConfigs()
	if len(list) != 1 || list[0].Kind != "butane" {
		t.Fatalf("seed must not touch an operator-owned name: %+v", list)
	}
}

func TestBuildSchematicHonorsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // a Factory that never answers in time
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	// The caller's deadline composes with factoryBuildTimeout (whichever fires
	// first wins) — a 50ms outer deadline proves the request is ctx-bounded
	// without waiting out the 15s production bound.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := buildSchematic(ctx, srv.URL, []byte("customization: {}\n"))
	if err == nil {
		t.Fatal("want deadline error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("buildSchematic ignored the context deadline (took %v)", elapsed)
	}
}
