package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	relayv1 "github.com/gyungsubLee/go-lobby-relay/gen/go/relay/v1"
	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"github.com/gyungsubLee/go-lobby-relay/internal/store"
	"google.golang.org/protobuf/proto"
)

type fakeWrite struct {
	data     []byte
	endpoint netip.AddrPort
	first    *byte
}

type fakeSocket struct {
	mu sync.Mutex

	read            func([]byte) (int, netip.AddrPort, error)
	writes          []fakeWrite
	deadlines       []time.Time
	events          []string
	writeCalls      int
	deadlineCalls   int
	closeCalls      int
	writeErrorAt    int
	shortWriteAt    int
	deadlineErrorAt int
	writeError      error
	deadlineError   error
	closeError      error
}

func (socket *fakeSocket) ReadFromUDPAddrPort(buffer []byte) (int, netip.AddrPort, error) {
	if socket.read == nil {
		return 0, netip.AddrPort{}, io.EOF
	}
	return socket.read(buffer)
}

func (socket *fakeSocket) WriteToUDPAddrPort(data []byte, endpoint netip.AddrPort) (int, error) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	socket.writeCalls++
	var first *byte
	if len(data) != 0 {
		first = &data[0]
	}
	socket.writes = append(socket.writes, fakeWrite{data: data, endpoint: endpoint, first: first})
	socket.events = append(socket.events, "write")
	if socket.writeCalls == socket.writeErrorAt {
		if socket.writeError == nil {
			socket.writeError = errors.New("write failed")
		}
		return 0, socket.writeError
	}
	if socket.writeCalls == socket.shortWriteAt {
		return len(data) - 1, nil
	}
	return len(data), nil
}

func (socket *fakeSocket) SetWriteDeadline(deadline time.Time) error {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	socket.deadlineCalls++
	socket.deadlines = append(socket.deadlines, deadline)
	socket.events = append(socket.events, "deadline")
	if socket.deadlineCalls == socket.deadlineErrorAt {
		if socket.deadlineError == nil {
			socket.deadlineError = errors.New("deadline failed")
		}
		return socket.deadlineError
	}
	return nil
}

func (socket *fakeSocket) Close() error {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	socket.closeCalls++
	return socket.closeError
}

func (socket *fakeSocket) snapshot() ([]fakeWrite, []time.Time, []string, int) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	return append([]fakeWrite(nil), socket.writes...), append([]time.Time(nil), socket.deadlines...),
		append([]string(nil), socket.events...), socket.closeCalls
}

type testClock struct {
	mu      sync.Mutex
	reading clock.Reading
}

func (fakeClock *testClock) now() clock.Reading {
	fakeClock.mu.Lock()
	defer fakeClock.mu.Unlock()
	return fakeClock.reading
}

func (fakeClock *testClock) set(mono time.Duration) {
	fakeClock.mu.Lock()
	defer fakeClock.mu.Unlock()
	delta := mono - fakeClock.reading.Mono
	fakeClock.reading.Wall = fakeClock.reading.Wall.Add(delta)
	fakeClock.reading.Mono = mono
}

type deterministicReader struct {
	mu      sync.Mutex
	next    byte
	failure error
}

func (reader *deterministicReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.failure != nil {
		return 0, reader.failure
	}
	if reader.next == 0 {
		reader.next = 1
	}
	for index := range buffer {
		buffer[index] = reader.next
	}
	reader.next++
	return len(buffer), nil
}

func (reader *deterministicReader) fail(err error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.failure = err
}

type storeFixture struct {
	store     *store.Store
	fakeClock *testClock
	random    *deterministicReader
}

type testClient struct {
	roomID, participantID, sessionID string
	grantID, bindingID               protocol.Bytes16
	secret, key                      protocol.Bytes32
	endpoint                         netip.AddrPort
}

func newStoreFixture(t testing.TB, limits store.Limits) *storeFixture {
	t.Helper()
	fakeClock := &testClock{reading: clock.Reading{Wall: time.Now().UTC().Add(time.Minute)}}
	random := &deterministicReader{}
	rooms, err := store.New(store.Config{Limits: limits, Now: fakeClock.now, Random: random})
	if err != nil {
		t.Fatalf("store.New(): %v", err)
	}
	return &storeFixture{store: rooms, fakeClock: fakeClock, random: random}
}

func (fixture *storeFixture) addRoom(t testing.TB, roomID string, participants int) store.Allocation {
	t.Helper()
	now := fixture.fakeClock.now().Wall
	definition := store.RoomDefinition{
		Capacity: uint32(participants), ExpiresAt: now.Add(time.Hour),
		Participants: make([]store.ParticipantDefinition, participants),
	}
	for index := range definition.Participants {
		definition.Participants[index] = store.ParticipantDefinition{
			ParticipantID:  roomID + "-participant-" + string(rune('a'+index)),
			SessionID:      roomID + "-session-" + string(rune('a'+index)),
			GrantExpiresAt: now.Add(30 * time.Minute),
		}
	}
	allocation, created, err := fixture.store.CreateRoom(roomID, definition)
	if err != nil || !created {
		t.Fatalf("CreateRoom(%q) = (_, %t, %v)", roomID, created, err)
	}
	return allocation
}

func (fixture *storeFixture) bindDirect(t testing.TB, roomID string, grant store.GrantAllocation, endpoint netip.AddrPort, nonceByte byte) testClient {
	t.Helper()
	nonce := filled16(nonceByte)
	challenge, reason := fixture.store.BeginChallenge(store.ChallengeRequest{
		RoomID: roomID, SessionID: grant.SessionID, GrantID: grant.GrantID,
		ClientNonce: nonce, Endpoint: endpoint, InputBytes: protocol.MinHelloBytes,
	})
	if reason != store.RejectNone {
		t.Fatalf("BeginChallenge(%q): %q", grant.SessionID, reason)
	}
	secret := *grant.GrantSecret
	key := protocol.BindingKey(secret, protocol.Revision, roomID, grant.SessionID, grant.GrantID,
		challenge.CandidateID, nonce, challenge.ServerNonce)
	bound, reason := fixture.store.Authenticate(store.AuthenticateRequest{
		RoomID: roomID, SessionID: grant.SessionID, CandidateID: challenge.CandidateID,
		Endpoint: endpoint, InputBytes: 100,
		AuthTag: protocol.AuthTag(secret, protocol.Revision, roomID, grant.SessionID, grant.GrantID,
			challenge.CandidateID, nonce, challenge.ServerNonce),
	})
	if reason != store.RejectNone {
		t.Fatalf("Authenticate(%q): %q", grant.SessionID, reason)
	}
	return testClient{
		roomID: roomID, participantID: grant.ParticipantID, sessionID: grant.SessionID,
		grantID: grant.GrantID, bindingID: bound.BindingID, secret: secret, key: key, endpoint: endpoint,
	}
}

