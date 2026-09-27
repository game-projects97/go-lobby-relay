package lobby

import (
	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
)

type queueKey struct {
	queueKey string
	capacity uint32
}

func (manager *Manager) matchQueueLocked(key queueKey, reading clock.Reading) error {
	manager.compactQueueLocked(key)
	for len(manager.queues[key]) >= int(key.capacity) {
		selectedIDs := manager.queues[key][:key.capacity]
		players := append([]string(nil), selectedIDs...)
		match, err := manager.allocateMatchLocked(players, reading)
		if err != nil {
			return err
		}
		for _, playerID := range players {
			ticket := manager.ticketsByPlayer[playerID]
			assignment := match.assignments[playerID]
			ticket.state = TicketStateMatched
			ticket.revision++
			ticket.expiresAt = match.expiresAt
			ticket.monoDeadline = match.deadline
			ticket.assignment = &assignment
		}
		manager.matchIDs[match.matchID] = struct{}{}
		remaining := append([]string(nil), manager.queues[key][key.capacity:]...)
		if len(remaining) == 0 {
			delete(manager.queues, key)
			return nil
		}
		manager.queues[key] = remaining
	}
	return nil
}

func (manager *Manager) compactQueueLocked(key queueKey) {
	queued := manager.queues[key]
	kept := queued[:0]
	for _, playerID := range queued {
		ticket := manager.ticketsByPlayer[playerID]
		if ticket != nil && ticket.state == TicketStateQueued && ticket.queueKey == key.queueKey && ticket.capacity == key.capacity {
			kept = append(kept, playerID)
		}
	}
	if len(kept) == 0 {
		delete(manager.queues, key)
		return
	}
	manager.queues[key] = kept
}

func (manager *Manager) removeQueuedPlayerLocked(key queueKey, playerID string) {
	queued := manager.queues[key]
	for index, candidate := range queued {
		if candidate != playerID {
			continue
		}
		manager.queues[key] = append(queued[:index], queued[index+1:]...)
		if len(manager.queues[key]) == 0 {
			delete(manager.queues, key)
		}
		return
	}
}
