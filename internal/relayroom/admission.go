package relayroom

import (
	"math"
	"net/netip"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"golang.org/x/time/rate"
)

const preauthSourceIdleTTL = 60 * time.Second

type RejectReason string

const (
	RejectNone               RejectReason = ""
	RejectMalformed          RejectReason = "malformed"
	RejectOversized          RejectReason = "oversized"
	RejectUnsupportedVersion RejectReason = "unsupported_version"
	RejectUnknownGrant       RejectReason = "unknown_grant"
	RejectAuthFailed         RejectReason = "auth_failed"
	RejectReplay             RejectReason = "replay"
	RejectExpired            RejectReason = "expired"
	RejectRevoked            RejectReason = "revoked"
	RejectWrongRoom          RejectReason = "wrong_room"
	RejectWrongEndpoint      RejectReason = "wrong_endpoint"
	RejectNotBound           RejectReason = "not_bound"
	RejectRateLimited        RejectReason = "rate_limited"
	RejectFanoutLimited      RejectReason = "fanout_limited"
	RejectDraining           RejectReason = "draining"
	RejectFatalRandom        RejectReason = "fatal_random"
)

type PreauthRequest struct {
	Endpoint   netip.AddrPort
	InputBytes int
}

type ClientDataRequest struct {
	RoomID, SessionID string
	BindingID         protocol.Bytes16
	Sequence          uint64
	Payload           []byte
	Endpoint          netip.AddrPort
	AuthTag           protocol.Bytes32
}

type PingRequest struct {
	RoomID, SessionID string
	BindingID         protocol.Bytes16
	Sequence          uint64
	Endpoint          netip.AddrPort
	AuthTag           protocol.Bytes32
}

type AdmittedClientData struct {
	store               *Store
	roomID              string
	sessionID           string
	senderParticipantID string
	bindingID           protocol.Bytes16
	bindingGeneration   uint64
	sequence            uint64
}

func (admitted AdmittedClientData) RoomID() string              { return admitted.roomID }
func (admitted AdmittedClientData) SessionID() string           { return admitted.sessionID }
func (admitted AdmittedClientData) SenderParticipantID() string { return admitted.senderParticipantID }
func (admitted AdmittedClientData) Sequence() uint64            { return admitted.sequence }

type FanoutPlan struct {
	RoomID, SessionID   string
	SenderParticipantID string
	Sequence            uint64
	Recipients          []netip.AddrPort
}

type replayWindow struct {
	highest     uint64
	bitmap      uint64
	initialized bool
}

func (window *replayWindow) accept(sequence uint64) bool {
	if !window.initialized {
		window.highest = sequence
		window.bitmap = 1
		window.initialized = true
		return true
	}
	if sequence > window.highest {
		shift := sequence - window.highest
		if shift >= 64 {
			window.bitmap = 1
		} else {
			window.bitmap = window.bitmap<<shift | 1
		}
		window.highest = sequence
		return true
	}
	delta := window.highest - sequence
	if delta >= 64 {
		return false
	}
	bit := uint64(1) << delta
	if window.bitmap&bit != 0 {
		return false
	}
	window.bitmap |= bit
	return true
}

type preauthSource struct {
	lastObserved time.Duration
	packets      *rate.Limiter
	bytes        *rate.Limiter
}

func (store *Store) AdmitPreauth(request PreauthRequest) RejectReason {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.admitPreauthLocked(request.Endpoint, request.InputBytes, store.now().Mono)
}

func (store *Store) AdmitClientData(request ClientDataRequest, inputBytes int) (AdmittedClientData, RejectReason) {
	store.mu.Lock()
	defer store.mu.Unlock()
	reading := store.now()
	grant, binding, reason := store.admitAuthenticatedLocked(
		request.RoomID, request.SessionID, request.BindingID, request.Sequence, request.Endpoint,
		request.AuthTag, inputBytes, reading.Mono,
		func(key protocol.Bytes32) protocol.Bytes32 {
			return protocol.ClientDataTag(key, protocol.Revision, request.RoomID, request.SessionID,
				request.BindingID, request.Sequence, request.Payload)
		},
	)
	if reason != RejectNone {
		return AdmittedClientData{}, reason
	}
	return AdmittedClientData{
		store: store, roomID: grant.roomID, sessionID: grant.sessionID,
		senderParticipantID: grant.participantID, bindingID: binding.id,
		bindingGeneration: binding.generation, sequence: request.Sequence,
	}, RejectNone
}