func (client testClient) data(sequence uint64, payload []byte) []byte {
	payloadCopy := append([]byte(nil), payload...)
	tag := protocol.ClientDataTag(client.key, protocol.Revision, client.roomID, client.sessionID,
		client.bindingID, sequence, payloadCopy)
	return marshalClient(&relayv1.Envelope{
		ProtocolRevision: protocol.Revision, Sequence: sequence, AuthTag: tag[:],
		RoomId: client.roomID, SessionId: client.sessionID,
		Body: &relayv1.Envelope_ClientData{ClientData: &relayv1.ClientData{
			BindingId: client.bindingID[:], Payload: payloadCopy,
		}},
	})
}

func (client testClient) ping(sequence uint64) []byte {
	tag := protocol.PingTag(client.key, protocol.Revision, client.roomID, client.sessionID,
		client.bindingID, sequence)
	return marshalClient(&relayv1.Envelope{
		ProtocolRevision: protocol.Revision, Sequence: sequence, AuthTag: tag[:],
		RoomId: client.roomID, SessionId: client.sessionID,
		Body: &relayv1.Envelope_Ping{Ping: &relayv1.Ping{BindingId: client.bindingID[:]}},
	})
}

func TestNewValidatesConfigurationAndOwnsSocketOnlyOnSuccess(t *testing.T) {
	fixture := newStoreFixture(t, store.DefaultLimits())
	socket := new(fakeSocket)
	for _, test := range []struct {
		name   string
		socket udpSocket
		rooms  *store.Store
		config Config
	}{
		{"nil socket", nil, fixture.store, Config{}},
		{"nil store", socket, nil, Config{}},
		{"negative timeout", socket, fixture.store, Config{WriteTimeout: -1}},
		{"timeout above maximum", socket, fixture.store, Config{WriteTimeout: 20*time.Millisecond + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if relay, err := New(test.socket, test.rooms, test.config); err == nil || relay != nil {
				t.Fatalf("New() = (%#v, %v), want error", relay, err)
			}
		})
	}
	if _, _, _, closes := socket.snapshot(); closes != 0 {
		t.Fatalf("failed New closed unowned socket %d times", closes)
	}

	relay, err := New(socket, fixture.store, Config{})
	if err != nil {
		t.Fatalf("New(default): %v", err)
	}
	if relay.writeTimeout != 2*time.Millisecond {
		t.Fatalf("default write timeout = %v", relay.writeTimeout)
	}
	if err := relay.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := relay.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if _, _, _, closes := socket.snapshot(); closes != 1 {
		t.Fatalf("Close calls = %d, want 1", closes)
	}
}

func TestDispatchHandlesEveryPacketKindAndReusesOneFanoutEncoding(t *testing.T) {
	fixture := newStoreFixture(t, store.DefaultLimits())
	allocation := fixture.addRoom(t, "room", 3)
	socket := new(fakeSocket)
	deadlineNow := time.Now().Add(time.Minute)
	nowCalls := 0
	relay, err := New(socket, fixture.store, Config{WriteTimeout: time.Millisecond, Now: func() time.Time {
		nowCalls++
		return deadlineNow.Add(time.Duration(nowCalls) * time.Second)
	}})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	senderEndpoint := netip.MustParseAddrPort("192.0.2.10:4000")
	clientNonce := filled16(0x81)
	hello := helloDatagram("room", allocation.Grants[0].SessionID, allocation.Grants[0].GrantID, clientNonce)
	if err := relay.dispatch(hello, senderEndpoint); err != nil {
		t.Fatalf("dispatch(HELLO): %v", err)
	}
	writes, _, events, _ := socket.snapshot()
	if len(writes) != 1 || len(writes[0].data) >= len(hello) {
		t.Fatalf("CHALLENGE writes/size = %d/%d, HELLO=%d", len(writes), len(writes[0].data), len(hello))
	}
	if got := events; !equalStrings(got, []string{"deadline", "write"}) {
		t.Fatalf("HELLO events = %v", got)
	}
	challengeEnvelope := unmarshalEnvelope(t, writes[0].data)
	challenge := challengeEnvelope.GetChallenge()
	if challenge == nil {
		t.Fatalf("HELLO response body = %T", challengeEnvelope.Body)
	}

	secret := *allocation.Grants[0].GrantSecret
	candidateID := fixed16(t, challenge.CandidateId)
	serverNonce := fixed32(t, challenge.ServerNonce)
	authTag := protocol.AuthTag(secret, protocol.Revision, "room", allocation.Grants[0].SessionID,
		allocation.Grants[0].GrantID, candidateID, clientNonce, serverNonce)
	auth := marshalClient(&relayv1.Envelope{
		ProtocolRevision: protocol.Revision, AuthTag: authTag[:], RoomId: "room",
		SessionId: allocation.Grants[0].SessionID,
		Body:      &relayv1.Envelope_Auth{Auth: &relayv1.Auth{CandidateId: candidateID[:]}},
	})
	if err := relay.dispatch(auth, senderEndpoint); err != nil {
		t.Fatalf("dispatch(AUTH): %v", err)
	}
	writes, deadlines, events, _ := socket.snapshot()
	if len(writes) != 2 || !equalStrings(events, []string{"deadline", "write", "deadline", "write"}) {
		t.Fatalf("AUTH writes/events = %d/%v", len(writes), events)
	}
	if len(deadlines) != 2 || !deadlines[1].After(deadlines[0]) {
		t.Fatalf("write deadlines = %v, want fresh increasing deadlines", deadlines)
	}
	boundEnvelope := unmarshalEnvelope(t, writes[1].data)
	bound := boundEnvelope.GetBound()
	if bound == nil {
		t.Fatalf("AUTH response body = %T", boundEnvelope.Body)
	}
	bindingID := fixed16(t, bound.BindingId)
	key := protocol.BindingKey(secret, protocol.Revision, "room", allocation.Grants[0].SessionID,
		allocation.Grants[0].GrantID, candidateID, clientNonce, serverNonce)
	wantBoundTag := protocol.BoundTag(key, protocol.Revision, "room", allocation.Grants[0].SessionID,
		candidateID, bindingID, bound.ExpiresUnixMs)
	if !protocol.EqualTag(wantBoundTag, boundEnvelope.AuthTag) {
		t.Fatal("BOUND tag did not authenticate the returned binding")
	}

	recipientA := fixture.bindDirect(t, "room", allocation.Grants[1], netip.MustParseAddrPort("192.0.2.11:4001"), 0x82)
	recipientB := fixture.bindDirect(t, "room", allocation.Grants[2], netip.MustParseAddrPort("192.0.2.12:4002"), 0x83)
	sender := testClient{
		roomID: "room", participantID: allocation.Grants[0].ParticipantID,
		sessionID: allocation.Grants[0].SessionID, grantID: allocation.Grants[0].GrantID,
		bindingID: bindingID, secret: secret, key: key, endpoint: senderEndpoint,
	}
	payload := []byte("payload-sentinel-8fc50c2d")
	if err := relay.dispatch(sender.data(1, payload), senderEndpoint); err != nil {
		t.Fatalf("dispatch(ClientData): %v", err)
	}
	writes, deadlines, events, _ = socket.snapshot()
	if len(writes) != 4 || !equalStrings(events,
		[]string{"deadline", "write", "deadline", "write", "deadline", "write", "write"}) {
		t.Fatalf("ClientData writes/events = %d/%v", len(writes), events)
	}
	if len(deadlines) != 3 {
		t.Fatalf("deadline count = %d, want one before fanout batch", len(deadlines))
	}
	if writes[2].first == nil || writes[2].first != writes[3].first {
		t.Fatal("fanout did not reuse the identical encoded slice")
	}
	if got, want := []netip.AddrPort{writes[2].endpoint, writes[3].endpoint},
		[]netip.AddrPort{recipientA.endpoint, recipientB.endpoint}; !equalEndpoints(got, want) {
		t.Fatalf("recipients = %v, want %v", got, want)
	}
	for _, write := range writes[2:] {
		serverEnvelope := unmarshalEnvelope(t, write.data)
		serverData := serverEnvelope.GetServerData()
		if serverEnvelope.RoomId != sender.roomID || serverEnvelope.SessionId != sender.sessionID ||
			serverEnvelope.Sequence != 1 || len(serverEnvelope.AuthTag) != 0 || serverData == nil ||
			serverData.SenderParticipantId != sender.participantID || !bytes.Equal(serverData.Payload, payload) {
			t.Fatalf("authoritative ServerData = %#v", serverEnvelope)
		}
	}

	beforeWrites := len(writes)
	beforeDeadlines := len(deadlines)
	if err := relay.dispatch(sender.ping(2), senderEndpoint); err != nil {
		t.Fatalf("dispatch(Ping): %v", err)
	}
	writes, deadlines, _, _ = socket.snapshot()
	if len(writes) != beforeWrites || len(deadlines) != beforeDeadlines {
		t.Fatal("Ping wrote or set a write deadline")
	}
	counters := relay.Counters()
	if counters.UDPReceived != 4 || counters.ClientDataAccepted != 1 || counters.UDPDropped != 0 ||
		counters.FanoutWriteAttempts != 2 || counters.FanoutWriteSuccesses != 2 || counters.FanoutWriteErrors != 0 {
		t.Fatalf("counters = %#v", counters)
	}
}

