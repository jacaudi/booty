package http

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/bootticket"
	"github.com/jeefy/booty/pkg/hardware"
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

func (allowProof) proven(*http.Request, *hardware.Host, string) bool { return true }

// refusalLogFor is tk wrapped in its refusal log, as baseMux wires it.
func refusalLogFor(t *testing.T, tk *bootticket.Ticketer) *refusalLog {
	t.Helper()
	l, err := newRefusalLog(tk)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// withTicket appends a valid boot ticket for mac and family to path.
func withTicket(t *testing.T, tk *bootticket.Ticketer, path, mac, family string) string {
	t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + bootticket.QueryKey + "=" + tk.Issue(mac, family)
}
