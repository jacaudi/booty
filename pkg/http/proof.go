package http

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jeefy/booty/pkg/hardware"
)

// noApprovedHostBody is the config routes' refusal body: a caller with no
// approved host and a caller without a valid boot ticket get the same answer,
// so it says nothing about whether a MAC is approved.
const noApprovedHostBody = "no approved host for this request"

// hostProof decides whether a config request proves it comes from the boot
// booty rendered for host mac, for OS family. A nil error means proven; the
// error says why not and never carries the presented proof.
// *bootticket.Ticketer is the production implementation (design R6).
type hostProof interface {
	Prove(r *http.Request, mac, family string) error
}

// errNoProofChecker is the refusal reason when no hostProof is wired.
var errNoProofChecker = errors.New("no boot ticket checker configured")

// proven reports whether r proves it is host's own boot for family (#88).
// Every refusal is logged with its reason, never with the ticket.
func proven(r *http.Request, p hostProof, host *hardware.Host, family string) bool {
	err := errNoProofChecker
	if p != nil {
		err = p.Prove(r, host.MAC, family)
	}
	if err == nil {
		return true
	}
	slog.WarnContext(r.Context(), "config refused: no valid boot ticket", "family", family, "mac", host.MAC, "err", err)
	return false
}