func TestDispatchEmitsHandshakeIndependentOfHostWall(t *testing.T) {
	for _, wall := range []time.Time{
		time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC),
	} {
		t.Run(wall.Format("2006"), func(t *testing.T) {
			fixture := newStoreFixture(t, store.DefaultLimits())
			fixture.fakeClock.reading = clock.Reading{Wall: wall}
			allocation := fixture.addRoom(t, "room", 1)
			socket := new(fakeSocket)
			relay, err := New(socket, fixture.store, Config{})
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			t.Cleanup(func() { _ = relay.Close() })

			endpoint := netip.MustParseAddrPort("192.0.2.99:4999")
			nonce := filled16(0x91)
			hello := helloDatagram("room", allocation.Grants[0].SessionID, allocation.Grants[0].GrantID, nonce)
			if err := relay.dispatch(hello, endpoint); err != nil {
				t.Fatalf("dispatch(HELLO): %v", err)
			}
			writes, _, _, _ := socket.snapshot()
			if len(writes) != 1 {
				t.Fatalf("CHALLENGE writes = %d, want 1", len(writes))
			}
			challenge := unmarshalEnvelope(t, writes[0].data).GetChallenge()
			candidateID := fixed16(t, challenge.CandidateId)
			serverNonce := fixed32(t, challenge.ServerNonce)
			secret := *allocation.Grants[0].GrantSecret
			authTag := protocol.AuthTag(secret, protocol.Revision, "room", allocation.Grants[0].SessionID,
				allocation.Grants[0].GrantID, candidateID, nonce, serverNonce)
			auth := marshalClient(&relayv1.Envelope{
				ProtocolRevision: protocol.Revision, RoomId: "room", SessionId: allocation.Grants[0].SessionID,
				AuthTag: authTag[:],
				Body:    &relayv1.Envelope_Auth{Auth: &relayv1.Auth{CandidateId: candidateID[:]}},
			})
			if err := relay.dispatch(auth, endpoint); err != nil {
				t.Fatalf("dispatch(AUTH): %v", err)
			}
			writes, _, _, _ = socket.snapshot()
			if len(writes) != 2 || unmarshalEnvelope(t, writes[1].data).GetBound() == nil {
				t.Fatalf("handshake writes = %d, want CHALLENGE then BOUND", len(writes))
			}
		})
	}
}

