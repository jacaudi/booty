package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/db"
)

// CacheEntryDTO is the wire shape of a cached version's inventory detail.
type CacheEntryDTO struct {
	ID        int64             `json:"id"`
	OS        string            `json:"os"`
	Arch      string            `json:"arch"`
	Params    map[string]string `json:"params"`
	Version   string            `json:"version"`
	Size      int64             `json:"size"`
	State     string            `json:"state"`
	Pinned    bool              `json:"pinned"`
	InWindow  bool              `json:"inWindow"`
	FetchedAt string            `json:"fetchedAt"`
}

func cacheState(inWindow, pinned bool) string {
	base := "archived"
	if inWindow {
		base = "in-cycle"
	}
	if pinned {
		return base + "-pinned"
	}
	return base
}

func toCacheDTO(r db.CacheEntryRow) CacheEntryDTO {
	params, _ := cache.DecodeParams(r.Params)
	return CacheEntryDTO{
		ID: r.ID, OS: r.OS, Arch: r.Arch, Params: params, Version: r.Version,
		Size: r.Size, State: cacheState(r.InWindow, r.Pinned), Pinned: r.Pinned,
		InWindow: r.InWindow, FetchedAt: r.FetchedAt,
	}
}

type listCacheOutput struct {
	Body struct {
		Entries []CacheEntryDTO `json:"entries"`
	}
}

// registerCache mounts /cache on the /api/v1 group. GET/pin/unpin/scan are open
// during the trust window; DELETE is wired but returns 403 until authentication lands (P10).
func registerCache(api huma.API, deps APIDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "list-cache", Method: http.MethodGet, Path: "/cache",
		Summary: "List cache inventory", Tags: []string{"cache"},
	}, func(ctx context.Context, in *struct {
		OS     string `query:"os"`
		Arch   string `query:"arch"`
		State  string `query:"state"`
		Pinned string `query:"pinned"`
	}) (*listCacheOutput, error) {
		f := db.CacheFilter{OS: in.OS, Arch: in.Arch}
		if in.Pinned != "" {
			b, err := strconv.ParseBool(in.Pinned)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity("pinned must be true or false")
			}
			f.Pinned = &b
		}
		if in.State == "in-cycle" || in.State == "archived" {
			iw := in.State == "in-cycle"
			f.InWindow = &iw
		}
		rows, err := deps.Store.ListCacheEntries(f)
		if err != nil {
			return nil, huma.Error500InternalServerError("list cache", err)
		}
		out := &listCacheOutput{}
		for _, r := range rows {
			out.Body.Entries = append(out.Body.Entries, toCacheDTO(r))
		}
		return out, nil
	})

	setPinned := func(id string, pinned bool) (*struct{ Body CacheEntryDTO }, error) {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("id must be an integer")
		}
		if _, err := deps.Store.GetCacheEntry(n); errors.Is(err, db.ErrNotFound) {
			return nil, huma.Error404NotFound("cache entry not found")
		}
		if err := deps.Store.SetCachePinned(n, pinned); err != nil {
			return nil, huma.Error500InternalServerError("set pinned", err)
		}
		r, err := deps.Store.GetCacheEntry(n)
		if err != nil {
			return nil, huma.Error500InternalServerError("reload entry", err)
		}
		return &struct{ Body CacheEntryDTO }{Body: toCacheDTO(r)}, nil
	}

	huma.Register(api, huma.Operation{
		OperationID: "pin-cache", Method: http.MethodPost, Path: "/cache/{id}/pin",
		Summary: "Pin a cached version", Tags: []string{"cache"},
	}, func(ctx context.Context, in *struct{ ID string `path:"id"` }) (*struct{ Body CacheEntryDTO }, error) {
		return setPinned(in.ID, true)
	})

	huma.Register(api, huma.Operation{
		OperationID: "unpin-cache", Method: http.MethodPost, Path: "/cache/{id}/unpin",
		Summary: "Unpin a cached version", Tags: []string{"cache"},
	}, func(ctx context.Context, in *struct{ ID string `path:"id"` }) (*struct{ Body CacheEntryDTO }, error) {
		return setPinned(in.ID, false)
	})

	huma.Register(api, huma.Operation{
		OperationID: "scan-cache", Method: http.MethodPost, Path: "/cache/scan",
		Summary: "Reconcile cache inventory to disk", Tags: []string{"cache"},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body cache.ScanResult }, error) {
		res, err := cache.Scan(deps.Store)
		if err != nil {
			return nil, huma.Error500InternalServerError("scan", err)
		}
		return &struct{ Body cache.ScanResult }{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "delete-cache", Method: http.MethodDelete, Path: "/cache/{id}",
		Summary: "Delete a cached version (disabled until auth)", Tags: []string{"cache"},
	}, func(ctx context.Context, _ *struct{ ID string `path:"id"` }) (*struct{}, error) {
		return nil, huma.Error403Forbidden("destructive endpoints are disabled until authentication lands (P10)")
	})
}
