package lobby

import (
	"errors"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"github.com/gyungsubLee/go-lobby-relay/internal/store"
)

type Assignment struct {
	MatchID, RoomID, PlayerID, SessionID string
	GrantID                              protocol.Bytes16
	GrantSecret                          protocol.Bytes32
	GrantExpiresAt                       time.Time
}

type matchAllocation struct {
	matchID, roomID string
	assignments     map[string]Assignment
	expiresAt       time.Time
	deadline        time.Duration
}

func (manager *Manager) allocateMatchLocked(players []string, reading clock.Reading) (matchAllocation, error) {
	expiresAt := reading.Wall.UTC().Add(MatchTTL)
	deadline, ok := clock.DeadlineAfter(reading.Mono, MatchTTL)
	if !ok {
		return matchAllocation{}, ErrUnavailable
	}
	for range maxIDDraws {
		matchID, err := manager.uniqueIDLocked("m-", func(candidate string) bool {
			_, exists := manager.matchIDs[candidate]
			return exists
		})
		if err != nil {
			return matchAllocation{}, err
		}
		roomID, err := manager.drawIDLocked("r-")
		if err != nil {
			return matchAllocation{}, err
		}
		participants := make([]store.ParticipantDefinition, len(players))
		sessions := make(map[string]struct{}, len(players))
		validAttempt := true
		for index, player := range players {
			sessionID, sessionErr := manager.uniqueIDLocked("s-", func(candidate string) bool {
				_, exists := sessions[candidate]
				return exists
			})
			if sessionErr != nil {
				return matchAllocation{}, sessionErr
			}
			if _, duplicate := sessions[sessionID]; duplicate {
				validAttempt = false
				break
			}
			sessions[sessionID] = struct{}{}
			participants[index] = store.ParticipantDefinition{ParticipantID: player, SessionID: sessionID, GrantExpiresAt: expiresAt}
		}
		if !validAttempt {
			continue
		}
		allocation, created, allocationErr := manager.rooms.CreateRoom(roomID, store.RoomDefinition{
			Capacity: uint32(len(players)), ExpiresAt: expiresAt, Participants: participants,
		})
		if errors.Is(allocationErr, store.ErrConflict) || allocationErr == nil && !created {
			continue
		}
		if allocationErr != nil {
			return matchAllocation{}, mapStoreError(allocationErr)
		}
		assignments := make(map[string]Assignment, len(players))
		for _, grant := range allocation.Grants {
			if grant.GrantSecret == nil {
				_ = manager.rooms.EndRoom(roomID)
				return matchAllocation{}, ErrUnavailable
			}
			assignments[grant.ParticipantID] = Assignment{
				MatchID: matchID, RoomID: roomID, PlayerID: grant.ParticipantID, SessionID: grant.SessionID,
				GrantID: grant.GrantID, GrantSecret: *grant.GrantSecret, GrantExpiresAt: grant.GrantExpiresAt,
			}
		}
		if len(assignments) != len(players) {
			_ = manager.rooms.EndRoom(roomID)
			return matchAllocation{}, ErrUnavailable
		}
		return matchAllocation{matchID: matchID, roomID: roomID, assignments: assignments, expiresAt: expiresAt, deadline: deadline}, nil
	}
	return matchAllocation{}, ErrFatalRandom
}

func mapStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrFatalRandom):
		return ErrFatalRandom
	case errors.Is(err, store.ErrCapacity):
		return ErrUnavailable
	default:
		return ErrUnavailable
	}
}

func firstAssignment(assignments map[string]Assignment) Assignment {
	for _, assignment := range assignments {
		return assignment
	}
	return Assignment{}
}
