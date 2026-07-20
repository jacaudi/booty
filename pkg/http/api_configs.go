package http

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

// ConfigDTO is the wire shape of a config identity.
type ConfigDTO struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Kind               string `json:"kind"`
	ActiveRevision     int    `json:"activeRevision"`
	RevisionCount      int    `json:"revisionCount"`
	UpdatedAt          string `json:"updatedAt"`
	DerivedSchematicID string `json:"derivedSchematicId,omitzero"` // kind='schematic' only: active revision's Factory ID
}

// RevisionDTO is the wire shape of one immutable revision.
type RevisionDTO struct {
	Revision  int    `json:"revision"`
	SHA256    string `json:"sha256"`
	CreatedAt string `json:"createdAt"`
	Active    bool   `json:"active"`
}

func toConfigListDTO(r db.ConfigListRow) ConfigDTO {
	return ConfigDTO{ID: r.ID, Name: r.Name, Kind: r.Kind, ActiveRevision: r.ActiveRevision, RevisionCount: r.RevisionCount, UpdatedAt: r.UpdatedAt, DerivedSchematicID: r.DerivedSchematicID}
}

// registerConfigs mounts /configs on the /api/v1 group. Mutations are OPEN in
// the trust window; DELETE is wired-but-403 until auth (P10).
func registerConfigs(api huma.API, deps APIDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "list-configs", Method: http.MethodGet, Path: "/configs",
		Summary: "List boot configs", Tags: []string{"configs"},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Configs []ConfigDTO `json:"configs"`
		}
	}, error) {
		list, err := deps.Store.ListConfigs()
		if err != nil {
			return nil, huma.Error500InternalServerError("list configs", err)
		}
		out := &struct {
			Body struct {
				Configs []ConfigDTO `json:"configs"`
			}
		}{}
		for _, r := range list {
			out.Body.Configs = append(out.Body.Configs, toConfigListDTO(r))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "create-config", Method: http.MethodPost, Path: "/configs",
		Summary: "Create a boot config", Tags: []string{"configs"}, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct {
		Body struct {
			Name   string `json:"name"`
			Kind   string `json:"kind" enum:"butane,machineconfig,schematic,taloscluster,debianconfig"`
			Source string `json:"source"`
		}
	}) (*struct{ Body ConfigDTO }, error) {
		if in.Body.Name == "" || in.Body.Source == "" {
			return nil, huma.Error422UnprocessableEntity("name and source are required")
		}
		// Per-kind validation gate (SGE I3): render for renderable kinds,
		// Factory build for schematics. Runs BEFORE CreateConfig so a
		// failed build leaves no config row, no revision, no target.
		derivedID, err := validateConfigSource(ctx, in.Body.Kind, in.Body.Source)
		if err != nil {
			return nil, err
		}
		id, err := deps.Store.CreateConfig(in.Body.Name, in.Body.Kind)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("create config (duplicate name?)", err)
		}
		if err := appendActiveRevision(deps.Store, id, in.Body.Source, derivedID); err != nil {
			return nil, huma.Error500InternalServerError("add revision", err)
		}
		if derivedID != nil {
			// The config+revision are already committed at this point, so a
			// pre-cache failure must not surface as a 500 misrepresenting a
			// resource that actually exists. Pre-caching is self-healing: the
			// reconciler's reconcileHostSchematics re-ensures the target on
			// the next tick — log and continue.
			if err := ensureSchematicPreCache(deps, *derivedID); err != nil {
				slog.Warn("schematic pre-cache failed; config created, will self-heal on next reconcile",
					"config_id", id, "config_name", in.Body.Name, "schematic", *derivedID, "error", err)
			}
		}
		return configDTOResp(deps.Store, id)
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-config", Method: http.MethodGet, Path: "/configs/{id}",
		Summary: "Get a boot config (with active source)", Tags: []string{"configs"},
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*struct {
		Body struct {
			ConfigDTO
			Source string `json:"source"`
		}
	}, error) {
		c, err := deps.Store.GetConfig(in.ID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("config not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get config", err)
		}
		out := &struct {
			Body struct {
				ConfigDTO
				Source string `json:"source"`
			}
		}{}
		n, _ := deps.Store.CountRevisions(in.ID)
		out.Body.ConfigDTO = ConfigDTO{ID: c.ID, Name: c.Name, Kind: c.Kind, RevisionCount: n, UpdatedAt: c.UpdatedAt}
		if rev, err := deps.Store.GetActiveRevision(in.ID); err == nil {
			out.Body.ActiveRevision = rev.Revision
			if rev.DerivedSchematicID != nil {
				out.Body.DerivedSchematicID = *rev.DerivedSchematicID
			}
			if src, derr := base64.StdEncoding.DecodeString(rev.SourceB64); derr == nil {
				out.Body.Source = string(src)
			}
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-config", Method: http.MethodPut, Path: "/configs/{id}",
		Summary: "Append a new config revision", Tags: []string{"configs"},
	}, func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body struct {
			Source string `json:"source"`
		}
	}) (*struct{ Body ConfigDTO }, error) {
		c, err := deps.Store.GetConfig(in.ID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("config not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get config", err)
		}
		if in.Body.Source == "" {
			return nil, huma.Error422UnprocessableEntity("source is required")
		}
		derivedID, verr := validateConfigSource(ctx, c.Kind, in.Body.Source)
		if verr != nil {
			return nil, verr
		}
		if err := appendActiveRevision(deps.Store, in.ID, in.Body.Source, derivedID); err != nil {
			return nil, huma.Error500InternalServerError("add revision", err)
		}
		if err := deps.Store.PruneRevisions(in.ID, viper.GetInt(config.ConfigRevisionsKeep)); err != nil {
			return nil, huma.Error500InternalServerError("prune revisions", err)
		}
		if derivedID != nil {
			// Same rationale as create-config: the new revision is already
			// committed, and pre-caching self-heals on the next reconcile —
			// log and continue instead of a misleading 500.
			if err := ensureSchematicPreCache(deps, *derivedID); err != nil {
				slog.Warn("schematic pre-cache failed; config updated, will self-heal on next reconcile",
					"config_id", in.ID, "config_name", c.Name, "schematic", *derivedID, "error", err)
			}
		}
		return configDTOResp(deps.Store, in.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "preview-config", Method: http.MethodPost, Path: "/configs/{id}/preview",
		Summary: "Render/validate a config against a host (or stub vars)", Tags: []string{"configs"},
	}, func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body struct {
			MAC string `json:"mac,omitempty"`
		}
	}) (*struct {
		Body struct {
			Rendered    string `json:"rendered"`
			ContentType string `json:"contentType"`
			Report      string `json:"report"`
		}
	}, error) {
		c, err := deps.Store.GetConfig(in.ID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("config not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get config", err)
		}
		if c.Kind == "schematic" || c.Kind == "taloscluster" {
			// Non-renderable kinds: schematics resolve to an ID + cache target;
			// taloscluster configs hold cluster/role patches. Neither is a template.
			return nil, huma.Error422UnprocessableEntity(c.Kind + " configs are not renderable")
		}
		rev, err := deps.Store.GetActiveRevision(in.ID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("config has no active revision")
		}
		src, _ := base64.StdEncoding.DecodeString(rev.SourceB64)
		vars := stubVars()
		if in.Body.MAC != "" {
			if h, herr := hardware.GetMacAddress(in.Body.MAC); herr == nil {
				vars = previewVars(deps.Store, h, c.Kind)
			}
		}
		out, ct, report, rerr := renderConfig(c.Kind, src, vars)
		resp := &struct {
			Body struct {
				Rendered    string `json:"rendered"`
				ContentType string `json:"contentType"`
				Report      string `json:"report"`
			}
		}{}
		resp.Body.Report = report
		if rerr != nil {
			resp.Body.Report = report + " | " + rerr.Error()
			resp.Body.ContentType = "text/plain"
			return resp, nil // report-only; a bad config is not a 5xx
		}
		resp.Body.Rendered = string(out)
		resp.Body.ContentType = ct
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-revisions", Method: http.MethodGet, Path: "/configs/{id}/revisions",
		Summary: "List a config's revisions", Tags: []string{"configs"},
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*struct {
		Body struct {
			Revisions []RevisionDTO `json:"revisions"`
		}
	}, error) {
		c, err := deps.Store.GetConfig(in.ID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("config not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get config", err)
		}
		revs, err := deps.Store.ListRevisions(in.ID)
		if err != nil {
			return nil, huma.Error500InternalServerError("list revisions", err)
		}
		out := &struct {
			Body struct {
				Revisions []RevisionDTO `json:"revisions"`
			}
		}{}
		activeID := c.ActiveRevisionID.Int64
		for _, r := range revs {
			out.Body.Revisions = append(out.Body.Revisions, RevisionDTO{
				Revision: r.Revision, SHA256: r.SHA256, CreatedAt: r.CreatedAt, Active: r.ID == activeID,
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "rollback-config", Method: http.MethodPost, Path: "/configs/{id}/rollback",
		Summary: "Roll a config back to an existing revision", Tags: []string{"configs"},
	}, func(ctx context.Context, in *struct {
		ID   int64 `path:"id"`
		Body struct {
			Revision int `json:"revision"`
		}
	}) (*struct{ Body ConfigDTO }, error) {
		if _, err := deps.Store.GetConfig(in.ID); errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("config not found")
		}
		// Revision lookup is config-scoped (GetRevision(configID, revision)), so
		// the revision is validated to belong to THIS config before the active
		// pointer moves — never pass an unvalidated revision id to SetActiveRevision.
		rev, err := deps.Store.GetRevision(in.ID, in.Body.Revision)
		if errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error422UnprocessableEntity("revision does not exist")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("get revision", err)
		}
		if err := deps.Store.SetActiveRevision(in.ID, rev.ID); err != nil {
			return nil, huma.Error500InternalServerError("rollback", err)
		}
		return configDTOResp(deps.Store, in.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "delete-config", Method: http.MethodDelete, Path: "/configs/{id}",
		Summary: "Delete a config (disabled until auth)", Tags: []string{"configs"},
	}, func(ctx context.Context, _ *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, huma.Error403Forbidden("destructive endpoints are disabled until authentication lands (P10)")
	})
}

