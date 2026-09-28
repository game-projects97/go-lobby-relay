// Package relaytransport defines how non-UDP carriers plug into the Relay.
//
// The Relay core (udprelay + relayroom) authenticates, rate-limits and fans out
// opaque datagrams keyed by a netip.AddrPort endpoint. A stream carrier such as
// WebSocket gives every connection a tagged endpoint: the peer address in IPv6
// form with a carrier zone. UDP never produces that zone, so endpoints of
// different carriers cannot collide, while relayroom still budgets pre-auth
// traffic by the untagged peer address.
package relaytransport

import "net/netip"

// Transport delivers Relay datagrams to the endpoints it owns. Send is
// best-effort like UDP: it must not block the caller and reports whether the
// datagram was queued.
type Transport interface {
	Owns(endpoint netip.AddrPort) bool
	Send(datagram []byte, endpoint netip.AddrPort) bool
}

// Tag places a stream peer address into the carrier's endpoint namespace.
func Tag(peer netip.AddrPort, zone string) netip.AddrPort {
	if !peer.IsValid() || zone == "" {
		return netip.AddrPort{}
	}
	address := netip.AddrFrom16(peer.Addr().As16()).WithZone(zone)
	return netip.AddrPortFrom(address, peer.Port())
}

// Tagged reports whether endpoint belongs to the carrier namespace zone.
func Tagged(endpoint netip.AddrPort, zone string) bool {
	return zone != "" && endpoint.IsValid() && endpoint.Addr().Zone() == zone
}
