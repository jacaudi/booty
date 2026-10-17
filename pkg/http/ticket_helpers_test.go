package http

import (
	"bytes"
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
