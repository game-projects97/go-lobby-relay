package relaytransport

import (
	"net/netip"
	"testing"
)

func TestTagSeparatesCarrierNamespaceFromUDP(t *testing.T) {
	peer := netip.MustParseAddrPort("192.0.2.10:4000")
	tagged := Tag(peer, "~ws")
	if !Tagged(tagged, "~ws") || Tagged(peer, "~ws") || tagged == peer {
		t.Fatalf("Tag(%v) = %v", peer, tagged)
	}
	if got := tagged.Addr().WithZone("").Unmap(); got != peer.Addr() || tagged.Port() != peer.Port() {
		t.Fatalf("untagged peer = %v:%d", got, tagged.Port())
	}
	if other := Tag(netip.MustParseAddrPort("192.0.2.10:4001"), "~ws"); other == tagged {
		t.Fatal("distinct peer ports share a tagged endpoint")
	}
	v6 := netip.MustParseAddrPort("[2001:db8::1]:5000")
	if tagged6 := Tag(v6, "~ws"); !Tagged(tagged6, "~ws") || tagged6.Addr().WithZone("") != v6.Addr() {
		t.Fatalf("Tag(v6) = %v", tagged6)
	}
	if Tag(netip.AddrPort{}, "~ws").IsValid() || Tag(peer, "").IsValid() || Tagged(tagged, "") {
		t.Fatal("invalid inputs produced a tagged endpoint")
	}
}
