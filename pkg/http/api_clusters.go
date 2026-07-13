package http

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/spf13/viper"
)

// ClusterDTO is the wire shape of a cluster (design §10). Members + status are
// derived (status from host.Booted, D5 — no health subsystem).
type ClusterDTO struct {
	ID           int64       `json:"id"`
	Name         string      `json:"name"`
	Endpoint     string      `json:"endpoint"`
	TalosVersion string      `json:"talosVersion"`
	K8sVersion   string      `json:"k8sVersion"`
	SpecConfigID *int64      `json:"specConfigId,omitzero"`
	Members      []MemberDTO `json:"members"`
	UpdatedAt    string      `json:"updatedAt"`
}

// MemberDTO is one cluster member. Schematic is the per-node P5 schematic;
// Status is derived from host.Booted.
type MemberDTO struct {
	MAC         string `json:"mac"`
	Hostname    string `json:"hostname"`
	MachineType string `json:"machineType"`
	Schematic   string `json:"schematic,omitzero"`
	Status      string `json:"status"`
}

// memberStatus derives a member's status from host.Booted (D5): a host that has
// reported a boot is "booted"; otherwise it is "pending". No liveness probing.
func memberStatus(h db.Host) string {
	if h.Booted != "" {
		return "booted"
	}
	return "pending"
}

// clusterToDTO assembles a cluster's wire shape, including its derived members.
func clusterToDTO(store *db.Store, c *db.Cluster) (*ClusterDTO, error) {
	members, err := store.ListClusterMembers(c.ID)
	if err != nil {
		return nil, err
	}
	dto := &ClusterDTO{
		ID: c.ID, Name: c.Name, Endpoint: c.Endpoint, TalosVersion: c.TalosVersion,
		K8sVersion: c.K8sVersion, SpecConfigID: c.SpecConfigID, UpdatedAt: c.UpdatedAt,
		// Non-nil so an empty membership serializes as [] not null — members is a
		// list field, and a null crashes list consumers (the web view's .length).
		Members: []MemberDTO{},
	}
	for _, h := range members {
		dto.Members = append(dto.Members, MemberDTO{
			MAC: h.MAC, Hostname: h.Hostname, MachineType: h.MachineType,
			Schematic: h.Schematic, Status: memberStatus(h),
		})
	}
	return dto, nil
}

// validateClusterInputs is the shared boundary check for create/update: a valid
// URL endpoint and a parseable (v-prefixed) Talos version. Reused so create and
// update reject the same bad inputs.
func validateClusterInputs(endpoint, talosVersion string) error {
	if u, err := url.ParseRequestURI(endpoint); err != nil || u.Host == "" {
		return huma.Error422UnprocessableEntity("endpoint must be a valid URL")
	}
	if _, err := talosconfig.ParseContractFromVersion(talosVersion); err != nil {
		return huma.Error422UnprocessableEntity("talosVersion must be a valid Talos version (e.g. v1.13.5)")
	}
	return nil
}

