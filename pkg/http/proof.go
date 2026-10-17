package http

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/nodereport"
)

// noApprovedHostBody is the config routes' refusal body: a caller with no
// approved host and a caller without a valid boot ticket get the same answer,
// so it says nothing about whether a MAC is approved.
const noApprovedHostBody = "no approved host for this request"

const (
	// refusalWarnPerSource is the refusals one source may log at Warn per
	// refusalWarnWindow; the rest log at Debug. The config routes take no
	// credential, so an unbounded Warn would let any client fill the log.
	refusalWarnPerSource = 3
	refusalWarnWindow    = time.Minute
	// refusalLimiterCapacity bounds how many source keys the throttle keeps.
	refusalLimiterCapacity = 4096
)

// hostProof decides whether a config request proves it comes from the boot
// booty rendered for host mac, for OS family. A nil error means proven; the
// error says why not and never carries the presented proof.
// *bootticket.Ticketer is the production implementation (design R6).
type hostProof interface {
	Prove(r *http.Request, mac, family string) error
}

// configProof is what the config routes ask: whether r proves it is host's own
// boot for family (#88). *refusalLog is the production implementation.
type configProof interface {
	proven(r *http.Request, host *hardware.Host, family string) bool
}

// refusalLog is the configProof baseMux gives the config routes: it asks proof
// and logs each refusal with its reason, never with the ticket, so an expired
// or missing ticket can be told apart from an unknown host. The Warn budget is
// per source (discoverySource), not per MAC: a refused request always names a
// registered MAC, so a per-MAC budget would let any device mute that host's
// own warning.
type refusalLog struct {
	proof hostProof
	warn  *nodereport.Limiter
}

func newRefusalLog(proof hostProof) (*refusalLog, error) {
	warn, err := nodereport.NewLimiter(refusalWarnPerSource, refusalWarnWindow, refusalLimiterCapacity, time.Now)
	if err != nil {
		return nil, fmt.Errorf("refusal log: %w", err)
	}
	return &refusalLog{proof: proof, warn: warn}, nil
}

func (l *refusalLog) proven(r *http.Request, host *hardware.Host, family string) bool {
	err := l.proof.Prove(r, host.MAC, family)
	if err == nil {
		return true
	}
	level := slog.LevelDebug
	if key, _ := discoverySource(r.RemoteAddr); l.warn.Allow(key) {
		level = slog.LevelWarn
	}
	slog.Log(r.Context(), level, "config refused: no valid boot ticket", "family", family, "mac", host.MAC, "err", err)
	return false
}
