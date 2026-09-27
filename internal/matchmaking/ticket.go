package matchmaking

import (
	"math"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
)

const (
	HardMaxTickets = 4096
	TicketTTL      = 2 * time.Minute
)

type TicketState string

const (
	TicketStateQueued    TicketState = "queued"
	TicketStateMatched   TicketState = "matched"
	TicketStateCancelled TicketState = "cancelled"
	TicketStateExpired   TicketState = "expired"
)

type CreateTicketRequest struct {
	QueueKey string
	Capacity uint32
}

type TicketSnapshot struct {
	TicketID, PlayerID, QueueKey string
	State                        TicketState
	Capacity                     uint32
	Revision                     uint64
	ExpiresAt                    time.Time
	Assignment                   *Assignment
}

type ticketRecord struct {
	id, playerID, queueKey string
	capacity               uint32
	state                  TicketState
	revision, sequence     uint64
	expiresAt              time.Time
	monoDeadline           time.Duration
	assignment             *Assignment
}

func (manager *Manager) CreateTicket(playerID string, request CreateTicketRequest) (TicketSnapshot, error) {
	if !protocol.ValidID(playerID) || !validCreateTicketRequest(request) {
		return TicketSnapshot{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	reading := manager.now()
	manager.expireLocked(reading)
	if _, exists := manager.lobbyByPlayer[playerID]; exists {
		return TicketSnapshot{}, ErrConflict
	}
	if _, exists := manager.ticketsByPlayer[playerID]; exists {
		return TicketSnapshot{}, ErrConflict
	}
	if len(manager.ticketsByPlayer) >= HardMaxTickets || manager.nextSequence == math.MaxUint64 {
		return TicketSnapshot{}, ErrCapacity
	}
	ticketID, err := manager.uniqueIDLocked("t-", func(candidate string) bool {
		for _, ticket := range manager.ticketsByPlayer {
			if ticket.id == candidate {
				return true
			}
		}
		return false
	})
	if err != nil {
		return TicketSnapshot{}, err
	}
	deadline, ok := clock.DeadlineAfter(reading.Mono, TicketTTL)
	if !ok {
		return TicketSnapshot{}, ErrInvalid
	}
	record := &ticketRecord{
		id: ticketID, playerID: playerID, queueKey: request.QueueKey, capacity: request.Capacity,
		state: TicketStateQueued, revision: 1, sequence: manager.takeSequenceLocked(),
		expiresAt: reading.Wall.UTC().Add(TicketTTL), monoDeadline: deadline,
	}
	manager.ticketsByPlayer[playerID] = record
	bucket := queueBucket{queueKey: request.QueueKey, capacity: request.Capacity}
	manager.queues[bucket] = append(manager.queues[bucket], playerID)
	if err := manager.matchQueueLocked(bucket, reading); err != nil {
		return TicketSnapshot{}, err
	}
	return ticketSnapshot(record), nil
}

func (manager *Manager) GetTicket(playerID string) (TicketSnapshot, error) {
	if !protocol.ValidID(playerID) {
		return TicketSnapshot{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked(manager.now())
	record := manager.ticketsByPlayer[playerID]
	if record == nil {
		return TicketSnapshot{}, ErrNotFound
	}
	return ticketSnapshot(record), nil
}

func (manager *Manager) CancelTicket(playerID string, revision uint64) (TicketSnapshot, error) {
	if !protocol.ValidID(playerID) || revision == 0 {
		return TicketSnapshot{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked(manager.now())
	record := manager.ticketsByPlayer[playerID]
	if record == nil {
		return TicketSnapshot{}, ErrNotFound
	}
	if record.state != TicketStateQueued || record.revision != revision || record.revision == math.MaxUint64 {
		return TicketSnapshot{}, ErrConflict
	}
	record.state = TicketStateCancelled
	record.revision++
	snapshot := ticketSnapshot(record)
	delete(manager.ticketsByPlayer, playerID)
	manager.removeQueuedPlayerLocked(queueBucket{queueKey: record.queueKey, capacity: record.capacity}, playerID)
	return snapshot, nil
}

func validCreateTicketRequest(request CreateTicketRequest) bool {
	return protocol.ValidID(request.QueueKey) && request.Capacity >= 2 && request.Capacity <= HardMaxMatchSize
}

func (manager *Manager) expireTicketsLocked(reading clock.Reading) {
	for playerID, ticket := range manager.ticketsByPlayer {
		if reading.Mono < ticket.monoDeadline {
			continue
		}
		if ticket.state == TicketStateQueued {
			manager.removeQueuedPlayerLocked(queueBucket{queueKey: ticket.queueKey, capacity: ticket.capacity}, playerID)
		}
		if ticket.state == TicketStateMatched && ticket.assignment != nil {
			_ = manager.rooms.EndRoom(ticket.assignment.RoomID)
			delete(manager.matchIDs, ticket.assignment.MatchID)
		}
		delete(manager.ticketsByPlayer, playerID)
	}
}

func ticketSnapshot(record *ticketRecord) TicketSnapshot {
	snapshot := TicketSnapshot{
		TicketID: record.id, PlayerID: record.playerID, QueueKey: record.queueKey,
		State: record.state, Capacity: record.capacity, Revision: record.revision, ExpiresAt: record.expiresAt,
	}
	if record.assignment != nil {
		assignment := *record.assignment
		snapshot.Assignment = &assignment
	}
	return snapshot
}