// registerClusters mounts /clusters on the /api/v1 group. Mutations are OPEN in
// the trust window; DELETE is wired-but-403 until auth (P10). The membership +
// import arms are added by Task 14 inside this same function.
func registerClusters(api huma.API, deps APIDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "list-clusters", Method: http.MethodGet, Path: "/clusters",
		Summary: "List Talos clusters", Tags: []string{"clusters"},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Clusters []ClusterDTO `json:"clusters"`
		}
	}, error) {
		list, err := deps.Store.ListClusters()
		if err != nil {
			return nil, huma.Error500InternalServerError("list clusters", err)
		}
		out := &struct {
			Body struct {
				Clusters []ClusterDTO `json:"clusters"`
			}
		}{}
		// Non-nil so an empty list serializes as [] not null (list field).
		out.Body.Clusters = []ClusterDTO{}
		for i := range list {
			dto, err := clusterToDTO(deps.Store, &list[i])
			if err != nil {
				return nil, huma.Error500InternalServerError("assemble cluster", err)
			}
			out.Body.Clusters = append(out.Body.Clusters, *dto)
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "create-cluster", Method: http.MethodPost, Path: "/clusters",
		Summary: "Create a Talos cluster (greenfield)", Tags: []string{"clusters"}, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct {
		Body struct {
			Name         string `json:"name"`
			Endpoint     string `json:"endpoint"`
			TalosVersion string `json:"talosVersion"`
			K8sVersion   string `json:"k8sVersion"`
		}
	}) (*struct{ Body ClusterDTO }, error) {
		if in.Body.Name == "" || in.Body.K8sVersion == "" {
			return nil, huma.Error422UnprocessableEntity("name and k8sVersion are required")
		}
		if err := validateClusterInputs(in.Body.Endpoint, in.Body.TalosVersion); err != nil {
			return nil, err
		}
		// Mint + encrypt the secrets bundle. Fail-closed: no --secretsKey => refuse.
		bundle, err := mintBundle(in.Body.TalosVersion)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("mint secrets bundle", err)
		}
		raw, err := marshalBundle(bundle)
		if err != nil {
			return nil, huma.Error500InternalServerError("marshal bundle", err)
		}
		enc, err := encryptSecrets(raw)
		if err != nil {
			if errors.Is(err, config.ErrNoSecretsKey) {
				return nil, huma.Error422UnprocessableEntity("cluster secrets require --secretsKey (fail-closed)")
			}
			return nil, huma.Error500InternalServerError("encrypt bundle", err)
		}
		id, err := deps.Store.CreateCluster(in.Body.Name, in.Body.Endpoint, in.Body.TalosVersion, in.Body.K8sVersion, enc)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("create cluster (duplicate name?)", err)
		}
		return clusterDTOResp(deps.Store, id)
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-cluster", Method: http.MethodGet, Path: "/clusters/{id}",
		Summary: "Get a cluster", Tags: []string{"clusters"},
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*struct{ Body ClusterDTO }, error) {
		return clusterDTOResp(deps.Store, in.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-cluster", Method: http.MethodPut, Path: "/clusters/{id}",
		Summary: "Update a cluster's pinned inputs", Tags: []string{"clusters"},
	}, func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body struct {
			Endpoint     string `json:"endpoint"`
			TalosVersion string `json:"talosVersion"`
			K8sVersion   string `json:"k8sVersion"`
			SpecConfigID *int64 `json:"specConfigId,omitempty"`
		}
	}) (*struct{ Body ClusterDTO }, error) {
		c, err := deps.Store.GetCluster(in.ID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("cluster not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get cluster", err)
		}
		if err := validateClusterInputs(in.Body.Endpoint, in.Body.TalosVersion); err != nil {
			return nil, err
		}
		if in.Body.K8sVersion == "" {
			return nil, huma.Error422UnprocessableEntity("k8sVersion is required")
		}
		// PUT preserves the existing spec binding when specConfigId is omitted: a
		// nil pointer can't be distinguished from an explicit null under omitempty,
		// so PUT cannot CLEAR the spec (no present need; a later PATCH could). This
		// avoids silently dropping the spec (and its machine.install override) on a
		// version-bump PUT that doesn't re-send specConfigId.
		specConfigID := in.Body.SpecConfigID
		if specConfigID == nil {
			specConfigID = c.SpecConfigID
		}
		if in.Body.SpecConfigID != nil {
			spec, gerr := deps.Store.GetConfig(*in.Body.SpecConfigID)
			if errors.Is(gerr, db.ErrNotFound) {
				return nil, huma.Error422UnprocessableEntity("spec config does not exist")
			}
			if gerr != nil {
				return nil, huma.Error500InternalServerError("get spec config", gerr)
			}
			if spec.Kind != "taloscluster" {
				return nil, huma.Error422UnprocessableEntity("spec config must be kind=taloscluster")
			}
		}
		versionBumped := c.TalosVersion != in.Body.TalosVersion
		// I4 atomicity + I1 desync guard: on a version bump, ensure + manually pin
		// every member's new-version cache targets and kick a reconcile BEFORE
		// committing the bump. The tftp netboot pin reads the LIVE
		// cluster.talos_version, so committing first would advance the pin even if
		// pre-caching then failed — and a retry would see versionBumped==false and
		// silently skip the guard forever. Pre-caching first means a failure leaves
		// the version (and the pin) unchanged and a retry re-runs the guard. Frozen
		// configs still lag until explicit re-bind (D-C): a member rebooting in the
		// pre-re-bind window netboots the new pinned kernel but installs the old
		// frozen image until re-bound — a self-healing skew, documented in CONFIGURATION.
		if versionBumped {
			if err := ensureClusterMemberTargets(deps, in.ID, in.Body.TalosVersion); err != nil {
				return nil, err
			}
		}
		if err := deps.Store.UpdateCluster(in.ID, in.Body.Endpoint, in.Body.TalosVersion, in.Body.K8sVersion, specConfigID); err != nil {
			return nil, huma.Error500InternalServerError("update cluster", err)
		}
		return clusterDTOResp(deps.Store, in.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "export-cluster-secrets", Method: http.MethodPost, Path: "/clusters/{id}/export",
		Summary: "Export a cluster's secrets bundle (secrets.yaml)", Tags: []string{"clusters"},
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*struct {
		Body struct {
			SecretsYAML string `json:"secretsYaml"`
		}
	}, error) {
		c, err := deps.Store.GetCluster(in.ID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("cluster not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get cluster", err)
		}
		raw, err := decryptSecrets(c.SecretsEnc)
		if err != nil {
			if errors.Is(err, config.ErrNoSecretsKey) {
				return nil, huma.Error422UnprocessableEntity("export requires --secretsKey (fail-closed)")
			}
			return nil, huma.Error500InternalServerError("decrypt bundle", err)
		}
		out := &struct {
			Body struct {
				SecretsYAML string `json:"secretsYaml"`
			}
		}{}
		out.Body.SecretsYAML = string(raw)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "delete-cluster", Method: http.MethodDelete, Path: "/clusters/{id}",
		Summary: "Delete a cluster (disabled until auth)", Tags: []string{"clusters"},
	}, func(ctx context.Context, _ *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, huma.Error403Forbidden("destructive endpoints are disabled until authentication lands (P10)")
	})

	registerClusterMembers(api, deps) // Task 14 (import + add/remove member)
}

