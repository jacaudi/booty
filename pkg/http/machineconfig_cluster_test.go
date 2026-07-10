package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func TestMemberServesFrozenBytesVerbatim(t *testing.T) {
	s := servingStore(t)
	testSecretsKey(t)
	const mac = "aa:bb:cc:dd:ee:70"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos", Approved: true}); err != nil {
		t.Fatal(err)
	}
	cid, err := s.CreateCluster("serve", "https://10.0.0.10:6443", "v1.13.5", "v1.34.0", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	frozen := []byte("version: v1alpha1\nmachine:\n  type: controlplane\n# frozen bytes\n")
	enc, err := encryptSecrets(frozen)
	if err != nil {
		t.Fatal(err)
	}
	ncID, _, err := s.AddClusterNodeConfig(mac, cid, enc, "sha", "generated", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := hardware.SetHostCluster(mac, &cid); err != nil {
		t.Fatal(err)
	}
	if err := hardware.SetHostNodeConfig(mac, &ncID); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac="+mac, nil)
	handleMachineConfigRequest(s)(rr, req)

	if rr.Code != 200 {
		t.Fatalf("member machineconfig = %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != string(frozen) {
		t.Fatalf("member config not byte-identical:\n got %q\nwant %q", rr.Body.String(), frozen)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/yaml" {
		t.Fatalf("Content-Type = %q, want text/yaml", ct)
	}
}

func TestNonMemberFallsThroughByteIdentical(t *testing.T) {
	s := servingStore(t)
	testSecretsKey(t)
	viper.Set("talosConfigFile", "config/machineconfig.yaml")
	writeFile(t, "config/machineconfig.yaml", "version: v1alpha1\nmachine:\n  network:\n    hostname: {{ .Hostname }}\n")
	const mac = "aa:bb:cc:dd:ee:71"
	// A talos host that is NOT a cluster member (no node_config_id).
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos", Hostname: "plain", Approved: true}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac="+mac, nil)
	handleMachineConfigRequest(s)(rr, req)
	if rr.Code != 200 || rr.Body.String() != "version: v1alpha1\nmachine:\n  network:\n    hostname: plain\n" {
		t.Fatalf("non-member must render the P4 file path unchanged: %d %q", rr.Code, rr.Body.String())
	}
}

func TestMemberWithUndecryptableConfigFailsLoud(t *testing.T) {
	s := servingStore(t)
	testSecretsKey(t)
	const mac = "aa:bb:cc:dd:ee:72"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos", Approved: true}); err != nil {
		t.Fatal(err)
	}
	cid, _ := s.CreateCluster("bad", "https://e:6443", "v1.13.5", "v1.34.0", []byte("x"))
	// Store ciphertext that the CURRENT key cannot decrypt.
	enc, _ := encryptSecrets([]byte("frozen"))
	ncID, _, _ := s.AddClusterNodeConfig(mac, cid, enc, "sha", "generated", "")
	_ = s
	if err := hardware.SetHostCluster(mac, &cid); err != nil {
		t.Fatal(err)
	}
	if err := hardware.SetHostNodeConfig(mac, &ncID); err != nil {
		t.Fatal(err)
	}
	testSecretsKey(t) // rotate to a different key -> decrypt must fail

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac="+mac, nil)
	handleMachineConfigRequest(s)(rr, req)
	if rr.Code != 500 {
		t.Fatalf("member with undecryptable config = %d, want 500 (never fall through)", rr.Code)
	}
}
