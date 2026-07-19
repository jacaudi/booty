package http

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	talosconfig "github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
)

// importedClusterFields are the durable inputs booty reconstructs from an
// uploaded controlplane.yaml (design §6.2/D8): endpoint + pinned versions +
// per-role schematic. The bytes themselves are still stored verbatim; these
// fields populate the structured clusters row.
type importedClusterFields struct {
	Endpoint     string
	TalosVersion string
	K8sVersion   string
	Schematic    string
	MachineType  string
}

// parseImportedConfig loads an uploaded config and runs the library's metal
// validation (SGE ADOPT): a malformed-but-parseable config is caught here,
// before it is ever frozen or served. The config stays opaque bytes
// thereafter — Validate is the admission gate, not a schema booty maintains.
func parseImportedConfig(source []byte) (talosconfig.Provider, error) {
	prov, err := configloader.NewFromBytes(source)
	if err != nil {
		return nil, fmt.Errorf("http: parse imported config: %w", err)
	}
	if _, err := prov.Validate(metalMode{}); err != nil {
		return nil, fmt.Errorf("http: imported config failed validation: %w", err)
	}
	return prov, nil
}

// extractClusterFields reads the durable inputs from a CONTROL-PLANE provider.
// A worker config is rejected: it carries crt-only CAs and cannot reconstruct
// the secrets bundle (design §14-B), so import requires controlplane.yaml (D8).
func extractClusterFields(prov talosconfig.Provider) (importedClusterFields, error) {
	if prov.Machine().Type() != machine.TypeControlPlane {
		return importedClusterFields{}, fmt.Errorf(
			"http: import requires a controlplane.yaml (got machine type %q); worker configs lack the CA keys to reconstruct a cluster",
			prov.Machine().Type())
	}
	schematic, talosVersion := parseInstallImage(prov.Machine().Install().Image())
	f := importedClusterFields{
		Endpoint:     prov.Cluster().Endpoint().String(),
		TalosVersion: talosVersion,
		K8sVersion:   k8sVersionFromAPIServerImage(prov.Cluster().APIServer().Image()),
		Schematic:    schematic,
		MachineType:  "controlplane",
	}
	return f, nil
}

// parseInstallImage splits a Talos installer ref
// (<host>/installer/<schematic>:<version>) into its schematic + version.
// Anything not matching that shape yields ("", "") — the caller then falls
// back to the --talosSchematic default and rejects a missing version.
func parseInstallImage(image string) (schematic, version string) {
	const marker = "/installer/"
	i := strings.Index(image, marker)
	if i < 0 {
		return "", ""
	}
	rest := image[i+len(marker):]
	colon := strings.LastIndex(rest, ":")
	if colon < 0 {
		return "", ""
	}
	return rest[:colon], rest[colon+1:]
}

// k8sVersionFromAPIServerImage returns the tag from a kube-apiserver image ref
// (registry.k8s.io/kube-apiserver:v1.34.0 -> v1.34.0), or "".
func k8sVersionFromAPIServerImage(image string) string {
	if colon := strings.LastIndex(image, ":"); colon >= 0 {
		return image[colon+1:]
	}
	return ""
}

// errEmptyClusterIdentity is returned when a control-plane config carries no
// usable cluster identity (neither an issuing CA nor a cluster id). Rejecting it
// stops two identity-less configs from false-matching as the "same cluster"
// (design §4.4 same-cluster guard, SGE M1).
var errEmptyClusterIdentity = errors.New("http: control-plane config has no cluster identity (empty issuing CA and id)")

// clusterIdentityKey derives a stable key identifying the cluster a control-plane
// config belongs to, from the issuing-CA certificate bytes (primary) and the
// cluster id (secondary discriminator). Configs from one cluster share both;
// configs from different clusters differ in the CA. Both empty → error.
func clusterIdentityKey(caCrt []byte, id string) (string, error) {
	if len(caCrt) == 0 && strings.TrimSpace(id) == "" {
		return "", errEmptyClusterIdentity
	}
	h := sha256.New()
	h.Write([]byte(id))
	h.Write([]byte{0}) // separator so id||crt is unambiguous
	h.Write(caCrt)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// clusterIdentity adapts a parsed control-plane provider to clusterIdentityKey,
// reading the issuing-CA cert bytes and cluster id the secrets bundle is built
// from (secrets.NewBundleFromConfig), so the guard and the bundle source agree.
func clusterIdentity(prov talosconfig.Provider) (string, error) {
	var caCrt []byte
	if ca := prov.Cluster().IssuingCA(); ca != nil {
		caCrt = ca.Crt
	}
	return clusterIdentityKey(caCrt, prov.Cluster().ID())
}
