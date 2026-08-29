package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/jeefy/booty/pkg/auth"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

// gatedHarness builds the /api/v1 group with the authorizer installed exactly
// as registerOperations does, and returns the raw handler so a test can attach
// arbitrary headers and cookies. newTestAPI (api_test.go:11) cannot be reused
// here: it returns only the TestAPI, whose Do() cannot carry a cookie.
func gatedHarness(t *testing.T, store *auth.Store, noAuth bool) http.Handler {
	t.Helper()
	handler, api := humatest.New(t)
	registerOperations(api, APIDeps{Auth: store, NoAuth: noAuth})
	return handler
}

// getOS issues GET /api/v1/os through handler after mut has decorated the
// request, and returns the status. /os is used throughout because it is the
// cheapest gated operation: it touches no store.
func getOS(handler http.Handler, mut func(*http.Request)) int {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/os", nil)
	if mut != nil {
		mut(req)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr.Code
}

func tokenStore(t *testing.T, tok string) *auth.Store {
	t.Helper()
	s := auth.NewStore(filepath.Join(t.TempDir(), auth.TokenFileName))
	if err := s.SetExplicit(tok); err != nil {
		t.Fatalf("SetExplicit(%q): %v", tok, err)
	}
	return s
}

func liveCookie(t *testing.T, store *auth.Store, expiry time.Time) string {
	t.Helper()
	key, ok := store.Key()
	if !ok {
		t.Fatal("store has no key")
	}
	return auth.IssueCookieValue(key, expiry)
}

func TestMiddlewareCredentialMatrix(t *testing.T) {
	const tok = "the-live-token"

	cases := []struct {
		name   string
		mutate func(r *http.Request)
		noAuth bool
		want   int
	}{
		{name: "no credential", mutate: nil, want: 401},
		{name: "wrong header token", mutate: func(r *http.Request) {
			r.Header.Set(TokenHeader, "nope")
		}, want: 401},
		{name: "correct header token", mutate: func(r *http.Request) {
			r.Header.Set(TokenHeader, tok)
		}, want: 200},
		{name: "empty header token", mutate: func(r *http.Request) {
			r.Header.Set(TokenHeader, "")
		}, want: 401},
		{name: "noAuth passes through with no credential", mutate: nil, noAuth: true, want: 200},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := gatedHarness(t, tokenStore(t, tok), tc.noAuth)
			if got := getOS(handler, tc.mutate); got != tc.want {
				t.Fatalf("GET /api/v1/os = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMiddlewareAcceptsAValidCookieAndRejectsABadOne(t *testing.T) {
	store := tokenStore(t, "cookie-token")
	handler := gatedHarness(t, store, false)

	good := liveCookie(t, store, time.Now().Add(auth.SessionLifetime))
	expired := liveCookie(t, store, time.Now().Add(-time.Minute))
	foreign := liveCookie(t, tokenStore(t, "some-other-token"), time.Now().Add(auth.SessionLifetime))

	cases := map[string]struct {
		value string
		want  int
	}{
		"valid":    {good, 200},
		"expired":  {expired, 401},
		"tampered": {good + "x", 401},
		"foreign":  {foreign, 401},
		"empty":    {"", 401},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := getOS(handler, func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tc.value})
			})
			if got != tc.want {
				t.Fatalf("cookie %q = %d, want %d", name, got, tc.want)
			}
		})
	}
}

// TestAStoreWithNoTokenRefusesAForgedCookie closes an AUTHENTICATION BYPASS.
// The cookie key is HMAC-SHA256(token, label); if a store with no usable token
// still exposed a key, that key would be a constant computable from this
// repo's source, and anyone could mint a valid session for the whole API.
//
// A store reaches this state through reachable production paths (a 0-byte
// api-token file, a crash mid-Rotate); pkg/auth refuses to install one, and
// this asserts the http layer refuses it too rather than trusting that.
func TestAStoreWithNoTokenRefusesAForgedCookie(t *testing.T) {
	empty := auth.NewStore(filepath.Join(t.TempDir(), auth.TokenFileName))
	if empty.HasToken() {
		t.Fatal("precondition: a fresh store must have no token")
	}
	// Forge a cookie under the key an empty token WOULD derive.
	forged := auth.IssueCookieValue([32]byte{}, time.Now().Add(auth.SessionLifetime))

	handler := gatedHarness(t, empty, false)
	got := getOS(handler, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: forged})
	})
	if got != 401 {
		t.Fatalf("forged cookie against a token-less store = %d, want 401", got)
	}
	if got := getOS(handler, nil); got != 401 {
		t.Fatalf("no credential against a token-less store = %d, want 401", got)
	}
}