func TestDispatchClassifiesFixedDropReasonsExactlyOnce(t *testing.T) {
	tests := []struct {
		name string
		want store.RejectReason
		run  func(testing.TB, *Relay, *fakeSocket, *storeFixture)
	}{
		{"malformed", store.RejectMalformed, func(_ testing.TB, relay *Relay, _ *fakeSocket, _ *storeFixture) {
			_ = relay.dispatch([]byte{0xff}, netip.MustParseAddrPort("192.0.2.1:4000"))
		}},
		{"oversized", store.RejectOversized, func(_ testing.TB, relay *Relay, _ *fakeSocket, _ *storeFixture) {
			_ = relay.dispatch(make([]byte, protocol.MaxDatagramBytes+1), netip.MustParseAddrPort("192.0.2.2:4000"))
		}},
		{"unsupported_version", store.RejectUnsupportedVersion, func(_ testing.TB, relay *Relay, _ *fakeSocket, _ *storeFixture) {
			wire := marshalClient(&relayv1.Envelope{ProtocolRevision: protocol.Revision + 1, RoomId: "room", SessionId: "session"})
			_ = relay.dispatch(wire, netip.MustParseAddrPort("192.0.2.3:4000"))
		}},
		{"unknown_grant", store.RejectUnknownGrant, func(_ testing.TB, relay *Relay, _ *fakeSocket, _ *storeFixture) {
			_ = relay.dispatch(helloDatagram("room", "session", filled16(0xee), filled16(0xef)),
				netip.MustParseAddrPort("192.0.2.4:4000"))
		}},
		{"auth_failed", store.RejectAuthFailed, func(_ testing.TB, relay *Relay, _ *fakeSocket, _ *storeFixture) {
			wire := marshalClient(&relayv1.Envelope{
				ProtocolRevision: protocol.Revision, RoomId: "room", SessionId: "session", AuthTag: make([]byte, 32),
				Body: &relayv1.Envelope_Auth{Auth: &relayv1.Auth{CandidateId: make([]byte, 16)}},
			})
			_ = relay.dispatch(wire, netip.MustParseAddrPort("192.0.2.5:4000"))
		}},
		{"not_bound", store.RejectNotBound, func(_ testing.TB, relay *Relay, _ *fakeSocket, _ *storeFixture) {
			wire := marshalClient(&relayv1.Envelope{
				ProtocolRevision: protocol.Revision, Sequence: 1, RoomId: "room", SessionId: "session", AuthTag: make([]byte, 32),
				Body: &relayv1.Envelope_Ping{Ping: &relayv1.Ping{BindingId: make([]byte, 16)}},
			})
			_ = relay.dispatch(wire, netip.MustParseAddrPort("192.0.2.6:4000"))
		}},
		{"wrong_room", store.RejectWrongRoom, func(t testing.TB, relay *Relay, _ *fakeSocket, fixture *storeFixture) {
			allocation := fixture.addRoom(t, "room", 1)
			client := fixture.bindDirect(t, "room", allocation.Grants[0], netip.MustParseAddrPort("192.0.2.7:4000"), 0x51)
			wire := unmarshalEnvelope(t, client.ping(1))
			wire.RoomId = "other-room"
			_ = relay.dispatch(marshalClient(wire), client.endpoint)
		}},
		{"wrong_endpoint", store.RejectWrongEndpoint, func(t testing.TB, relay *Relay, _ *fakeSocket, fixture *storeFixture) {
			allocation := fixture.addRoom(t, "room", 1)
			client := fixture.bindDirect(t, "room", allocation.Grants[0], netip.MustParseAddrPort("192.0.2.8:4000"), 0x52)
			_ = relay.dispatch(client.ping(1), netip.MustParseAddrPort("192.0.2.88:4888"))
		}},
		{"bad_hmac", store.RejectAuthFailed, func(t testing.TB, relay *Relay, _ *fakeSocket, fixture *storeFixture) {
			allocation := fixture.addRoom(t, "room", 1)
			client := fixture.bindDirect(t, "room", allocation.Grants[0], netip.MustParseAddrPort("192.0.2.9:4000"), 0x53)
			wire := unmarshalEnvelope(t, client.ping(1))
			wire.AuthTag[0] ^= 1
			_ = relay.dispatch(marshalClient(wire), client.endpoint)
		}},
		{"replay", store.RejectReplay, func(t testing.TB, relay *Relay, _ *fakeSocket, fixture *storeFixture) {
			allocation := fixture.addRoom(t, "room", 1)
			client := fixture.bindDirect(t, "room", allocation.Grants[0], netip.MustParseAddrPort("192.0.2.10:4000"), 0x54)
			wire := client.ping(1)
			if err := relay.dispatch(wire, client.endpoint); err != nil {
				t.Fatalf("first Ping: %v", err)
			}
			_ = relay.dispatch(wire, client.endpoint)
		}},
		{"expired", store.RejectExpired, func(t testing.TB, relay *Relay, _ *fakeSocket, fixture *storeFixture) {
			allocation := fixture.addRoom(t, "room", 1)
			client := fixture.bindDirect(t, "room", allocation.Grants[0], netip.MustParseAddrPort("192.0.2.11:4000"), 0x55)
			fixture.fakeClock.set(store.HardMaxBindingTTL)
			_ = relay.dispatch(client.ping(1), client.endpoint)
		}},
		{"rate_limited", store.RejectRateLimited, func(_ testing.TB, relay *Relay, _ *fakeSocket, fixture *storeFixture) {
			endpoint := netip.MustParseAddrPort("192.0.2.12:4000")
			_ = fixture.store.AdmitPreauth(store.PreauthRequest{Endpoint: endpoint, InputBytes: 1})
			_ = relay.dispatch([]byte{0xff}, endpoint)
		}},
		{"fanout_limited", store.RejectFanoutLimited, func(t testing.TB, relay *Relay, _ *fakeSocket, fixture *storeFixture) {
			allocation := fixture.addRoom(t, "room", 3)
			clients := make([]testClient, 3)
			for index := range clients {
				clients[index] = fixture.bindDirect(t, "room", allocation.Grants[index],
					netip.MustParseAddrPort("192.0.2."+string(rune('1'+index))+":4100"), byte(0x60+index))
			}
			_ = relay.dispatch(clients[0].data(1, nil), clients[0].endpoint)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := store.DefaultLimits()
			if test.want == store.RejectRateLimited {
				limits.PreauthSourcePacketRate, limits.PreauthSourcePacketBurst = 1, 1
			}
			if test.want == store.RejectFanoutLimited {
				limits.RoomFanoutWriteRate, limits.RoomFanoutWriteBurst = 1, 1
			}
			fixture := newStoreFixture(t, limits)
			socket := new(fakeSocket)
			relay, err := New(socket, fixture.store, Config{})
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			t.Cleanup(func() { _ = relay.Close() })
			test.run(t, relay, socket, fixture)
			counters := relay.Counters()
			if counters.UDPDropped != 1 || dropTotal(counters.DropReasons) != 1 ||
				dropCount(counters.DropReasons, test.want) != 1 {
				t.Fatalf("counters for %s = %#v", test.want, counters)
			}
			writes, _, _, _ := socket.snapshot()
			if len(writes) != 0 {
				t.Fatalf("rejected input wrote %d datagrams", len(writes))
			}
		})
	}
}

