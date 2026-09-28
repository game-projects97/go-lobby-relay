package matchmaking

import (
	"errors"
	"testing"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/relayroom"
)

func TestMatchTTLConfigBounds(t *testing.T) {
	relayStore, err := relayroom.New(relayroom.Config{Limits: relayroom.DefaultLimits()})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	for _, ttl := range []time.Duration{-time.Second, HardMaxMatchTTL + time.Nanosecond} {
		if _, err := New(Config{Rooms: relayStore, MatchTTL: ttl}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("New(MatchTTL=%v) err = %v, want ErrInvalid", ttl, err)
		}
	}
	if _, err := New(Config{Rooms: relayStore, MatchTTL: HardMaxMatchTTL}); err != nil {
		t.Fatalf("New(MatchTTL=max) = %v", err)
	}
}

func TestConfiguredMatchTTLBoundsRoomAndGrants(t *testing.T) {
	fakeClock := &lobbyTestClock{wall: lobbyTestWall}
	relayStore, err := relayroom.New(relayroom.Config{
		Limits: relayroom.DefaultLimits(), Now: fakeClock.read, Random: &incrementingReader{next: 0x8000},
	})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	const ttl = 20 * time.Minute
	manager, err := New(Config{Rooms: relayStore, Now: fakeClock.read, Random: &incrementingReader{next: 0x1000}, MatchTTL: ttl})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := manager.CreateTicket("player-a", CreateTicketRequest{QueueKey: "race", Capacity: 2}); err != nil {
		t.Fatalf("CreateTicket(a): %v", err)
	}
	matched, err := manager.CreateTicket("player-b", CreateTicketRequest{QueueKey: "race", Capacity: 2})
	if err != nil || matched.Assignment == nil {
		t.Fatalf("CreateTicket(b) = %#v, %v", matched, err)
	}
	if want := lobbyTestWall.Add(ttl); !matched.Assignment.GrantExpiresAt.Equal(want) {
		t.Fatalf("grant expiry = %v, want %v", matched.Assignment.GrantExpiresAt, want)
	}
	room, err := relayStore.GetRoom(matched.Assignment.RoomID)
	if err != nil || !room.ExpiresAt.Equal(lobbyTestWall.Add(ttl)) {
		t.Fatalf("room = %#v, %v", room, err)
	}

	fakeClock.advance(MatchTTL + time.Second)
	manager.Expire()
	if still, err := manager.GetTicket("player-b"); err != nil || still.Assignment == nil {
		t.Fatalf("assignment after default TTL = %#v, %v; want still live", still, err)
	}
}
