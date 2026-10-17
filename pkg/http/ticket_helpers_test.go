package http

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/bootticket"
)

// testTicketer is the boot-ticket issuer and checker pkg/http tests use. The
// key is fixed, so tickets from one call verify under another.
func testTicketer(t *testing.T) *bootticket.Ticketer {
	t.Helper()
	tk, err := bootticket.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// allowProof proves every request: tests that exercise rendering, not identity,
// keep their behaviour.
type allowProof struct{}

func (allowProof) Prove(*http.Request, string, string) error { return nil }

// withTicket appends a valid boot ticket for mac and family to path.
func withTicket(t *testing.T, tk *bootticket.Ticketer, path, mac, family string) string {
	t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + bootticket.QueryKey + "=" + tk.Issue(mac, family)
}