func TestDispatchClassifiesRetiredCredentialsAfterEndRoom(t *testing.T) {
	tests := []struct {
		name    string
		want    store.RejectReason
		prepare func(testing.TB, *storeFixture, store.Allocation) ([]byte, netip.AddrPort)
	}{
		{
			name: "HELLO is unknown_grant",
			want: store.RejectUnknownGrant,
			prepare: func(_ testing.TB, _ *storeFixture, allocation store.Allocation) ([]byte, netip.AddrPort) {
				return helloDatagram("room", allocation.Grants[0].SessionID, allocation.Grants[0].GrantID, filled16(0xa1)),
					netip.MustParseAddrPort("192.0.2.101:4101")
			},
		},
		{
			name: "AUTH is auth_failed",
			want: store.RejectAuthFailed,
			prepare: func(t testing.TB, fixture *storeFixture, allocation store.Allocation) ([]byte, netip.AddrPort) {
				endpoint := netip.MustParseAddrPort("192.0.2.102:4102")
				nonce := filled16(0xa2)
				challenge, reason := fixture.store.BeginChallenge(store.ChallengeRequest{
					RoomID: "room", SessionID: allocation.Grants[0].SessionID, GrantID: allocation.Grants[0].GrantID,
					ClientNonce: nonce, Endpoint: endpoint, InputBytes: protocol.MinHelloBytes,
				})
				if reason != store.RejectNone {
					t.Fatalf("BeginChallenge(): %q", reason)
				}
				secret := *allocation.Grants[0].GrantSecret
				tag := protocol.AuthTag(secret, protocol.Revision, "room", allocation.Grants[0].SessionID,
					allocation.Grants[0].GrantID, challenge.CandidateID, nonce, challenge.ServerNonce)
				return marshalClient(&relayv1.Envelope{
					ProtocolRevision: protocol.Revision, RoomId: "room", SessionId: allocation.Grants[0].SessionID,
					AuthTag: tag[:],
					Body:    &relayv1.Envelope_Auth{Auth: &relayv1.Auth{CandidateId: challenge.CandidateID[:]}},
				}), endpoint
			},
		},
		{
			name: "ClientData is not_bound",
			want: store.RejectNotBound,
			prepare: func(t testing.TB, fixture *storeFixture, allocation store.Allocation) ([]byte, netip.AddrPort) {
				client := fixture.bindDirect(t, "room", allocation.Grants[0], netip.MustParseAddrPort("192.0.2.103:4103"), 0xa3)
				return client.data(1, []byte("stale")), client.endpoint
			},
		},
		{
			name: "Ping is not_bound",
			want: store.RejectNotBound,
			prepare: func(t testing.TB, fixture *storeFixture, allocation store.Allocation) ([]byte, netip.AddrPort) {
				client := fixture.bindDirect(t, "room", allocation.Grants[0], netip.MustParseAddrPort("192.0.2.104:4104"), 0xa4)
				return client.ping(1), client.endpoint
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStoreFixture(t, store.DefaultLimits())
			allocation := fixture.addRoom(t, "room", 1)
			wire, endpoint := test.prepare(t, fixture, allocation)
			if err := fixture.store.EndRoom("room"); err != nil {
				t.Fatalf("EndRoom(): %v", err)
			}
			socket := new(fakeSocket)
			relay, err := New(socket, fixture.store, Config{})
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			t.Cleanup(func() { _ = relay.Close() })

			if err := relay.dispatch(wire, endpoint); err != nil {
				t.Fatalf("dispatch(): %v", err)
			}
			counters := relay.Counters()
			if counters.UDPReceived != 1 || counters.UDPDropped != 1 || dropTotal(counters.DropReasons) != 1 ||
				dropCount(counters.DropReasons, test.want) != 1 || counters.DropReasons.Revoked != 0 {
				t.Fatalf("retired credential counters = %#v", counters)
			}
			writes, deadlines, _, _ := socket.snapshot()
			if len(writes) != 0 || len(deadlines) != 0 {
				t.Fatalf("retired credential produced output: writes=%d deadlines=%d", len(writes), len(deadlines))
			}
			if _, err := fixture.store.GetRoom("room"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("GetRoom() after stale traffic error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestDropCountersCoverEveryFixedReasonAndNeverFatalRandom(t *testing.T) {
	fixture := newStoreFixture(t, store.DefaultLimits())
	relay, err := New(new(fakeSocket), fixture.store, Config{})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	reasons := []store.RejectReason{
		store.RejectMalformed, store.RejectOversized, store.RejectUnsupportedVersion,
		store.RejectUnknownGrant, store.RejectAuthFailed, store.RejectReplay, store.RejectExpired,
		store.RejectRevoked, store.RejectWrongRoom, store.RejectWrongEndpoint, store.RejectNotBound,
		store.RejectRateLimited, store.RejectFanoutLimited, store.RejectDraining,
	}
	for _, reason := range reasons {
		relay.recordDrop(reason)
	}
	relay.recordDrop(store.RejectFatalRandom)
	counters := relay.Counters()
	if counters.UDPDropped != uint64(len(reasons)) || dropTotal(counters.DropReasons) != uint64(len(reasons)) {
		t.Fatalf("fixed drop counters = %#v", counters)
	}
	for _, reason := range reasons {
		if dropCount(counters.DropReasons, reason) != 1 {
			t.Fatalf("drop %q count = %d", reason, dropCount(counters.DropReasons, reason))
		}
	}
}

func TestDispatchWriteFailuresAreSilentBoundedAndNotInputDrops(t *testing.T) {
	for _, test := range []struct {
		name          string
		configure     func(*fakeSocket)
		wantAttempts  uint64
		wantSuccesses uint64
		wantErrors    uint64
		wantWrites    int
	}{
		{"deadline", func(socket *fakeSocket) { socket.deadlineErrorAt = 1 }, 0, 0, 0, 0},
		{"first write", func(socket *fakeSocket) { socket.writeErrorAt = 1 }, 1, 0, 1, 1},
		{"short write", func(socket *fakeSocket) { socket.shortWriteAt = 1 }, 1, 0, 1, 1},
		{"second write", func(socket *fakeSocket) { socket.writeErrorAt = 2 }, 2, 1, 1, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStoreFixture(t, store.DefaultLimits())
			allocation := fixture.addRoom(t, "room", 3)
			clients := make([]testClient, 3)
			for index := range clients {
				clients[index] = fixture.bindDirect(t, "room", allocation.Grants[index],
					netip.AddrPortFrom(netip.MustParseAddr("198.51.100.1"), uint16(4200+index)), byte(0x70+index))
			}
			socket := new(fakeSocket)
			test.configure(socket)
			relay, err := New(socket, fixture.store, Config{})
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			t.Cleanup(func() { _ = relay.Close() })
			if err := relay.dispatch(clients[0].data(1, []byte("write-failure")), clients[0].endpoint); err != nil {
				t.Fatalf("dispatch(): %v", err)
			}
			counters := relay.Counters()
			if counters.ClientDataAccepted != 1 || counters.UDPDropped != 0 ||
				counters.FanoutWriteAttempts != test.wantAttempts ||
				counters.FanoutWriteSuccesses != test.wantSuccesses ||
				counters.FanoutWriteErrors != test.wantErrors {
				t.Fatalf("counters = %#v", counters)
			}
			writes, _, _, _ := socket.snapshot()
			if len(writes) != test.wantWrites {
				t.Fatalf("writes = %d, want %d", len(writes), test.wantWrites)
			}
		})
	}

	for _, failure := range []string{"deadline", "write", "short"} {
		t.Run("handshake "+failure, func(t *testing.T) {
			fixture := newStoreFixture(t, store.DefaultLimits())
			allocation := fixture.addRoom(t, "room", 1)
			socket := new(fakeSocket)
			switch failure {
			case "deadline":
				socket.deadlineErrorAt = 1
			case "write":
				socket.writeErrorAt = 1
			case "short":
				socket.shortWriteAt = 1
			}
			relay, err := New(socket, fixture.store, Config{})
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			t.Cleanup(func() { _ = relay.Close() })
			wire := helloDatagram("room", allocation.Grants[0].SessionID, allocation.Grants[0].GrantID, filled16(0xa1))
			if err := relay.dispatch(wire, netip.MustParseAddrPort("203.0.113.1:4300")); err != nil {
				t.Fatalf("dispatch(HELLO): %v", err)
			}
			if counters := relay.Counters(); counters.UDPDropped != 0 {
				t.Fatalf("handshake output failure became input drop: %#v", counters)
			}
		})
	}
}

func TestRunUsesExactBufferNormalizesSourceAndGuardsImpossibleReadCounts(t *testing.T) {
	fixture := newStoreFixture(t, store.DefaultLimits())
	for _, test := range []struct {
		name string
		n    int
	}{
		{"negative", -1},
		{"past buffer", protocol.MaxDatagramBytes + 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotSize := 0
			socket := &fakeSocket{read: func(buffer []byte) (int, netip.AddrPort, error) {
				gotSize = len(buffer)
				return test.n, netip.AddrPort{}, nil
			}}
			relay, err := New(socket, fixture.store, Config{})
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			t.Cleanup(func() { _ = relay.Close() })
			if err := relay.Run(); err == nil {
				t.Fatal("Run() accepted impossible read count")
			}
			if gotSize != protocol.MaxDatagramBytes+1 {
				t.Fatalf("read buffer = %d, want %d", gotSize, protocol.MaxDatagramBytes+1)
			}
		})
	}

	allocation := fixture.addRoom(t, "mapped-room", 1)
	endpoint := netip.MustParseAddrPort("192.0.2.44:4400")
	mapped := netip.AddrPortFrom(netip.AddrFrom16(endpoint.Addr().As16()), endpoint.Port())
	wire := helloDatagram("mapped-room", allocation.Grants[0].SessionID, allocation.Grants[0].GrantID, filled16(0xb1))
	reads := 0
	socket := &fakeSocket{read: func(buffer []byte) (int, netip.AddrPort, error) {
		reads++
		if reads == 1 {
			return copy(buffer, wire), mapped, nil
		}
		return 0, netip.AddrPort{}, io.EOF
	}}
	relay, err := New(socket, fixture.store, Config{})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	if err := relay.Run(); err == nil {
		t.Fatal("Run() terminal read error = nil")
	}
	writes, _, _, _ := socket.snapshot()
	if len(writes) != 1 || writes[0].endpoint != endpoint {
		t.Fatalf("normalized response endpoint = %v", writes)
	}
}

func TestDispatchNormalizesMappedSourceBeforeExactEndpointBinding(t *testing.T) {
	fixture := newStoreFixture(t, store.DefaultLimits())
	allocation := fixture.addRoom(t, "room", 1)
	socket := new(fakeSocket)
	relay, err := New(socket, fixture.store, Config{})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	endpoint := netip.MustParseAddrPort("192.0.2.45:4450")
	mapped := netip.AddrPortFrom(netip.AddrFrom16(endpoint.Addr().As16()), endpoint.Port())
	wire := helloDatagram("room", allocation.Grants[0].SessionID, allocation.Grants[0].GrantID, filled16(0xb2))
	if err := relay.dispatch(wire, mapped); err != nil {
		t.Fatalf("dispatch(): %v", err)
	}
	writes, _, _, _ := socket.snapshot()
	if len(writes) != 1 || writes[0].endpoint != endpoint {
		t.Fatalf("response endpoint = %v, want %v", writes, endpoint)
	}
}

func TestCloseIsConcurrentIdempotentAndRunLifecycleIsDeterministic(t *testing.T) {
	fixture := newStoreFixture(t, store.DefaultLimits())

	t.Run("close before run", func(t *testing.T) {
		socket := new(fakeSocket)
		relay, err := New(socket, fixture.store, Config{})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		if err := relay.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
		if err := relay.Run(); err != nil {
			t.Fatalf("Run() after Close = %v", err)
		}
	})

	t.Run("concurrent close and second run", func(t *testing.T) {
		entered := make(chan struct{})
		closed := make(chan struct{})
		var closeSignal sync.Once
		socket := &fakeSocket{read: func([]byte) (int, netip.AddrPort, error) {
			close(entered)
			<-closed
			return 0, netip.AddrPort{}, net.ErrClosed
		}}
		relay, err := New(&closeSignalSocket{fakeSocket: socket, closeSignal: &closeSignal, closed: closed}, fixture.store, Config{})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		runDone := make(chan error, 1)
		go func() { runDone <- relay.Run() }()
		<-entered
		if err := relay.Run(); err == nil {
			t.Fatal("second Run() was not rejected")
		}
		var wait sync.WaitGroup
		for range 16 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if err := relay.Close(); err != nil {
					t.Errorf("Close(): %v", err)
				}
			}()
		}
		wait.Wait()
		if err := <-runDone; err != nil {
			t.Fatalf("Run() after local Close = %v", err)
		}
		_, _, _, closes := socket.snapshot()
		if closes != 1 {
			t.Fatalf("raw Close calls = %d, want 1", closes)
		}
	})
}

type closeSignalSocket struct {
	*fakeSocket
	closeSignal *sync.Once
	closed      chan struct{}
}

func (socket *closeSignalSocket) Close() error {
	socket.closeSignal.Do(func() { close(socket.closed) })
	return socket.fakeSocket.Close()
}

func TestFatalRandomReturnsSafeRunErrorWithoutDropOrDiagnostic(t *testing.T) {
	limits := store.DefaultLimits()
	fakeClock := &testClock{reading: clock.Reading{Wall: time.Now().UTC().Add(time.Minute)}}
	random := new(deterministicReader)
	rooms, err := store.New(store.Config{Limits: limits, Now: fakeClock.now, Random: random})
	if err != nil {
		t.Fatalf("store.New(): %v", err)
	}
	fixture := &storeFixture{store: rooms, fakeClock: fakeClock, random: random}
	allocation := fixture.addRoom(t, "room", 1)
	client := fixture.bindDirect(t, "room", allocation.Grants[0], netip.MustParseAddrPort("192.0.2.89:4899"), 0xc0)
	payload := []byte("gameplay-payload-f2ec4b8c")
	secrets := [][]byte{payload, client.secret[:], client.key[:]}
	var encodedSentinels []string
	for _, secret := range secrets {
		encodedSentinels = append(encodedSentinels, string(secret),
			base64.RawURLEncoding.EncodeToString(secret), hex.EncodeToString(secret))
	}
	random.fail(errors.New(strings.Join(encodedSentinels, "|")))
	wire := helloDatagram("room", allocation.Grants[0].SessionID, allocation.Grants[0].GrantID, filled16(0xc1))
	read := false
	socket := &fakeSocket{read: func(buffer []byte) (int, netip.AddrPort, error) {
		if read {
			return 0, netip.AddrPort{}, io.EOF
		}
		read = true
		return copy(buffer, wire), netip.MustParseAddrPort("192.0.2.90:4900"), nil
	}}
	relay, err := New(socket, rooms, Config{})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	err = relay.Run()
	if err == nil {
		t.Fatal("Run() fatal random error = nil")
	}
	for _, encoded := range encodedSentinels {
		if strings.Contains(err.Error(), encoded) {
			t.Fatalf("Run error disclosed sentinel form %q", encoded)
		}
	}
	if counters := relay.Counters(); counters.UDPDropped != 0 || dropTotal(counters.DropReasons) != 0 {
		t.Fatalf("fatal random entered drop counters: %#v", counters)
	}
}

func TestRelaySourceContainsNoPacketLoggingGoroutineOrQueue(t *testing.T) {
	source, err := os.ReadFile("udp.go")
	if err != nil {
		t.Fatalf("ReadFile(udp.go): %v", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "udp.go", source, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(imports): %v", err)
	}
	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, `"`)
		if path == "fmt" || path == "log" || path == "log/slog" {
			t.Fatalf("forbidden diagnostic import %q", path)
		}
	}
	file, err = parser.ParseFile(token.NewFileSet(), "udp.go", source, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.GoStmt:
			t.Error("udp.go starts a goroutine")
		case *ast.ChanType:
			t.Error("udp.go declares a channel/queue")
		}
		return true
	})
	for _, forbidden := range [][]byte{[]byte("fmt.Print"), []byte("log."), []byte("slog.")} {
		if bytes.Contains(source, forbidden) {
			t.Fatalf("udp.go contains forbidden diagnostic call %q", forbidden)
		}
	}
}

