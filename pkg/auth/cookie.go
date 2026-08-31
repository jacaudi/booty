package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// CookieName is the session cookie the browser UI carries. The middleware
// accepts it as an alternative to the X-Booty-Token header.
const CookieName = "booty_session"

// SessionLifetime is a FIXED (not sliding) session window, encoded as an
// absolute expiry inside the signed payload. Fixed rather than sliding because
// a sliding window needs a re-issue on every request and buys nothing for a
// single-operator homelab; a lapsed session simply re-prompts for the token.
const SessionLifetime = 7 * 24 * time.Hour

// IssueCookieValue returns "<payload>.<sig>", where payload is the base64url
// unix expiry and sig is base64url HMAC-SHA256(key, payload).
//
// The cookie is a self-verifying bearer derived from the token, NOT the token
// itself. That buys two things over setting the raw token as the cookie: a
// server-enforced expiry, and a value that cannot be replayed as an
// X-Booty-Token header if it leaks through a proxy log or a shared machine.
func IssueCookieValue(key [32]byte, expiry time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(expiry.Unix(), 10)))
	return payload + "." + sign(key, payload)
}

// VerifyCookieValue reports whether value is well-formed, signed by key, and
// not expired as of now. Every failure mode returns false; the caller does not
// need to distinguish them, and refusing to say which check failed keeps the
// 401 from being an oracle.
func VerifyCookieValue(key [32]byte, value string, now time.Time) bool {
	payload, sig, ok := strings.Cut(value, ".")
	if !ok || payload == "" || sig == "" {
		return false
	}
	if !hmac.Equal([]byte(sig), []byte(sign(key, payload))) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	unix, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return false
	}
	return now.Before(time.Unix(unix, 0))
}

func sign(key [32]byte, payload string) string {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
