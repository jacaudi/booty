package http

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func baseGenInput(t *testing.T) nodeGenInput {
	t.Helper()
	b, err := mintBundle("v1.13.5")
	if err != nil {
		t.Fatalf("mintBundle: %v", err)
	}
	return nodeGenInput{
		Bundle: b, Name: "probe", Endpoint: "https://10.0.0.10:6443",
		TalosVersion: "v1.13.5", K8sVersion: "v1.34.0",
		Schematic:   config.DefaultTalosSchematic,
		MachineType: "controlplane", SinglePlaneScheduling: true,
	}
}

func TestGenerateNodeConfigIsDeterministic(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	in := baseGenInput(t)

	a, err := generateNodeConfig(in)
	if err != nil {
		t.Fatalf("generate a: %v", err)
	}
	b, err := generateNodeConfig(in)
	if err != nil {
		t.Fatalf("generate b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("same inputs must produce byte-identical config (freeze depends on this)")
	}
	// The install image embeds the pinned version + schematic (installer ref).
	if !bytes.Contains(a, []byte("v1.13.5")) || !bytes.Contains(a, []byte(config.DefaultTalosSchematic)) {
		t.Fatalf("generated config missing pinned install image markers")
	}
}

func TestGenerateNodeConfigContractChangeDiffers(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	in := baseGenInput(t)
	a, err := generateNodeConfig(in)
	if err != nil {
		t.Fatal(err)
	}

	// A different contract (via a different bundle+version) must differ.
	b12, err := mintBundle("v1.12.0")
	if err != nil {
		t.Fatal(err)
	}
	in.Bundle = b12
	in.TalosVersion = "v1.12.0"
	c, err := generateNodeConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("different version contract must produce different bytes (§5 rationale)")
	}
}

func TestBundleMarshalRoundTripRegeneratesIdentical(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	in := baseGenInput(t)
	a, err := generateNodeConfig(in)
	if err != nil {
		t.Fatal(err)
	}

	// Persist + reload the bundle (the store path): regeneration must match,
	// proving the encrypted-at-rest bundle round-trips losslessly.
	raw, err := marshalBundle(in.Bundle)
	if err != nil {
		t.Fatalf("marshalBundle: %v", err)
	}
	rt, err := unmarshalBundle(raw)
	if err != nil {
		t.Fatalf("unmarshalBundle: %v", err)
	}
	in.Bundle = rt
	b, err := generateNodeConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("regeneration from a persisted bundle must be byte-identical")
	}
}

func TestGenerateNodeConfigAppliesLayeredPatches(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	in := baseGenInput(t)
	// NOTE: machine.network.hostname is deliberately NOT used here. For a
	// v1.13.x contract, generate.NewInput's Config() always emits a separate
	// multi-doc "HostnameConfig" document (VersionContract.
	// MultidocNetworkConfigSupported()); patching the legacy v1alpha1
	// machine.network.hostname field then trips the library's own
	// V1Alpha1ConflictValidate ("static hostname is already set in v1alpha1
	// config") because both a HostnameConfig document AND a v1alpha1
	// hostname are set. cluster.network.dnsDomain has no multi-doc
	// counterpart and exercises the same strategic-merge code path.
	in.PatchSources = []string{
		"cluster:\n  network:\n    dnsDomain: cp-from-cluster-patch.local\n",
	}
	out, err := generateNodeConfig(in)
	if err != nil {
		t.Fatalf("generate with patch: %v", err)
	}
	if !bytes.Contains(out, []byte("cp-from-cluster-patch")) {
		t.Fatal("strategic-merge patch was not applied")
	}
}

func TestGenerateNodeConfigValidatesBeforeFreeze(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.TalosFactoryURL, "https://factory.talos.dev")
	in := baseGenInput(t)
	// NOTE: a patch cannot null the install disk to prove this gate: Talos's
	// own strategic-merge (config/merge.Merge) skips zero-valued scalars
	// ("disk: \"\"") by design — "data in the left is replaced with data in
	// the right unless it's zero value" — so a previously-set string field
	// can never be cleared via a strategic-merge patch. Instead, this patch
	// sets install.extraKernelArgs, which is mutually exclusive with the
	// default grubUseUKICmdline=true and trips the same
	// mode.RequiresInstall() validation branch metal mode gates on.
	in.PatchSources = []string{
		"machine:\n  install:\n    extraKernelArgs:\n      - \"console=ttyS0\"\n",
	}
	if _, err := generateNodeConfig(in); err == nil {
		t.Fatal("config failing metal validation must not be produced")
	}
}

func TestParseClusterSpecLayers(t *testing.T) {
	spec, err := parseClusterSpec([]byte(`
clusterPatches:
  - "cluster:\n  network:\n    dnsDomain: cluster.internal\n"
controlPlanePatches:
  - "machine:\n  network:\n    hostname: cp\n"
workerPatches:
  - "machine:\n  network:\n    hostname: w\n"
`))
	if err != nil {
		t.Fatalf("parseClusterSpec: %v", err)
	}
	cp := patchSourcesFor(spec, "controlplane", "machine:\n  certSANs: [1.2.3.4]\n")
	// cluster-wide first, then role, then per-host (narrowest last — slice order).
	if len(cp) != 3 || !strings.Contains(cp[0], "dnsDomain") ||
		!strings.Contains(cp[1], "hostname: cp") || !strings.Contains(cp[2], "certSANs") {
		t.Fatalf("controlplane patch layering wrong: %#v", cp)
	}
	w := patchSourcesFor(spec, "worker", "")
	if len(w) != 2 || !strings.Contains(w[1], "hostname: w") {
		t.Fatalf("worker patch layering wrong: %#v", w)
	}
}

func TestValidateClusterSpecSourceRejectsBadPatch(t *testing.T) {
	if err := validateClusterSpecSource([]byte("clusterPatches:\n  - \"::: not yaml :::\"\n")); err == nil {
		t.Fatal("a spec with an unparseable patch must be rejected")
	}
	if err := validateClusterSpecSource([]byte("clusterPatches: []\n")); err != nil {
		t.Fatalf("an empty valid spec must pass: %v", err)
	}
}
