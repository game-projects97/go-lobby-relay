package matchmaking

import (
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/relayroom"
)

var lobbyTestWall = time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)

type lobbyTestClock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

func (fakeClock *lobbyTestClock) read() clock.Reading {
	fakeClock.mu.Lock()
	defer fakeClock.mu.Unlock()
	return clock.Reading{Wall: fakeClock.wall, Mono: fakeClock.mono}
}

func (fakeClock *lobbyTestClock) advance(delta time.Duration) {
	fakeClock.mu.Lock()
	fakeClock.wall = fakeClock.wall.Add(delta)
	fakeClock.mono += delta
	fakeClock.mu.Unlock()
}

type incrementingReader struct {
	mu   sync.Mutex
	next uint64
}

func (reader *incrementingReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	for index := range buffer {
		buffer[index] = 0
	}
	if len(buffer) >= 8 {
		binary.BigEndian.PutUint64(buffer[len(buffer)-8:], reader.next)
	} else {
		for index := range buffer {
			buffer[index] = byte(reader.next >> (8 * (len(buffer) - index - 1)))
		}
	}
	reader.next++
	return len(buffer), nil
}

func TestCreateListAndPrivateLobbyVisibility(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	publicLobby, err := manager.CreateLobby("player-a", CreateLobbyRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2})
	if err != nil {
		t.Fatalf("Create public: %v", err)
	}
	privateLobby, err := manager.CreateLobby("player-b", CreateLobbyRequest{Visibility: VisibilityPrivate, QueueKey: "duel", Capacity: 2})
	if err != nil {
		t.Fatalf("Create private: %v", err)
	}
	secondPublic, err := manager.CreateLobby("player-c", CreateLobbyRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2})
	if err != nil {
		t.Fatalf("Create second public: %v", err)
	}
	if publicLobby.Revision != 1 || publicLobby.OwnerPlayerID != "player-a" || len(publicLobby.Members) != 1 || publicLobby.Members[0].Ready {
		t.Fatalf("public Lobby = %#v", publicLobby)
	}
	if !publicLobby.ExpiresAt.Equal(lobbyTestWall.Add(DefaultLobbyTTL)) {
		t.Fatalf("expires = %v", publicLobby.ExpiresAt)
	}

	page, err := manager.ListLobbies("duel", "", 1)
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if len(page.Lobbies) != 1 || page.Lobbies[0].LobbyID != publicLobby.LobbyID || page.NextCursor == "" {
		t.Fatalf("page = %#v", page)
	}
	next, err := manager.ListLobbies("duel", page.NextCursor, HardMaxListPage)
	if err != nil {
		t.Fatalf("List next page: %v", err)
	}
	if len(next.Lobbies) != 1 || next.Lobbies[0].LobbyID != secondPublic.LobbyID || next.NextCursor != "" {
		t.Fatalf("next page = %#v", next)
	}
	if _, err := manager.GetLobby("player-a", privateLobby.LobbyID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private Get by non-member = %v, want not found", err)
	}
	if got, err := manager.GetLobby("player-b", privateLobby.LobbyID); err != nil || got.LobbyID != privateLobby.LobbyID {
		t.Fatalf("private Get by member = %#v, %v", got, err)
	}
}

func TestLobbyCreateAndListValidation(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	for name, request := range map[string]CreateLobbyRequest{
		"bad visibility": {Visibility: "friends", QueueKey: "duel", Capacity: 2},
		"bad queue":      {Visibility: VisibilityPublic, QueueKey: "bad!", Capacity: 2},
		"capacity low":   {Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 1},
		"capacity high":  {Visibility: VisibilityPublic, QueueKey: "duel", Capacity: HardMaxMatchSize + 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manager.CreateLobby("player-a", request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Create = %v, want invalid", err)
			}
		})
	}
	if _, err := manager.CreateLobby("bad!", CreateLobbyRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid player = %v", err)
	}
	for _, request := range []struct {
		cursor string
		limit  int
	}{
		{cursor: "01", limit: 20},
		{cursor: "-1", limit: 20},
		{cursor: "", limit: 0},
		{cursor: "", limit: HardMaxListPage + 1},
	} {
		if _, err := manager.ListLobbies("duel", request.cursor, request.limit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("List(%q,%d) = %v, want invalid", request.cursor, request.limit, err)
		}
	}
}

