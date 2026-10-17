package http

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
)

// openRoute is one allowlisted route: a mux pattern that answers without a
// credential.
type openRoute struct {
	// probe is a concrete request path that lands on the pattern.
	probe string
	// mutates is true when the handler changes server-side state. Exactly two
	// open routes may set it: POST /node/report and GET /booty.ipxe.
	mutates bool
	// why is the reason the route is open.
	why string
}

// openRouteTable is the complete set of routes on the served mux that answer
// without a credential, keyed by the pattern the mux matched (see routeOf). It
// was derived by probing the built handler (TestOnlyAllowlistedRoutesAnswerWithoutACredential
// prints any difference), not by reading registration source: the huma
// entries at the bottom are registered by huma/humago itself and no booty
// source names them.
//
// Read-only routes are registered for GET (which also matches HEAD), so any
// other method falls to the "/" catch-all instead of reaching a read handler
// (#149). A pattern with no method answers every method with its handler,
// which is why "mutates" is declared per route rather than inferred.
func openRouteTable() map[string]openRoute {
	const huma = "registered by huma at adapter level, outside the gated group: "
	return map[string]openRoute{
		"/":                  {probe: "/", why: "catch-all: every unmatched path and method, including a wrong method on an /api/v1 path, is a 302 to /ui/"},
		"GET /ignition.json": {probe: "/ignition.json", why: "boot path: Ignition config for an approved booting host carrying a valid boot ticket, the reboot config to any other caller; read-only"},
		"GET /machineconfig": {probe: "/machineconfig", why: "boot path: Talos machine config for an approved booting host carrying a valid boot ticket, 404 to any other caller; read-only"},
		"GET /preseed":       {probe: "/preseed", why: "boot path: Debian preseed for an approved booting host carrying a valid boot ticket, 404 to any other caller; read-only"},
		"GET /booty.ipxe":    {probe: "/booty.ipxe", mutates: true, why: "boot path: the rendered iPXE script (holding, assigned or menu); also records an unknown MAC in discovered_hosts (new rows limited 8 per source IP per 10 min, capped at 1024 with no eviction, refreshes debounced to one write per 20 s per MAC; a row grants nothing)"},
		"GET /menu/":         {probe: "/menu/x", why: "boot path: iPXE boot-menu selection script; read-only"},
		"/node/report": {
			probe:   "/node/report",
			mutates: true,
			why:     "POST records a node health report; authenticated by a per-host HMAC token and rate limited rather than by the API gate",
		},
		"GET /version.txt":              {probe: "/version.txt", why: "newest cached Flatcar/CoreOS versions; read-only"},
		"GET /version.json":             {probe: "/version.json", why: "newest cached Flatcar/CoreOS versions; read-only"},
		"GET /healthz":                  {probe: "/healthz", why: "liveness probe; read-only"},
		"/login":                        {probe: "/login", why: "POST trades the API token for a session cookie; it authenticates the caller itself and keeps no server-side state"},
		"/logout":                       {probe: "/logout", why: "POST clears the session cookie and requires a valid one; keeps no server-side state"},
		"GET /data/":                    {probe: "/data/x", why: "boot path: artifacts under dataDir, served only from an explicit allowlist; read-only"},
		"GET /ui/":                      {probe: "/ui/x", why: "the embedded web UI's static files; read-only"},
		bootPathPrefix + bootRouteLabel: {probe: bootPathPrefix + "ipxe.efi", why: "boot path: embedded iPXE binaries, GET and HEAD only; intercepted by withBootRoute ahead of the mux"},

		"GET /api/v1/docs":             {probe: "/api/v1/docs", why: huma + "API documentation page"},
		"GET /api/v1/openapi.json":     {probe: "/api/v1/openapi.json", why: huma + "OpenAPI document"},
		"GET /api/v1/openapi.yaml":     {probe: "/api/v1/openapi.yaml", why: huma + "OpenAPI document"},
		"GET /api/v1/openapi-3.0.json": {probe: "/api/v1/openapi-3.0.json", why: huma + "OpenAPI 3.0 downgrade of the document"},
		"GET /api/v1/openapi-3.0.yaml": {probe: "/api/v1/openapi-3.0.yaml", why: huma + "OpenAPI 3.0 downgrade of the document"},
		"GET /schemas/{schema}":        {probe: "/schemas/Host.json", why: huma + "JSON schemas the document links to"},
	}
}

// bootRouteLabel names withBootRoute's interception in the route key, because
// it runs ahead of the mux, which therefore never records a pattern for it.
const bootRouteLabel = " (withBootRoute)"

// routeOf returns the key a served request is filed under: the pattern the
// mux matched, which ServeMux records in r.Pattern, except for /boot/, which
// withBootRoute serves before the mux is consulted.
func routeOf(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, bootPathPrefix) {
		return bootPathPrefix + bootRouteLabel
	}
	if r.Pattern == "" {
		// Answered by a wrapper outside the mux, not by any mux route.
		return "outside the mux: " + r.Method + " " + r.URL.Path
	}
	return r.Pattern
}

