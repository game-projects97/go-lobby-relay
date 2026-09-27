package store

import (
	"io"
	"sort"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"golang.org/x/time/rate"
)

type GrantState string

const (
	GrantStateIssued  GrantState = "issued"
	GrantStateBound   GrantState = "bound"
	GrantStateExpired GrantState = "expired"
	GrantStateRevoked GrantState = "revoked"
)

type BindingState string

const (
	BindingStateUnbound       BindingState = "unbound"
	BindingStateBound         BindingState = "bound"
	BindingStateRebindPending BindingState = "rebind_pending"
	BindingStateExpired       BindingState = "expired"
	BindingStateRevoked       BindingState = "revoked"
)

type ParticipantDefinition struct {
	ParticipantID  string
	SessionID      string
	GrantExpiresAt time.Time
}

type RoomDefinition struct {
	Capacity     uint32
	ExpiresAt    time.Time
	Participants []ParticipantDefinition
}

type Allocation struct {
	RoomID               string
	CreatedAt, ExpiresAt time.Time
	Capacity             uint32
	Grants               []GrantAllocation
}

type GrantAllocation struct {
	ParticipantID, SessionID string
	GrantID                  protocol.Bytes16
	GrantSecret              *protocol.Bytes32
	GrantExpiresAt           time.Time
	State                    GrantState
}

type RoomSnapshot struct {
	RoomID               string
	CreatedAt, ExpiresAt time.Time
	Capacity             uint32
	Participants         []ParticipantSnapshot
}

type ParticipantSnapshot struct {
	ParticipantID, SessionID string
	GrantExpiresAt           time.Time
	GrantState               GrantState
	BindingState             BindingState
}

type roomRecordState uint8

const (
	roomStateOpen roomRecordState = iota
	roomStateEmpty
	roomStateTombstone
)

type roomRecord struct {
	state             roomRecordState
	capacity          uint32
	createdAt         time.Time
	expiresAt         time.Time
	monoDeadline      time.Duration
	grants            []*grantRecord
	tombstoneDeadline time.Duration
	ingressPackets    *rate.Limiter
	ingressBytes      *rate.Limiter
	fanoutWrites      *rate.Limiter
	fanoutBytes       *rate.Limiter
}

type grantRecord struct {
	roomID         string
	participantID  string
	sessionID      string
	id             protocol.Bytes16
	secret         *protocol.Bytes32
	expiresAt      time.Time
	monoDeadline   time.Duration
	state          GrantState
	bindingState   BindingState
	generation     uint64
	pending        *challengeRecord
	recent         *completedHandshake
	binding        *bindingRecord
	ingressPackets *rate.Limiter
	ingressBytes   *rate.Limiter
}