// TestRotationInvalidatesAnIssuedCookie is the end-to-end D3 guarantee through
// the middleware, not just the codec.
func TestRotationInvalidatesAnIssuedCookie(t *testing.T) {
	store := auth.NewStore(filepath.Join(t.TempDir(), auth.TokenFileName))
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	handler := gatedHarness(t, store, false)
	cookie := liveCookie(t, store, time.Now().Add(auth.SessionLifetime))
	attach := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie}) }

	if got := getOS(handler, attach); got != 200 {
		t.Fatalf("precondition: the cookie must work before rotation, got %d", got)
	}
	if _, err := store.Rotate(); err != nil {
		t.Fatal(err)
	}
	if got := getOS(handler, attach); got != 401 {
		t.Fatalf("after rotation the old cookie = %d, want 401", got)
	}
}

// TestEveryRegisteredOperationIsGated enumerates the group's operations from
// the built OpenAPI document rather than a hand-written list, so a registrar
// added later cannot silently escape the gate. A hardcoded path list would
// not catch that -- which is the whole failure mode a group-level gate exists
// to prevent, so the test must not reintroduce it.
func TestEveryRegisteredOperationIsGated(t *testing.T) {
	handler, api := humatest.New(t)
	registerOperations(api, APIDeps{Auth: tokenStore(t, "tok")})

	checked := 0
	for path, item := range api.OpenAPI().Paths {
		if !strings.HasPrefix(path, "/api/v1/") {
			continue
		}
		// huma.PathItem has NO Operations() accessor -- it exposes one field
		// per method (openapi.go:1037-1090). Enumerate the five booty uses.
		for method, op := range map[string]*huma.Operation{
			http.MethodGet: item.Get, http.MethodPut: item.Put, http.MethodPost: item.Post,
			http.MethodDelete: item.Delete, http.MethodPatch: item.Patch,
		} {
			if op == nil {
				continue
			}
			// Substitute a placeholder for every path parameter so the route
			// resolves; the gate must fire before any handler validates it.
			concrete := strings.NewReplacer("{id}", "1", "{mac}", "aa:bb:cc:dd:ee:ff", "{v}", "v1.0.0").Replace(path)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(method, concrete, nil))
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s uncredentialed = %d, want 401", method, concrete, rr.Code)
			}
			checked++
		}
	}
	// 45 pre-existing operations today. Task 6 adds create-host and get-info;
	// raise this guard to 47 in that task.
	if checked < 45 {
		t.Fatalf("only %d operations enumerated, want >= 45; the walk matched too little", checked)
	}
}

// TestOpenAdapterLevelRoutesStayOpen must use a real ServeMux + humago adapter:
// docs, openapi, and schemas are registered by huma at ADAPTER level, outside
// huma.NewGroup, and that is precisely the property under test. humatest does
// not mount them the same way, so this cannot use gatedHarness.
//
// The -3.0 pair and /schemas/ are included deliberately: they are huma
// defaults that pkg/http/api.go never names, and an earlier draft of the
// open-surface table missed all three by reading api.go instead of probing.
func TestOpenAdapterLevelRoutesStayOpen(t *testing.T) {
	mux := http.NewServeMux()
	RegisterAPI(mux, APIDeps{Auth: tokenStore(t, "tok")})

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/docs", 200},
		{"/api/v1/openapi.json", 200},
		{"/api/v1/openapi.yaml", 200},
		{"/api/v1/openapi-3.0.json", 200},
		{"/api/v1/openapi-3.0.yaml", 200},
		{"/schemas/Host.json", 200},
		{"/api/v1/openapi", 404}, // the bare path does NOT exist (plan section 0 R2)
		{"/api/v1/os", 401},
	} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rr.Code != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.path, rr.Code, tc.want)
		}
	}
}

