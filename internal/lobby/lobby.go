package lobby

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
)

type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

type LobbyState string

const (
	LobbyStateOpen    LobbyState = "open"
	LobbyStateMatched LobbyState = "matched"
	LobbyStateClosed  LobbyState = "closed"
)

type CreateRequest struct {
	Visibility Visibility
	QueueKey   string
	Capacity   uint32
}

type MemberSnapshot struct {
	PlayerID string
	Ready    bool
}

type LobbySummary struct {
	LobbyID, OwnerPlayerID, QueueKey string
	Visibility                       Visibility
	Capacity, MemberCount            uint32
	Revision                         uint64
	ExpiresAt                        time.Time
}

type LobbySnapshot struct {
	LobbyID, OwnerPlayerID, QueueKey string
	Visibility                       Visibility
	Capacity                         uint32
	Revision                         uint64
	State                            LobbyState
	Members                          []MemberSnapshot
	ExpiresAt                        time.Time
	Assignment                       *Assignment
}

type LobbyPage struct {
	Lobbies    []LobbySummary
	NextCursor string
}

type lobbyRecord struct {
	id, ownerPlayerID, queueKey string
	visibility                  Visibility
	capacity                    uint32
	state                       LobbyState
	revision, sequence          uint64
	createdAt, expiresAt        time.Time
	monoDeadline                time.Duration
	members                     map[string]*memberRecord
	assignments                 map[string]Assignment
}

type memberRecord struct {
	playerID     string
	ready        bool
	joinSequence uint64
}

