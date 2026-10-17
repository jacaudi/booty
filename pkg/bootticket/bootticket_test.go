package bootticket

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

const (
	testMAC    = "aa:bb:cc:dd:ee:01"
	testFamily = "talos"
)

func newTestTicketer(t *testing.T, b byte) *Ticketer {
	t.Helper()
	tk, err := New(bytes.Repeat([]byte{b}, keyLen))
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// withExp rebuilds ticket with exp replaced, keeping the signature.
func withExp(ticket, exp string) string {
	parts := strings.Split(ticket, ".")
	return parts[0] + "." + exp + "." + parts[2]
}

// signedWithExp is a v1 ticket whose exp is exactly exp and whose signature is
// genuinely tk's over that exp, so only the exp-format rule can refuse it.
func signedWithExp(tk *Ticketer, exp string) string {
	return "v1." + exp + "." + tk.sign(testMAC, testFamily, exp)
}

const b64url = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// tamperLast flips the lowest bit of the signature's last character's alphabet
// index. A 32-byte tag leaves the last character's low 2 bits unused, so that
// changes only a padding bit: a decode-then-compare check would accept the
// result, and the encoded-string compare must refuse it.
func tamperLast(ticket string) string {
	last := ticket[len(ticket)-1]
	i := strings.IndexByte(b64url, last)
	return ticket[:len(ticket)-1] + string(b64url[i^1])
}

func TestCheck(t *testing.T) {
	tk := newTestTicketer(t, 7)
	good := tk.Issue(testMAC, testFamily)
	parts := strings.Split(good, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		t.Fatalf("Issue = %q, want v1.<exp>.<sig>", good)
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		t.Fatalf("exp %q: %v", parts[1], err)
	}
	bumped := withExp(good, strconv.FormatInt(exp+1, 10))
	other := newTestTicketer(t, 9).Issue(testMAC, testFamily)
	zero := &Ticketer{}
	zeroSigned := "v1." + parts[1] + "." + zero.sign(testMAC, testFamily, parts[1])

	cases := []struct {
		name    string
		tk      *Ticketer
		ticket  string
		mac     string
		family  string
		wantErr error
	}{
		{"valid", tk, good, testMAC, testFamily, nil},
		{"wrong MAC", tk, good, "aa:bb:cc:dd:ee:02", testFamily, ErrInvalid},
		{"wrong family", tk, good, testMAC, "ignition", ErrInvalid},
		{"another key", tk, other, testMAC, testFamily, ErrInvalid},
		{"tampered sig", tk, tamperLast(good), testMAC, testFamily, ErrInvalid},
		{"exp changed by 1", tk, bumped, testMAC, testFamily, ErrInvalid},
		{"v2 prefix", tk, "v2" + good[2:], testMAC, testFamily, ErrInvalid},
		{"two parts", tk, parts[0] + "." + parts[1], testMAC, testFamily, ErrInvalid},
		{"four parts", tk, good + ".x", testMAC, testFamily, ErrInvalid},
		{"plus-signed exp", tk, signedWithExp(tk, "+"+parts[1]), testMAC, testFamily, ErrInvalid},
		{"leading-zero exp", tk, signedWithExp(tk, "0"+parts[1]), testMAC, testFamily, ErrInvalid},
		{"non-numeric exp", tk, signedWithExp(tk, "12a"), testMAC, testFamily, ErrInvalid},
		{"zero Ticketer, own signature", zero, zeroSigned, testMAC, testFamily, ErrInvalid},
		{"empty MAC", tk, good, "", testFamily, ErrInvalid},
		{"empty family", tk, good, testMAC, "", ErrInvalid},
		{"nil Ticketer", nil, good, testMAC, testFamily, ErrInvalid},
		{"zero Ticketer", &Ticketer{}, good, testMAC, testFamily, ErrInvalid},
		{"no ticket", tk, "", testMAC, testFamily, ErrMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.tk.Check(c.ticket, c.mac, c.family)
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Check = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestNewRejectsWrongKeyLength(t *testing.T) {
	for _, n := range []int{0, 31, 33} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Errorf("New(%d bytes) = nil error, want one", n)
		}
	}
}

func TestNewCopiesKey(t *testing.T) {
	key := bytes.Repeat([]byte{7}, keyLen)
	tk, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	ticket := tk.Issue(testMAC, testFamily)
	key[0] = 8 // a caller reusing its buffer must not change tk's key
	if err := tk.Check(ticket, testMAC, testFamily); err != nil {
		t.Fatalf("Check after the caller changed its key buffer = %v", err)
	}
}

func TestIssueRefusesWithoutKeyOrIdentity(t *testing.T) {
	var nilTk *Ticketer
	for name, got := range map[string]string{
		"nil Ticketer":  nilTk.Issue(testMAC, testFamily),
		"zero Ticketer": (&Ticketer{}).Issue(testMAC, testFamily),
		"empty MAC":     newTestTicketer(t, 7).Issue("", testFamily),
		"empty family":  newTestTicketer(t, 7).Issue(testMAC, ""),
	} {
		if got != "" {
			t.Errorf("%s: Issue = %q, want \"\"", name, got)
		}
	}
}

// TestTicketSurvivesNewTicketer: a Ticketer rebuilt from the same key (a booty
// restart) accepts tickets the old one issued.
func TestTicketSurvivesNewTicketer(t *testing.T) {
	ticket := newTestTicketer(t, 7).Issue(testMAC, testFamily)
	if err := newTestTicketer(t, 7).Check(ticket, testMAC, testFamily); err != nil {
		t.Fatalf("Check by a rebuilt Ticketer = %v, want nil", err)
	}
}

func TestCheckExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tk := newTestTicketer(t, 7)
		ticket := tk.Issue(testMAC, testFamily)
		time.Sleep(TTL - time.Second)
		if err := tk.Check(ticket, testMAC, testFamily); err != nil {
			t.Fatalf("1 s before expiry: Check = %v, want nil", err)
		}
		time.Sleep(time.Second)
		if err := tk.Check(ticket, testMAC, testFamily); !errors.Is(err, ErrExpired) {
			t.Fatalf("at expiry: Check = %v, want ErrExpired", err)
		}
		time.Sleep(time.Second)
		if err := tk.Check(ticket, testMAC, testFamily); !errors.Is(err, ErrExpired) {
			t.Fatalf("1 s after expiry: Check = %v, want ErrExpired", err)
		}
	})
}

