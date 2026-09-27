package udprelay

import (
	"errors"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	relayv1 "github.com/gyungsubLee/go-lobby-relay/gen/go/relay/v1"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"github.com/gyungsubLee/go-lobby-relay/internal/relayroom"
)

const (
	defaultWriteTimeout = 2 * time.Millisecond
	maxWriteTimeout     = 20 * time.Millisecond
)

var (
	errInvalidConfig = errors.New("udprelay: invalid configuration")
	errRead          = errors.New("udprelay: socket read failed")
	errClose         = errors.New("udprelay: socket close failed")
	errAlreadyRun    = errors.New("udprelay: already running")
	errInternal      = errors.New("udprelay: internal failure")
)

type udpSocket interface {
	ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error)
	WriteToUDPAddrPort([]byte, netip.AddrPort) (int, error)
	SetWriteDeadline(time.Time) error
	Close() error
}

type Config struct {
	WriteTimeout time.Duration
	Now          func() time.Time
}

type DropReasons struct {
	Malformed          uint64
	Oversized          uint64
	UnsupportedVersion uint64
	UnknownGrant       uint64
	AuthFailed         uint64
	Replay             uint64
	Expired            uint64
	Revoked            uint64
	WrongRoom          uint64
	WrongEndpoint      uint64
	NotBound           uint64
	RateLimited        uint64
	FanoutLimited      uint64
	Draining           uint64
}

type Counters struct {
	UDPReceived          uint64
	ClientDataAccepted   uint64
	UDPDropped           uint64
	FanoutWriteAttempts  uint64
	FanoutWriteSuccesses uint64
	FanoutWriteErrors    uint64
	DropReasons          DropReasons
}

type Server struct {
	socket       udpSocket
	rooms        *relayroom.Store
	writeTimeout time.Duration
	now          func() time.Time

	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
	runMu     sync.Mutex
	run       bool

	countersMu sync.Mutex
	counters   Counters
}