func TestJoinLeaveOwnershipAndReadyReset(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	lobby, err := manager.CreateLobby("player-a", CreateLobbyRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 3})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	lobby, err = manager.SetReady("player-a", lobby.LobbyID, lobby.Revision, true)
	if err != nil {
		t.Fatalf("SetReady: %v", err)
	}
	lobby, err = manager.JoinLobby("player-b", lobby.LobbyID, lobby.Revision)
	if err != nil {
		t.Fatalf("Join player-b: %v", err)
	}
	for _, member := range lobby.Members {
		if member.Ready {
			t.Fatalf("membership change did not reset ready: %#v", lobby.Members)
		}
	}
	if _, err := manager.JoinLobby("player-b", lobby.LobbyID, lobby.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate Join = %v, want conflict", err)
	}
	if _, err := manager.JoinLobby("player-c", lobby.LobbyID, lobby.Revision-1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Join = %v, want conflict", err)
	}
	lobby, err = manager.JoinLobby("player-c", lobby.LobbyID, lobby.Revision)
	if err != nil {
		t.Fatalf("Join player-c: %v", err)
	}
	if _, err := manager.JoinLobby("player-d", lobby.LobbyID, lobby.Revision); !errors.Is(err, ErrCapacity) {
		t.Fatalf("over-capacity Join = %v, want capacity", err)
	}

	lobby, err = manager.LeaveLobby("player-a", lobby.LobbyID, lobby.Revision)
	if err != nil {
		t.Fatalf("owner Leave: %v", err)
	}
	if lobby.OwnerPlayerID != "player-b" || len(lobby.Members) != 2 {
		t.Fatalf("after owner leave = %#v", lobby)
	}
	lobby, err = manager.LeaveLobby("player-b", lobby.LobbyID, lobby.Revision)
	if err != nil {
		t.Fatalf("second owner Leave: %v", err)
	}
	if lobby.OwnerPlayerID != "player-c" {
		t.Fatalf("owner = %q, want player-c", lobby.OwnerPlayerID)
	}
	lobby, err = manager.LeaveLobby("player-c", lobby.LobbyID, lobby.Revision)
	if err != nil {
		t.Fatalf("empty Leave: %v", err)
	}
	if lobby.State != LobbyStateClosed || len(lobby.Members) != 0 {
		t.Fatalf("closed Lobby = %#v", lobby)
	}
	if _, err := manager.GetLobby("player-c", lobby.LobbyID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get closed = %v, want not found", err)
	}
}

func TestStartRequiresOwnerFullAndAllReady(t *testing.T) {
	manager, relayStore, _ := newLobbyFixture(t)
	lobby, err := manager.CreateLobby("player-a", CreateLobbyRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := manager.StartMatch("player-a", lobby.LobbyID, lobby.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("Start not full = %v, want conflict", err)
	}
	lobby, err = manager.JoinLobby("player-b", lobby.LobbyID, lobby.Revision)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if _, err := manager.StartMatch("player-b", lobby.LobbyID, lobby.Revision); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner Start = %v, want forbidden", err)
	}
	if _, err := manager.StartMatch("player-a", lobby.LobbyID, lobby.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("Start not ready = %v, want conflict", err)
	}
	lobby, err = manager.SetReady("player-a", lobby.LobbyID, lobby.Revision, true)
	if err != nil {
		t.Fatalf("ready a: %v", err)
	}
	lobby, err = manager.SetReady("player-b", lobby.LobbyID, lobby.Revision, true)
	if err != nil {
		t.Fatalf("ready b: %v", err)
	}
	assignmentA, err := manager.StartMatch("player-a", lobby.LobbyID, lobby.Revision)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if assignmentA.PlayerID != "player-a" || assignmentA.RoomID == "" || assignmentA.GrantSecret == ([32]byte{}) {
		t.Fatalf("assignment a = %#v", assignmentA)
	}
	matchedA, err := manager.GetLobby("player-a", lobby.LobbyID)
	if err != nil || matchedA.State != LobbyStateMatched || matchedA.Assignment == nil || *matchedA.Assignment != assignmentA {
		t.Fatalf("matched a = %#v, %v", matchedA, err)
	}
	matchedB, err := manager.GetLobby("player-b", lobby.LobbyID)
	if err != nil || matchedB.Assignment == nil || matchedB.Assignment.PlayerID != "player-b" {
		t.Fatalf("matched b = %#v, %v", matchedB, err)
	}
	if matchedB.Assignment.GrantID == assignmentA.GrantID || matchedB.Assignment.GrantSecret == assignmentA.GrantSecret {
		t.Fatalf("assignments not isolated: a=%#v b=%#v", assignmentA, matchedB.Assignment)
	}
	room, err := relayStore.GetRoom(assignmentA.RoomID)
	if err != nil || room.Capacity != 2 || len(room.Participants) != 2 {
		t.Fatalf("Relay room = %#v, %v", room, err)
	}
	if _, err := manager.StartMatch("player-a", lobby.LobbyID, matchedA.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeated Start = %v, want conflict", err)
	}
}