func (store *Store) AdmitPing(request PingRequest, inputBytes int) RejectReason {
	store.mu.Lock()
	defer store.mu.Unlock()
	reading := store.now()
	_, _, reason := store.admitAuthenticatedLocked(
		request.RoomID, request.SessionID, request.BindingID, request.Sequence, request.Endpoint,
		request.AuthTag, inputBytes, reading.Mono,
		func(key protocol.Bytes32) protocol.Bytes32 {
			return protocol.PingTag(key, protocol.Revision, request.RoomID, request.SessionID,
				request.BindingID, request.Sequence)
		},
	)
	return reason
}

func (store *Store) PlanFanout(admitted AdmittedClientData, outputBytes int) (FanoutPlan, RejectReason) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if admitted.store != store {
		return FanoutPlan{}, RejectNotBound
	}
	reading := store.now()
	grant := store.bindingsByID[admitted.bindingID]
	if grant == nil || grant.binding == nil || grant.binding.id != admitted.bindingID ||
		grant.binding.generation != admitted.bindingGeneration || grant.roomID != admitted.roomID ||
		grant.sessionID != admitted.sessionID || grant.participantID != admitted.senderParticipantID {
		return FanoutPlan{}, RejectNotBound
	}
	room, reason := store.liveAuthority(grant, reading.Mono)
	if reason != RejectNone {
		return FanoutPlan{}, reason
	}
	binding := grant.binding
	if binding == nil || reading.Mono >= binding.deadline {
		store.clearBinding(grant)
		return FanoutPlan{}, RejectExpired
	}
	if outputBytes < 0 || outputBytes > protocol.MaxDatagramBytes {
		return FanoutPlan{}, RejectOversized
	}

	recipients := make([]netip.AddrPort, 0, len(room.grants)-1)
	for _, recipient := range room.grants {
		if recipient == grant || !grantLive(recipient) {
			continue
		}
		if reading.Mono >= recipient.monoDeadline {
			store.terminalGrant(recipient, GrantStateExpired)
			continue
		}
		store.expireRelayState(recipient, reading.Mono)
		if recipient.binding != nil {
			recipients = append(recipients, recipient.binding.endpoint)
		}
	}
	plan := FanoutPlan{
		RoomID: grant.roomID, SessionID: grant.sessionID, SenderParticipantID: grant.participantID,
		Sequence: admitted.sequence, Recipients: recipients,
	}
	if len(recipients) == 0 {
		return plan, RejectNone
	}
	if outputBytes > math.MaxInt/len(recipients) {
		return FanoutPlan{}, RejectOversized
	}
	plannedBytes := outputBytes * len(recipients)
	if !allowAtomic(limiterTime(reading.Mono),
		limiterCharge{room.fanoutWrites, len(recipients)},
		limiterCharge{room.fanoutBytes, plannedBytes},
		limiterCharge{store.globalFanoutWrites, len(recipients)},
		limiterCharge{store.globalFanoutBytes, plannedBytes},
	) {
		return FanoutPlan{}, RejectFanoutLimited
	}
	return plan, RejectNone
}

func (store *Store) admitAuthenticatedLocked(
	roomID, sessionID string,
	bindingID protocol.Bytes16,
	sequence uint64,
	endpoint netip.AddrPort,
	authTag protocol.Bytes32,
	inputBytes int,
	now time.Duration,
	expectedTag func(protocol.Bytes32) protocol.Bytes32,
) (*grantRecord, *bindingRecord, RejectReason) {
	rejectPreauth := func(reason RejectReason) RejectReason {
		if store.admitPreauthLocked(endpoint, inputBytes, now) != RejectNone {
			return RejectRateLimited
		}
		return reason
	}
	if sequence == 0 || inputBytes < 0 {
		return nil, nil, rejectPreauth(RejectMalformed)
	}
	grant := store.bindingsByID[bindingID]
	if grant == nil || grant.binding == nil || grant.binding.id != bindingID {
		return nil, nil, rejectPreauth(RejectNotBound)
	}
	if grant.state == GrantStateRevoked {
		return nil, nil, rejectPreauth(RejectRevoked)
	}
	if grant.state == GrantStateExpired {
		return nil, nil, rejectPreauth(RejectExpired)
	}
	room := store.roomsByID[grant.roomID]
	if room == nil || room.state != roomStateOpen {
		return nil, nil, rejectPreauth(RejectRevoked)
	}
	if now >= room.monoDeadline {
		reason := rejectPreauth(RejectExpired)
		if reason == RejectExpired {
			store.tombstoneRoom(room, now, GrantStateExpired)
		}
		return nil, nil, reason
	}
	if now >= grant.monoDeadline {
		reason := rejectPreauth(RejectExpired)
		if reason == RejectExpired {
			store.terminalGrant(grant, GrantStateExpired)
		}
		return nil, nil, reason
	}
	if roomID != grant.roomID {
		return nil, nil, rejectPreauth(RejectWrongRoom)
	}
	if sessionID != grant.sessionID {
		return nil, nil, rejectPreauth(RejectAuthFailed)
	}
	binding := grant.binding
	if binding == nil || binding.id != bindingID {
		return nil, nil, rejectPreauth(RejectNotBound)
	}
	if now >= binding.deadline {
		reason := rejectPreauth(RejectExpired)
		if reason == RejectExpired {
			store.clearBinding(grant)
		}
		return nil, nil, reason
	}
	if !endpoint.IsValid() || binding.endpoint != endpoint {
		return nil, nil, rejectPreauth(RejectWrongEndpoint)
	}
	if !protocol.EqualTag(expectedTag(binding.key), authTag[:]) {
		return nil, nil, rejectPreauth(RejectAuthFailed)
	}

	fresh := binding.replay.accept(sequence)
	if !allowAtomic(limiterTime(now),
		limiterCharge{grant.ingressPackets, 1}, limiterCharge{grant.ingressBytes, inputBytes},
		limiterCharge{room.ingressPackets, 1}, limiterCharge{room.ingressBytes, inputBytes},
		limiterCharge{store.authenticatedGlobalPackets, 1}, limiterCharge{store.authenticatedGlobalBytes, inputBytes},
	) {
		return nil, nil, RejectRateLimited
	}
	if !fresh {
		return nil, nil, RejectReplay
	}
	return grant, binding, RejectNone
}