func TestRealLoopbackEndToEnd(t *testing.T) {
	limits := store.DefaultLimits()
	fixture := newStoreFixture(t, limits)
	room := fixture.addRoom(t, "room", 2)
	otherRoom := fixture.addRoom(t, "other-room", 1)
	expiryRoom := fixture.addRoom(t, "expiry-room", 2)

	relayConn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatalf("ListenUDP(relay): %v", err)
	}
	relay, err := New(relayConn, fixture.store, Config{})
	if err != nil {
		_ = relayConn.Close()
		t.Fatalf("New(): %v", err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- relay.Run() }()
	relayEndpoint := relayConn.LocalAddr().(*net.UDPAddr).AddrPort()

	a := newLoopClient(t)
	b := newLoopClient(t)
	c := newLoopClient(t)
	d := newLoopClient(t)
	e := newLoopClient(t)
	rogue := newLoopClient(t)
	newA := newLoopClient(t)
	clients := []*loopClient{a, b, c, d, e, rogue, newA}
	defer func() {
		for _, client := range clients {
			_ = client.conn.Close()
		}
	}()

	a.bind(t, relayEndpoint, "room", room.Grants[0], 0xd1)
	b.bind(t, relayEndpoint, "room", room.Grants[1], 0xd2)
	c.bind(t, relayEndpoint, "other-room", otherRoom.Grants[0], 0xd3)
	d.bind(t, relayEndpoint, "expiry-room", expiryRoom.Grants[0], 0xd4)
	e.bind(t, relayEndpoint, "expiry-room", expiryRoom.Grants[1], 0xd5)

	payload := []byte{0, 1, 2, 0xfe, 0xff, 'x'}
	a.sendData(t, relayEndpoint, 1, payload)
	b.expectData(t, a.participantID, 1, payload)
	a.expectSilence(t)
	c.expectSilence(t)

	wrongSource := a.data(2, []byte("wrong-source"))
	rogue.send(t, relayEndpoint, wrongSource)
	b.expectSilence(t)
	a.send(t, relayEndpoint, wrongSource)
	b.expectData(t, a.participantID, 2, []byte("wrong-source"))

	a.sendData(t, relayEndpoint, 4, []byte("four"))
	b.expectData(t, a.participantID, 4, []byte("four"))
	a.sendData(t, relayEndpoint, 3, []byte("three"))
	b.expectData(t, a.participantID, 3, []byte("three"))
	a.sendData(t, relayEndpoint, 3, []byte("three"))
	b.expectSilence(t)

	newA.bind(t, relayEndpoint, "room", room.Grants[0], 0xe1)
	a.sendData(t, relayEndpoint, 5, []byte("old-binding"))
	b.expectSilence(t)
	newA.sendData(t, relayEndpoint, 1, []byte("new-binding"))
	b.expectData(t, newA.participantID, 1, []byte("new-binding"))

	if err := fixture.store.EndRoom("room"); err != nil {
		t.Fatalf("EndRoom(): %v", err)
	}
	newA.sendData(t, relayEndpoint, 2, []byte("after-delete"))
	b.expectSilence(t)

	fixture.fakeClock.set(store.HardMaxBindingTTL)
	d.sendData(t, relayEndpoint, 1, []byte("at-expiry"))
	e.expectSilence(t)

	if err := relay.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() after cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not join after Close")
	}
}

