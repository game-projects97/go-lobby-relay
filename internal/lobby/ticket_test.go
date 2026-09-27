package lobby

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/store"
)

func TestQuickMatchFIFOEqualityAndOneOver(t *testing.T) {
	manager, relayStore, _ := newLobbyFixture(t)
	first, err := manager.Enqueue("player-a", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	if err != nil || first.State != TicketStateQueued || first.Assignment != nil {
		t.Fatalf("first Enqueue = %#v, %v", first, err)
	}
	second, err := manager.Enqueue("player-b", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	if err != nil || second.State != TicketStateMatched || second.Assignment == nil {
		t.Fatalf("second Enqueue = %#v, %v", second, err)
	}
	firstMatched, err := manager.GetTicket("player-a")
	if err != nil || firstMatched.State != TicketStateMatched || firstMatched.Assignment == nil {
		t.Fatalf("first matched = %#v, %v", firstMatched, err)
	}
	if firstMatched.Assignment.MatchID != second.Assignment.MatchID || firstMatched.Assignment.RoomID != second.Assignment.RoomID {
		t.Fatalf("different match: first=%#v second=%#v", firstMatched.Assignment, second.Assignment)
	}
	if firstMatched.Assignment.PlayerID != "player-a" || second.Assignment.PlayerID != "player-b" ||
		firstMatched.Assignment.GrantID == second.Assignment.GrantID || firstMatched.Assignment.GrantSecret == second.Assignment.GrantSecret {
		t.Fatalf("assignment privacy = first %#v second %#v", firstMatched.Assignment, second.Assignment)
	}
	room, err := relayStore.GetRoom(second.Assignment.RoomID)
	if err != nil || room.Capacity != 2 {
		t.Fatalf("Relay room = %#v, %v", room, err)
	}

	third, err := manager.Enqueue("player-c", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	if err != nil || third.State != TicketStateQueued {
		t.Fatalf("one-over Enqueue = %#v, %v", third, err)
	}
}

func TestQuickMatchIsolatesQueueKeyAndCapacity(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	requests := []struct {
		player   string
		queueKey string
		capacity uint32
	}{
		{"player-a", "duel", 2},
		{"player-b", "coop", 2},
		{"player-c", "duel", 3},
	}
	for _, request := range requests {
		ticket, err := manager.Enqueue(request.player, EnqueueRequest{QueueKey: request.queueKey, Capacity: request.capacity})
		if err != nil || ticket.State != TicketStateQueued {
			t.Fatalf("Enqueue(%s) = %#v, %v", request.player, ticket, err)
		}
	}
	duel, err := manager.Enqueue("player-d", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	if err != nil || duel.State != TicketStateMatched {
		t.Fatalf("compatible Enqueue = %#v, %v", duel, err)
	}
	for _, player := range []string{"player-b", "player-c"} {
		ticket, err := manager.GetTicket(player)
		if err != nil || ticket.State != TicketStateQueued {
			t.Fatalf("isolated ticket %s = %#v, %v", player, ticket, err)
		}
	}
}

func TestLobbyAndTicketOwnershipAreMutuallyExclusive(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	lobby, err := manager.Create("player-a", CreateRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := manager.Enqueue("player-a", EnqueueRequest{QueueKey: "duel", Capacity: 2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Enqueue while in Lobby = %v, want conflict", err)
	}
	if _, err := manager.Enqueue("player-b", EnqueueRequest{QueueKey: "duel", Capacity: 2}); err != nil {
		t.Fatalf("Enqueue player-b: %v", err)
	}
	if _, err := manager.Join("player-b", lobby.LobbyID, lobby.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("Join while queued = %v, want conflict", err)
	}
	if _, err := manager.Create("player-b", CreateRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create while queued = %v, want conflict", err)
	}
}

func TestCancelTicketRequiresRevisionAndReleasesPlayer(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	ticket, err := manager.Enqueue("player-a", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := manager.CancelTicket("player-a", ticket.Revision+1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Cancel = %v, want conflict", err)
	}
	cancelled, err := manager.CancelTicket("player-a", ticket.Revision)
	if err != nil || cancelled.State != TicketStateCancelled || cancelled.Revision != ticket.Revision+1 {
		t.Fatalf("Cancel = %#v, %v", cancelled, err)
	}
	if _, err := manager.GetTicket("player-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get cancelled = %v, want not found", err)
	}
	if _, err := manager.Create("player-a", CreateRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2}); err != nil {
		t.Fatalf("Create after cancel: %v", err)
	}
}

func TestMatchedTicketCannotBeCancelled(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	first, _ := manager.Enqueue("player-a", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	_, _ = manager.Enqueue("player-b", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	matched, err := manager.GetTicket("player-a")
	if err != nil {
		t.Fatalf("Get matched: %v", err)
	}
	if _, err := manager.CancelTicket("player-a", matched.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("Cancel matched (initial revision %d) = %v, want conflict", first.Revision, err)
	}
}

func TestTicketAuthorityExpiresAtExactDeadline(t *testing.T) {
	manager, _, fakeClock := newLobbyFixture(t)
	ticket, err := manager.Enqueue("player-a", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	fakeClock.advance(TicketTTL - time.Nanosecond)
	if _, err := manager.GetTicket("player-a"); err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}
	fakeClock.advance(time.Nanosecond)
	if _, err := manager.GetTicket("player-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get at expiry = %v, want not found", err)
	}
	manager.Expire()
	if len(manager.ticketsByPlayer) != 0 {
		t.Fatalf("expired tickets retained: %d", len(manager.ticketsByPlayer))
	}
	if _, err := manager.Create("player-a", CreateRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2}); err != nil {
		t.Fatalf("Create after ticket expiry: %v", err)
	}
	if ticket.ExpiresAt != lobbyTestWall.Add(TicketTTL) {
		t.Fatalf("ticket expiry = %v", ticket.ExpiresAt)
	}
}

func TestRelayCapacityFailurePreservesFIFOSelection(t *testing.T) {
	manager, relayStore, fakeClock := newLimitedLobbyFixture(t)
	fillRelayStore(t, relayStore, fakeClock.read().Wall)
	first, err := manager.Enqueue("player-a", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	if err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if _, err := manager.Enqueue("player-b", EnqueueRequest{QueueKey: "duel", Capacity: 2}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("second Enqueue = %v, want unavailable", err)
	}
	for _, player := range []string{"player-a", "player-b"} {
		ticket, getErr := manager.GetTicket(player)
		if getErr != nil || ticket.State != TicketStateQueued {
			t.Fatalf("queued after failure %s = %#v, %v", player, ticket, getErr)
		}
	}
	if err := relayStore.EndRoom("occupied"); err != nil {
		t.Fatalf("EndRoom occupied: %v", err)
	}
	fakeClock.advance(store.DefaultLimits().TombstoneTTL)
	relayStore.Expire()
	third, err := manager.Enqueue("player-c", EnqueueRequest{QueueKey: "duel", Capacity: 2})
	if err != nil || third.State != TicketStateQueued {
		t.Fatalf("third Enqueue = %#v, %v", third, err)
	}
	firstMatched, err := manager.GetTicket("player-a")
	if err != nil || firstMatched.State != TicketStateMatched {
		t.Fatalf("first matched = %#v, %v (initial %#v)", firstMatched, err, first)
	}
	secondMatched, err := manager.GetTicket("player-b")
	if err != nil || secondMatched.State != TicketStateMatched || secondMatched.Assignment.MatchID != firstMatched.Assignment.MatchID {
		t.Fatalf("second matched = %#v, %v", secondMatched, err)
	}
	thirdQueued, err := manager.GetTicket("player-c")
	if err != nil || thirdQueued.State != TicketStateQueued {
		t.Fatalf("third queued = %#v, %v", thirdQueued, err)
	}
}

func TestConcurrentEnqueueFormsExactNonOverlappingPairs(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	const players = 100
	var wait sync.WaitGroup
	for index := range players {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			if _, err := manager.Enqueue(playerID(index), EnqueueRequest{QueueKey: "duel", Capacity: 2}); err != nil {
				t.Errorf("Enqueue %d: %v", index, err)
			}
		}(index)
	}
	wait.Wait()
	matches := make(map[string]map[string]struct{})
	for index := range players {
		ticket, err := manager.GetTicket(playerID(index))
		if err != nil || ticket.State != TicketStateMatched || ticket.Assignment == nil {
			t.Fatalf("ticket %d = %#v, %v", index, ticket, err)
		}
		playersInMatch := matches[ticket.Assignment.MatchID]
		if playersInMatch == nil {
			playersInMatch = make(map[string]struct{})
			matches[ticket.Assignment.MatchID] = playersInMatch
		}
		if _, duplicate := playersInMatch[ticket.PlayerID]; duplicate {
			t.Fatalf("duplicate player %s in match %s", ticket.PlayerID, ticket.Assignment.MatchID)
		}
		playersInMatch[ticket.PlayerID] = struct{}{}
	}
	if len(matches) != players/2 {
		t.Fatalf("matches = %d, want %d", len(matches), players/2)
	}
	for matchID, grouped := range matches {
		if len(grouped) != 2 {
			t.Fatalf("match %s has %d players", matchID, len(grouped))
		}
	}
}

func TestQuickMatchValidation(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	for _, request := range []EnqueueRequest{
		{QueueKey: "bad!", Capacity: 2},
		{QueueKey: "duel", Capacity: 1},
		{QueueKey: "duel", Capacity: HardMaxMembers + 1},
	} {
		if _, err := manager.Enqueue("player-a", request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Enqueue(%#v) = %v, want invalid", request, err)
		}
	}
	for index := range HardMaxTickets {
		manager.ticketsByPlayer[fmt.Sprintf("held-%04d", index)] = &ticketRecord{
			id: fmt.Sprintf("ticket-%04d", index), state: TicketStateQueued, monoDeadline: time.Hour,
		}
	}
	if _, err := manager.Enqueue("capacity-player", EnqueueRequest{QueueKey: "duel", Capacity: 2}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("Enqueue at hard maximum = %v, want capacity", err)
	}
}

func TestQuickMatchFatalRandomLeavesSelectedTicketsQueued(t *testing.T) {
	fakeClock := &lobbyTestClock{wall: lobbyTestWall}
	relayStore, err := store.New(store.Config{Limits: store.DefaultLimits(), Now: fakeClock.read, Random: &incrementingReader{next: 0xa000}})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	random := append(bytes.Repeat([]byte{0x11}, 16), bytes.Repeat([]byte{0x22}, 16)...)
	manager, err := New(Config{Rooms: relayStore, Now: fakeClock.read, Random: bytes.NewReader(random)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := manager.Enqueue("player-a", EnqueueRequest{QueueKey: "duel", Capacity: 2}); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if _, err := manager.Enqueue("player-b", EnqueueRequest{QueueKey: "duel", Capacity: 2}); !errors.Is(err, ErrFatalRandom) {
		t.Fatalf("second Enqueue = %v, want fatal random", err)
	}
	for _, player := range []string{"player-a", "player-b"} {
		ticket, getErr := manager.GetTicket(player)
		if getErr != nil || ticket.State != TicketStateQueued || ticket.Assignment != nil {
			t.Fatalf("ticket after fatal random %s = %#v, %v", player, ticket, getErr)
		}
	}
}

func newLimitedLobbyFixture(t *testing.T) (*Manager, *store.Store, *lobbyTestClock) {
	t.Helper()
	fakeClock := &lobbyTestClock{wall: lobbyTestWall}
	limits := store.DefaultLimits()
	limits.MaxOpenRooms = 1
	relayStore, err := store.New(store.Config{Limits: limits, Now: fakeClock.read, Random: &incrementingReader{next: 0x9000}})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	manager, err := New(Config{Rooms: relayStore, Now: fakeClock.read, Random: &incrementingReader{next: 0x3000}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return manager, relayStore, fakeClock
}

func fillRelayStore(t *testing.T, relayStore *store.Store, now time.Time) {
	t.Helper()
	_, _, err := relayStore.CreateRoom("occupied", store.RoomDefinition{
		Capacity:  2,
		ExpiresAt: now.Add(time.Hour),
		Participants: []store.ParticipantDefinition{
			{ParticipantID: "occupied-a", SessionID: "occupied-session-a", GrantExpiresAt: now.Add(time.Hour)},
			{ParticipantID: "occupied-b", SessionID: "occupied-session-b", GrantExpiresAt: now.Add(time.Hour)},
		},
	})
	if err != nil {
		t.Fatalf("Create occupied room: %v", err)
	}
}
