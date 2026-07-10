package http

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/config"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/spf13/viper"
	yaml "go.yaml.in/yaml/v4"
)

// defaultInstallDisk mirrors `talosctl gen config`'s --install-disk default.
// Without an install disk (or diskSelector) a generated config fails Talos's
// own metal validation and cannot install (plan divergence D-B). Operators
// override via a machine.install strategic-merge patch.
const defaultInstallDisk = "/dev/sda"

// metalMode is the validation.RuntimeMode booty validates generated/imported
// configs under: bare-metal install context (RequiresInstall=true), not a
// container. Validation is performed by the library — booty never parses the
// Talos schema itself (P4 D6; the admission-gate exception, SGE ADOPT).
type metalMode struct{}

func (metalMode) String() string        { return "metal" }
func (metalMode) RequiresInstall() bool { return true }
func (metalMode) InContainer() bool     { return false }

// machineTypeFor maps booty's machine_type vocabulary to machine.Type.
func machineTypeFor(machineType string) (machine.Type, error) {
	switch machineType {
	case "controlplane":
		return machine.TypeControlPlane, nil
	case "worker":
		return machine.TypeWorker, nil
	default:
		return machine.TypeUnknown, fmt.Errorf("http: invalid machine type %q (want controlplane|worker)", machineType)
	}
}

// clusterSpec is the parsed taloscluster-kind spec: cluster-wide + per-role
// patch SOURCES (each a strategic-merge or JSON6902 document). It carries
// patches ONLY — never node identity (that is the host binding, design §3).
type clusterSpec struct {
	ClusterPatches      []string `yaml:"clusterPatches"`
	ControlPlanePatches []string `yaml:"controlPlanePatches"`
	WorkerPatches       []string `yaml:"workerPatches"`
}

// parseClusterSpec decodes a taloscluster spec source. An empty source yields
// a zero spec (no patches). YAML errors are returned for the API to 422.
func parseClusterSpec(source []byte) (clusterSpec, error) {
	var spec clusterSpec
	if len(strings.TrimSpace(string(source))) == 0 {
		return spec, nil
	}
	if err := yaml.Unmarshal(source, &spec); err != nil {
		return clusterSpec{}, fmt.Errorf("http: parse cluster spec: %w", err)
	}
	return spec, nil
}

// patchSourcesFor composes the ordered patch layer for one member (design §9):
// cluster-wide, then the machine-type role layer, then the optional per-host
// patch (narrowest, last — configpatcher.Apply applies in slice order).
func patchSourcesFor(spec clusterSpec, machineType, hostPatch string) []string {
	out := slices.Clone(spec.ClusterPatches)
	switch machineType {
	case "controlplane":
		out = append(out, spec.ControlPlanePatches...)
	case "worker":
		out = append(out, spec.WorkerPatches...)
	}
	if strings.TrimSpace(hostPatch) != "" {
		out = append(out, hostPatch)
	}
	return out
}

// loadPatchList parses each source with configpatcher.LoadPatch (auto-detecting
// strategic-merge vs JSON6902). LoadPatch is used deliberately over LoadPatches:
// the latter treats a leading '@' as a server-local filename, which must never
// be reachable from API input.
func loadPatchList(sources []string) ([]configpatcher.Patch, error) {
	patches := make([]configpatcher.Patch, 0, len(sources))
	for i, src := range sources {
		p, err := configpatcher.LoadPatch([]byte(src))
		if err != nil {
			return nil, fmt.Errorf("http: load patch %d: %w", i, err)
		}
		patches = append(patches, p)
	}
	return patches, nil
}

// validateClusterSpecSource is the taloscluster-kind admission gate (Task 12):
// the spec must parse AND every patch it names must load. It does not render or
// serve anything (a taloscluster config is not a template).
func validateClusterSpecSource(source []byte) error {
	spec, err := parseClusterSpec(source)
	if err != nil {
		return err
	}
	all := slices.Concat(spec.ClusterPatches, spec.ControlPlanePatches, spec.WorkerPatches)
	if _, err := loadPatchList(all); err != nil {
		return err
	}
	return nil
}

// mintBundle generates a fresh secrets bundle for a greenfield cluster, pinned
// to talosVersion's contract with a FIXED clock so regeneration is stable
// (§5). The fixed instant is arbitrary-but-constant: certificate not-before is
// pinned, not "now", which is what makes the frozen bytes reproducible.
func mintBundle(talosVersion string) (*secrets.Bundle, error) {
	contract, err := talosconfig.ParseContractFromVersion(talosVersion)
	if err != nil {
		return nil, fmt.Errorf("http: contract for %q: %w", talosVersion, err)
	}
	b, err := secrets.NewBundle(secrets.NewFixedClock(fixedBundleClock), contract)
	if err != nil {
		return nil, fmt.Errorf("http: mint secrets bundle: %w", err)
	}
	return b, nil
}

