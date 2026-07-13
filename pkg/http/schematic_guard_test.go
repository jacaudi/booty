package http

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// bindSchematicKindConfig force-binds a schematic-kind config as the host's
// hosts.config_id DIRECTLY through the store — the API refuses this (see
// TestBindRejectsSchematicKindConfigID), so this simulates bad data to prove
// the serving path is safe in depth.
func bindSchematicKindConfig(t *testing.T, s *db.Store, mac string) {
	t.Helper()
	cid, err := s.CreateConfig("sch-guard", "schematic")
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	id := "a1b2c3d4"
	rid, _, err := s.AddConfigRevision(cid, base64.StdEncoding.EncodeToString([]byte("customization: {}\n")), "sha", &id)
	if err != nil {
		t.Fatalf("AddConfigRevision: %v", err)
	}
	if err := s.SetActiveRevision(cid, rid); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostConfig(mac, &cid); err != nil {
		t.Fatal(err)
	}
}

// Coverage note: these guards exercise resolveConfig directly plus the
// machineconfig handler end-to-end. /ignition.json and /preseed are NOT tested
// directly here and do not need to be — all three serving rungs share the one
// resolveConfig gate, whose family-match check (familyAllowsKind never
// returns true for "schematic") is the single point that excludes schematic-kind
// configs. TestResolveConfigNeverServesSchematicKind proves that gate; the
// machineconfig test proves the fall-through wiring one handler exercises for
// all three. A future reader: this is transitive coverage, not a hole.
func TestResolveConfigNeverServesSchematicKind(t *testing.T) {
	s := servingStore(t)
	const mac = "aa:bb:cc:dd:ee:50"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos", Approved: true}); err != nil {
		t.Fatal(err)
	}
	bindSchematicKindConfig(t, s, mac)
	h, err := hardware.GetMacAddress(mac)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := resolveConfig(s, h); ok {
		t.Fatal("resolveConfig served a schematic-kind config (family guard hole)")
	}
}

func TestMachineConfigFallsThroughForSchematicBoundHost(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
	writeFile(t, "config/machineconfig.yaml", "machine:\n  install:\n    disk: /dev/sda\n")
	const mac = "aa:bb:cc:dd:ee:51"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos", Approved: true}); err != nil {
		t.Fatal(err)
	}
	bindSchematicKindConfig(t, s, mac)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac="+mac, nil)
	handleMachineConfigRequest(s)(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "/dev/sda") {
		t.Fatalf("schematic-bound host must fall through to the server-default file: %d %s", rr.Code, rr.Body.String())
	}
}

func TestBindRejectsSchematicKindConfigID(t *testing.T) {
	deps := hostsTestSetup(t)
	api := newTestAPI(t, deps)
	const mac = "aa:bb:cc:00:00:52"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos"}); err != nil {
		t.Fatal(err)
	}
	cid, _ := deps.Store.CreateConfig("sch-bind", "schematic")
	id := "a1b2c3d4"
	rid, _, _ := deps.Store.AddConfigRevision(cid, base64.StdEncoding.EncodeToString([]byte("customization: {}\n")), "sha", &id)
	deps.Store.SetActiveRevision(cid, rid)

	// The talos family's config kind is 'machineconfig'; 'schematic' can never
	// satisfy familyAllowsKind, so /bind and /approve both 422.
	if resp := api.Post("/api/v1/hosts/"+mac+"/bind", map[string]any{"configId": cid}); resp.Code != 422 {
		t.Fatalf("bind(schematic config) = %d, want 422: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Post("/api/v1/hosts/"+mac+"/approve", map[string]any{"configId": cid}); resp.Code != 422 {
		t.Fatalf("approve(schematic config) = %d, want 422: %s", resp.Code, resp.Body.String())
	}
	// The failed approve must have left the host untouched (P4 invariant).
	h, _ := hardware.GetMacAddress(mac)
	if h.Approved || h.ConfigID != nil {
		t.Fatalf("failed binding mutated the host: %+v", *h)
	}
}
