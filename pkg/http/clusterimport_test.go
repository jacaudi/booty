package http

import (
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/spf13/viper"
)

// genControlPlaneBytes builds a real control-plane machineconfig via the T6
// engine so the import path is tested against genuine Talos output.
func genControlPlaneBytes(t *testing.T, machineType string) []byte {
	t.Helper()
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	b, err := mintBundle("v1.13.5")
	if err != nil {
		t.Fatal(err)
	}
	out, err := generateNodeConfig(nodeGenInput{
		Bundle: b, Name: "imp", Endpoint: "https://10.0.0.10:6443",
		TalosVersion: "v1.13.5", K8sVersion: "v1.34.0",
		Schematic: config.DefaultTalosSchematic, MachineType: machineType,
		SinglePlaneScheduling: machineType == "controlplane",
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// genCPFromBundle builds a control-plane machineconfig from a SHARED secrets
// bundle. Two calls with the SAME bundle produce configs that belong to the same
// cluster (identical CA); a non-empty installDisk applies a machine.install.disk
// patch so those configs differ byte-for-byte (the "2 nodes share a config
// except disk IDs" case). A distinct bundle per call yields a distinct cluster.
func genCPFromBundle(t *testing.T, b *secrets.Bundle, endpoint, installDisk string) []byte {
	t.Helper()
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	var patches []string
	if installDisk != "" {
		patches = []string{"machine:\n  install:\n    disk: " + installDisk + "\n"}
	}
	out, err := generateNodeConfig(nodeGenInput{
		Bundle: b, Name: "imp", Endpoint: endpoint,
		TalosVersion: "v1.13.5", K8sVersion: "v1.34.0",
		Schematic: config.DefaultTalosSchematic, MachineType: "controlplane",
		SinglePlaneScheduling: true, PatchSources: patches,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseInstallImage(t *testing.T) {
	sch, ver := parseInstallImage("factory.talos.dev/installer/" + config.DefaultTalosSchematic + ":v1.13.5")
	if sch != config.DefaultTalosSchematic || ver != "v1.13.5" {
		t.Fatalf("parseInstallImage = (%q, %q)", sch, ver)
	}
	if s, v := parseInstallImage("nonsense"); s != "" || v != "" {
		t.Fatalf("unrecognized image should yield empty, got (%q,%q)", s, v)
	}
}

func TestExtractClusterFieldsFromControlPlane(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	cp := genControlPlaneBytes(t, "controlplane")

	prov, err := parseImportedConfig(cp)
	if err != nil {
		t.Fatalf("parseImportedConfig: %v", err)
	}
	f, err := extractClusterFields(prov)
	if err != nil {
		t.Fatalf("extractClusterFields: %v", err)
	}
	if f.Endpoint != "https://10.0.0.10:6443" {
		t.Errorf("endpoint = %q", f.Endpoint)
	}
	if f.TalosVersion != "v1.13.5" {
		t.Errorf("talosVersion = %q", f.TalosVersion)
	}
	if f.K8sVersion != "v1.34.0" {
		t.Errorf("k8sVersion = %q", f.K8sVersion)
	}
	if f.Schematic != config.DefaultTalosSchematic {
		t.Errorf("schematic = %q", f.Schematic)
	}
	if f.MachineType != "controlplane" {
		t.Errorf("machineType = %q", f.MachineType)
	}
}

func TestImportRejectsWorkerOnly(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	w := genControlPlaneBytes(t, "worker")

	prov, err := parseImportedConfig(w)
	if err != nil {
		t.Fatalf("parseImportedConfig(worker): %v", err) // a worker config still parses+validates
	}
	// But it cannot reconstruct a cluster (no CA keys, §6.2/§14-B) — reject.
	if _, err := extractClusterFields(prov); err == nil {
		t.Fatal("worker-only import must be rejected (controlplane.yaml required)")
	}
}

func TestParseImportedConfigRejectsGarbage(t *testing.T) {
	if _, err := parseImportedConfig([]byte("not a talos config\n")); err == nil {
		t.Fatal("unparseable import must be rejected")
	}
}

func TestClusterIdentityKey(t *testing.T) {
	// Empty identity (no CA and no id) is rejected — prevents two identity-less
	// configs from false-matching as the "same cluster".
	if _, err := clusterIdentityKey(nil, ""); err == nil {
		t.Fatal("empty identity must be rejected")
	}
	if _, err := clusterIdentityKey([]byte{}, "   "); err == nil {
		t.Fatal("whitespace-only id with no CA must be rejected")
	}
	// Same CA + id → same key (deterministic).
	k1, err := clusterIdentityKey([]byte("CA-A"), "id-1")
	if err != nil {
		t.Fatalf("valid identity errored: %v", err)
	}
	k2, _ := clusterIdentityKey([]byte("CA-A"), "id-1")
	if k1 != k2 {
		t.Fatal("same identity must yield the same key")
	}
	// Different CA → different key (the CA is the discriminator).
	if k3, _ := clusterIdentityKey([]byte("CA-B"), "id-1"); k1 == k3 {
		t.Fatal("different CA must yield a different key")
	}
	// A CA with no id is still a valid identity (CA is primary).
	if _, err := clusterIdentityKey([]byte("CA-A"), ""); err != nil {
		t.Fatalf("CA-only identity must be valid: %v", err)
	}
}