func TestLobbyAuthorityExpiresAtExactDeadline(t *testing.T) {
	manager, _, fakeClock := newLobbyFixture(t)
	lobby, err := manager.CreateLobby("player-a", CreateLobbyRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fakeClock.advance(DefaultLobbyTTL - time.Nanosecond)
	if _, err := manager.GetLobby("player-a", lobby.LobbyID); err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}
	fakeClock.advance(time.Nanosecond)
	if _, err := manager.GetLobby("player-a", lobby.LobbyID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get at expiry = %v, want not found", err)
	}
	manager.Expire()
	if len(manager.lobbiesByID) != 0 || len(manager.lobbyByPlayer) != 0 {
		t.Fatalf("expired state retained: lobbies=%d players=%d", len(manager.lobbiesByID), len(manager.lobbyByPlayer))
	}
	if _, err := manager.CreateLobby("player-a", CreateLobbyRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: 2}); err != nil {
		t.Fatalf("Create after expiry: %v", err)
	}
}

func TestConcurrentJoinNeverExceedsCapacity(t *testing.T) {
	manager, _, _ := newLobbyFixture(t)
	lobby, err := manager.CreateLobby("owner", CreateLobbyRequest{Visibility: VisibilityPublic, QueueKey: "duel", Capacity: HardMaxMatchSize})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var wait sync.WaitGroup
	var successes int
	var successesMu sync.Mutex
	for index := range 100 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := manager.JoinLobby(playerID(index), lobby.LobbyID, lobby.Revision)
			if err == nil {
				successesMu.Lock()
				successes++
				successesMu.Unlock()
			} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrCapacity) {
				t.Errorf("Join %d: %v", index, err)
			}
		}(index)
	}
	wait.Wait()
	got, err := manager.GetLobby("owner", lobby.LobbyID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if successes > HardMaxMatchSize-1 || len(got.Members) > HardMaxMatchSize {
		t.Fatalf("successes=%d members=%d", successes, len(got.Members))
	}
}

func newLobbyFixture(t *testing.T) (*Manager, *relayroom.Store, *lobbyTestClock) {
	t.Helper()
	fakeClock := &lobbyTestClock{wall: lobbyTestWall}
	relayStore, err := relayroom.New(relayroom.Config{
		Limits: relayroom.DefaultLimits(),
		Now:    fakeClock.read,
		Random: &incrementingReader{next: 0x8000},
	})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	manager, err := New(Config{Rooms: relayStore, Now: fakeClock.read, Random: &incrementingReader{next: 0x1000}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return manager, relayStore, fakeClock
}

func playerID(index int) string {
	const digits = "0123456789"
	if index < 10 {
		return "player-0" + string(digits[index])
	}
	return "player-" + string(digits[index/10]) + string(digits[index%10])
}
