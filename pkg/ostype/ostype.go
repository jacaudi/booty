// Package ostype is booty's OS taxonomy: a Family (boot-config mechanism +
// iPXE template shape) groups one or more OSes; each OS owns version discovery,
// validation, ordering, and artifact location. It is the bounded seam that lets
// a new OS be one new file. In P1a it is built and unit-tested in isolation —
// not yet wired into the boot path or cache reconciler (P1b/P1c).
package ostype

import (
	"cmp"
	"context"
	"slices"
)

// Artifact is one downloadable boot file and its upstream URL.
type Artifact struct {
	Filename string
	URL      string
}

// Family is plain DATA — every field is a constant. The version/arch/baseurl
// tokens are injected at render time by the boot path (P1c), not by the family,
// so an interface here would be data wearing a method set. Template holds the
// kernel-cmdline config-URL directive the family injects (design §2.1); the
// full iPXE skeleton is composed by the boot path when ostype is wired in P1c.
type Family struct {
	Name       string // "ignition" | "talos" | "debian"
	ConfigKind string // "ignition" | "machineconfig" | "preseed"
	Template   string // config-URL directive: "ignition.config.url=", "talos.config=", "auto url="
}

// OS is a distro belonging to a family. Each implementor has real per-instance
// behavior, so unlike Family it earns an interface.
type OS interface {
	Name() string
	Family() Family
	RequiredParams() []string
	ValidateVersion(v string) error
	CompareVersions(a, b string) int
	// DiscoverVersions returns the upstream-advertised versions for one target.
	// params carries the target's decoded params: discovery for flatcar/fcos is
	// channel-scoped (params["channel"]); talos/debian ignore the argument.
	// (Same widening P1a already applied to Artifacts.)
	DiscoverVersions(ctx context.Context, params map[string]string) ([]string, error)
	Artifacts(version, arch string, params map[string]string) []Artifact
}

// families is the single source of the family contract.
var families = map[string]Family{
	"ignition": {Name: "ignition", ConfigKind: "ignition", Template: "ignition.config.url="},
	"talos":    {Name: "talos", ConfigKind: "machineconfig", Template: "talos.config="},
	"debian":   {Name: "debian", ConfigKind: "preseed", Template: "auto url="},
}

// FamilyByName returns the named family.
func FamilyByName(name string) (Family, bool) {
	f, ok := families[name]
	return f, ok
}

// osRegistry holds every OS, populated by each OS file's init().
var osRegistry = map[string]OS{}

func register(o OS) { osRegistry[o.Name()] = o }

// Lookup returns the OS with the given name.
func Lookup(name string) (OS, bool) {
	o, ok := osRegistry[name]
	return o, ok
}

// All returns every registered OS, ordered by name.
func All() []OS {
	out := make([]OS, 0, len(osRegistry))
	for _, o := range osRegistry {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b OS) int { return cmp.Compare(a.Name(), b.Name()) })
	return out
}
