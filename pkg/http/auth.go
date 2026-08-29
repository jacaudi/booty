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
// The browser UI uses the session cookie instead (D2).
const TokenHeader = "X-Booty-Token"

// authMiddleware gates every operation on the /api/v1 group. It accepts EITHER
// a matching X-Booty-Token header or a valid session cookie, and nothing else.
//
// It is installed once on the group rather than per-operation on purpose: a
// route added to any registrar later inherits the gate automatically, which is
// the failure mode a per-route list exists to have and this one does not.
//
// disabled is the --noAuth escape hatch: a pass-through, logged loudly at
// startup by the caller.
func authMiddleware(api huma.API, store *auth.Store, disabled bool) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		// A nil store means no Auth was wired. Only tests construct APIDeps
		// that way; StartHTTP refuses to start without one unless --noAuth is
		// set, so a production nil can never reach here silently.
		if disabled || store == nil || authorized(store, ctx) {
			next(ctx)
			return
		}
		// huma renders this as application/problem+json, so the JSON
		// content-type requirement of design section 9 is satisfied here
		// without a hand-written body.
		_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "missing or invalid credential")
	}
}

func authorized(store *auth.Store, ctx huma.Context) bool {
	// Fail closed when no usable token is installed. This guard is what stops
	// the cookie path from being an AUTHENTICATION BYPASS: the cookie key is
	// derived from the token, so a token-less store would otherwise verify
	// against a key computable from this repo's source. Store.Key returns
	// ok=false in exactly that state -- never treat a zero key as a fallback.
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
			// be sent at all -- the cleartext consequence is documented for
			// operators (design section 2). But the standard hardening path is
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
func handleLogout(w http.ResponseWriter, r *http.Request) {
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

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "detail": msg})
}