// registeredPaths returns the path of every literal pattern this package's
// non-test sources hand to a Handle or HandleFunc call. It exists so a route
// added to the base mux under a path no list here names is still probed: a
// fixed path list can never find a route it does not know about, and the "/"
// catch-all would otherwise hide it. A pattern that is not a plain literal
// fails the test instead of being skipped.
func registeredPaths(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var paths []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: %s pattern is not a string literal; extend registeredPaths", fset.Position(call.Pos()), sel.Sel.Name)
				return true
			}
			pattern, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Errorf("%s: %v", fset.Position(lit.Pos()), err)
				return true
			}
			if _, path, ok := strings.Cut(pattern, " "); ok {
				pattern = path
			}
			if strings.Contains(pattern, "{") {
				t.Errorf("%s: wildcard pattern %q; extend registeredPaths", fset.Position(lit.Pos()), pattern)
				return true
			}
			paths = append(paths, pattern)
			return true
		})
	}
	return paths
}

// gateFired reports whether rr is the /api/v1 gate's rejection, as opposed to
// a 401 a handler produced itself (/login with a bad token, /logout without a
// session): those are open routes that merely refuse the caller.
func gateFired(rr *httptest.ResponseRecorder) bool {
	return rr.Code == http.StatusUnauthorized && strings.Contains(rr.Body.String(), "missing or invalid credential")
}

// TestOnlyAllowlistedRoutesAnswerWithoutACredential pins the whole
// unauthenticated surface of the handler StartHTTP serves, closing the gap
// TestEveryRegisteredOperationIsGated leaves: that test walks huma's
// operations and never sees the base mux, so a route registered next to /login
// would pass it while sitting outside the gate.
//
// It builds the served handler with auth ON (a real token store), sends every
// candidate path every method with no credential, and files each request that
// the gate did not reject under the mux pattern that served it. That set must
// equal openRouteTable exactly: an extra route fails (it is open and nobody
// reviewed it), and a missing one fails (the table is stale, or a route was
// dropped). Exactly two allowlisted routes may change state.
//
// The handler probed is the one StartHTTP serves (srv.Handler), so a wrapper
// StartHTTP gains later is probed too, and the /api/v1 operations are read
// from the OpenAPI document that handler serves.
func TestOnlyAllowlistedRoutesAnswerWithoutACredential(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.HttpPort, 0)

	store, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// NodeReports is set so POST /node/report reaches its real receiver and
	// not the feature-off 404.
	deps := APIDeps{
		Store:       store,
		Auth:        tokenStore(t, "tok"),
		BootFiles:   fstest.MapFS{"ipxe.efi": {Data: []byte("x")}},
		NodeReports: &NodeReports{Key: nodeReportKey()},
		Tickets:     testTicketer(t),
	}
	srv, err := StartHTTP(deps, make(chan error, 1))
	if err != nil {
		t.Fatalf("StartHTTP: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	handler := srv.Handler

	table := openRouteTable()

	// Candidate paths: whatever the base mux's source registers, every
	// allowlisted route's own probe path, and every /api/v1 operation (the
	// gate must hold for each, under every method).
	candidates := map[string]bool{}
	for _, p := range registeredPaths(t) {
		candidates[p] = true
	}
	for _, r := range table {
		candidates[r.probe] = true
	}
	param := regexp.MustCompile(`\{[^}]*\}`)
	for p := range servedOpenAPIPaths(t, handler) {
		candidates[param.ReplaceAllString(p, "1")] = true
	}

	methods := []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace,
	}
	open := map[string]bool{}
	for _, path := range slices.Sorted(maps.Keys(candidates)) {
		for _, method := range methods {
			req := httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if gateFired(rr) {
				continue
			}
			pattern := routeOf(req)
			open[pattern] = true
			if pattern == "/" && (rr.Code != http.StatusFound || rr.Header().Get("Location") != "/ui/") {
				t.Errorf("%s %s fell to the catch-all and answered %d Location %q, want a 302 to /ui/",
					method, path, rr.Code, rr.Header().Get("Location"))
			}
		}
	}

	for _, pattern := range slices.Sorted(maps.Keys(open)) {
		if _, ok := table[pattern]; !ok {
			t.Errorf("route %q answers without a credential and is not allowlisted; gate it under /api/v1, or add it to openRouteTable with the reason it is open", pattern)
		}
	}
	var mutating []string
	for _, pattern := range slices.Sorted(maps.Keys(table)) {
		route := table[pattern]
		if !open[pattern] {
			t.Errorf("allowlisted route %q no longer answers without a credential; drop it from openRouteTable or restore the route", pattern)
		}
		if route.mutates {
			mutating = append(mutating, pattern)
		}
		if verb, _, _ := strings.Cut(pattern, " "); !route.mutates &&
			slices.Contains([]string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}, verb) {
			t.Errorf("allowlisted route %q is registered for a state-changing method but not marked mutates", pattern)
		}
	}
	if want := []string{"/node/report", "GET /booty.ipxe"}; !slices.Equal(mutating, want) {
		t.Errorf("open routes that change state = %q, want %q: every other state-changing endpoint belongs under the gated /api/v1 group", mutating, want)
	}
}

// servedOpenAPIPaths returns the path keys of the OpenAPI document h serves at
// /api/v1/openapi.json (open, so no credential is needed).
func servedOpenAPIPaths(t *testing.T, h http.Handler) map[string]json.RawMessage {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/openapi.json", http.NoBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/openapi.json = %d", rr.Code)
	}
	var doc struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode OpenAPI document: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("the served OpenAPI document lists no paths")
	}
	return doc.Paths
}