func New(socket udpSocket, rooms *relayroom.Store, config Config) (*Server, error) {
	if nilSocket(socket) || rooms == nil || config.WriteTimeout < 0 || config.WriteTimeout > maxWriteTimeout {
		return nil, errInvalidConfig
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = defaultWriteTimeout
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Server{socket: socket, rooms: rooms, writeTimeout: config.WriteTimeout, now: config.Now}, nil
}

func nilSocket(socket udpSocket) bool {
	if socket == nil {
		return true
	}
	value := reflect.ValueOf(socket)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func (relay *Server) Run() error {
	relay.runMu.Lock()
	if relay.run {
		relay.runMu.Unlock()
		return errAlreadyRun
	}
	if relay.closed.Load() {
		relay.runMu.Unlock()
		return nil
	}
	relay.run = true
	relay.runMu.Unlock()

	buffer := make([]byte, protocol.MaxDatagramBytes+1)
	for {
		if relay.closed.Load() {
			return nil
		}
		read, endpoint, err := relay.socket.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if relay.closed.Load() {
				return nil
			}
			return errRead
		}
		if read < 0 || read > len(buffer) {
			return errRead
		}
		endpoint = normalizeEndpoint(endpoint)
		if err := relay.handleDatagram(buffer[:read], endpoint); err != nil {
			return err
		}
	}
}

func normalizeEndpoint(endpoint netip.AddrPort) netip.AddrPort {
	if !endpoint.IsValid() {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
}

func (relay *Server) Close() error {
	relay.closeOnce.Do(func() {
		relay.closed.Store(true)
		if relay.socket.Close() != nil {
			relay.closeErr = errClose
		}
	})
	return relay.closeErr
}

func (relay *Server) Counters() Counters {
	relay.countersMu.Lock()
	defer relay.countersMu.Unlock()
	return relay.counters
}

func (relay *Server) handleDatagram(datagram []byte, endpoint netip.AddrPort) error {
	endpoint = normalizeEndpoint(endpoint)
	relay.countersMu.Lock()
	relay.counters.UDPReceived++
	relay.countersMu.Unlock()

	envelope, err := protocol.DecodeClient(datagram)
	if err != nil {
		reason := protocolRejectReason(err)
		if relay.rooms.AdmitPreauth(relayroom.PreauthRequest{Endpoint: endpoint, InputBytes: len(datagram)}) != relayroom.RejectNone {
			reason = relayroom.RejectRateLimited
		}
		relay.recordDrop(reason)
		return nil
	}

	switch body := envelope.Body.(type) {
	case *relayv1.Envelope_Hello:
		result, reason := relay.rooms.AdmitHello(relayroom.HelloRequest{
			RoomID: envelope.RoomId, SessionID: envelope.SessionId,
			GrantID: copy16(body.Hello.GrantId), ClientNonce: copy16(body.Hello.ClientNonce),
			Endpoint: endpoint, InputBytes: len(datagram),
		})
		if reason != relayroom.RejectNone {
			return relay.reject(reason)
		}
		response, err := protocol.EncodeServer(&relayv1.Envelope{
			ProtocolRevision: protocol.Revision, RoomId: envelope.RoomId, SessionId: envelope.SessionId,
			Body: &relayv1.Envelope_Challenge{Challenge: &relayv1.Challenge{
				CandidateId: result.CandidateID[:], ServerNonce: result.ServerNonce[:],
				ExpiresUnixMs: result.ExpiresUnixMS,
			}},
		})
		if err == nil && len(response) < len(datagram) {
			relay.writeOne(response, endpoint)
		}
		return nil

	case *relayv1.Envelope_Auth:
		result, reason := relay.rooms.AdmitAuth(relayroom.AuthRequest{
			RoomID: envelope.RoomId, SessionID: envelope.SessionId,
			CandidateID: copy16(body.Auth.CandidateId), Endpoint: endpoint,
			AuthTag: copy32(envelope.AuthTag), InputBytes: len(datagram),
		})
		if reason != relayroom.RejectNone {
			return relay.reject(reason)
		}
		response, err := protocol.EncodeServer(&relayv1.Envelope{
			ProtocolRevision: protocol.Revision, RoomId: envelope.RoomId, SessionId: envelope.SessionId,
			AuthTag: result.AuthTag[:],
			Body: &relayv1.Envelope_Bound{Bound: &relayv1.Bound{
				BindingId: result.BindingID[:], ExpiresUnixMs: result.ExpiresUnixMS,
			}},
		})
		if err == nil {
			relay.writeOne(response, endpoint)
		}
		return nil

	case *relayv1.Envelope_ClientData:
		admitted, reason := relay.rooms.AdmitClientData(relayroom.ClientDataRequest{
			RoomID: envelope.RoomId, SessionID: envelope.SessionId,
			BindingID: copy16(body.ClientData.BindingId), Sequence: envelope.Sequence,
			Payload: body.ClientData.Payload, Endpoint: endpoint, AuthTag: copy32(envelope.AuthTag),
		}, len(datagram))
		if reason != relayroom.RejectNone {
			return relay.reject(reason)
		}
		response, err := protocol.EncodeServer(&relayv1.Envelope{
			ProtocolRevision: protocol.Revision, Sequence: admitted.Sequence(),
			RoomId: admitted.RoomID(), SessionId: admitted.SessionID(),
			Body: &relayv1.Envelope_ServerData{ServerData: &relayv1.ServerData{
				SenderParticipantId: admitted.SenderParticipantID(), Payload: body.ClientData.Payload,
			}},
		})
		if err != nil {
			relay.recordDrop(protocolRejectReason(err))
			return nil
		}
		plan, reason := relay.rooms.PlanFanout(admitted, len(response))
		if reason != relayroom.RejectNone {
			return relay.reject(reason)
		}
		relay.countersMu.Lock()
		relay.counters.ClientDataAccepted++
		relay.countersMu.Unlock()
		relay.writeFanout(response, plan.Recipients)
		return nil

	case *relayv1.Envelope_Ping:
		reason := relay.rooms.AdmitPing(relayroom.PingRequest{
			RoomID: envelope.RoomId, SessionID: envelope.SessionId,
			BindingID: copy16(body.Ping.BindingId), Sequence: envelope.Sequence,
			Endpoint: endpoint, AuthTag: copy32(envelope.AuthTag),
		}, len(datagram))
		if reason != relayroom.RejectNone {
			return relay.reject(reason)
		}
		return nil
	default:
		return errInternal
	}
}

func copy16(input []byte) (output protocol.Bytes16) {
	copy(output[:], input)
	return output
}

func copy32(input []byte) (output protocol.Bytes32) {
	copy(output[:], input)
	return output
}

func protocolRejectReason(err error) relayroom.RejectReason {
	switch protocol.ReasonOf(err) {
	case protocol.ReasonOversized:
		return relayroom.RejectOversized
	case protocol.ReasonUnsupportedVersion:
		return relayroom.RejectUnsupportedVersion
	default:
		return relayroom.RejectMalformed
	}
}

func (relay *Server) reject(reason relayroom.RejectReason) error {
	if reason == relayroom.RejectFatalRandom {
		return errInternal
	}
	relay.recordDrop(reason)
	return nil
}

func (relay *Server) writeOne(datagram []byte, endpoint netip.AddrPort) {
	if relay.socket.SetWriteDeadline(relay.now().Add(relay.writeTimeout)) != nil {
		return
	}
	_, _ = relay.socket.WriteToUDPAddrPort(datagram, endpoint)
}

func (relay *Server) writeFanout(datagram []byte, recipients []netip.AddrPort) {
	if len(recipients) == 0 || relay.socket.SetWriteDeadline(relay.now().Add(relay.writeTimeout)) != nil {
		return
	}
	for _, recipient := range recipients {
		relay.countersMu.Lock()
		relay.counters.FanoutWriteAttempts++
		relay.countersMu.Unlock()
		written, err := relay.socket.WriteToUDPAddrPort(datagram, recipient)
		relay.countersMu.Lock()
		if err != nil || written != len(datagram) {
			relay.counters.FanoutWriteErrors++
			relay.countersMu.Unlock()
			return
		}
		relay.counters.FanoutWriteSuccesses++
		relay.countersMu.Unlock()
	}
}

func (relay *Server) recordDrop(reason relayroom.RejectReason) {
	relay.countersMu.Lock()
	defer relay.countersMu.Unlock()
	recorded := true
	switch reason {
	case relayroom.RejectMalformed:
		relay.counters.DropReasons.Malformed++
	case relayroom.RejectOversized:
		relay.counters.DropReasons.Oversized++
	case relayroom.RejectUnsupportedVersion:
		relay.counters.DropReasons.UnsupportedVersion++
	case relayroom.RejectUnknownGrant:
		relay.counters.DropReasons.UnknownGrant++
	case relayroom.RejectAuthFailed:
		relay.counters.DropReasons.AuthFailed++
	case relayroom.RejectReplay:
		relay.counters.DropReasons.Replay++
	case relayroom.RejectExpired:
		relay.counters.DropReasons.Expired++
	case relayroom.RejectRevoked:
		relay.counters.DropReasons.Revoked++
	case relayroom.RejectWrongRoom:
		relay.counters.DropReasons.WrongRoom++
	case relayroom.RejectWrongEndpoint:
		relay.counters.DropReasons.WrongEndpoint++
	case relayroom.RejectNotBound:
		relay.counters.DropReasons.NotBound++
	case relayroom.RejectRateLimited:
		relay.counters.DropReasons.RateLimited++
	case relayroom.RejectFanoutLimited:
		relay.counters.DropReasons.FanoutLimited++
	case relayroom.RejectDraining:
		relay.counters.DropReasons.Draining++
	default:
		recorded = false
	}
	if recorded {
		relay.counters.UDPDropped++
	}
}