func TestLoginIssuesACookieForTheRightToken(t *testing.T) {
	const tok = "login-token"
	store := tokenStore(t, tok)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"token":"`+tok+`"}`))
	handleLogin(store)(rr, req)

	if rr.Code != 204 {
		t.Fatalf("POST /login = %d, want 204 (body %s)", rr.Code, rr.Body.String())
	}
	var session *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login must set the session cookie")
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly so XSS cannot read it")
	}
	if session.SameSite != http.SameSiteStrictMode {
		t.Error("session cookie must be SameSite=Strict (this IS the CSRF defence)")
	}
	if session.Path != "/" {
		t.Errorf("session cookie Path = %q, want /", session.Path)
	}
	if session.Secure {
		t.Error("Secure must be off over plain HTTP or the homelab cookie never rides")
	}
	key, ok := store.Key()
	if !ok {
		t.Fatal("store has no key")
	}
	if !auth.VerifyCookieValue(key, session.Value, time.Now()) {
		t.Error("the issued cookie must verify under the live key")
	}
}

// TestLoginSetsSecureBehindATLSProxy covers the normal hardening deployment:
// Traefik/nginx terminates TLS and forwards plain HTTP to booty. Keying Secure
// off r.TLS alone would issue a non-Secure cookie for an HTTPS session.
func TestLoginSetsSecureBehindATLSProxy(t *testing.T) {
	store := tokenStore(t, "proxy-token")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.Header.Set(TokenHeader, "proxy-token")
	req.Header.Set("X-Forwarded-Proto", "https")
	handleLogin(store)(rr, req)

	if rr.Code != 204 {
		t.Fatalf("login = %d, want 204", rr.Code)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName && !c.Secure {
			t.Fatal("X-Forwarded-Proto: https must produce a Secure cookie")
		}
	}
}

func TestLoginAcceptsTheHeaderForm(t *testing.T) {
	store := tokenStore(t, "header-login")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.Header.Set(TokenHeader, "header-login")
	handleLogin(store)(rr, req)

	if rr.Code != 204 {
		t.Fatalf("header login = %d, want 204", rr.Code)
	}
}

func TestLoginRejectsAWrongTokenAndABadMethod(t *testing.T) {
	store := tokenStore(t, "right")

	rr := httptest.NewRecorder()
	handleLogin(store)(rr, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"token":"wrong"}`)))
	if rr.Code != 401 {
		t.Fatalf("wrong token = %d, want 401", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("401 Content-Type = %q, want a JSON type", ct)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			t.Error("a failed login must not set a session cookie")
		}
	}

	rr2 := httptest.NewRecorder()
	handleLogin(store)(rr2, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rr2.Code != 405 {
		t.Fatalf("GET /login = %d, want 405", rr2.Code)
	}
}

// TestLoginWithNoTokenStoreRefuses guards the wiring StartHTTP permits under
// --noAuth: deps.Auth may be nil there, and handleLogin must refuse rather
// than dereference it.
func TestLoginWithNoTokenStoreRefuses(t *testing.T) {
	rr := httptest.NewRecorder()
	handleLogin(nil)(rr, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"token":"anything"}`)))
	if rr.Code != 401 {
		t.Fatalf("login against a nil store = %d, want 401 (and no panic)", rr.Code)
	}
}

func TestLogoutClearsTheCookie(t *testing.T) {
	rr := httptest.NewRecorder()
	handleLogout(rr, httptest.NewRequest(http.MethodPost, "/logout", nil))

	if rr.Code != 204 {
		t.Fatalf("POST /logout = %d, want 204", rr.Code)
	}
	var cleared bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value == "" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout must emit an expiring, empty session cookie")
	}
}

// TestTokenFileIsNotServedOverData is the regression guard for the allowlist
// interaction: <dataDir>/api-token sits outside cache/ and public/, so it is
// unreachable by construction. This asserts that; it adds no rule.
func TestTokenFileIsNotServedOverData(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	if err := os.WriteFile(filepath.Join(dir, auth.TokenFileName), []byte("super-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// dataMux is the production-shaped harness already defined at
	// pkg/http/http_test.go:19 -- use it rather than rebuilding a mux, so this
	// test exercises the same StripPrefix chain production does.
	mux := dataMux(dir)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/data/"+auth.TokenFileName, nil))
	if rr.Code != 404 {
		t.Fatalf("GET /data/%s = %d, want 404", auth.TokenFileName, rr.Code)
	}
	if strings.Contains(rr.Body.String(), "super-secret") {
		t.Fatal("the token file must never appear in a /data/ response body")
	}
}