func (manager *Manager) Create(playerID string, request CreateRequest) (LobbySnapshot, error) {
	if !protocol.ValidID(playerID) || !validCreateRequest(request) {
		return LobbySnapshot{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	reading := manager.now()
	manager.expireLocked(reading)
	if _, exists := manager.lobbyByPlayer[playerID]; exists {
		return LobbySnapshot{}, ErrConflict
	}
	if _, exists := manager.ticketsByPlayer[playerID]; exists {
		return LobbySnapshot{}, ErrConflict
	}
	if len(manager.lobbiesByID) >= HardMaxOpenLobbies || manager.nextSequence == math.MaxUint64 {
		return LobbySnapshot{}, ErrCapacity
	}
	lobbyID, err := manager.uniqueIDLocked("l-", func(candidate string) bool {
		_, exists := manager.lobbiesByID[candidate]
		return exists
	})
	if err != nil {
		return LobbySnapshot{}, err
	}
	deadline, ok := clock.DeadlineAfter(reading.Mono, DefaultLobbyTTL)
	if !ok {
		return LobbySnapshot{}, ErrInvalid
	}
	record := &lobbyRecord{
		id:            lobbyID,
		ownerPlayerID: playerID,
		queueKey:      request.QueueKey,
		visibility:    request.Visibility,
		capacity:      request.Capacity,
		state:         LobbyStateOpen,
		revision:      1,
		sequence:      manager.takeSequenceLocked(),
		createdAt:     reading.Wall.UTC(),
		expiresAt:     reading.Wall.UTC().Add(DefaultLobbyTTL),
		monoDeadline:  deadline,
		members:       make(map[string]*memberRecord, request.Capacity),
		assignments:   make(map[string]Assignment),
	}
	record.members[playerID] = &memberRecord{playerID: playerID, joinSequence: record.sequence}
	manager.lobbiesByID[lobbyID] = record
	manager.lobbyByPlayer[playerID] = lobbyID
	return snapshotFor(record, playerID), nil
}

func (manager *Manager) List(queueKey, cursor string, limit int) (LobbyPage, error) {
	if !protocol.ValidID(queueKey) || limit <= 0 || limit > HardMaxListPage {
		return LobbyPage{}, ErrInvalid
	}
	after, err := parseCursor(cursor)
	if err != nil {
		return LobbyPage{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked(manager.now())
	records := make([]*lobbyRecord, 0, len(manager.lobbiesByID))
	for _, record := range manager.lobbiesByID {
		if record.state == LobbyStateOpen && record.visibility == VisibilityPublic && record.queueKey == queueKey && record.sequence > after {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(left, right int) bool { return records[left].sequence < records[right].sequence })
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	page := LobbyPage{Lobbies: make([]LobbySummary, len(records))}
	for index, record := range records {
		page.Lobbies[index] = summaryFor(record)
	}
	if hasMore {
		page.NextCursor = strconv.FormatUint(records[len(records)-1].sequence, 10)
	}
	return page, nil
}

func (manager *Manager) Get(playerID, lobbyID string) (LobbySnapshot, error) {
	if !protocol.ValidID(playerID) || !protocol.ValidID(lobbyID) {
		return LobbySnapshot{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked(manager.now())
	record := manager.lobbiesByID[lobbyID]
	if record == nil {
		return LobbySnapshot{}, ErrNotFound
	}
	_, member := record.members[playerID]
	if (record.visibility == VisibilityPrivate || record.state == LobbyStateMatched) && !member {
		return LobbySnapshot{}, ErrNotFound
	}
	return snapshotFor(record, playerID), nil
}

func (manager *Manager) Join(playerID, lobbyID string, revision uint64) (LobbySnapshot, error) {
	if !protocol.ValidID(playerID) || !protocol.ValidID(lobbyID) || revision == 0 {
		return LobbySnapshot{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked(manager.now())
	record := manager.lobbiesByID[lobbyID]
	if record == nil {
		return LobbySnapshot{}, ErrNotFound
	}
	if record.state != LobbyStateOpen || record.revision != revision {
		return LobbySnapshot{}, ErrConflict
	}
	if _, exists := manager.lobbyByPlayer[playerID]; exists {
		return LobbySnapshot{}, ErrConflict
	}
	if _, exists := manager.ticketsByPlayer[playerID]; exists {
		return LobbySnapshot{}, ErrConflict
	}
	if len(record.members) >= int(record.capacity) {
		return LobbySnapshot{}, ErrCapacity
	}
	if manager.nextSequence == math.MaxUint64 || record.revision == math.MaxUint64 {
		return LobbySnapshot{}, ErrCapacity
	}
	joinSequence := manager.takeSequenceLocked()
	record.members[playerID] = &memberRecord{playerID: playerID, joinSequence: joinSequence}
	manager.lobbyByPlayer[playerID] = lobbyID
	resetReady(record)
	record.revision++
	return snapshotFor(record, playerID), nil
}

func (manager *Manager) Leave(playerID, lobbyID string, revision uint64) (LobbySnapshot, error) {
	if !protocol.ValidID(playerID) || !protocol.ValidID(lobbyID) || revision == 0 {
		return LobbySnapshot{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked(manager.now())
	record := manager.lobbiesByID[lobbyID]
	if record == nil {
		return LobbySnapshot{}, ErrNotFound
	}
	if record.state != LobbyStateOpen || record.revision != revision {
		return LobbySnapshot{}, ErrConflict
	}
	if _, member := record.members[playerID]; !member {
		return LobbySnapshot{}, ErrNotFound
	}
	if record.revision == math.MaxUint64 {
		return LobbySnapshot{}, ErrCapacity
	}
	delete(record.members, playerID)
	delete(manager.lobbyByPlayer, playerID)
	record.revision++
	if len(record.members) == 0 {
		record.state = LobbyStateClosed
		record.ownerPlayerID = ""
		closed := snapshotFor(record, playerID)
		delete(manager.lobbiesByID, lobbyID)
		return closed, nil
	}
	if record.ownerPlayerID == playerID {
		record.ownerPlayerID = firstMember(record).playerID
	}
	resetReady(record)
	return snapshotFor(record, playerID), nil
}

func (manager *Manager) SetReady(playerID, lobbyID string, revision uint64, ready bool) (LobbySnapshot, error) {
	if !protocol.ValidID(playerID) || !protocol.ValidID(lobbyID) || revision == 0 {
		return LobbySnapshot{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked(manager.now())
	record := manager.lobbiesByID[lobbyID]
	if record == nil {
		return LobbySnapshot{}, ErrNotFound
	}
	if record.state != LobbyStateOpen || record.revision != revision {
		return LobbySnapshot{}, ErrConflict
	}
	member := record.members[playerID]
	if member == nil {
		return LobbySnapshot{}, ErrNotFound
	}
	if member.ready == ready {
		return snapshotFor(record, playerID), nil
	}
	if record.revision == math.MaxUint64 {
		return LobbySnapshot{}, ErrCapacity
	}
	member.ready = ready
	record.revision++
	return snapshotFor(record, playerID), nil
}

func (manager *Manager) Start(playerID, lobbyID string, revision uint64) (Assignment, error) {
	if !protocol.ValidID(playerID) || !protocol.ValidID(lobbyID) || revision == 0 {
		return Assignment{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	reading := manager.now()
	manager.expireLocked(reading)
	record := manager.lobbiesByID[lobbyID]
	if record == nil {
		return Assignment{}, ErrNotFound
	}
	if record.state != LobbyStateOpen || record.revision != revision {
		return Assignment{}, ErrConflict
	}
	if record.ownerPlayerID != playerID {
		return Assignment{}, ErrForbidden
	}
	if len(record.members) != int(record.capacity) || !allReady(record) || record.revision == math.MaxUint64 {
		return Assignment{}, ErrConflict
	}
	players := membersInJoinOrder(record)
	match, err := manager.allocateMatchLocked(players, reading)
	if err != nil {
		return Assignment{}, err
	}
	record.state = LobbyStateMatched
	record.revision++
	record.expiresAt = match.expiresAt
	record.monoDeadline = match.deadline
	record.assignments = match.assignments
	manager.matchIDs[match.matchID] = struct{}{}
	return match.assignments[playerID], nil
}

func validCreateRequest(request CreateRequest) bool {
	return (request.Visibility == VisibilityPublic || request.Visibility == VisibilityPrivate) &&
		protocol.ValidID(request.QueueKey) && request.Capacity >= 2 && request.Capacity <= HardMaxMembers
}

func parseCursor(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil || value == 0 || strconv.FormatUint(value, 10) != cursor {
		return 0, ErrInvalid
	}
	return value, nil
}

func (manager *Manager) expireLobbiesLocked(reading clock.Reading) {
	// ponytail: bounded M1 maps are scanned once; add deadline heaps only if profiling proves this too costly.
	for lobbyID, record := range manager.lobbiesByID {
		if reading.Mono < record.monoDeadline {
			continue
		}
		if record.state == LobbyStateMatched && len(record.assignments) != 0 {
			_ = manager.rooms.EndRoom(firstAssignment(record.assignments).RoomID)
			delete(manager.matchIDs, firstAssignment(record.assignments).MatchID)
		}
		for playerID := range record.members {
			delete(manager.lobbyByPlayer, playerID)
		}
		delete(manager.lobbiesByID, lobbyID)
	}
}

func resetReady(record *lobbyRecord) {
	for _, member := range record.members {
		member.ready = false
	}
}

func allReady(record *lobbyRecord) bool {
	for _, member := range record.members {
		if !member.ready {
			return false
		}
	}
	return true
}

func firstMember(record *lobbyRecord) *memberRecord {
	members := membersInJoinOrder(record)
	return record.members[members[0]]
}

func membersInJoinOrder(record *lobbyRecord) []string {
	members := make([]*memberRecord, 0, len(record.members))
	for _, member := range record.members {
		members = append(members, member)
	}
	sort.Slice(members, func(left, right int) bool { return members[left].joinSequence < members[right].joinSequence })
	result := make([]string, len(members))
	for index, member := range members {
		result[index] = member.playerID
	}
	return result
}

func snapshotFor(record *lobbyRecord, playerID string) LobbySnapshot {
	snapshot := LobbySnapshot{
		LobbyID: record.id, OwnerPlayerID: record.ownerPlayerID, QueueKey: record.queueKey,
		Visibility: record.visibility, Capacity: record.capacity, Revision: record.revision,
		State: record.state, ExpiresAt: record.expiresAt,
	}
	ordered := membersInJoinOrder(record)
	snapshot.Members = make([]MemberSnapshot, len(ordered))
	for index, memberID := range ordered {
		member := record.members[memberID]
		snapshot.Members[index] = MemberSnapshot{PlayerID: member.playerID, Ready: member.ready}
	}
	if assignment, exists := record.assignments[playerID]; exists {
		copy := assignment
		snapshot.Assignment = &copy
	}
	return snapshot
}

func summaryFor(record *lobbyRecord) LobbySummary {
	return LobbySummary{
		LobbyID: record.id, OwnerPlayerID: record.ownerPlayerID, QueueKey: record.queueKey,
		Visibility: record.visibility, Capacity: record.capacity, MemberCount: uint32(len(record.members)),
		Revision: record.revision, ExpiresAt: record.expiresAt,
	}
}
