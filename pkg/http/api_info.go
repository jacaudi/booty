package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// InfoDTO is the gated replacement for the retired base-mux GET /info. The
// shape is byte-compatible with the old hand-marshaled JSON so the UI's
// AboutView needs only a path change, not a parse change.
type InfoDTO struct {
	Flatcar VersionDTO `json:"flatcar"`
	CoreOS  VersionDTO `json:"coreos"`
	Booty   BootyInfo  `json:"booty"`
}

// VersionDTO and BootyInfo are EXPORTED because huma derives OpenAPI schema
// names from the Go type name; unexported nested types produce awkward
// generated names in the published document.
type VersionDTO struct {
	Version string `json:"version"`
}

type BootyInfo struct {
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
}

// registerInfo mounts GET /info on the /api/v1 group. It is gated like the
// rest of the surface -- a pre-login caller is not left with nothing, since
// the open /version.txt and /version.json endpoints, and /healthz, already
// expose versions.
func registerInfo(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-info", Method: http.MethodGet, Path: "/info",
		Summary: "Cached OS versions and booty build info", Tags: []string{"info"},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body InfoDTO }, error) {
		return &struct{ Body InfoDTO }{Body: InfoDTO{
			Flatcar: VersionDTO{Version: cache.NewestCached("flatcar", viper.GetString(config.FlatcarArchitecture), nil)},
			CoreOS:  VersionDTO{Version: cache.NewestCached("coreos", viper.GetString(config.CoreOSArchitecture), nil)},
			Booty: BootyInfo{
				Version:   viper.GetString("version"),
				Timestamp: viper.GetString("timestamp"),
			},
		}}, nil
	})
}
