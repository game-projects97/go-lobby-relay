package udprelay

import (
	"bytes"
	"errors"
	"net/netip"
	"sync"
	"testing"

	"github.com/gyungsubLee/go-lobby-relay/internal/relayroom"
	"github.com/gyungsubLee/go-lobby-relay/internal/relaytransport"
)

const testZone = "~test"

var errTestStop = errors.New("test: stop reading")

type fakeTransport struct {
	mu     sync.Mutex
	full   bool
	writes []fakeWrite
}

func (transport *fakeTransport) Owns(endpoint netip.AddrPort) bool {
	return relaytransport.Tagged(endpoint, testZone)
}

func (transport *fakeTransport) Send(datagram []byte, endpoint netip.AddrPort) bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.full {
		return false
	}
	transport.writes = append(transport.writes, fakeWrite{data: append([]byte(nil), datagram...), endpoint: endpoint})
	return true
}

func (transport *fakeTransport) snapshot() []fakeWrite {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]fakeWrite(nil), transport.writes...)
}

func TestNewRejectsNilTransport(t *testing.T) {
	fixture := newStoreFixture(t, relayroom.DefaultLimits())
	var typedNil *fakeTransport
	for _, transport := range []relaytransport.Transport{nil, typedNil} {
		if relay, err := New(new(fakeSocket), fixture.store, Config{Transports: []relaytransport.Transport{transport}}); err == nil || relay != nil {
			t.Fatalf("New(nil transport) = (%#v, %v), want error", relay, err)
		}
	}
}

func TestTransportEndpointsShareRoomsWithUDP(t *testing.T) {
	fixture := newStoreFixture(t, relayroom.DefaultLimits())
	allocation := fixture.addRoom(t, "room", 3)
	socket := new(fakeSocket)
	carrier := new(fakeTransport)
	relay, err := New(socket, fixture.store, Config{Transports: []relaytransport.Transport{carrier}})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	udpClient := fixture.bindDirect(t, "room", allocation.Grants[0], netip.MustParseAddrPort("192.0.2.10:4000"), 0x81)
	streamA := fixture.bindDirect(t, "room", allocation.Grants[1],
		relaytransport.Tag(netip.MustParseAddrPort("192.0.2.10:4000"), testZone), 0x82)
	streamB := fixture.bindDirect(t, "room", allocation.Grants[2],
		relaytransport.Tag(netip.MustParseAddrPort("192.0.2.11:4001"), testZone), 0x83)

	payload := []byte("udp-to-stream")
	if err := relay.handleDatagram(udpClient.data(1, payload), udpClient.endpoint); err != nil {
		t.Fatalf("UDP ClientData: %v", err)
	}
	streamWrites := carrier.snapshot()
	if len(streamWrites) != 2 || !equalEndpoints([]netip.AddrPort{streamWrites[0].endpoint, streamWrites[1].endpoint},
		[]netip.AddrPort{streamA.endpoint, streamB.endpoint}) {
		t.Fatalf("stream writes = %#v", streamWrites)
	}
	if writes, _, _, _ := socket.snapshot(); len(writes) != 0 {
		t.Fatalf("UDP socket carried %d stream-owned writes", len(writes))
	}
	if serverData := unmarshalEnvelope(t, streamWrites[0].data).GetServerData(); serverData == nil ||
		!bytes.Equal(serverData.Payload, payload) || serverData.SenderParticipantId != udpClient.participantID {
		t.Fatalf("stream ServerData = %#v", serverData)
	}

	reply := []byte("stream-to-all")
	if err := relay.Deliver(streamA.data(1, reply), streamA.endpoint); err != nil {
		t.Fatalf("Deliver(): %v", err)
	}
	writes, _, _, _ := socket.snapshot()
	if len(writes) != 1 || writes[0].endpoint != udpClient.endpoint {
		t.Fatalf("UDP writes = %#v", writes)
	}
	if streamWrites = carrier.snapshot(); len(streamWrites) != 3 || streamWrites[2].endpoint != streamB.endpoint {
		t.Fatalf("stream writes after reply = %#v", streamWrites)
	}

	carrier.mu.Lock()
	carrier.full = true
	carrier.mu.Unlock()
	if err := relay.handleDatagram(udpClient.data(2, payload), udpClient.endpoint); err != nil {
		t.Fatalf("UDP ClientData to full carrier: %v", err)
	}
	counters := relay.Counters()
	if counters.FanoutWriteErrors != 2 || counters.FanoutWriteAttempts != 6 {
		t.Fatalf("counters after full carrier = %#v", counters)
	}
}

func TestTransportNamespaceIsNotAcceptedFromWrongCarrier(t *testing.T) {
	fixture := newStoreFixture(t, relayroom.DefaultLimits())
	allocation := fixture.addRoom(t, "room", 2)
	carrier := new(fakeTransport)
	tagged := relaytransport.Tag(netip.MustParseAddrPort("192.0.2.20:4000"), testZone)
	datagram := helloDatagram("room", allocation.Grants[0].SessionID, allocation.Grants[0].GrantID, filled16(0x90))
	read := false
	socket := &fakeSocket{read: func(buffer []byte) (int, netip.AddrPort, error) {
		if read {
			return 0, netip.AddrPort{}, errTestStop
		}
		read = true
		return copy(buffer, datagram), tagged, nil
	}}
	relay, err := New(socket, fixture.store, Config{Transports: []relaytransport.Transport{carrier}})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	_ = relay.Run()
	if err := relay.Deliver(datagram, netip.MustParseAddrPort("192.0.2.20:4000")); err != nil {
		t.Fatalf("Deliver(untagged): %v", err)
	}
	if counters := relay.Counters(); counters.DropReasons.WrongEndpoint != 2 || counters.UDPReceived != 2 {
		t.Fatalf("counters = %#v", counters)
	}
	if writes, _, _, _ := socket.snapshot(); len(writes) != 0 || len(carrier.snapshot()) != 0 {
		t.Fatal("cross-carrier datagram produced a response")
	}
}
