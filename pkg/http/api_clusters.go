package http

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/config"
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
		if err := deps.Store.UpdateCluster(in.ID, in.Body.Endpoint, in.Body.TalosVersion, in.Body.K8sVersion, in.Body.SpecConfigID); err != nil {
			return nil, huma.Error500InternalServerError("update cluster", err)
		}
		// I1 desync guard (Important #1): the tftp netboot pin reads the LIVE
		// cluster.talos_version, but frozen configs lag until explicit re-bind
		// (D-C). So a version bump immediately advances every member's netboot
		// pin — pre-cache the new (schematic, version) targets and kick a
		// reconcile NOW so a member rebooting in the pre-re-bind window can fetch
		// the new boot kernel instead of 404ing. (It still installs the old frozen
		// image until re-bound — a self-healing skew, documented in CONFIGURATION.)
		if versionBumped {
			if err := ensureClusterMemberTargets(deps, in.ID); err != nil {
				return nil, err
			}
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
func ensureClusterMemberTargets(deps APIDeps, clusterID int64) error {
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
		if err := cache.EnsureSchematicTarget(deps.Store, schematic); err != nil {
			return huma.Error422UnprocessableEntity("ensure member cache target: "+err.Error(), err)
		}
	}
	deps.Trigger()
	return nil
}

// registerClusterMembers is filled in by Task 14 (import + add/remove member).
func registerClusterMembers(api huma.API, deps APIDeps) {}
