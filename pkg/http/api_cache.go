package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
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
	Verified  *bool             `json:"verified,omitempty"`
	VerifyErr string            `json:"verifyErr,omitempty"`
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
		Verified: r.Verified, VerifyErr: r.VerifyErr,
	}
}

type listCacheOutput struct {
	Body struct {
		Entries []CacheEntryDTO `json:"entries"`
	}
}

// registerCache mounts /cache on the /api/v1 group. Every operation here
// requires a credential like the rest of /api/v1; DELETE is wired but not
// implemented yet, and returns 403 regardless of credential.
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
		if _, err := deps.Store.GetCacheEntry(n); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, huma.Error404NotFound("cache entry not found")
			}
			return nil, huma.Error500InternalServerError("get entry", err)
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
		OperationID: "reverify-cache", Method: http.MethodPost, Path: "/cache/{id}/reverify",
		Summary: "Recompute a cached version's verification verdict from disk", Tags: []string{"cache"},
	}, func(ctx context.Context, in *struct{ ID string `path:"id"` }) (*struct{ Body CacheEntryDTO }, error) {
		n, err := strconv.ParseInt(in.ID, 10, 64)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("id must be an integer")
		}
		row, err := deps.Store.GetCacheEntry(n)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, huma.Error404NotFound("cache entry not found")
			}
			return nil, huma.Error500InternalServerError("get entry", err)
		}
		// An explicit operator ask always verifies (ignores --signaturePolicy off).
		// Reset BOTH discovery memos so reverify compares against fresh upstream
		// material: the FCOS streams doc (D17) and the netboot.xyz endpoints
		// manifest. The netboot.xyz reset is required now that VerifyVersion no
		// longer short-circuits the tool family — it calls Artifacts, the only
		// reader of that memo, so a stale manifest would mean comparing a cached
		// file against a different release's digests. This is NOT the #73
		// regression: that was a per-TARGET reset inside reconcileTarget, not a
		// rare manual endpoint. The netboot.xyz reset does carry a mid-pass cost
		// — see ResetNetbootxyzCache's doc comment, which states it rather than
		// repeating it here.
		ostype.ResetStreamsCache()
		ostype.ResetNetbootxyzCache()
		verified, verifyErr, verr := cache.VerifyVersion(ctx, deps.Store, n)
		// Recording is CONDITIONAL. One hazard from the original unconditional
		// write survives, deliberately; the other two are closed here.
		//
		// CLOSED (#83): a row already REJECTED for a checksum mismatch has had its
		// directory removed (D4a), so reverify finds nothing to examine. Recording
		// that as a verdict overwrote the operator-meaningful reason with a claim
		// about a different fault. It is now ErrVersionUnevaluable, and no verdict
		// is recorded. Its in-flight twin — a reverify during a post-window
		// re-download — is closed by the same sentinel.
		//
		// SURVIVES: a nil verdict from a superseded tool tag, or from a version
		// whose artifacts declare no material at all, still writes verified=NULL
		// AND an empty verify_err, releasing db.VerifyRejectedWithin's guard early
		// and destroying any recorded reason. Defensible — an operator explicitly
		// asked — and SetCacheVerified's nil-clearing capability exists for the
		// real case its own doc comment names.
		switch {
		case errors.Is(verr, cache.ErrVersionUnevaluable):
			// NOT an HTTP error: the Cache view runs reverify as a BULK action, so
			// a 4xx here would report mass failure for a correct no-op.
			slog.Warn("cache: reverify found no material to examine; verdict not recorded",
				"id", n, "os", row.OS, "version", row.Version, "err", verr)
			// D10: withdraw a standing AFFIRMATION. "verified=true" over material
			// that is gone is a claim about bytes that are not there, and an
			// ARCHIVED version never re-enters the reconcile loop to correct it
			// (the in_window=1 clause in pkg/db/versions.go). A recorded FAILURE is
			// left alone — preserving it is this issue's whole point.
			//
			// WHICH ROWS TO TOUCH IS DECIDED IN SQL, NOT FROM `row`. row was read
			// before VerifyVersion, which fetches and re-hashes multi-GB material,
			// and the reconciler goroutine (started alongside this server in
			// cmd/main.go) writes this same cache_entries row via
			// UpsertCacheEntryArchived. VerifyVersion itself never writes the DB,
			// but the process around it does, so a rejection can land between the
			// read and this line; branching on row.Verified would erase it. It is
			// WithdrawCacheAffirmation's verified=1 predicate — not the read — that
			// makes the withdrawal safe, and therefore also what keeps it from
			// releasing db.VerifyRejectedWithin's retry guard, whose predicate needs
			// the verified=0 the withdrawal now leaves alone.
			if err := deps.Store.WithdrawCacheAffirmation(row.TargetVersionID); err != nil {
				return nil, huma.Error500InternalServerError("withdraw verdict", err)
			}
		case verr != nil:
			return nil, huma.Error500InternalServerError("verify", verr)
		default:
			if err := deps.Store.SetCacheVerified(row.TargetVersionID, verified, verifyErr); err != nil {
				return nil, huma.Error500InternalServerError("record verdict", err)
			}
		}
		r, err := deps.Store.GetCacheEntry(n)
		if err != nil {
			return nil, huma.Error500InternalServerError("reload entry", err)
		}
		return &struct{ Body CacheEntryDTO }{Body: toCacheDTO(r)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "delete-cache", Method: http.MethodDelete, Path: "/cache/{id}",
		Summary: "Delete a cached version (not implemented)", Tags: []string{"cache"},
	}, func(ctx context.Context, _ *struct{ ID string `path:"id"` }) (*struct{}, error) {
		return nil, huma.Error403Forbidden(msgDestructiveNotImplemented)
	})
}