// validateConfigSource is the per-kind validation gate shared by create-config
// and update-config (SGE I3). Renderable kinds (butane/machineconfig/preseed/
// debianconfig) validate by a stub-var render; 'schematic' validates by
// BUILDING against the Image Factory — the Factory owns schematic validation
// (design §4) and the returned content-addressed ID becomes the revision's
// derived_schematic_id.
// P6's 'taloscluster' (the next non-renderable kind, validated by spec/patch
// parse) slots in as a new case arm here — an additive change, not an edit to
// the create/update handlers (No-Wall).
// The returned *string is non-nil only for kind='schematic'.
func validateConfigSource(ctx context.Context, kind, source string) (*string, error) {
	switch kind {
	case "schematic":
		id, err := buildSchematic(ctx, viper.GetString(config.TalosFactoryURL), []byte(source))
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("schematic build failed: "+err.Error(), err)
		}
		return &id, nil
	case "taloscluster":
		// The next non-renderable kind (P6): validated by parsing the cluster
		// spec and loading every patch it names — never rendered or served. This
		// is the arm P5 reserved here (No-Wall: additive, siblings untouched).
		if err := validateClusterSpecSource([]byte(source)); err != nil {
			return nil, huma.Error422UnprocessableEntity("taloscluster spec validation failed: "+err.Error(), err)
		}
		return nil, nil
	default: // renderable kinds: butane | machineconfig | preseed | debianconfig
		if _, _, report, err := renderConfig(kind, []byte(source), stubVars()); err != nil {
			return nil, huma.Error422UnprocessableEntity("config validation failed: "+report, err)
		}
		return nil, nil
	}
}

