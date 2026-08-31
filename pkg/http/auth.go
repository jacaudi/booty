package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jeefy/booty/pkg/auth"
)

// TokenHeader is the credential header external and programmatic clients send.
// The browser UI uses the session cookie instead.
const TokenHeader = "X-Booty-Token"

// authMiddleware gates every operation on the /api/v1 group. It accepts EITHER
// a matching X-Booty-Token header or a valid session cookie, and nothing else.
//
// It is installed once on the group (via grp.UseMiddleware in
// registerOperations, pkg/http/api.go) rather than per-operation, so a new
// registrar does not need its own copy of this check -- it only needs to be
// called from registerOperations AFTER UseMiddleware runs, which is a
// positional requirement, not an automatic one: huma.Register snapshots the
// group's middleware chain at each call's own registration time (see the
// comment on grp.UseMiddleware in api.go). What actually catches a registrar
// that escapes the gate -- whether by being called too early, or not being
// called from registerOperations at all -- is
// TestEveryRegisteredOperationIsGated (auth_test.go), which walks every
// operation in the built OpenAPI document rather than trusting placement.
//
// disabled is the --noAuth escape hatch: a pass-through, logged loudly at
// startup by the caller.
func authMiddleware(api huma.API, store *auth.Store, disabled bool) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		// A nil store means no Auth was wired. Only tests construct APIDeps
		// that way: StartHTTP (pkg/http/http.go) refuses to start with a nil
		// store unless --noAuth is set, so this pass-through is a test-only
		// affordance, not a production guarantee.
		if disabled || store == nil || authorized(store, ctx) {
			next(ctx)
			return
		}
		// huma renders this as application/problem+json, which is what
		// every other error response on the /api/v1 surface already sends,
		// so no hand-written body is needed to keep this response consistent
		// with the rest of the API.
		_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "missing or invalid credential")
	}
}

func authorized(store *auth.Store, ctx huma.Context) bool {
	// Fail closed when no usable token is installed. Store.Key already
	// returns ok=false for this identical state (both read the same
	// atomic credential pointer), and newCredential's ErrEmptyToken makes an
	// installed-but-empty token unreachable to begin with -- so this check is
	// defence in depth, not the sole thing standing between a token-less
	// store and a forged cookie. It earns its place by making the fail-closed
	// intent explicit at the top of the function, for a reader who has not
	// traced Key()'s nil branch.
	if !store.HasToken() {
		return false
	}
	if presented := ctx.Header(TokenHeader); presented != "" && store.Verify(presented) {
		return true
	}
	c, err := huma.ReadCookie(ctx, auth.CookieName)
	if err != nil || c == nil {
		return false
	}
	key, ok := store.Key()
	if !ok {
		return false
	}
	return auth.VerifyCookieValue(key, c.Value, time.Now())
}

// handleLogin is the open base-mux endpoint the UI posts the token to once. It
// accepts the token in a JSON body or in the X-Booty-Token header, and on a
// constant-time match sets the session cookie.
func handleLogin(store *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		// StartHTTP permits a nil store when --noAuth is set, and /login is
		// still mounted in that mode, so refuse rather than dereference.
		if store == nil || !store.HasToken() {
			writeJSONError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		presented := r.Header.Get(TokenHeader)
		if presented == "" {
			var body struct {
				Token string `json:"token"`
			}
			// A malformed body is simply a missing credential; do not echo the
			// parse error, which would tell an unauthenticated caller about
			// the expected shape.
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)
			presented = body.Token
		}
		if presented == "" || !store.Verify(presented) {
			writeJSONError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		key, ok := store.Key()
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     auth.CookieName,
			Value:    auth.IssueCookieValue(key, time.Now().Add(auth.SessionLifetime)),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			// Secure only when the BROWSER's connection is TLS. booty is
			// usually plain HTTP on a LAN, where a Secure cookie would never
			// be sent at all -- the cleartext consequence (the cookie and the
			// X-Booty-Token header both travel the LAN in plaintext without
			// TLS) is documented for operators in docs/CONFIGURATION.md's
			// Authentication section. But the standard hardening path is
			// a TLS-terminating proxy forwarding plain HTTP to booty, so
			// r.TLS alone would wrongly issue a non-Secure cookie for an HTTPS
			// session. Honour X-Forwarded-Proto too.
			Secure: requestIsTLS(r),
			MaxAge: int(auth.SessionLifetime / time.Second),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// requestIsTLS reports whether the BROWSER reached booty over HTTPS, directly
// or through a TLS-terminating reverse proxy.
//
// X-Forwarded-Proto is client-spoofable in principle. Trusting it here is safe
// because the only effect is to make the cookie MORE restrictive (Secure), and
// the design's stated threat model already accepts plain-HTTP LAN traffic.
func requestIsTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// handleLogout clears the session cookie. There is no server-side state to
// revoke: the cookie is self-verifying, so logout is purely client-side.
//
// The method check alone is not enough once this is mounted on the base mux:
// SameSite governs whether cookies are SENT on a request, not whether a
// Set-Cookie on the RESPONSE is honored, so a plain method guard still lets a
// cross-site AUTO-SUBMITTING <form method="POST" action="http://booty/logout">
// clear a visitor's session -- that is a simple cross-origin POST, which
// needs no CORS preflight. Requiring the caller already hold a VALID session
// cookie closes this completely: under SameSite=Strict a cross-site request
// can never carry the victim's cookie for the forged form to ride along
// with, so there is no credential for an attacker to present.
func handleLogout(store *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if store == nil || !hasValidSessionCookie(store, r) {
			writeJSONError(w, http.StatusUnauthorized, "missing or invalid session")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     auth.CookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   -1,
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// hasValidSessionCookie reports whether r carries a session cookie that
// verifies against store's live key. This is the plain net/http counterpart
// of authorized's cookie branch (authorized also accepts the X-Booty-Token
// header; logout deliberately does not, since the whole point is proving the
// caller already holds a session a cross-site request could not have sent).
func hasValidSessionCookie(store *auth.Store, r *http.Request) bool {
	if !store.HasToken() {
		return false
	}
	c, err := r.Cookie(auth.CookieName)
	if err != nil || c == nil {
		return false
	}
	key, ok := store.Key()
	if !ok {
		return false
	}
	return auth.VerifyCookieValue(key, c.Value, time.Now())
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "detail": msg})
}
