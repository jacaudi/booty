package auth

import (
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T, tok string) [32]byte {
	t.Helper()
	c, err := newCredential(tok)
	if err != nil {
		t.Fatalf("newCredential(%q): %v", tok, err)
	}
	return c.key
}

func TestCookieRoundTrip(t *testing.T) {
	key := testKey(t, "tok")
	now := time.Unix(1_700_000_000, 0)

	v := IssueCookieValue(key, now.Add(SessionLifetime))
	if !strings.Contains(v, ".") {
		t.Fatalf("cookie value %q must be payload.signature", v)
	}
	if !VerifyCookieValue(key, v, now) {
		t.Fatal("a freshly issued cookie must verify")
	}
}

func TestCookieRejectsTampering(t *testing.T) {
	key := testKey(t, "tok")
	now := time.Unix(1_700_000_000, 0)
	v := IssueCookieValue(key, now.Add(SessionLifetime))
	payload, sig, _ := strings.Cut(v, ".")

	cases := map[string]string{
		"flipped signature": payload + "." + flipLast(sig),
		"flipped payload":   flipLast(payload) + "." + sig,
		"no separator":      payload + sig,
		"empty":             "",
		"payload only":      payload,
		"empty signature":   payload + ".",
		"not base64":        "!!!.@@@",
	}
	for name, bad := range cases {
		if VerifyCookieValue(key, bad, now) {
			t.Errorf("%s: VerifyCookieValue(%q) = true, want false", name, bad)
		}
	}
}

func TestCookieRejectsExpired(t *testing.T) {
	key := testKey(t, "tok")
	issued := time.Unix(1_700_000_000, 0)
	v := IssueCookieValue(key, issued.Add(SessionLifetime))

	if !VerifyCookieValue(key, v, issued.Add(SessionLifetime-time.Second)) {
		t.Fatal("must verify one second before expiry")
	}
	if VerifyCookieValue(key, v, issued.Add(SessionLifetime+time.Second)) {
		t.Fatal("must NOT verify one second after expiry")
	}
}

// TestCookieRejectedAfterRotation is the D3 guarantee: rotating the token
// changes the derived key, so every outstanding cookie stops verifying with
// no server-side session state to revoke.
func TestCookieRejectedAfterRotation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	oldKey := testKey(t, "before-rotation")
	v := IssueCookieValue(oldKey, now.Add(SessionLifetime))
	if !VerifyCookieValue(oldKey, v, now) {
		t.Fatal("precondition: the cookie must verify under its own key")
	}

	newKey := testKey(t, "after-rotation")
	if VerifyCookieValue(newKey, v, now) {
		t.Fatal("a cookie issued before rotation must not verify after it")
	}
}

func flipLast(s string) string {
	if s == "" {
		return "x"
	}
	b := []byte(s)
	if b[len(b)-1] == 'A' {
		b[len(b)-1] = 'B'
	} else {
		b[len(b)-1] = 'A'
	}
	return string(b)
}
