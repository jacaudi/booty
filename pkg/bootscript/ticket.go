package bootscript

import (
	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/ostype"
)

// TicketIssuer mints the boot ticket a rendered OS script carries in its
// config URL. *bootticket.Ticketer is the production implementation; the
// config routes in pkg/http check what it mints (#88).
type TicketIssuer interface {
	Issue(mac, family string) string
}

// configFamily is the name of cacheName's OS family when that family takes a
// per-host config, else "" (tool OSes and unknown names).
func configFamily(cacheName string) string {
	o, ok := ostype.Lookup(cache.CacheNameToCanonical(cacheName))
	if !ok || o.Family().ConfigKind == "" {
		return ""
	}
	return o.Family().Name
}

// ticketFor is the ticket for a cacheName script booted by mac, or "" when
// there is no issuer, no MAC, or the OS takes no config. "" fails closed: the
// config routes refuse an empty ticket.
func ticketFor(issuer TicketIssuer, mac, cacheName string) string {
	family := configFamily(cacheName)
	if issuer == nil || mac == "" || family == "" {
		return ""
	}
	return issuer.Issue(mac, family)
}