func (store *Store) admitPreauthLocked(endpoint netip.AddrPort, inputBytes int, now time.Duration) RejectReason {
	key := sourceKey(endpoint)
	if !key.IsValid() {
		return RejectRateLimited
	}
	if inputBytes < 0 {
		inputBytes = 0
	}
	limiterNow := limiterTime(now)
	source := store.preauthSources[key]
	if source != nil && now >= saturatingAdd(source.lastObserved, preauthSourceIdleTTL) {
		delete(store.preauthSources, key)
		source = nil
	}
	if source == nil && len(store.preauthSources) >= HardMaxPreauthSources {
		if allowAtomic(limiterNow,
			limiterCharge{store.preauthGlobalPackets, 1},
			limiterCharge{store.preauthGlobalBytes, inputBytes},
		) {
			return RejectRateLimited
		}
		return RejectRateLimited
	}
	if source == nil {
		source = &preauthSource{
			packets: rate.NewLimiter(store.limits.PreauthSourcePacketRate, store.limits.PreauthSourcePacketBurst),
			bytes:   rate.NewLimiter(store.limits.PreauthSourceByteRate, store.limits.PreauthSourceByteBurst),
		}
		if !allowAtomic(limiterNow,
			limiterCharge{source.packets, 1}, limiterCharge{source.bytes, inputBytes},
			limiterCharge{store.preauthGlobalPackets, 1}, limiterCharge{store.preauthGlobalBytes, inputBytes},
		) {
			return RejectRateLimited
		}
		source.lastObserved = now
		store.preauthSources[key] = source
		return RejectNone
	}
	source.lastObserved = now
	if !allowAtomic(limiterNow,
		limiterCharge{source.packets, 1}, limiterCharge{source.bytes, inputBytes},
		limiterCharge{store.preauthGlobalPackets, 1}, limiterCharge{store.preauthGlobalBytes, inputBytes},
	) {
		return RejectRateLimited
	}
	return RejectNone
}

type limiterCharge struct {
	limiter *rate.Limiter
	cost    int
}

func allowAtomic(now time.Time, charges ...limiterCharge) bool {
	for _, charge := range charges {
		if charge.cost > charge.limiter.Burst() || charge.limiter.TokensAt(now) < float64(charge.cost) {
			return false
		}
	}
	for _, charge := range charges {
		if !charge.limiter.AllowN(now, charge.cost) {
			panic("store: limiter changed under store lock")
		}
	}
	return true
}

func sourceKey(endpoint netip.AddrPort) netip.Prefix {
	// Stream carriers tag endpoints with a zone; budgets follow the peer address.
	address := endpoint.Addr().WithZone("").Unmap()
	if address.Is4() {
		return netip.PrefixFrom(address, 32)
	}
	if address.Is6() {
		return netip.PrefixFrom(address, 64).Masked()
	}
	return netip.Prefix{}
}

func limiterTime(now time.Duration) time.Time {
	return time.Unix(0, int64(now))
}
