package matchmaking

import (
	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
)

type queueBucket struct {
	queueKey string
	capacity uint32
}

func (manager *Manager) matchQueueLocked(bucket queueBucket, reading clock.Reading) error {
	manager.compactQueueLocked(bucket)
	for len(manager.queues[bucket]) >= int(bucket.capacity) {
		selectedIDs := manager.queues[bucket][:bucket.capacity]
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
		remaining := append([]string(nil), manager.queues[bucket][bucket.capacity:]...)
		if len(remaining) == 0 {
			delete(manager.queues, bucket)
			return nil
		}
		manager.queues[bucket] = remaining
	}
	return nil
}

func (manager *Manager) compactQueueLocked(bucket queueBucket) {
	queued := manager.queues[bucket]
	kept := queued[:0]
	for _, playerID := range queued {
		ticket := manager.ticketsByPlayer[playerID]
		if ticket != nil && ticket.state == TicketStateQueued && ticket.queueKey == bucket.queueKey && ticket.capacity == bucket.capacity {
			kept = append(kept, playerID)
		}
	}
	if len(kept) == 0 {
		delete(manager.queues, bucket)
		return
	}
	manager.queues[bucket] = kept
}

func (manager *Manager) removeQueuedPlayerLocked(bucket queueBucket, playerID string) {
	queued := manager.queues[bucket]
	for index, candidate := range queued {
		if candidate != playerID {
			continue
		}
		manager.queues[bucket] = append(queued[:index], queued[index+1:]...)
		if len(manager.queues[bucket]) == 0 {
			delete(manager.queues, bucket)
		}
		return
	}
}
