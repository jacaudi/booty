package http

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/db"
)

func TestCacheAPIListPinAndDelete403(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)

	tid, _ := deps.Store.CreateTarget(db.Target{OS: "talos", Arch: "amd64", Params: `{"schematic":"abc"}`, Mode: "discovery", RetainN: 1, Enabled: true})
	_ = deps.Store.UpsertTargetVersion(db.TargetVersion{TargetID: tid, Version: "v1.13.5", Source: "discovered", Cached: true})
	tvID, _ := deps.Store.TargetVersionID(tid, "v1.13.5")
	_ = deps.Store.UpsertCacheEntry(tvID, 100)

	resp := api.Get("/api/v1/cache")
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"version":"v1.13.5"`) || !strings.Contains(resp.Body.String(), `"state":"in-cycle"`) {
		t.Fatalf("cache list = %d: %s", resp.Code, resp.Body.String())
	}

	rows, _ := deps.Store.ListCacheEntries(db.CacheFilter{})
	id := rows[0].ID
	if r := api.Post(fmt.Sprintf("/api/v1/cache/%d/pin", id)); r.Code != 200 {
		t.Fatalf("pin = %d: %s", r.Code, r.Body.String())
	}
	rows, _ = deps.Store.ListCacheEntries(db.CacheFilter{})
	if !rows[0].Pinned {
		t.Fatal("pin endpoint should set pinned")
	}

	if r := api.Delete(fmt.Sprintf("/api/v1/cache/%d", id)); r.Code != 403 {
		t.Fatalf("DELETE = %d, want 403", r.Code)
	}

	// unpin: should return 200 and clear pinned flag
	if r := api.Post(fmt.Sprintf("/api/v1/cache/%d/unpin", id)); r.Code != 200 {
		t.Fatalf("unpin = %d: %s", r.Code, r.Body.String())
	}
	rows, _ = deps.Store.ListCacheEntries(db.CacheFilter{})
	if rows[0].Pinned {
		t.Fatal("unpin endpoint should clear pinned")
	}

	// scan: should return 200 with a summary body that includes the "scanned" field
	if r := api.Post("/api/v1/cache/scan"); r.Code != 200 || !strings.Contains(r.Body.String(), `"scanned"`) {
		t.Fatalf("scan = %d: %s", r.Code, r.Body.String())
	}
}