type loopClient struct {
	conn                             *net.UDPConn
	roomID, sessionID, participantID string
	bindingID                        protocol.Bytes16
	key                              protocol.Bytes32
}

func newLoopClient(t testing.TB) *loopClient {
	t.Helper()
	conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatalf("ListenUDP(client): %v", err)
	}
	return &loopClient{conn: conn}
}

func (client *loopClient) bind(t testing.TB, relayEndpoint netip.AddrPort, roomID string, grant store.GrantAllocation, nonceByte byte) {
	t.Helper()
	nonce := filled16(nonceByte)
	client.send(t, relayEndpoint, helloDatagram(roomID, grant.SessionID, grant.GrantID, nonce))
	challengeEnvelope := client.receive(t)
	challenge := challengeEnvelope.GetChallenge()
	if challenge == nil {
		t.Fatalf("HELLO response body = %T", challengeEnvelope.Body)
	}
	candidateID := fixed16(t, challenge.CandidateId)
	serverNonce := fixed32(t, challenge.ServerNonce)
	secret := *grant.GrantSecret
	authTag := protocol.AuthTag(secret, protocol.Revision, roomID, grant.SessionID, grant.GrantID,
		candidateID, nonce, serverNonce)
	client.send(t, relayEndpoint, marshalClient(&relayv1.Envelope{
		ProtocolRevision: protocol.Revision, RoomId: roomID, SessionId: grant.SessionID, AuthTag: authTag[:],
		Body: &relayv1.Envelope_Auth{Auth: &relayv1.Auth{CandidateId: candidateID[:]}},
	}))
	boundEnvelope := client.receive(t)
	bound := boundEnvelope.GetBound()
	if bound == nil {
		t.Fatalf("AUTH response body = %T", boundEnvelope.Body)
	}
	bindingID := fixed16(t, bound.BindingId)
	key := protocol.BindingKey(secret, protocol.Revision, roomID, grant.SessionID, grant.GrantID,
		candidateID, nonce, serverNonce)
	wantTag := protocol.BoundTag(key, protocol.Revision, roomID, grant.SessionID,
		candidateID, bindingID, bound.ExpiresUnixMs)
	if !protocol.EqualTag(wantTag, boundEnvelope.AuthTag) {
		t.Fatal("BOUND tag mismatch")
	}
	client.roomID, client.sessionID, client.participantID = roomID, grant.SessionID, grant.ParticipantID
	client.bindingID, client.key = bindingID, key
}

func (client *loopClient) data(sequence uint64, payload []byte) []byte {
	return (testClient{roomID: client.roomID, sessionID: client.sessionID, participantID: client.participantID,
		bindingID: client.bindingID, key: client.key}).data(sequence, payload)
}