// clusterDTOResp reads a cluster back and assembles its DTO, or 404/500.
func clusterDTOResp(store *db.Store, id int64) (*struct{ Body ClusterDTO }, error) {
	c, err := store.GetCluster(id)
	if errors.Is(err, db.ErrNotFound) {
		return nil, huma.Error404NotFound("cluster not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("get cluster", err)
	}
	dto, err := clusterToDTO(store, c)
	if err != nil {
		return nil, huma.Error500InternalServerError("assemble cluster", err)
	}
	return &struct{ Body ClusterDTO }{Body: *dto}, nil
}

// ensureClusterMemberTargets ensures a discovery cache target for every member's
// (schematic) so the reconciler eagerly fetches the cluster's PINNED talos
// version's boot assets, then kicks a reconcile. Called on a version bump (I1
// desync guard, Important #1) so a member rebooting before re-bind can netboot
// the new pinned kernel. A member with no explicit schematic resolves to the
// --talosSchematic default (the SAME resolution the tftp non-member path and
// clusterPins use — Minor: schematic-default parity).
func ensureClusterMemberTargets(deps APIDeps, clusterID int64, talosVersion string) error {
	members, err := deps.Store.ListClusterMembers(clusterID)
	if err != nil {
		return huma.Error500InternalServerError("list members", err)
	}
	def := viper.GetString(config.TalosSchematic)
	for _, m := range members {
		schematic := m.Schematic
		if schematic == "" {
			schematic = def
		}
		if err := pinClusterMemberVersion(deps, schematic, talosVersion); err != nil {
			return err
		}
	}
	deps.Trigger()
	return nil
}

// pinClusterMemberVersion ensures the member's schematic discovery target exists
// AND pins the cluster's talos version as a MANUAL target version, so the
// reconciler fetches those boot assets even when the version is already below
// the discovery window (closes the D-F back-fetch gap, I5) and never prunes it
// (manual rows are retained). It does NOT Trigger — callers batch the reconcile.
// Auto-created pins are not removed on remove-member (DELETE version is 403
// until P10); they accumulate like frozen revisions do, to be cleaned up at P10.
func pinClusterMemberVersion(deps APIDeps, schematic, talosVersion string) error {
	if schematic == "" {
		return nil
	}
	if err := cache.EnsureSchematicTarget(deps.Store, schematic); err != nil {
		return huma.Error422UnprocessableEntity("ensure schematic cache target: "+err.Error(), err)
	}
	if talosVersion == "" {
		return nil
	}
	targets, err := deps.Store.ListTargets()
	if err != nil {
		return huma.Error500InternalServerError("list targets", err)
	}
	for _, t := range targets {
		if t.OS != "talos" {
			continue
		}
		var p map[string]string
		if json.Unmarshal([]byte(t.Params), &p) == nil && p["schematic"] == schematic {
			if err := deps.Store.PinManualVersion(t.ID, talosVersion); err != nil {
				return huma.Error500InternalServerError("pin cluster version", err)
			}
			return nil
		}
	}
	return nil
}

// clusterHostPatchLayers builds a member's ordered patch source list from the
// cluster's bound taloscluster spec + the (effective) per-host patch, layered
// cluster → role → host (§9). A cluster with no spec contributes no
// cluster/role layers. The per-host patch is a DURABLE input, persisted on the
// frozen revision and reused on re-bind (see effectiveHostPatch / freezeAndBind).
func clusterHostPatchLayers(store *db.Store, cluster *db.Cluster, machineType, hostPatch string) ([]string, error) {
	var spec clusterSpec
	if cluster.SpecConfigID != nil {
		if rev, err := store.GetActiveRevision(*cluster.SpecConfigID); err == nil {
			src, derr := base64.StdEncoding.DecodeString(rev.SourceB64)
			if derr != nil {
				return nil, derr
			}
			spec, err = parseClusterSpec(src)
			if err != nil {
				return nil, err
			}
		}
	}
	return patchSourcesFor(spec, machineType, hostPatch), nil
}

// addMember generates + freezes + binds one member (design §6.3): compose its
// patch layers, generate its config (allowSched=true when this is a CP and the
// cluster has no worker yet — D9/D-D), freeze it age-encrypted, pre-cache its
// (schematic, version) assets, and write the host membership columns. Shared by
// the add-member endpoint and the import path.
func addMember(deps APIDeps, cluster *db.Cluster, mac, machineType, schematic, hostPatch string) error {
	patches, err := clusterHostPatchLayers(deps.Store, cluster, machineType, hostPatch)
	if err != nil {
		return huma.Error422UnprocessableEntity("compose patches: "+err.Error(), err)
	}
	bundleRaw, err := decryptSecrets(cluster.SecretsEnc)
	if err != nil {
		if errors.Is(err, config.ErrNoSecretsKey) {
			return huma.Error422UnprocessableEntity("cluster operations require --secretsKey (fail-closed)")
		}
		return huma.Error500InternalServerError("decrypt bundle", err)
	}
	bundle, err := unmarshalBundle(bundleRaw)
	if err != nil {
		return huma.Error500InternalServerError("load bundle", err)
	}
	singlePlane := machineType == "controlplane" && !clusterHasWorker(deps.Store, cluster.ID)
	produced, err := generateNodeConfig(nodeGenInput{
		Bundle: bundle, Name: cluster.Name, Endpoint: cluster.Endpoint,
		TalosVersion: cluster.TalosVersion, K8sVersion: cluster.K8sVersion,
		Schematic: schematic, MachineType: machineType,
		SinglePlaneScheduling: singlePlane, PatchSources: patches,
	})
	if err != nil {
		return huma.Error422UnprocessableEntity("generate node config: "+err.Error(), err)
	}
	// hostPatch is persisted on the frozen revision so a later re-bind that omits
	// it can reuse it (Fold 3 / §1.1 durable inputs). Generated members store the
	// effective patch; imported members store "" (their bytes are verbatim).
	if err := freezeAndBind(deps, cluster.ID, mac, machineType, schematic, cluster.TalosVersion, produced, "generated", hostPatch); err != nil {
		return err
	}
	return nil
}

// effectiveHostPatch resolves the per-host patch for an add-member / re-bind:
// the request patch when supplied; otherwise, on a re-bind (the host already
// has an active frozen revision), the patch persisted on that revision — so the
// customization survives a re-bind that omits it; otherwise "". This is what
// makes the per-host patch a durable generation input (Fold 3).
func effectiveHostPatch(deps APIDeps, host *hardware.Host, requestPatch string) string {
	if strings.TrimSpace(requestPatch) != "" {
		return requestPatch
	}
	if host.NodeConfigID != nil {
		if nc, err := deps.Store.GetClusterNodeConfig(*host.NodeConfigID); err == nil {
			return nc.HostPatch
		}
	}
	return ""
}

// freezeAndBind age-encrypts the produced bytes, appends a frozen revision
// (persisting hostPatch — the per-host patch that produced them, "" for
// imported/patch-less), pre-caches the member's schematic assets, and writes
// the host membership columns (schematic via P5's setter, cluster/type/
// node-config via P6's). It is the shared tail of both generate and import.
func freezeAndBind(deps APIDeps, clusterID int64, mac, machineType, schematic, talosVersion string, produced []byte, source, hostPatch string) error {
	enc, err := encryptSecrets(produced)
	if err != nil {
		if errors.Is(err, config.ErrNoSecretsKey) {
			return huma.Error422UnprocessableEntity("cluster operations require --secretsKey (fail-closed)")
		}
		return huma.Error500InternalServerError("encrypt node config", err)
	}
	sum := sha256.Sum256(produced)
	ncID, _, err := deps.Store.AddClusterNodeConfig(mac, clusterID, enc, hex.EncodeToString(sum[:]), source, hostPatch)
	if err != nil {
		return huma.Error500InternalServerError("freeze node config", err)
	}
	// Pre-cache the member's boot assets (retention-pinned, §8) and kick a
	// reconcile. Failure is non-fatal (self-heals next tick) — log via Trigger's
	// own path; here a target error is surfaced only if EnsureSchematicTarget fails.
	if schematic != "" {
		if err := pinClusterMemberVersion(deps, schematic, talosVersion); err != nil {
			return err
		}
		deps.Trigger()
	}
	if schematic != "" {
		if err := hardware.SetSchematic(mac, schematic); err != nil {
			return huma.Error500InternalServerError("bind schematic", err)
		}
	}
	if err := hardware.SetHostCluster(mac, &clusterID); err != nil {
		return huma.Error500InternalServerError("bind cluster", err)
	}
	if err := hardware.SetHostMachineType(mac, machineType); err != nil {
		return huma.Error500InternalServerError("bind machine type", err)
	}
	if err := hardware.SetHostNodeConfig(mac, &ncID); err != nil {
		return huma.Error500InternalServerError("bind node config", err)
	}
	return nil
}

// clusterHasWorker reports whether the cluster already has a worker member (D9:
// a control-plane generated while no worker exists gets allowSchedulingOn
// ControlPlanes=true so a single-node cluster is usable).
func clusterHasWorker(store *db.Store, clusterID int64) bool {
	members, err := store.ListClusterMembers(clusterID)
	if err != nil {
		return true // conservative: on error, do NOT taint the CP as schedulable
	}
	for _, m := range members {
		if m.MachineType == "worker" {
			return true
		}
	}
	return false
}

func registerClusterMembers(api huma.API, deps APIDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "add-cluster-member", Method: http.MethodPost, Path: "/clusters/{id}/members",
		Summary: "Add (or re-bind) a host to a cluster", Tags: []string{"clusters"},
	}, func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body struct {
			MAC         string `json:"mac"`
			MachineType string `json:"machineType"`
			SchematicID *int64 `json:"schematicId,omitempty"`
			Schematic   string `json:"schematic,omitempty"`
			Patch       string `json:"patch,omitempty"`
		}
	}) (*struct{ Body ClusterDTO }, error) {
		cluster, err := deps.Store.GetCluster(in.ID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("cluster not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get cluster", err)
		}
		if in.Body.MachineType != "controlplane" && in.Body.MachineType != "worker" {
			return nil, huma.Error422UnprocessableEntity("machineType must be controlplane or worker")
		}
		h, err := hardware.GetMacAddress(in.Body.MAC)
		if errors.Is(err, hardware.ErrNotFound) {
			return nil, huma.Error404NotFound("host not found")
		}
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		if h.OS != "talos" {
			return nil, huma.Error422UnprocessableEntity("cluster membership applies to Talos hosts only")
		}
		// A host is in <=1 cluster: reject a host already bound to ANOTHER cluster
		// (re-binding to the SAME cluster is allowed — that is the regenerate path).
		if h.ClusterID != nil && *h.ClusterID != in.ID {
			return nil, huma.Error422UnprocessableEntity("host already belongs to another cluster")
		}
		// M4: a re-bind (same cluster) regenerates the member's frozen config but
		// must not silently change its role — a controlplane<->worker switch changes
		// the generated config type and leaves stale opposite-role revisions. Remove
		// the member and add it again to change the machine type.
		if h.ClusterID != nil && h.MachineType != "" && h.MachineType != in.Body.MachineType {
			return nil, huma.Error422UnprocessableEntity("cannot change machineType on re-bind; remove the member and add it again")
		}
		// Resolve the member's schematic: explicit config/raw, else the host's
		// current schematic, else the operator's configured --talosSchematic
		// default (Minor: parity with the tftp non-member path and clusterPins'
		// "" resolution — NOT the DefaultTalosSchematic constant, which would
		// silently give a member vanilla instead of the configured default).
		schematic := h.Schematic
		if in.Body.SchematicID != nil || in.Body.Schematic != "" {
			schematic, err = resolveSchematicID(deps.Store, in.Body.SchematicID, in.Body.Schematic)
			if err != nil {
				return nil, err
			}
		}
		if schematic == "" {
			schematic = viper.GetString(config.TalosSchematic)
		}
		// Per-host patch: request patch, else (on re-bind) the persisted patch
		// from the current frozen revision (Fold 3 — durable input, reused).
		hostPatch := effectiveHostPatch(deps, h, in.Body.Patch)
		if err := addMember(deps, cluster, h.MAC, in.Body.MachineType, schematic, hostPatch); err != nil {
			return nil, err
		}
		return clusterDTOResp(deps.Store, in.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "remove-cluster-member", Method: http.MethodDelete, Path: "/clusters/{id}/members/{mac}",
		Summary: "Remove a host from a cluster (stop provisioning)", Tags: []string{"clusters"},
	}, func(ctx context.Context, in *struct {
		ID  int64  `path:"id"`
		MAC string `path:"mac"`
	}) (*struct{ Body ClusterDTO }, error) {
		if _, err := deps.Store.GetCluster(in.ID); errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("cluster not found")
		}
		h, err := hardware.GetMacAddress(in.MAC)
		if errors.Is(err, hardware.ErrNotFound) {
			return nil, huma.Error404NotFound("host not found")
		}
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid MAC", err)
		}
		if h.ClusterID == nil || *h.ClusterID != in.ID {
			return nil, huma.Error422UnprocessableEntity("host is not a member of this cluster")
		}
		// Revert to P4 precedence (§6.4). Order is retry-safe: clear node_config_id
		// FIRST (serving immediately reverts to P4, and no row references a deleted
		// config), then prune the frozen revisions, and clear cluster_id LAST — so
		// if any step fails mid-sequence, cluster_id is still set and a retry passes
		// the membership guard above and completes the cleanup (rather than 422ing).
		if err := hardware.SetHostNodeConfig(in.MAC, nil); err != nil {
			return nil, huma.Error500InternalServerError("clear node config", err)
		}
		if err := deps.Store.DeleteClusterNodeConfigs(h.MAC, in.ID); err != nil {
			return nil, huma.Error500InternalServerError("prune node configs", err)
		}
		if err := hardware.SetHostMachineType(in.MAC, ""); err != nil {
			return nil, huma.Error500InternalServerError("clear machine type", err)
		}
		if err := hardware.SetHostCluster(in.MAC, nil); err != nil {
			return nil, huma.Error500InternalServerError("clear cluster", err)
		}
		return clusterDTOResp(deps.Store, in.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "import-cluster", Method: http.MethodPost, Path: "/clusters/import",
		Summary: "Adopt an existing cluster from its controlplane.yaml", Tags: []string{"clusters"}, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct {
		Body struct {
			Name            string `json:"name"`
			Controlplane    string `json:"controlplane"`
			ControlplaneMAC string `json:"controlplaneMac"`
		}
	}) (*struct{ Body ClusterDTO }, error) {
		if in.Body.Name == "" || in.Body.Controlplane == "" || in.Body.ControlplaneMAC == "" {
			return nil, huma.Error422UnprocessableEntity("name, controlplane, and controlplaneMac are required")
		}
		prov, err := parseImportedConfig([]byte(in.Body.Controlplane))
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid controlplane config: "+err.Error(), err)
		}
		fields, err := extractClusterFields(prov)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error(), err) // worker-only rejected here
		}
		if fields.TalosVersion == "" {
			return nil, huma.Error422UnprocessableEntity("could not determine Talos version from install image")
		}
		if fields.K8sVersion == "" {
			return nil, huma.Error422UnprocessableEntity("could not determine Kubernetes version from the control-plane config")
		}
		schematic := fields.Schematic
		if schematic == "" {
			schematic = viper.GetString(config.TalosSchematic)
		}
		// Resolve + validate the control-plane host BEFORE creating the cluster, so
		// a failed/guarded import never leaves an orphan cluster row (name is UNIQUE
		// and DELETE is 403 until P10). Same host guards add-member enforces, plus:
		// import always creates a NEW cluster, so a host already in ANY cluster is a
		// conflict — never silently steal it from another cluster.
		h, err := hardware.GetMacAddress(in.Body.ControlplaneMAC)
		if errors.Is(err, hardware.ErrNotFound) {
			return nil, huma.Error404NotFound("controlplane host not found")
		}
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid controlplane MAC", err)
		}
		if h.OS != "talos" {
			return nil, huma.Error422UnprocessableEntity("cluster membership applies to Talos hosts only")
		}
		if h.ClusterID != nil {
			return nil, huma.Error422UnprocessableEntity("host already belongs to a cluster")
		}
		// Reconstruct + persist the encrypted secrets bundle from the CP config.
		// Reuse the already-parsed prov (Fold 5): secrets.NewBundleFromConfig takes
		// the config.Config that config.Provider embeds — no second parse.
		bundle := secrets.NewBundleFromConfig(secrets.NewFixedClock(fixedBundleClock), prov)
		bundleRaw, err := marshalBundle(bundle)
		if err != nil {
			return nil, huma.Error500InternalServerError("marshal bundle", err)
		}
		encBundle, err := encryptSecrets(bundleRaw)
		if err != nil {
			if errors.Is(err, config.ErrNoSecretsKey) {
				return nil, huma.Error422UnprocessableEntity("import requires --secretsKey (fail-closed)")
			}
			return nil, huma.Error500InternalServerError("encrypt bundle", err)
		}
		cid, err := deps.Store.CreateCluster(in.Body.Name, fields.Endpoint, fields.TalosVersion, fields.K8sVersion, encBundle)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("create cluster (duplicate name?)", err)
		}
		cluster, err := deps.Store.GetCluster(cid)
		if err != nil {
			return nil, huma.Error500InternalServerError("read cluster", err)
		}
		// Bind the CP host (resolved + validated above) to the VERBATIM imported
		// bytes (source='imported', byte-identical recreation, D8). Reuse
		// freezeAndBind so the host is wired exactly like a generated member.
		// Imported bytes are verbatim; there is no separate per-host patch, so
		// host_patch is "" (Fold 3).
		if err := freezeAndBind(deps, cluster.ID, h.MAC, "controlplane", schematic, cluster.TalosVersion, []byte(in.Body.Controlplane), "imported", ""); err != nil {
			return nil, err
		}
		return clusterDTOResp(deps.Store, cid)
	})
}