// fixedBundleClock pins certificate not-before for reproducible generation.
// A fixed instant (not time.Now) is required by the freeze model — see §5.
var fixedBundleClock = mustTime("2020-01-01T00:00:00Z")

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// marshalBundle / unmarshalBundle serialize the bundle with the same yaml the
// machinery LoadBundle path uses, so an exported secrets.yaml stays
// talosctl-compatible and a persisted bundle round-trips losslessly
// (probe-verified: regeneration from a round-tripped bundle is byte-identical).
func marshalBundle(b *secrets.Bundle) ([]byte, error) {
	out, err := yaml.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("http: marshal bundle: %w", err)
	}
	return out, nil
}

func unmarshalBundle(raw []byte) (*secrets.Bundle, error) {
	var b secrets.Bundle
	if err := yaml.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("http: unmarshal bundle: %w", err)
	}
	b.Clock = secrets.NewFixedClock(fixedBundleClock)
	return &b, nil
}

// installerImageRef builds the Talos installer image the machineconfig pins:
// <factory-host>/installer/<schematic>:<talosVersion>. factoryURL's scheme is
// stripped (image refs carry no scheme); talosVersion keeps its v-prefix
// (image tags are v-prefixed).
func installerImageRef(factoryURL, schematic, talosVersion string) string {
	host := factoryURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host = strings.TrimSuffix(host, "/")
	return fmt.Sprintf("%s/installer/%s:%s", host, schematic, talosVersion)
}

// nodeGenInput is the full input to one node-config generation.
type nodeGenInput struct {
	Bundle                *secrets.Bundle
	Name                  string
	Endpoint              string
	TalosVersion          string // v-prefixed
	K8sVersion            string // v-prefix trimmed before NewInput
	Schematic             string
	MachineType           string
	SinglePlaneScheduling bool // D9: allow scheduling on control planes (no workers yet)
	PatchSources          []string
}

// generateNodeConfig produces one node's machineconfig bytes: NewInput (bundle
// + pinned contract + install image + install disk) -> Config(type) -> apply
// layered patches in order -> Bytes. The composed bytes are re-loaded and
// Validate(metal)-checked BEFORE returning, so a config that cannot install is
// never frozen (plan divergence D-B). The output is opaque bytes to booty; the
// library owns both generation and validation.
func generateNodeConfig(in nodeGenInput) ([]byte, error) {
	contract, err := talosconfig.ParseContractFromVersion(in.TalosVersion)
	if err != nil {
		return nil, fmt.Errorf("http: contract for %q: %w", in.TalosVersion, err)
	}
	mt, err := machineTypeFor(in.MachineType)
	if err != nil {
		return nil, err
	}
	image := installerImageRef(viper.GetString(config.TalosFactoryURL), in.Schematic, in.TalosVersion)

	input, err := generate.NewInput(in.Name, in.Endpoint, strings.TrimPrefix(in.K8sVersion, "v"),
		generate.WithSecretsBundle(in.Bundle),
		generate.WithVersionContract(contract),
		generate.WithInstallImage(image),
		generate.WithInstallDisk(defaultInstallDisk),
		generate.WithAllowSchedulingOnControlPlanes(in.SinglePlaneScheduling),
	)
	if err != nil {
		return nil, fmt.Errorf("http: build generate input: %w", err)
	}
	prov, err := input.Config(mt)
	if err != nil {
		return nil, fmt.Errorf("http: generate %s config: %w", in.MachineType, err)
	}

	patches, err := loadPatchList(in.PatchSources)
	if err != nil {
		return nil, err
	}
	out, err := configpatcher.Apply(configpatcher.WithConfig(prov), patches)
	if err != nil {
		return nil, fmt.Errorf("http: apply patches: %w", err)
	}
	produced, err := out.Bytes()
	if err != nil {
		return nil, fmt.Errorf("http: serialize config: %w", err)
	}

	// Admission gate (D-B): the composed bytes must pass Talos's own metal
	// validation before they are frozen. Warnings are tolerated; a fatal error
	// (e.g. no install disk) refuses the config. Library-owned; opaque to booty.
	loaded, err := configloader.NewFromBytes(produced)
	if err != nil {
		return nil, fmt.Errorf("http: reload generated config: %w", err)
	}
	if _, err := loaded.Validate(metalMode{}); err != nil {
		return nil, fmt.Errorf("http: generated config failed validation: %w", err)
	}
	return produced, nil
}