func (client *loopClient) sendData(t testing.TB, relayEndpoint netip.AddrPort, sequence uint64, payload []byte) {
	t.Helper()
	client.send(t, relayEndpoint, client.data(sequence, payload))
}

func (client *loopClient) send(t testing.TB, endpoint netip.AddrPort, wire []byte) {
	t.Helper()
	if err := client.conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetWriteDeadline(): %v", err)
	}
	written, err := client.conn.WriteToUDPAddrPort(wire, endpoint)
	if err != nil || written != len(wire) {
		t.Fatalf("WriteToUDPAddrPort() = (%d, %v), want %d", written, err, len(wire))
	}
}

func (client *loopClient) receive(t testing.TB) *relayv1.Envelope {
	t.Helper()
	if err := client.conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline(): %v", err)
	}
	buffer := make([]byte, protocol.MaxDatagramBytes+1)
	read, _, err := client.conn.ReadFromUDPAddrPort(buffer)
	if err != nil {
		t.Fatalf("ReadFromUDPAddrPort(): %v", err)
	}
	return unmarshalEnvelope(t, buffer[:read])
}

func (client *loopClient) expectData(t testing.TB, participant string, sequence uint64, payload []byte) {
	t.Helper()
	envelope := client.receive(t)
	serverData := envelope.GetServerData()
	if serverData == nil || serverData.SenderParticipantId != participant || envelope.Sequence != sequence ||
		!bytes.Equal(serverData.Payload, payload) {
		t.Fatalf("ServerData = %#v", envelope)
	}
}

func (client *loopClient) expectSilence(t testing.TB) {
	t.Helper()
	if err := client.conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline(): %v", err)
	}
	buffer := make([]byte, protocol.MaxDatagramBytes+1)
	if read, endpoint, err := client.conn.ReadFromUDPAddrPort(buffer); err == nil {
		t.Fatalf("unexpected datagram: %d bytes from %v", read, endpoint)
	} else if networkError, ok := err.(net.Error); !ok || !networkError.Timeout() {
		t.Fatalf("ReadFromUDPAddrPort() = %v, want timeout", err)
	}
}

func FuzzDispatch(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xff})
	f.Add(make([]byte, protocol.MaxDatagramBytes+1))
	f.Add(helloDatagram("room", "room-session-a", filled16(1), filled16(0xf1)))

	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > protocol.MaxDatagramBytes+1 {
			input = input[:protocol.MaxDatagramBytes+1]
		}
		limits := store.DefaultLimits()
		limits.MaxOpenRooms, limits.MaxRoomRecords = 1, 1
		limits.MaxRoomCapacity, limits.MaxActiveSessions = 1, 1
		fixture := newStoreFixture(t, limits)
		_ = fixture.addRoom(t, "room", 1)
		socket := new(fakeSocket)
		relay, err := New(socket, fixture.store, Config{})
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		if err := relay.dispatch(input, netip.MustParseAddrPort("192.0.2.200:5000")); err != nil {
			t.Fatalf("dispatch(): %v", err)
		}
		counters := relay.Counters()
		if counters.UDPReceived != 1 || counters.UDPDropped != dropTotal(counters.DropReasons) || counters.UDPDropped > 1 {
			t.Fatalf("counters = %#v", counters)
		}
		writes, deadlines, _, _ := socket.snapshot()
		if len(writes) > 1 || len(deadlines) > 1 {
			t.Fatalf("unbounded output: writes=%d deadlines=%d", len(writes), len(deadlines))
		}
		for _, write := range writes {
			if len(write.data) > protocol.MaxDatagramBytes {
				t.Fatalf("output bytes = %d", len(write.data))
			}
		}
		snapshot, err := fixture.store.GetRoom("room")
		if err != nil || len(snapshot.Participants) != 1 {
			t.Fatalf("bounded store snapshot = (%#v, %v)", snapshot, err)
		}
	})
}

func helloDatagram(roomID, sessionID string, grantID, nonce protocol.Bytes16) []byte {
	return marshalClient(&relayv1.Envelope{
		ProtocolRevision: protocol.Revision, RoomId: roomID, SessionId: sessionID,
		Body: &relayv1.Envelope_Hello{Hello: &relayv1.Hello{
			GrantId: grantID[:], ClientNonce: nonce[:], Padding: make([]byte, protocol.MinHelloBytes),
		}},
	})
}

func marshalClient(envelope *relayv1.Envelope) []byte {
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return wire
}

func unmarshalEnvelope(t testing.TB, wire []byte) *relayv1.Envelope {
	t.Helper()
	envelope := new(relayv1.Envelope)
	if err := proto.Unmarshal(wire, envelope); err != nil {
		t.Fatalf("proto.Unmarshal(): %v", err)
	}
	return envelope
}

func fixed16(t testing.TB, input []byte) (output protocol.Bytes16) {
	t.Helper()
	if len(input) != len(output) {
		t.Fatalf("fixed16 input = %d bytes", len(input))
	}
	copy(output[:], input)
	return output
}

func fixed32(t testing.TB, input []byte) (output protocol.Bytes32) {
	t.Helper()
	if len(input) != len(output) {
		t.Fatalf("fixed32 input = %d bytes", len(input))
	}
	copy(output[:], input)
	return output
}

func filled16(value byte) (output protocol.Bytes16) {
	for index := range output {
		output[index] = value
	}
	return output
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalEndpoints(left, right []netip.AddrPort) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func dropTotal(reasons DropReasons) uint64 {
	return reasons.Malformed + reasons.Oversized + reasons.UnsupportedVersion + reasons.UnknownGrant +
		reasons.AuthFailed + reasons.Replay + reasons.Expired + reasons.Revoked + reasons.WrongRoom +
		reasons.WrongEndpoint + reasons.NotBound + reasons.RateLimited + reasons.FanoutLimited + reasons.Draining
}

func dropCount(reasons DropReasons, reason store.RejectReason) uint64 {
	switch reason {
	case store.RejectMalformed:
		return reasons.Malformed
	case store.RejectOversized:
		return reasons.Oversized
	case store.RejectUnsupportedVersion:
		return reasons.UnsupportedVersion
	case store.RejectUnknownGrant:
		return reasons.UnknownGrant
	case store.RejectAuthFailed:
		return reasons.AuthFailed
	case store.RejectReplay:
		return reasons.Replay
	case store.RejectExpired:
		return reasons.Expired
	case store.RejectRevoked:
		return reasons.Revoked
	case store.RejectWrongRoom:
		return reasons.WrongRoom
	case store.RejectWrongEndpoint:
		return reasons.WrongEndpoint
	case store.RejectNotBound:
		return reasons.NotBound
	case store.RejectRateLimited:
		return reasons.RateLimited
	case store.RejectFanoutLimited:
		return reasons.FanoutLimited
	case store.RejectDraining:
		return reasons.Draining
	default:
		return 0
	}
}
