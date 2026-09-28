package relayroom

import (
	"net/netip"
	"testing"

	"github.com/gyungsubLee/go-lobby-relay/internal/relaytransport"
)

func TestSourceKeyIgnoresCarrierZone(t *testing.T) {
	for _, peer := range []string{"192.0.2.10:4000", "[2001:db8::1]:5000"} {
		endpoint := netip.MustParseAddrPort(peer)
		tagged := relaytransport.Tag(endpoint, "~ws")
		if got, want := sourceKey(tagged), sourceKey(endpoint); !want.IsValid() || got != want {
			t.Fatalf("sourceKey(%v) = %v, want %v", tagged, got, want)
		}
	}
}