func (store *Store) CreateRoom(roomID string, definition RoomDefinition) (Allocation, bool, error) {
	canonical, err := canonicalDefinition(roomID, definition, store.limits)
	if err != nil {
		return Allocation{}, false, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	reading := store.now()

	if existing := store.roomsByID[roomID]; existing != nil {
		switch existing.state {
		case roomStateTombstone:
			if reading.Mono < existing.tombstoneDeadline {
				return Allocation{}, false, ErrConflict
			}
			delete(store.roomsByID, roomID)
		case roomStateEmpty:
			return Allocation{}, false, ErrConflict
		case roomStateOpen:
			if roomAccessTerminal(existing, reading.Mono) || !sameDefinition(existing, canonical) {
				return Allocation{}, false, ErrConflict
			}
			return allocationAt(roomID, existing, reading.Mono), false, nil
		default:
			return Allocation{}, false, ErrConflict
		}
	}

	wall := reading.Wall.UTC()
	roomTTL := canonical.expiresAt.Sub(wall)
	roomDeadline, ok := clock.DeadlineAfter(reading.Mono, roomTTL)
	if !ok || roomTTL > store.limits.MaxRoomTTL {
		return Allocation{}, false, ErrInvalid
	}
	grantDeadlines := make([]time.Duration, len(canonical.participants))
	for index, participant := range canonical.participants {
		grantTTL := participant.GrantExpiresAt.Sub(wall)
		grantDeadlines[index], ok = clock.DeadlineAfter(reading.Mono, grantTTL)
		if !ok || grantTTL > store.limits.MaxGrantTTL {
			return Allocation{}, false, ErrInvalid
		}
	}
	if store.openRooms >= store.limits.MaxOpenRooms ||
		len(store.roomsByID) >= store.limits.MaxRoomRecords ||
		store.activeSessions > store.limits.MaxActiveSessions-len(canonical.participants) {
		return Allocation{}, false, ErrCapacity
	}

	// ponytail: CSPRNG reads stay under the one store lock; split reservation/commit only if profiling shows contention.
	grants := make([]*grantRecord, len(canonical.participants))
	stagedIDs := make(map[protocol.Bytes16]struct{}, len(canonical.participants))
	for index, participant := range canonical.participants {
		grantID, ok := store.uniqueGrantID(stagedIDs)
		if !ok {
			return Allocation{}, false, ErrFatalRandom
		}
		var secret protocol.Bytes32
		if _, err := io.ReadFull(store.random, secret[:]); err != nil {
			return Allocation{}, false, ErrFatalRandom
		}
		secretCopy := secret
		grant := &grantRecord{
			roomID:         roomID,
			participantID:  participant.ParticipantID,
			sessionID:      participant.SessionID,
			id:             grantID,
			secret:         &secretCopy,
			expiresAt:      participant.GrantExpiresAt,
			monoDeadline:   grantDeadlines[index],
			state:          GrantStateIssued,
			bindingState:   BindingStateUnbound,
			ingressPackets: rate.NewLimiter(store.limits.SessionPacketRate, store.limits.SessionPacketBurst),
			ingressBytes:   rate.NewLimiter(store.limits.SessionByteRate, store.limits.SessionByteBurst),
		}
		grants[index] = grant
		stagedIDs[grantID] = struct{}{}
	}

	record := &roomRecord{
		state:          roomStateOpen,
		capacity:       canonical.capacity,
		createdAt:      wall,
		expiresAt:      canonical.expiresAt,
		monoDeadline:   roomDeadline,
		grants:         grants,
		ingressPackets: rate.NewLimiter(store.limits.RoomPacketRate, store.limits.RoomPacketBurst),
		ingressBytes:   rate.NewLimiter(store.limits.RoomByteRate, store.limits.RoomByteBurst),
		fanoutWrites:   rate.NewLimiter(store.limits.RoomFanoutWriteRate, store.limits.RoomFanoutWriteBurst),
		fanoutBytes:    rate.NewLimiter(store.limits.RoomFanoutByteRate, store.limits.RoomFanoutByteBurst),
	}
	store.roomsByID[roomID] = record
	for _, grant := range grants {
		store.grantsByID[grant.id] = grant
	}
	store.openRooms++
	store.activeSessions += len(grants)
	return allocationAt(roomID, record, reading.Mono), true, nil
}

func (store *Store) GetRoom(roomID string) (RoomSnapshot, error) {
	if !protocol.ValidID(roomID) {
		return RoomSnapshot{}, ErrInvalid
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	reading := store.now()
	record := store.roomsByID[roomID]
	if record == nil || record.state != roomStateOpen || roomAccessTerminal(record, reading.Mono) {
		return RoomSnapshot{}, ErrNotFound
	}
	return snapshotAt(roomID, record, reading.Mono), nil
}

func (store *Store) EndRoom(roomID string) error {
	if !protocol.ValidID(roomID) {
		return ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	reading := store.now()
	record := store.roomsByID[roomID]
	if record == nil {
		return nil
	}
	if record.state == roomStateTombstone {
		if reading.Mono >= record.tombstoneDeadline {
			delete(store.roomsByID, roomID)
		}
		return nil
	}
	store.tombstoneRoom(record, reading.Mono, GrantStateRevoked)
	return nil
}

type normalizedDefinition struct {
	capacity     uint32
	expiresAt    time.Time
	participants []ParticipantDefinition
}

func canonicalDefinition(roomID string, definition RoomDefinition, limits Limits) (normalizedDefinition, error) {
	if !protocol.ValidID(roomID) || definition.Capacity == 0 || len(definition.Participants) == 0 ||
		uint64(definition.Capacity) != uint64(len(definition.Participants)) {
		return normalizedDefinition{}, ErrInvalid
	}
	if uint64(definition.Capacity) > uint64(limits.MaxRoomCapacity) || len(definition.Participants) > limits.MaxRoomCapacity {
		return normalizedDefinition{}, ErrCapacity
	}
	expiresAt := definition.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		return normalizedDefinition{}, ErrInvalid
	}

	participants := append([]ParticipantDefinition(nil), definition.Participants...)
	participantIDs := make(map[string]struct{}, len(participants))
	sessionIDs := make(map[string]struct{}, len(participants))
	for index := range participants {
		participant := &participants[index]
		if !protocol.ValidID(participant.ParticipantID) || !protocol.ValidID(participant.SessionID) || participant.GrantExpiresAt.IsZero() {
			return normalizedDefinition{}, ErrInvalid
		}
		if _, exists := participantIDs[participant.ParticipantID]; exists {
			return normalizedDefinition{}, ErrInvalid
		}
		if _, exists := sessionIDs[participant.SessionID]; exists {
			return normalizedDefinition{}, ErrInvalid
		}
		participantIDs[participant.ParticipantID] = struct{}{}
		sessionIDs[participant.SessionID] = struct{}{}
		participant.GrantExpiresAt = participant.GrantExpiresAt.UTC()
		if participant.GrantExpiresAt.After(expiresAt) {
			return normalizedDefinition{}, ErrInvalid
		}
	}
	sort.Slice(participants, func(left, right int) bool {
		if participants[left].ParticipantID == participants[right].ParticipantID {
			return participants[left].SessionID < participants[right].SessionID
		}
		return participants[left].ParticipantID < participants[right].ParticipantID
	})
	return normalizedDefinition{capacity: definition.Capacity, expiresAt: expiresAt, participants: participants}, nil
}

func (store *Store) uniqueGrantID(staged map[protocol.Bytes16]struct{}) (protocol.Bytes16, bool) {
	for range 9 {
		var grantID protocol.Bytes16
		if _, err := io.ReadFull(store.random, grantID[:]); err != nil {
			return protocol.Bytes16{}, false
		}
		if _, exists := store.grantsByID[grantID]; exists {
			continue
		}
		if _, exists := staged[grantID]; exists {
			continue
		}
		return grantID, true
	}
	return protocol.Bytes16{}, false
}

func allocationAt(roomID string, room *roomRecord, now time.Duration) Allocation {
	allocation := Allocation{
		RoomID:    roomID,
		CreatedAt: room.createdAt,
		ExpiresAt: room.expiresAt,
		Capacity:  room.capacity,
		Grants:    make([]GrantAllocation, len(room.grants)),
	}
	for index, grant := range room.grants {
		state := grantStateAt(grant, now)
		allocationGrant := GrantAllocation{
			ParticipantID:  grant.participantID,
			SessionID:      grant.sessionID,
			GrantID:        grant.id,
			GrantExpiresAt: grant.expiresAt,
			State:          state,
		}
		if (state == GrantStateIssued || state == GrantStateBound) && grant.secret != nil {
			secret := *grant.secret
			allocationGrant.GrantSecret = &secret
		}
		allocation.Grants[index] = allocationGrant
	}
	return allocation
}

func snapshotAt(roomID string, room *roomRecord, now time.Duration) RoomSnapshot {
	snapshot := RoomSnapshot{
		RoomID:       roomID,
		CreatedAt:    room.createdAt,
		ExpiresAt:    room.expiresAt,
		Capacity:     room.capacity,
		Participants: make([]ParticipantSnapshot, len(room.grants)),
	}
	for index, grant := range room.grants {
		state := grantStateAt(grant, now)
		bindingState := grant.bindingState
		if bindingState == "" {
			bindingState = BindingStateUnbound
		}
		switch state {
		case GrantStateExpired:
			bindingState = BindingStateExpired
		case GrantStateRevoked:
			bindingState = BindingStateRevoked
		default:
			if grant.binding != nil {
				if now >= grant.binding.deadline {
					state = GrantStateIssued
					bindingState = BindingStateExpired
				} else if grant.pending != nil && now < grant.pending.deadline {
					bindingState = BindingStateRebindPending
				} else {
					bindingState = BindingStateBound
				}
			}
		}
		snapshot.Participants[index] = ParticipantSnapshot{
			ParticipantID:  grant.participantID,
			SessionID:      grant.sessionID,
			GrantExpiresAt: grant.expiresAt,
			GrantState:     state,
			BindingState:   bindingState,
		}
	}
	return snapshot
}

func (store *Store) tombstoneRoom(room *roomRecord, now time.Duration, terminalState GrantState) {
	if room.state == roomStateTombstone {
		return
	}
	if room.state == roomStateOpen {
		store.openRooms--
	}
	for _, grant := range room.grants {
		store.terminalGrant(grant, terminalState)
	}
	*room = roomRecord{
		state:             roomStateTombstone,
		tombstoneDeadline: saturatingAdd(now, store.limits.TombstoneTTL),
	}
}

func (store *Store) terminalGrant(grant *grantRecord, terminalState GrantState) {
	store.clearRelay(grant)
	grant.ingressPackets = nil
	grant.ingressBytes = nil
	if grantLive(grant) {
		store.activeSessions--
		grant.state = terminalState
		if terminalState == GrantStateRevoked {
			grant.bindingState = BindingStateRevoked
		} else {
			grant.bindingState = BindingStateExpired
		}
	}
	delete(store.grantsByID, grant.id)
	if grant.secret != nil {
		*grant.secret = protocol.Bytes32{}
		grant.secret = nil
	}
}

func roomAccessTerminal(room *roomRecord, now time.Duration) bool {
	return room.state != roomStateOpen || now >= room.monoDeadline || now >= lastGrantDeadline(room)
}

func lastGrantDeadline(room *roomRecord) time.Duration {
	var deadline time.Duration
	for index, grant := range room.grants {
		if index == 0 || grant.monoDeadline > deadline {
			deadline = grant.monoDeadline
		}
	}
	return deadline
}

func grantLive(grant *grantRecord) bool {
	return grant.state == GrantStateIssued || grant.state == GrantStateBound
}

func grantStateAt(grant *grantRecord, now time.Duration) GrantState {
	if grantLive(grant) && now >= grant.monoDeadline {
		return GrantStateExpired
	}
	if grant.state == GrantStateBound && grant.binding != nil && now >= grant.binding.deadline {
		return GrantStateIssued
	}
	return grant.state
}

func sameDefinition(room *roomRecord, definition normalizedDefinition) bool {
	if room.capacity != definition.capacity || room.expiresAt != definition.expiresAt || len(room.grants) != len(definition.participants) {
		return false
	}
	for index, participant := range definition.participants {
		grant := room.grants[index]
		if grant.participantID != participant.ParticipantID || grant.sessionID != participant.SessionID ||
			grant.expiresAt != participant.GrantExpiresAt {
			return false
		}
	}
	return true
}
