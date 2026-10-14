package http

import (
	"net"
	"net/http"

	"github.com/j-keck/arping"

	"github.com/jeefy/booty/pkg/hardware"
)

// arpFunc resolves an IP to the hardware address that answers for it on the
// local link. It is injected so tests need no network.
type arpFunc func(ip net.IP) (net.HardwareAddr, error)

// arpLookup is the production arpFunc: an ARP request via arping.
func arpLookup(ip net.IP) (net.HardwareAddr, error) {
	hw, _, err := arping.Ping(ip)
	return hw, err
}

// requestFromMAC reports whether the request's source IP ARP-resolves to mac.
// Any failure (bad RemoteAddr, ARP error, unparsable MAC) is false.
func requestFromMAC(r *http.Request, mac string, arp arpFunc) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	hw, err := arp(ip)
	if err != nil {
		return false
	}
	got, err := hardware.NormalizeMAC(hw.String())
	if err != nil {
		return false
	}
	want, err := hardware.NormalizeMAC(mac)
	if err != nil {
		return false
	}
	return got == want
}

// loggableMAC is raw normalised, or "" when it does not parse. Log this, never
// the raw (attacker-controlled) query value.
func loggableMAC(raw string) string {
	mac, err := hardware.NormalizeMAC(raw)
	if err != nil {
		return ""
	}
	return mac
}
