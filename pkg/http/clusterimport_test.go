package http

import (
	"testing"

	"github.com/jeefy/booty/pkg/config"
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
