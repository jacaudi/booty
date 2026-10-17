// Package bootticket issues and checks the short-lived boot tickets booty puts
// in every rendered OS script's config URL. A valid ticket proves a config
// request came from a boot booty rendered for that MAC and OS family (#88;
// design docs/designs/2026-10-16-config-route-identity-design.md).
package bootticket

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// TTL is how long an issued ticket stays valid.
	TTL = time.Hour
	// QueryKey is the config-URL query parameter that carries the ticket.
	QueryKey = "ticket"

	version = "v1"
	keyLen  = 32
	domain  = "booty-boot-ticket\x00" + version + "\x00"
)

var (
	// ErrMissing is the refusal for a request that carries no ticket.
	ErrMissing = errors.New("bootticket: no ticket")
	// ErrInvalid is the refusal for a malformed ticket, one signed for another
	// MAC, family or key, and for a Ticketer with no usable key.
	ErrInvalid = errors.New("bootticket: invalid ticket")
	// ErrExpired is the refusal for a well-formed, correctly signed ticket past
	// its expiry.
	ErrExpired = errors.New("bootticket: expired ticket")
)

// Ticketer issues and checks tickets under one key. A nil *Ticketer and the
// zero value issue nothing and accept nothing.
type Ticketer struct {
	key []byte
}

// New returns a Ticketer for a copy of key, or an error when key is not 32 bytes.
func New(key []byte) (*Ticketer, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("bootticket: key is %d bytes, want %d", len(key), keyLen)
	}
	return &Ticketer{key: bytes.Clone(key)}, nil
}

// Issue returns a v1 ticket for mac and family, expiring TTL after now. It
// returns "" when t has no usable key or mac or family is empty. mac is bound
// byte for byte: issuer and checker must pass the same normalised form
// (callers pass the stored host.MAC).
func (t *Ticketer) Issue(mac, family string) string {
	if !t.usable() || mac == "" || family == "" {
		return ""
	}
	exp := strconv.FormatInt(time.Now().Add(TTL).Unix(), 10)
	return version + "." + exp + "." + t.sign(mac, family, exp)
}

// Check returns nil when ticket is a valid, unexpired v1 ticket for mac and
// family. The signature is checked before the expiry, and compared as the
// encoded string, so a base64 padding-bit variant is refused. mac is bound byte
// for byte: issuer and checker must pass the same normalised form (callers pass
// the stored host.MAC).
func (t *Ticketer) Check(ticket, mac, family string) error {
	if !t.usable() || mac == "" || family == "" {
		return ErrInvalid
	}
	if ticket == "" {
		return ErrMissing
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 || parts[0] != version {
		return ErrInvalid
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || strconv.FormatInt(exp, 10) != parts[1] {
		return ErrInvalid
	}
	if !hmac.Equal([]byte(parts[2]), []byte(t.sign(mac, family, parts[1]))) {
		return ErrInvalid
	}
	if !time.Now().Before(time.Unix(exp, 0)) {
		return ErrExpired
	}
	return nil
}

// Prove checks the ticket r carries in its QueryKey parameter (the first one,
// if there are several). r must be a server request (non-nil URL).
func (t *Ticketer) Prove(r *http.Request, mac, family string) error {
	return t.Check(r.URL.Query().Get(QueryKey), mac, family)
}

func (t *Ticketer) usable() bool { return t != nil && len(t.key) == keyLen }

// sign is the unpadded base64url HMAC-SHA256 of the domain-separated MAC,
// family and expiry, exp exactly as it appears in the ticket.
func (t *Ticketer) sign(mac, family, exp string) string {
	m := hmac.New(sha256.New, t.key)
	m.Write([]byte(domain + mac + "\x00" + family + "\x00" + exp))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
