package http

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/jeefy/booty/pkg/auth"
	"github.com/jeefy/booty/pkg/db"
)

// APIDeps are the collaborators the /api/v1 handlers need. Trigger kicks an
// async cache reconcile after a target/version mutation (cache.Reconciler.Trigger).
// Auth carries the live API token; NoAuth disables the gate entirely (--noAuth).
type APIDeps struct {
	Store   *db.Store
	Trigger func()
	Auth    *auth.Store
	NoAuth  bool
}

// RegisterAPI mounts the Huma /api/v1 surface on mux (the existing booty
// ServeMux) using the stdlib humago adapter, and returns the huma.API for tests.
// Legacy boot/registration routes on the same mux are untouched.
func RegisterAPI(mux *http.ServeMux, deps APIDeps) huma.API {
	cfg := huma.DefaultConfig("Booty API", "v1")
	cfg.DocsPath = "/api/v1/docs"
	cfg.OpenAPIPath = "/api/v1/openapi"
	api := humago.New(mux, cfg)
	registerOperations(api, deps)
	return api
}

// registerOperations attaches every /api/v1 operation group. Each group lives in
// its own file (catalog, targets, hosts) and is additive — a new group is a new
// registrar call here, siblings untouched (No-Wall).
func registerOperations(api huma.API, deps APIDeps) {
	grp := huma.NewGroup(api, "/api/v1")
	// ONE gate for the whole surface. huma.Register captures the group's
	// middleware chain (Group.Middlewares(), a snapshot of the slice
	// UseMiddleware appends to) at THAT CALL's registration time, not
	// lazily — so this UseMiddleware call must run before every registrar
	// below it in this function, not merely "somewhere in the file". A
	// registrar invoked above this line would register its operations
	// ungated with no compile-time signal. TestEveryRegisteredOperationIsGated
	// (auth_test.go) is what actually enforces the invariant: it walks every
	// operation in the built OpenAPI document and asserts each one 401s
	// uncredentialed, so a registrar moved above this line, or a new one
	// added without being called from here at all, fails that test.
	grp.UseMiddleware(authMiddleware(api, deps.Auth, deps.NoAuth))
	registerCatalog(grp)          // Task 5
	registerTargets(grp, deps)    // Task 6
	registerHosts(grp, deps)      // Task 7
	registerCache(grp, deps)      // P3a
	registerConfigs(grp, deps)    // P4 T8
	registerRoles(grp, deps)      // P4 T9
	registerSchematics(grp, deps) // P5 T8
	registerClusters(grp, deps)   // P6 T13
	registerInfo(grp)             // replaces the retired base-mux GET /info
}
