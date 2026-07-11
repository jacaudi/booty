package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/hardware"
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

// TestConfigPreviewDebianConfigRenders: preview is FREE for debianconfig (I1)
// — the non-renderable guard names only schematic/taloscluster, and the
// stub-vars/previewVars paths need no change.
func TestConfigPreviewDebianConfigRenders(t *testing.T) {
	deps, _ := targetsTestDeps(t)
	api := newTestAPI(t, deps)
	resp := api.Post("/api/v1/configs", map[string]any{
		"name": "prev", "kind": "debianconfig",
		"source": "hostname: \"{{ .Hostname }}\"\nlocale: en_US.UTF-8\n",
	})
	if resp.Code != 201 {
		t.Fatalf("create = %d: %s", resp.Code, resp.Body.String())
	}
	prev := api.Post("/api/v1/configs/1/preview", map[string]any{})
	if prev.Code != 200 {
		t.Fatalf("preview = %d: %s", prev.Code, prev.Body.String())
	}
	// stubVars() sets Hostname "preview-host" — the translated preseed carries it.
	if !strings.Contains(prev.Body.String(), "d-i netcfg/get_hostname string preview-host") {
		t.Fatalf("preview body = %s, want translated preseed with stub hostname", prev.Body.String())
	}
}

// TestDebianConfigEndToEndServe: create via the API -> bind to a Debian host
// via the API -> GET /preseed returns the rendered flat preseed BYTE-EXACT
// (design §11 integration).
func TestDebianConfigEndToEndServe(t *testing.T) {
	deps := hostsTestDeps(t)
	api := newTestAPI(t, deps)
	const mac = "aa:bb:cc:dd:ee:62"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "debian", Hostname: "deb-node", Approved: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	create := api.Post("/api/v1/configs", map[string]any{
		"name": "deb-e2e", "kind": "debianconfig",
		"source": "hostname: \"{{ .Hostname }}\"\nlocale: en_US.UTF-8\npackages: [openssh-server]\n",
	})
	if create.Code != 201 {
		t.Fatalf("create = %d: %s", create.Code, create.Body.String())
	}
	bind := api.Post("/api/v1/hosts/"+mac+"/bind", map[string]any{"configId": 1})
	if bind.Code != 200 {
		t.Fatalf("bind = %d: %s", bind.Code, bind.Body.String())
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preseed?mac="+mac, nil)
	handlePreseedRequest(deps.Store)(rr, req)
	want := "d-i debian-installer/locale string en_US.UTF-8\n" +
		"d-i netcfg/get_hostname string deb-node\n" +
		"d-i pkgsel/include string openssh-server\n"
	if rr.Code != 200 || rr.Body.String() != want {
		t.Fatalf("serve = %d:\ngot:\n%s\nwant:\n%s", rr.Code, rr.Body.String(), want)
	}
}

// TestDebianConfigBindNonDebianHostRejected: familyAllowsKind rejects
// debianconfig for a non-preseed family — hostsTestDeps seeds a FLATCAR
// (ignition-family) host at aa:bb:cc:dd:ee:40.
func TestDebianConfigBindNonDebianHostRejected(t *testing.T) {
	deps := hostsTestDeps(t)
	api := newTestAPI(t, deps)
	create := api.Post("/api/v1/configs", map[string]any{
		"name": "deb-wrong", "kind": "debianconfig", "source": "hostname: x\n",
	})
	if create.Code != 201 {
		t.Fatalf("create = %d: %s", create.Code, create.Body.String())
	}
	bind := api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:40/bind", map[string]any{"configId": 1})
	if bind.Code != 422 {
		t.Fatalf("cross-family bind = %d, want 422: %s", bind.Code, bind.Body.String())
	}
	if !strings.Contains(bind.Body.String(), "config kind does not match host OS family") {
		t.Fatalf("bind body = %s, want family-mismatch message", bind.Body.String())
	}
}