// FuzzCheck: Check parses untrusted query input. It must never panic, and
// nothing but the ticket tk issued verifies.
func FuzzCheck(f *testing.F) {
	key := bytes.Repeat([]byte{7}, keyLen)
	tk, err := New(key)
	if err != nil {
		f.Fatal(err)
	}
	good := tk.Issue(testMAC, testFamily)
	for _, seed := range []string{good, "", "v1", "v1..", "v1.1.x", "v2." + good[3:], good + ".x", "v1.+1.AAAA", "v1.01.AAAA"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, ticket string) {
		if tk.Check(ticket, testMAC, testFamily) == nil && ticket != good {
			t.Fatalf("Check accepted %q, which tk did not issue", ticket)
		}
	})
}

func TestProveReadsTheQueryKey(t *testing.T) {
	tk := newTestTicketer(t, 7)
	ticket := tk.Issue(testMAC, testFamily)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/machineconfig?mac="+testMAC+"&"+QueryKey+"="+ticket, http.NoBody)
	if err := tk.Prove(req, testMAC, testFamily); err != nil {
		t.Fatalf("Prove = %v, want nil", err)
	}
	bare := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/machineconfig?mac="+testMAC, http.NoBody)
	if err := tk.Prove(bare, testMAC, testFamily); !errors.Is(err, ErrMissing) {
		t.Fatalf("Prove without a ticket = %v, want ErrMissing", err)
	}
}