// ensureSchematicPreCache ensures the boot-asset cache target for a freshly
// built schematic and kicks an async reconcile so the assets fetch eagerly
// (design D4). Shared by the create and update handlers.
func ensureSchematicPreCache(deps APIDeps, id string) error {
	if err := cache.EnsureSchematicTarget(deps.Store, id); err != nil {
		return huma.Error500InternalServerError("ensure schematic cache target", err)
	}
	deps.Trigger()
	return nil
}

// appendActiveRevision base64-encodes source, records its sha256, appends the
// revision (with its Factory-derived schematic ID, when kind='schematic'), and
// advances the config's active pointer.
func appendActiveRevision(store *db.Store, configID int64, source string, derivedSchematicID *string) error {
	sum := sha256.Sum256([]byte(source))
	revID, _, err := store.AddConfigRevision(configID, base64.StdEncoding.EncodeToString([]byte(source)), hex.EncodeToString(sum[:]), derivedSchematicID)
	if err != nil {
		return err
	}
	return store.SetActiveRevision(configID, revID)
}

func configDTOResp(store *db.Store, id int64) (*struct{ Body ConfigDTO }, error) {
	c, err := store.GetConfig(id)
	if err != nil {
		return nil, huma.Error500InternalServerError("read back config", err)
	}
	n, _ := store.CountRevisions(id)
	dto := ConfigDTO{ID: c.ID, Name: c.Name, Kind: c.Kind, RevisionCount: n, UpdatedAt: c.UpdatedAt}
	if rev, err := store.GetActiveRevision(id); err == nil {
		dto.ActiveRevision = rev.Revision
		if rev.DerivedSchematicID != nil {
			dto.DerivedSchematicID = *rev.DerivedSchematicID
		}
	}
	return &struct{ Body ConfigDTO }{Body: dto}, nil
}

// stubVars are placeholder render vars for validation/preview without a host.
func stubVars() TemplateVars {
	return TemplateVars{
		Hostname: "preview-host", MAC: "00:00:00:00:00:00", IP: "0.0.0.0",
		ServerIP: "0.0.0.0:80", ServerHTTPPort: "80",
	}
}

// previewVars populates render vars from a real host for preview, DISPATCHED
// BY THE CONFIG'S KIND (not the host's OS) so preview uses the same vars the
// boot path would use for that kind: butane/preseed get the ignition-family
// vars (host:port .ServerIP); machineconfig gets the machineconfig-family vars
// (host-only .ServerIP + .ServerHTTPPort, .Schematic, .TalosVersion, .Roles).
func previewVars(store *db.Store, host *hardware.Host, kind string) TemplateVars {
	switch kind {
	case "machineconfig":
		return machineConfigPreviewVars(store, host)
	default: // "butane", "preseed"
		return ignitionVars(store, host)
	}
}
