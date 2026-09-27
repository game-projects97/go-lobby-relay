package relayroom

import (
	"errors"
	"io"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
)

var testWall = time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)

func TestDefaultLimitsAndHardMaxima(t *testing.T) {
	want := Limits{
		MaxOpenRooms:                   256,
		MaxRoomRecords:                 4096,
		MaxRoomCapacity:                16,
		MaxActiveSessions:              4096,
		MaxRoomTTL:                     2 * time.Hour,
		MaxGrantTTL:                    2 * time.Hour,
		SweepInterval:                  time.Second,
		EmptyGrace:                     5 * time.Second,
		TombstoneTTL:                   60 * time.Second,
		ChallengeTTL:                   3 * time.Second,
		BindingTTL:                     60 * time.Second,
		PreauthSourcePacketRate:        16,
		PreauthSourcePacketBurst:       160,
		PreauthSourceByteRate:          19_200,
		PreauthSourceByteBurst:         192_000,
		PreauthGlobalPacketRate:        128,
		PreauthGlobalPacketBurst:       1_280,
		PreauthGlobalByteRate:          153_600,
		PreauthGlobalByteBurst:         1_536_000,
		SessionPacketRate:              40,
		SessionPacketBurst:             40,
		SessionByteRate:                20_480,
		SessionByteBurst:               20_480,
		RoomPacketRate:                 160,
		RoomPacketBurst:                160,
		RoomByteRate:                   81_920,
		RoomByteBurst:                  81_920,
		AuthenticatedGlobalPacketRate:  1_280,
		AuthenticatedGlobalPacketBurst: 1_280,
		AuthenticatedGlobalByteRate:    655_360,
		AuthenticatedGlobalByteBurst:   655_360,
		RoomFanoutWriteRate:            480,
		RoomFanoutWriteBurst:           480,
		RoomFanoutByteRate:             245_760,
		RoomFanoutByteBurst:            245_760,
		GlobalFanoutWriteRate:          3_840,
		GlobalFanoutWriteBurst:         3_840,
		GlobalFanoutByteRate:           1_966_080,
		GlobalFanoutByteBurst:          1_966_080,
	}
	if got := DefaultLimits(); got != want {
		t.Fatalf("DefaultLimits() = %#v, want %#v", got, want)
	}

	hard := Limits{
		MaxOpenRooms:                   HardMaxOpenRooms,
		MaxRoomRecords:                 HardMaxRoomRecords,
		MaxRoomCapacity:                HardMaxRoomCapacity,
		MaxActiveSessions:              HardMaxActiveSessions,
		MaxRoomTTL:                     HardMaxRoomTTL,
		MaxGrantTTL:                    HardMaxGrantTTL,
		SweepInterval:                  HardMaxSweepInterval,
		EmptyGrace:                     HardMaxEmptyGrace,
		TombstoneTTL:                   HardMaxTombstoneTTL,
		ChallengeTTL:                   HardMaxChallengeTTL,
		BindingTTL:                     HardMaxBindingTTL,
		PreauthSourcePacketRate:        HardMaxPreauthSourcePacketRate,
		PreauthSourcePacketBurst:       HardMaxPreauthSourcePacketBurst,
		PreauthSourceByteRate:          HardMaxPreauthSourceByteRate,
		PreauthSourceByteBurst:         HardMaxPreauthSourceByteBurst,
		PreauthGlobalPacketRate:        HardMaxPreauthGlobalPacketRate,
		PreauthGlobalPacketBurst:       HardMaxPreauthGlobalPacketBurst,
		PreauthGlobalByteRate:          HardMaxPreauthGlobalByteRate,
		PreauthGlobalByteBurst:         HardMaxPreauthGlobalByteBurst,
		SessionPacketRate:              HardMaxSessionPacketRate,
		SessionPacketBurst:             HardMaxSessionPacketBurst,
		SessionByteRate:                HardMaxSessionByteRate,
		SessionByteBurst:               HardMaxSessionByteBurst,
		RoomPacketRate:                 HardMaxRoomPacketRate,
		RoomPacketBurst:                HardMaxRoomPacketBurst,
		RoomByteRate:                   HardMaxRoomByteRate,
		RoomByteBurst:                  HardMaxRoomByteBurst,
		AuthenticatedGlobalPacketRate:  HardMaxAuthenticatedGlobalPacketRate,
		AuthenticatedGlobalPacketBurst: HardMaxAuthenticatedGlobalPacketBurst,
		AuthenticatedGlobalByteRate:    HardMaxAuthenticatedGlobalByteRate,
		AuthenticatedGlobalByteBurst:   HardMaxAuthenticatedGlobalByteBurst,
		RoomFanoutWriteRate:            HardMaxRoomFanoutWriteRate,
		RoomFanoutWriteBurst:           HardMaxRoomFanoutWriteBurst,
		RoomFanoutByteRate:             HardMaxRoomFanoutByteRate,
		RoomFanoutByteBurst:            HardMaxRoomFanoutByteBurst,
		GlobalFanoutWriteRate:          HardMaxGlobalFanoutWriteRate,
		GlobalFanoutWriteBurst:         HardMaxGlobalFanoutWriteBurst,
		GlobalFanoutByteRate:           HardMaxGlobalFanoutByteRate,
		GlobalFanoutByteBurst:          HardMaxGlobalFanoutByteBurst,
	}
	if hard != want {
		t.Fatalf("hard maxima = %#v, want %#v", hard, want)
	}

	store, err := New(Config{Limits: hard})
	if err != nil {
		t.Fatalf("New(exact hard maxima): %v", err)
	}
	if store.now == nil || store.random == nil {
		t.Fatal("New with nil clock/random did not install production defaults")
	}
}

func TestNewRejectsEveryInvalidLimit(t *testing.T) {
	intFields := []struct {
		name string
		max  int
		set  func(*Limits, int)
	}{
		{"MaxOpenRooms", HardMaxOpenRooms, func(l *Limits, value int) { l.MaxOpenRooms = value }},
		{"MaxRoomRecords", HardMaxRoomRecords, func(l *Limits, value int) { l.MaxRoomRecords = value }},
		{"MaxRoomCapacity", HardMaxRoomCapacity, func(l *Limits, value int) { l.MaxRoomCapacity = value }},
		{"MaxActiveSessions", HardMaxActiveSessions, func(l *Limits, value int) { l.MaxActiveSessions = value }},
	}
	for _, field := range intFields {
		for _, value := range []int{0, -1, field.max + 1} {
			t.Run(field.name+"/"+limitValueName(value), func(t *testing.T) {
				limits := DefaultLimits()
				field.set(&limits, value)
				if _, err := New(Config{Limits: limits}); !errors.Is(err, ErrInvalid) {
					t.Fatalf("New() error = %v, want ErrInvalid", err)
				}
			})
		}
	}

	durationFields := []struct {
		name string
		max  time.Duration
		set  func(*Limits, time.Duration)
	}{
		{"MaxRoomTTL", HardMaxRoomTTL, func(l *Limits, value time.Duration) { l.MaxRoomTTL = value }},
		{"MaxGrantTTL", HardMaxGrantTTL, func(l *Limits, value time.Duration) { l.MaxGrantTTL = value }},
		{"SweepInterval", HardMaxSweepInterval, func(l *Limits, value time.Duration) { l.SweepInterval = value }},
		{"EmptyGrace", HardMaxEmptyGrace, func(l *Limits, value time.Duration) { l.EmptyGrace = value }},
		{"TombstoneTTL", HardMaxTombstoneTTL, func(l *Limits, value time.Duration) { l.TombstoneTTL = value }},
		{"ChallengeTTL", HardMaxChallengeTTL, func(l *Limits, value time.Duration) { l.ChallengeTTL = value }},
		{"BindingTTL", HardMaxBindingTTL, func(l *Limits, value time.Duration) { l.BindingTTL = value }},
	}
	for _, field := range durationFields {
		for _, value := range []time.Duration{0, -1, field.max + time.Nanosecond} {
			t.Run(field.name+"/"+limitValueName(int(value)), func(t *testing.T) {
				limits := DefaultLimits()
				field.set(&limits, value)
				if _, err := New(Config{Limits: limits}); !errors.Is(err, ErrInvalid) {
					t.Fatalf("New() error = %v, want ErrInvalid", err)
				}
			})
		}
	}

	relationships := []struct {
		name   string
		mutate func(*Limits)
	}{
		{"open rooms exceed records", func(l *Limits) { l.MaxRoomRecords = l.MaxOpenRooms - 1 }},
		{"room capacity exceeds sessions", func(l *Limits) { l.MaxActiveSessions = l.MaxRoomCapacity - 1 }},
		{"grant TTL exceeds room TTL", func(l *Limits) { l.MaxRoomTTL = l.MaxGrantTTL - time.Nanosecond }},
	}
	for _, tt := range relationships {
		t.Run(tt.name, func(t *testing.T) {
			limits := DefaultLimits()
			tt.mutate(&limits)
			if _, err := New(Config{Limits: limits}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("New() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestDefaultClockReturnsUTCMonotonicReadings(t *testing.T) {
	store, err := New(Config{Limits: DefaultLimits()})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	first := store.now()
	second := store.now()
	if first.Wall.Location() != time.UTC || second.Wall.Location() != time.UTC {
		t.Fatalf("clock locations = %v, %v; want UTC", first.Wall.Location(), second.Wall.Location())
	}
	if second.Mono < first.Mono {
		t.Fatalf("monotonic readings moved backward: first=%v second=%v", first.Mono, second.Mono)
	}
	if delta := second.Wall.Sub(first.Wall) - (second.Mono - first.Mono); delta < -time.Millisecond || delta > time.Millisecond {
		t.Fatalf("wall/monotonic deltas differ by %v", delta)
	}
}

func TestCreateRoomSamplesClockAfterAcquiringStoreLock(t *testing.T) {
	limits := DefaultLimits()
	random := newScriptedReader()
	var store *Store
	now := func() clock.Reading {
		if store.mu.TryLock() {
			store.mu.Unlock()
			// A pre-lock sample can become this stale while waiting behind a CSPRNG read.
			return clock.Reading{Wall: testWall, Mono: 0}
		}
		return clock.Reading{Wall: testWall.Add(2 * time.Hour), Mono: 2 * time.Hour}
	}
	var err error
	store, err = New(Config{Limits: limits, Now: now, Random: random})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	if _, created, err := store.CreateRoom("room", validRoomSpec(testWall, 1)); !errors.Is(err, ErrInvalid) || created {
		t.Fatalf("CreateRoom() = (_, %t, %v), want fresh post-lock clock to reject expired definition", created, err)
	}
	if len(random.calls) != 0 {
		t.Fatalf("expired definition used randomness after lock wait: calls=%v", random.calls)
	}
	assertStoreCounts(t, store, 0, 0, 0, 0)
}

func TestCreateRoomCanonicalRetryAndDeepCopies(t *testing.T) {
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 10 * time.Second}}
	random := newScriptedReader(
		filled(0x11, 16), filled(0x21, 32),
		filled(0x12, 16), filled(0x22, 32),
	)
	store := newTestStore(t, DefaultLimits(), fakeClock, random)

	roomSpec := RoomSpec{
		Capacity:  2,
		ExpiresAt: testWall.Add(2 * time.Hour),
		Participants: []ParticipantSpec{
			{ParticipantID: "bob", SessionID: "session-b", GrantExpiresAt: testWall.Add(90 * time.Minute)},
			{ParticipantID: "alice", SessionID: "session-a", GrantExpiresAt: testWall.Add(time.Hour)},
		},
	}
	original := cloneRoomSpec(roomSpec)

	allocation, created, err := store.CreateRoom("room-1", roomSpec)
	if err != nil || !created {
		t.Fatalf("CreateRoom() = (_, %t, %v), want created", created, err)
	}
	if !reflect.DeepEqual(roomSpec, original) {
		t.Fatalf("CreateRoom mutated input: got %#v want %#v", roomSpec, original)
	}
	if allocation.RoomID != "room-1" || allocation.CreatedAt != testWall ||
		allocation.ExpiresAt != testWall.Add(2*time.Hour) || allocation.Capacity != 2 {
		t.Fatalf("allocation header = %#v", allocation)
	}
	if len(allocation.Grants) != 2 {
		t.Fatalf("len(Grants) = %d, want 2", len(allocation.Grants))
	}
	assertGrant(t, allocation.Grants[0], "alice", "session-a", bytes16(0x11), bytes32(0x21), testWall.Add(time.Hour), GrantStateIssued)
	assertGrant(t, allocation.Grants[1], "bob", "session-b", bytes16(0x12), bytes32(0x22), testWall.Add(90*time.Minute), GrantStateIssued)
	assertReads(t, random.calls, 16, 32, 16, 32)
	if fakeClock.calls != 1 {
		t.Fatalf("clock calls = %d, want 1", fakeClock.calls)
	}

	roomSpec.Participants[0].ParticipantID = "mutated-input"
	allocation.RoomID = "mutated-output"
	allocation.Capacity = 99
	allocation.Grants[0].ParticipantID = "mutated-grant"
	(*allocation.Grants[0].GrantSecret)[0] = 0xff
	allocation.Grants = append(allocation.Grants, ParticipantGrant{})

	zone := time.FixedZone("retry-offset", 9*60*60)
	retryRoomSpec := RoomSpec{
		Capacity:  2,
		ExpiresAt: original.ExpiresAt.In(zone),
		Participants: []ParticipantSpec{
			{
				ParticipantID:  original.Participants[1].ParticipantID,
				SessionID:      original.Participants[1].SessionID,
				GrantExpiresAt: original.Participants[1].GrantExpiresAt.In(zone),
			},
			{
				ParticipantID:  original.Participants[0].ParticipantID,
				SessionID:      original.Participants[0].SessionID,
				GrantExpiresAt: original.Participants[0].GrantExpiresAt.In(zone),
			},
		},
	}
	retry, created, err := store.CreateRoom("room-1", retryRoomSpec)
	if err != nil || created {
		t.Fatalf("retry CreateRoom() = (_, %t, %v), want existing", created, err)
	}
	if retry.RoomID != "room-1" || retry.Capacity != 2 || len(retry.Grants) != 2 {
		t.Fatalf("retry allocation was affected by caller mutation: %#v", retry)
	}
	assertGrant(t, retry.Grants[0], "alice", "session-a", bytes16(0x11), bytes32(0x21), testWall.Add(time.Hour), GrantStateIssued)
	assertGrant(t, retry.Grants[1], "bob", "session-b", bytes16(0x12), bytes32(0x22), testWall.Add(90*time.Minute), GrantStateIssued)
	assertReads(t, random.calls, 16, 32, 16, 32)
	if fakeClock.calls != 2 {
		t.Fatalf("clock calls after retry = %d, want 2", fakeClock.calls)
	}
	assertStoreCounts(t, store, 1, 2, 1, 2)
}

func TestCreateRoomValidatesSpecsBeforeRandomness(t *testing.T) {
	longID := strings.Repeat("a", protocol.MaxIDBytes+1)
	valid := validRoomSpec(testWall, 1)
	tests := []struct {
		name   string
		roomID string
		limits Limits
		change func(*RoomSpec)
		want   error
	}{
		{"empty room ID", "", DefaultLimits(), func(*RoomSpec) {}, ErrInvalid},
		{"long room ID", longID, DefaultLimits(), func(*RoomSpec) {}, ErrInvalid},
		{"punctuated first room ID", ".room", DefaultLimits(), func(*RoomSpec) {}, ErrInvalid},
		{"slash room ID", "room/one", DefaultLimits(), func(*RoomSpec) {}, ErrInvalid},
		{"empty participants", "room", DefaultLimits(), func(d *RoomSpec) { d.Participants = nil }, ErrInvalid},
		{"zero capacity", "room", DefaultLimits(), func(d *RoomSpec) { d.Capacity = 0 }, ErrInvalid},
		{"capacity mismatch", "room", DefaultLimits(), func(d *RoomSpec) { d.Capacity = 2 }, ErrInvalid},
		{"empty participant ID", "room", DefaultLimits(), func(d *RoomSpec) { d.Participants[0].ParticipantID = "" }, ErrInvalid},
		{"long participant ID", "room", DefaultLimits(), func(d *RoomSpec) { d.Participants[0].ParticipantID = longID }, ErrInvalid},
		{"punctuated first participant ID", "room", DefaultLimits(), func(d *RoomSpec) { d.Participants[0].ParticipantID = "_p" }, ErrInvalid},
		{"empty session ID", "room", DefaultLimits(), func(d *RoomSpec) { d.Participants[0].SessionID = "" }, ErrInvalid},
		{"long session ID", "room", DefaultLimits(), func(d *RoomSpec) { d.Participants[0].SessionID = longID }, ErrInvalid},
		{"non-ASCII session ID", "room", DefaultLimits(), func(d *RoomSpec) { d.Participants[0].SessionID = "세션" }, ErrInvalid},
		{"duplicate participant ID", "room", DefaultLimits(), duplicateParticipantID, ErrInvalid},
		{"duplicate session ID", "room", DefaultLimits(), duplicateSessionID, ErrInvalid},
		{"zero room expiry", "room", DefaultLimits(), func(d *RoomSpec) { d.ExpiresAt = time.Time{} }, ErrInvalid},
		{"zero grant expiry", "room", DefaultLimits(), func(d *RoomSpec) { d.Participants[0].GrantExpiresAt = time.Time{} }, ErrInvalid},
		{"grant past room expiry", "room", DefaultLimits(), func(d *RoomSpec) { d.Participants[0].GrantExpiresAt = d.ExpiresAt.Add(time.Nanosecond) }, ErrInvalid},
		{"configured capacity over", "room", withLimit(DefaultLimits(), func(l *Limits) { l.MaxRoomCapacity = 1 }), threeParticipants, ErrCapacity},
		{"hard participant count over", "room", DefaultLimits(), seventeenParticipants, ErrCapacity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: time.Second}}
			random := newScriptedReader()
			store := newTestStore(t, tt.limits, fakeClock, random)
			roomSpec := cloneRoomSpec(valid)
			tt.change(&roomSpec)

			if _, _, err := store.CreateRoom(tt.roomID, roomSpec); !errors.Is(err, tt.want) {
				t.Fatalf("CreateRoom() error = %v, want %v", err, tt.want)
			}
			if len(random.calls) != 0 {
				t.Fatalf("invalid request used randomness: calls=%v", random.calls)
			}
			assertStoreCounts(t, store, 0, 0, 0, 0)

			random.reset(filled(0x31, 16), filled(0x41, 32))
			if _, created, err := store.CreateRoom("fallback", valid); err != nil || !created {
				t.Fatalf("valid create after rejection = (_, %t, %v), want created", created, err)
			}
		})
	}
}

func TestCreateRoomAcceptsIdentifierAndCapacityBoundaries(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOpenRooms = 2
	limits.MaxRoomRecords = 2
	limits.MaxRoomCapacity = 2
	limits.MaxActiveSessions = 4
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
	random := newScriptedReader(
		filled(0x01, 16), filled(0x11, 32),
		filled(0x02, 16), filled(0x12, 32),
		filled(0x03, 16), filled(0x13, 32),
		filled(0x04, 16), filled(0x14, 32),
	)
	store := newTestStore(t, limits, fakeClock, random)
	boundaryID := strings.Repeat("a", protocol.MaxIDBytes)
	roomSpec := RoomSpec{
		Capacity:  2,
		ExpiresAt: testWall.Add(time.Hour),
		Participants: []ParticipantSpec{
			{ParticipantID: boundaryID, SessionID: boundaryID, GrantExpiresAt: testWall.Add(time.Minute)},
			{ParticipantID: "z._-", SessionID: "z._-", GrantExpiresAt: testWall.Add(time.Minute)},
		},
	}
	if _, created, err := store.CreateRoom(boundaryID, roomSpec); err != nil || !created {
		t.Fatalf("boundary CreateRoom() = (_, %t, %v), want created", created, err)
	}
	if _, created, err := store.CreateRoom("second", roomSpec); err != nil || !created {
		t.Fatalf("same participant/session IDs in another room = (_, %t, %v), want created", created, err)
	}
	assertStoreCounts(t, store, 2, 4, 2, 4)
}

func TestCreateRoomTTLBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		limits      Limits
		roomExpiry  time.Time
		grantExpiry time.Time
		want        error
	}{
		{"room at now", DefaultLimits(), testWall, testWall, ErrInvalid},
		{"room now plus one nanosecond", DefaultLimits(), testWall.Add(time.Nanosecond), testWall.Add(time.Nanosecond), nil},
		{"room exact maximum", DefaultLimits(), testWall.Add(HardMaxRoomTTL), testWall.Add(time.Hour), nil},
		{"room maximum plus one nanosecond", DefaultLimits(), testWall.Add(HardMaxRoomTTL + time.Nanosecond), testWall.Add(time.Hour), ErrInvalid},
		{"grant at now", DefaultLimits(), testWall.Add(time.Hour), testWall, ErrInvalid},
		{"grant now plus one nanosecond", DefaultLimits(), testWall.Add(time.Hour), testWall.Add(time.Nanosecond), nil},
		{"grant exact maximum", DefaultLimits(), testWall.Add(HardMaxRoomTTL), testWall.Add(HardMaxGrantTTL), nil},
		{
			"grant configured maximum plus one nanosecond",
			withLimit(DefaultLimits(), func(l *Limits) { l.MaxGrantTTL = time.Hour }),
			testWall.Add(2 * time.Hour),
			testWall.Add(time.Hour + time.Nanosecond),
			ErrInvalid,
		},
		{"grant equals room deadline", DefaultLimits(), testWall.Add(time.Hour), testWall.Add(time.Hour), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 37 * time.Second}}
			random := newScriptedReader(filled(0x51, 16), filled(0x61, 32))
			store := newTestStore(t, tt.limits, fakeClock, random)
			roomSpec := RoomSpec{
				Capacity:  1,
				ExpiresAt: tt.roomExpiry,
				Participants: []ParticipantSpec{{
					ParticipantID:  "participant",
					SessionID:      "session",
					GrantExpiresAt: tt.grantExpiry,
				}},
			}
			allocation, created, err := store.CreateRoom("room", roomSpec)
			if tt.want != nil {
				if !errors.Is(err, tt.want) || created {
					t.Fatalf("CreateRoom() = (_, %t, %v), want %v", created, err, tt.want)
				}
				if len(random.calls) != 0 {
					t.Fatalf("invalid TTL used randomness: calls=%v", random.calls)
				}
				assertStoreCounts(t, store, 0, 0, 0, 0)
				return
			}
			if err != nil || !created {
				t.Fatalf("CreateRoom() = (_, %t, %v), want created", created, err)
			}
			if allocation.ExpiresAt != tt.roomExpiry.UTC() || allocation.Grants[0].GrantExpiresAt != tt.grantExpiry.UTC() {
				t.Fatalf("canonical expiries = %v/%v", allocation.ExpiresAt, allocation.Grants[0].GrantExpiresAt)
			}
			assertReads(t, random.calls, 16, 32)
		})
	}
}

func TestCreateRoomConflictsPrecedeCapacityAndRandomness(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOpenRooms = 1
	limits.MaxRoomRecords = 1
	limits.MaxRoomCapacity = 2
	limits.MaxActiveSessions = 2
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: time.Minute}}
	random := newScriptedReader(
		filled(0x71, 16), filled(0x81, 32),
		filled(0x72, 16), filled(0x82, 32),
	)
	store := newTestStore(t, limits, fakeClock, random)
	base := validRoomSpec(testWall, 2)
	if _, created, err := store.CreateRoom("room", base); err != nil || !created {
		t.Fatalf("initial CreateRoom() = (_, %t, %v)", created, err)
	}
	initialCalls := len(random.calls)

	changes := []struct {
		name   string
		change func(*RoomSpec)
	}{
		{"capacity", func(d *RoomSpec) { d.Capacity = 1; d.Participants = d.Participants[:1] }},
		{"room expiry", func(d *RoomSpec) { d.ExpiresAt = d.ExpiresAt.Add(time.Minute) }},
		{"participant tuple", func(d *RoomSpec) { d.Participants[0].ParticipantID = "different" }},
		{"session tuple", func(d *RoomSpec) { d.Participants[0].SessionID = "different" }},
		{"grant expiry", func(d *RoomSpec) {
			d.Participants[0].GrantExpiresAt = d.Participants[0].GrantExpiresAt.Add(time.Nanosecond)
		}},
	}
	for _, tt := range changes {
		t.Run(tt.name, func(t *testing.T) {
			roomSpec := cloneRoomSpec(base)
			tt.change(&roomSpec)
			if _, _, err := store.CreateRoom("room", roomSpec); !errors.Is(err, ErrConflict) {
				t.Fatalf("CreateRoom() error = %v, want ErrConflict", err)
			}
		})
	}
	if len(random.calls) != initialCalls {
		t.Fatalf("conflicts used randomness: calls=%v", random.calls)
	}

	retry, created, err := store.CreateRoom("room", base)
	if err != nil || created || len(retry.Grants) != 2 {
		t.Fatalf("full-cap retry = (_, %t, %v), want existing", created, err)
	}
	if len(random.calls) != initialCalls {
		t.Fatalf("retry used randomness: calls=%v", random.calls)
	}
	if _, _, err := store.CreateRoom("new-room", base); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new room at full caps error = %v, want ErrCapacity", err)
	}
	if len(random.calls) != initialCalls {
		t.Fatalf("capacity rejection used randomness: calls=%v", random.calls)
	}
	assertStoreCounts(t, store, 1, 2, 1, 2)
}

func TestCreateRoomEnforcesConfiguredCapsBeforeRandomness(t *testing.T) {
	tests := []struct {
		name   string
		limits Limits
		fill   []RoomSpec
	}{
		{
			"open room and resident record caps",
			withLimit(DefaultLimits(), func(l *Limits) {
				l.MaxOpenRooms = 2
				l.MaxRoomRecords = 2
				l.MaxRoomCapacity = 1
				l.MaxActiveSessions = 3
			}),
			[]RoomSpec{validRoomSpec(testWall, 1), validRoomSpec(testWall, 1)},
		},
		{
			"active session cap",
			withLimit(DefaultLimits(), func(l *Limits) {
				l.MaxOpenRooms = 3
				l.MaxRoomRecords = 3
				l.MaxRoomCapacity = 1
				l.MaxActiveSessions = 2
			}),
			[]RoomSpec{validRoomSpec(testWall, 1), validRoomSpec(testWall, 1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
			random := newScriptedReader(
				filled(0x91, 16), filled(0xa1, 32),
				filled(0x92, 16), filled(0xa2, 32),
			)
			store := newTestStore(t, tt.limits, fakeClock, random)
			for index, roomSpec := range tt.fill {
				if _, created, err := store.CreateRoom("room-"+string(rune('a'+index)), roomSpec); err != nil || !created {
					t.Fatalf("fill %d = (_, %t, %v), want created", index, created, err)
				}
			}
			calls := len(random.calls)
			if _, _, err := store.CreateRoom("one-over", validRoomSpec(testWall, 1)); !errors.Is(err, ErrCapacity) {
				t.Fatalf("one-over CreateRoom() error = %v, want ErrCapacity", err)
			}
			if len(random.calls) != calls {
				t.Fatalf("one-over request used randomness: calls=%v", random.calls)
			}
			assertStoreCounts(t, store, len(tt.fill), len(tt.fill), len(tt.fill), len(tt.fill))
		})
	}
}

func TestCreateRoomRetryUsesStoredMonotonicDeadlines(t *testing.T) {
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 100 * time.Second}}
	random := newScriptedReader(
		filled(0xb1, 16), filled(0xc1, 32),
		filled(0xb2, 16), filled(0xc2, 32),
	)
	store := newTestStore(t, DefaultLimits(), fakeClock, random)
	roomSpec := RoomSpec{
		Capacity:  2,
		ExpiresAt: testWall.Add(2 * time.Hour),
		Participants: []ParticipantSpec{
			{ParticipantID: "alice", SessionID: "session-a", GrantExpiresAt: testWall.Add(time.Hour)},
			{ParticipantID: "bob", SessionID: "session-b", GrantExpiresAt: testWall.Add(90 * time.Minute)},
		},
	}
	first, created, err := store.CreateRoom("room", roomSpec)
	if err != nil || !created {
		t.Fatalf("initial CreateRoom() = (_, %t, %v)", created, err)
	}
	initialCalls := len(random.calls)

	fakeClock.reading = clock.Reading{Wall: testWall.Add(10 * time.Hour), Mono: 100*time.Second + 30*time.Minute}
	retry, created, err := store.CreateRoom("room", roomSpec)
	if err != nil || created {
		t.Fatalf("forward-wall retry = (_, %t, %v), want existing", created, err)
	}
	if retry.Grants[0].State != GrantStateIssued || retry.Grants[0].GrantSecret == nil {
		t.Fatalf("forward wall step changed live grant: %#v", retry.Grants[0])
	}

	fakeClock.reading = clock.Reading{Wall: testWall.Add(-10 * time.Hour), Mono: 100*time.Second + time.Hour}
	retry, created, err = store.CreateRoom("room", roomSpec)
	if err != nil || created {
		t.Fatalf("exact grant-deadline retry = (_, %t, %v), want existing", created, err)
	}
	if retry.Grants[0].GrantID != first.Grants[0].GrantID || retry.Grants[0].GrantExpiresAt != first.Grants[0].GrantExpiresAt ||
		retry.Grants[0].State != GrantStateExpired || retry.Grants[0].GrantSecret != nil {
		t.Fatalf("terminal grant was reissued or extended: %#v", retry.Grants[0])
	}
	if retry.Grants[1].GrantID != first.Grants[1].GrantID || retry.Grants[1].State != GrantStateIssued || retry.Grants[1].GrantSecret == nil {
		t.Fatalf("later grant did not remain live: %#v", retry.Grants[1])
	}
	if len(random.calls) != initialCalls {
		t.Fatalf("deadline retries used randomness: calls=%v", random.calls)
	}
	if fakeClock.calls != 3 {
		t.Fatalf("clock calls = %d, want one per operation", fakeClock.calls)
	}
	assertStoreCounts(t, store, 1, 2, 1, 2)
}

func TestCreateRoomRetriesGrantIDCollisions(t *testing.T) {
	t.Run("same batch", func(t *testing.T) {
		fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
		random := newScriptedReader(
			filled(0xd1, 16), filled(0xe1, 32),
			filled(0xd1, 16), filled(0xd2, 16), filled(0xe2, 32),
		)
		store := newTestStore(t, DefaultLimits(), fakeClock, random)
		allocation, created, err := store.CreateRoom("room", validRoomSpec(testWall, 2))
		if err != nil || !created {
			t.Fatalf("CreateRoom() = (_, %t, %v)", created, err)
		}
		if allocation.Grants[0].GrantID != bytes16(0xd1) || allocation.Grants[1].GrantID != bytes16(0xd2) {
			t.Fatalf("grant IDs = %x, %x", allocation.Grants[0].GrantID, allocation.Grants[1].GrantID)
		}
		assertReads(t, random.calls, 16, 32, 16, 16, 32)
		assertStoreCounts(t, store, 1, 2, 1, 2)
	})

	t.Run("existing index", func(t *testing.T) {
		fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
		random := newScriptedReader(
			filled(0xd1, 16), filled(0xe1, 32),
			filled(0xd1, 16), filled(0xd2, 16), filled(0xe2, 32),
		)
		store := newTestStore(t, DefaultLimits(), fakeClock, random)
		first, _, err := store.CreateRoom("room-a", validRoomSpec(testWall, 1))
		if err != nil {
			t.Fatalf("first CreateRoom(): %v", err)
		}
		second, created, err := store.CreateRoom("room-b", validRoomSpec(testWall, 1))
		if err != nil || !created {
			t.Fatalf("second CreateRoom() = (_, %t, %v)", created, err)
		}
		if first.Grants[0].GrantID != bytes16(0xd1) || second.Grants[0].GrantID != bytes16(0xd2) {
			t.Fatalf("grant IDs = %x, %x", first.Grants[0].GrantID, second.Grants[0].GrantID)
		}
		assertReads(t, random.calls, 16, 32, 16, 16, 32)
		assertStoreCounts(t, store, 2, 2, 2, 2)
	})

	t.Run("ninth draw succeeds", func(t *testing.T) {
		chunks := [][]byte{filled(0xd1, 16), filled(0xe1, 32)}
		for range 8 {
			chunks = append(chunks, filled(0xd1, 16))
		}
		chunks = append(chunks, filled(0xd2, 16), filled(0xe2, 32))
		fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
		random := newScriptedReader(chunks...)
		store := newTestStore(t, DefaultLimits(), fakeClock, random)
		if _, _, err := store.CreateRoom("room-a", validRoomSpec(testWall, 1)); err != nil {
			t.Fatalf("seed CreateRoom(): %v", err)
		}
		allocation, created, err := store.CreateRoom("room-b", validRoomSpec(testWall, 1))
		if err != nil || !created {
			t.Fatalf("ninth-draw CreateRoom() = (_, %t, %v)", created, err)
		}
		if allocation.Grants[0].GrantID != bytes16(0xd2) {
			t.Fatalf("grant ID = %x, want %x", allocation.Grants[0].GrantID, bytes16(0xd2))
		}
		if got := len(random.calls); got != 12 {
			t.Fatalf("random calls = %d, want 12 (seed ID/secret + 9 IDs + secret)", got)
		}
	})

	t.Run("nine collisions are fatal and atomic", func(t *testing.T) {
		chunks := [][]byte{filled(0xd1, 16), filled(0xe1, 32)}
		for range 9 {
			chunks = append(chunks, filled(0xd1, 16))
		}
		fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
		random := newScriptedReader(chunks...)
		store := newTestStore(t, DefaultLimits(), fakeClock, random)
		if _, _, err := store.CreateRoom("room-a", validRoomSpec(testWall, 1)); err != nil {
			t.Fatalf("seed CreateRoom(): %v", err)
		}
		if _, created, err := store.CreateRoom("room-b", validRoomSpec(testWall, 1)); !errors.Is(err, ErrFatalRandom) || created {
			t.Fatalf("collision exhaustion = (_, %t, %v), want ErrFatalRandom", created, err)
		}
		assertStoreCounts(t, store, 1, 1, 1, 1)
		if got := len(random.calls); got != 11 {
			t.Fatalf("random calls = %d, want 11 (seed ID/secret + 9 IDs)", got)
		}
	})
}

func TestCreateRoomRandomReadFailuresRollbackAtomically(t *testing.T) {
	boom := errors.New("random failed")
	tests := []struct {
		name      string
		chunks    [][]byte
		failAt    int
		wantCalls []int
	}{
		{"short first ID", [][]byte{filled(0x01, 15)}, 0, []int{16}},
		{"error first ID", nil, 1, []int{16}},
		{"short first secret", [][]byte{filled(0x01, 16), filled(0x11, 31)}, 0, []int{16, 32}},
		{"error first secret", [][]byte{filled(0x01, 16)}, 2, []int{16, 32}},
		{"short second ID", [][]byte{filled(0x01, 16), filled(0x11, 32), filled(0x02, 15)}, 0, []int{16, 32, 16}},
		{"error second ID", [][]byte{filled(0x01, 16), filled(0x11, 32)}, 3, []int{16, 32, 16}},
		{"short second secret", [][]byte{filled(0x01, 16), filled(0x11, 32), filled(0x02, 16), filled(0x12, 31)}, 0, []int{16, 32, 16, 32}},
		{"error second secret", [][]byte{filled(0x01, 16), filled(0x11, 32), filled(0x02, 16)}, 4, []int{16, 32, 16, 32}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
			random := newScriptedReader(tt.chunks...)
			random.failAt = tt.failAt
			random.failure = boom
			store := newTestStore(t, DefaultLimits(), fakeClock, random)
			roomSpec := validRoomSpec(testWall, 2)

			if _, created, err := store.CreateRoom("room", roomSpec); !errors.Is(err, ErrFatalRandom) || created {
				t.Fatalf("CreateRoom() = (_, %t, %v), want ErrFatalRandom", created, err)
			}
			if !reflect.DeepEqual(random.calls, tt.wantCalls) {
				t.Fatalf("random calls = %v, want %v", random.calls, tt.wantCalls)
			}
			assertStoreCounts(t, store, 0, 0, 0, 0)

			random.reset(
				filled(0x01, 16), filled(0x11, 32),
				filled(0x02, 16), filled(0x12, 32),
			)
			allocation, created, err := store.CreateRoom("room", roomSpec)
			if err != nil || !created {
				t.Fatalf("retry after random failure = (_, %t, %v), want created", created, err)
			}
			if allocation.Grants[0].GrantID != bytes16(0x01) || allocation.Grants[1].GrantID != bytes16(0x02) {
				t.Fatalf("retry IDs = %x, %x", allocation.Grants[0].GrantID, allocation.Grants[1].GrantID)
			}
			assertReads(t, random.calls, 16, 32, 16, 32)
			assertStoreCounts(t, store, 1, 2, 1, 2)
		})
	}
}

func TestGetRoomReturnsSecretFreeImmutableMonotonicSnapshots(t *testing.T) {
	baseMono := 100 * time.Second
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: baseMono}}
	random := newScriptedReader(
		filled(0x11, 16), filled(0x21, 32),
		filled(0x12, 16), filled(0x22, 32),
	)
	store := newTestStore(t, DefaultLimits(), fakeClock, random)
	roomSpec := RoomSpec{
		Capacity:  2,
		ExpiresAt: testWall.Add(2 * time.Hour),
		Participants: []ParticipantSpec{
			{ParticipantID: "bob", SessionID: "session-b", GrantExpiresAt: testWall.Add(90 * time.Minute)},
			{ParticipantID: "alice", SessionID: "session-a", GrantExpiresAt: testWall.Add(time.Hour)},
		},
	}
	allocation, created, err := store.CreateRoom("room", roomSpec)
	if err != nil || !created {
		t.Fatalf("CreateRoom() = (_, %t, %v), want created", created, err)
	}
	initialReads := len(random.calls)

	assertSnapshotTypeHasNoSecrets(t)
	snapshot, err := store.GetRoom("room")
	if err != nil {
		t.Fatalf("GetRoom(): %v", err)
	}
	if snapshot.RoomID != "room" || snapshot.CreatedAt != testWall || snapshot.ExpiresAt != roomSpec.ExpiresAt.UTC() ||
		snapshot.Capacity != 2 || len(snapshot.Participants) != 2 {
		t.Fatalf("snapshot header = %#v", snapshot)
	}
	assertSnapshotParticipant(t, snapshot.Participants[0], "alice", "session-a", testWall.Add(time.Hour), GrantStateIssued, BindingStateUnbound)
	assertSnapshotParticipant(t, snapshot.Participants[1], "bob", "session-b", testWall.Add(90*time.Minute), GrantStateIssued, BindingStateUnbound)

	snapshot.RoomID = "mutated"
	snapshot.Capacity = 99
	snapshot.Participants[0].ParticipantID = "mutated"
	snapshot.Participants = append(snapshot.Participants, ParticipantSnapshot{})
	second, err := store.GetRoom("room")
	if err != nil {
		t.Fatalf("second GetRoom(): %v", err)
	}
	if second.RoomID != "room" || second.Capacity != 2 || len(second.Participants) != 2 || second.Participants[0].ParticipantID != "alice" {
		t.Fatalf("snapshot shares mutable storage: %#v", second)
	}

	fakeClock.reading = clock.Reading{Wall: testWall.Add(24 * time.Hour), Mono: baseMono + 30*time.Minute}
	if _, err := store.GetRoom("room"); err != nil {
		t.Fatalf("forward wall jump ended monotonic room: %v", err)
	}

	fakeClock.reading = clock.Reading{Wall: testWall.Add(-24 * time.Hour), Mono: baseMono + time.Hour}
	partial, err := store.GetRoom("room")
	if err != nil {
		t.Fatalf("partial-expiry GetRoom(): %v", err)
	}
	assertSnapshotParticipant(t, partial.Participants[0], "alice", "session-a", testWall.Add(time.Hour), GrantStateExpired, BindingStateExpired)
	assertSnapshotParticipant(t, partial.Participants[1], "bob", "session-b", testWall.Add(90*time.Minute), GrantStateIssued, BindingStateUnbound)
	retry, created, err := store.CreateRoom("room", roomSpec)
	if err != nil || created {
		t.Fatalf("partial-expiry retry = (_, %t, %v), want existing", created, err)
	}
	if retry.Grants[0].GrantID != allocation.Grants[0].GrantID || retry.Grants[0].GrantSecret != nil || retry.Grants[0].State != GrantStateExpired {
		t.Fatalf("expired grant changed on retry: %#v", retry.Grants[0])
	}
	if retry.Grants[1].GrantID != allocation.Grants[1].GrantID || retry.Grants[1].GrantSecret == nil || retry.Grants[1].State != GrantStateIssued {
		t.Fatalf("live grant changed on retry: %#v", retry.Grants[1])
	}
	if len(random.calls) != initialReads {
		t.Fatalf("Get/retry used randomness: calls=%v", random.calls)
	}

	fakeClock.reading = clock.Reading{Wall: testWall.Add(48 * time.Hour), Mono: baseMono + 90*time.Minute - time.Nanosecond}
	if _, err := store.GetRoom("room"); err != nil {
		t.Fatalf("GetRoom(final deadline - 1ns): %v", err)
	}
	fakeClock.reading = clock.Reading{Wall: testWall.Add(-48 * time.Hour), Mono: baseMono + 90*time.Minute}
	if _, err := store.GetRoom("room"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRoom(final deadline) error = %v, want ErrNotFound", err)
	}
	if _, _, err := store.CreateRoom("room", roomSpec); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateRoom(final deadline) error = %v, want ErrConflict", err)
	}
	if len(random.calls) != initialReads {
		t.Fatalf("terminal access used randomness: calls=%v", random.calls)
	}
	assertStoreCounts(t, store, 1, 2, 1, 2)
}

func TestGetRoomDeniesExactRoomDeadlineDespiteWallJumps(t *testing.T) {
	baseMono := 7 * time.Second
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: baseMono}}
	random := newScriptedReader(filled(0x31, 16), filled(0x41, 32))
	store := newTestStore(t, DefaultLimits(), fakeClock, random)
	roomSpec := RoomSpec{
		Capacity:  1,
		ExpiresAt: testWall.Add(time.Hour),
		Participants: []ParticipantSpec{{
			ParticipantID:  "participant",
			SessionID:      "session",
			GrantExpiresAt: testWall.Add(time.Hour),
		}},
	}
	if _, _, err := store.CreateRoom("room", roomSpec); err != nil {
		t.Fatalf("CreateRoom(): %v", err)
	}
	fakeClock.reading = clock.Reading{Wall: testWall.Add(24 * time.Hour), Mono: baseMono + time.Hour - time.Nanosecond}
	if _, err := store.GetRoom("room"); err != nil {
		t.Fatalf("GetRoom(room deadline - 1ns): %v", err)
	}
	fakeClock.reading = clock.Reading{Wall: testWall.Add(-24 * time.Hour), Mono: baseMono + time.Hour}
	if _, err := store.GetRoom("room"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRoom(room deadline) error = %v, want ErrNotFound", err)
	}
	if _, _, err := store.CreateRoom("room", roomSpec); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateRoom(room deadline) error = %v, want ErrConflict", err)
	}
	if _, err := store.GetRoom("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRoom(missing) error = %v, want ErrNotFound", err)
	}
	assertStoreCounts(t, store, 1, 1, 1, 1)
}

func TestEndRoomRevokesKnownRoomAndIsIdempotent(t *testing.T) {
	limits := DefaultLimits()
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 50 * time.Second}}
	random := newScriptedReader(
		filled(0x51, 16), filled(0x61, 32),
		filled(0x52, 16), filled(0x62, 32),
	)
	store := newTestStore(t, limits, fakeClock, random)
	roomSpec := validRoomSpec(testWall, 2)
	if _, _, err := store.CreateRoom("room", roomSpec); err != nil {
		t.Fatalf("CreateRoom(): %v", err)
	}
	store.mu.RLock()
	oldGrants := append([]*grantRecord(nil), store.roomsByID["room"].grants...)
	store.mu.RUnlock()

	if err := store.EndRoom("room"); err != nil {
		t.Fatalf("EndRoom(): %v", err)
	}
	wantDeadline := 50*time.Second + limits.TombstoneTTL
	assertTombstoneOnly(t, store, "room", wantDeadline)
	assertStoreCounts(t, store, 1, 0, 0, 0)
	for _, grant := range oldGrants {
		if grant.state != GrantStateRevoked || grant.secret != nil {
			t.Fatalf("revoked grant retained state/secret: %#v", grant)
		}
	}
	if _, err := store.GetRoom("room"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRoom(ended) error = %v, want ErrNotFound", err)
	}
	reads := len(random.calls)
	if _, _, err := store.CreateRoom("room", roomSpec); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateRoom(tombstone) error = %v, want ErrConflict", err)
	}
	if len(random.calls) != reads {
		t.Fatalf("tombstone conflict used randomness: calls=%v", random.calls)
	}

	fakeClock.reading.Mono++
	if err := store.EndRoom("room"); err != nil {
		t.Fatalf("repeated EndRoom(): %v", err)
	}
	store.Expire()
	assertTombstoneOnly(t, store, "room", wantDeadline)
	if err := store.EndRoom("never-existed"); err != nil {
		t.Fatalf("EndRoom(missing): %v", err)
	}
	assertStoreCounts(t, store, 1, 0, 0, 0)

	fakeClock.reading.Mono = wantDeadline
	if err := store.EndRoom("room"); err != nil {
		t.Fatalf("EndRoom(stale tombstone): %v", err)
	}
	assertStoreCounts(t, store, 0, 0, 0, 0)
}

func TestExpireReleasesPartialAndFinalGrantAccountingExactlyOnce(t *testing.T) {
	limits := DefaultLimits()
	baseMono := 10 * time.Second
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: baseMono}}
	random := newScriptedReader(
		filled(0x71, 16), filled(0x81, 32),
		filled(0x72, 16), filled(0x82, 32),
	)
	store := newTestStore(t, limits, fakeClock, random)
	roomSpec := RoomSpec{
		Capacity:  2,
		ExpiresAt: testWall.Add(2 * time.Hour),
		Participants: []ParticipantSpec{
			{ParticipantID: "alice", SessionID: "session-a", GrantExpiresAt: testWall.Add(time.Hour)},
			{ParticipantID: "bob", SessionID: "session-b", GrantExpiresAt: testWall.Add(90 * time.Minute)},
		},
	}
	allocation, _, err := store.CreateRoom("room", roomSpec)
	if err != nil {
		t.Fatalf("CreateRoom(): %v", err)
	}
	store.mu.RLock()
	alice := store.roomsByID["room"].grants[0]
	bob := store.roomsByID["room"].grants[1]
	store.mu.RUnlock()

	fakeClock.reading.Mono = baseMono + time.Hour - time.Nanosecond
	store.Expire()
	assertStoreCounts(t, store, 1, 2, 1, 2)

	fakeClock.reading.Mono = baseMono + time.Hour
	store.Expire()
	if alice.state != GrantStateExpired || alice.secret != nil || bob.state != GrantStateIssued || bob.secret == nil {
		t.Fatalf("partial expiry state = alice:%#v bob:%#v", alice, bob)
	}
	assertStoreCounts(t, store, 1, 1, 1, 1)
	partial, err := store.GetRoom("room")
	if err != nil {
		t.Fatalf("GetRoom(partial expiry): %v", err)
	}
	assertSnapshotParticipant(t, partial.Participants[0], "alice", "session-a", testWall.Add(time.Hour), GrantStateExpired, BindingStateExpired)
	assertSnapshotParticipant(t, partial.Participants[1], "bob", "session-b", testWall.Add(90*time.Minute), GrantStateIssued, BindingStateUnbound)
	retry, created, err := store.CreateRoom("room", roomSpec)
	if err != nil || created || retry.Grants[0].GrantID != allocation.Grants[0].GrantID ||
		retry.Grants[0].GrantSecret != nil || retry.Grants[0].State != GrantStateExpired ||
		retry.Grants[1].GrantID != allocation.Grants[1].GrantID || retry.Grants[1].GrantSecret == nil {
		t.Fatalf("retry after physical partial expiry = (_, %t, %v, %#v)", created, err, retry)
	}
	store.Expire()
	assertStoreCounts(t, store, 1, 1, 1, 1)

	finalDeadline := baseMono + 90*time.Minute
	fakeClock.reading.Mono = finalDeadline
	if _, err := store.GetRoom("room"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRoom(final grant deadline) error = %v, want ErrNotFound", err)
	}
	if _, _, err := store.CreateRoom("room", roomSpec); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateRoom(final grant deadline) error = %v, want ErrConflict", err)
	}
	assertStoreCounts(t, store, 1, 1, 1, 1)

	fakeClock.reading.Mono = finalDeadline + 2*time.Second
	store.Expire()
	if bob.state != GrantStateExpired || bob.secret != nil {
		t.Fatalf("final grant was not cleared: %#v", bob)
	}
	assertRoomState(t, store, "room", roomStateEmpty)
	assertStoreCounts(t, store, 1, 0, 0, 0)
	store.Expire()
	assertStoreCounts(t, store, 1, 0, 0, 0)

	emptyDeadline := finalDeadline + limits.EmptyGrace
	fakeClock.reading.Mono = emptyDeadline - time.Nanosecond
	store.Expire()
	assertRoomState(t, store, "room", roomStateEmpty)
	fakeClock.reading.Mono = emptyDeadline
	store.Expire()
	assertTombstoneOnly(t, store, "room", emptyDeadline+limits.TombstoneTTL)
	assertStoreCounts(t, store, 1, 0, 0, 0)
	assertStoreInvariants(t, store)
}

func TestExpireUsesEarlierRoomTTLOrAnchoredEmptyGrace(t *testing.T) {
	tests := []struct {
		name             string
		roomTTL          time.Duration
		grantTTL         time.Duration
		emptyGrace       time.Duration
		physicalDeadline time.Duration
	}{
		{"room TTL earlier", 10 * time.Second, 8 * time.Second, 5 * time.Second, 10 * time.Second},
		{"room TTL equals empty deadline", 10 * time.Second, 5 * time.Second, 5 * time.Second, 10 * time.Second},
		{"empty grace earlier", 20 * time.Second, 5 * time.Second, 5 * time.Second, 10 * time.Second},
		{"lower configured empty grace", 20 * time.Second, 5 * time.Second, 2 * time.Second, 7 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := DefaultLimits()
			limits.EmptyGrace = tt.emptyGrace
			fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
			random := newScriptedReader(filled(0x91, 16), filled(0xa1, 32))
			store := newTestStore(t, limits, fakeClock, random)
			roomSpec := RoomSpec{
				Capacity:  1,
				ExpiresAt: testWall.Add(tt.roomTTL),
				Participants: []ParticipantSpec{{
					ParticipantID:  "participant",
					SessionID:      "session",
					GrantExpiresAt: testWall.Add(tt.grantTTL),
				}},
			}
			if _, _, err := store.CreateRoom("room", roomSpec); err != nil {
				t.Fatalf("CreateRoom(): %v", err)
			}

			fakeClock.reading.Mono = tt.grantTTL
			store.Expire()
			if tt.grantTTL < tt.roomTTL {
				assertRoomState(t, store, "room", roomStateEmpty)
			}
			fakeClock.reading.Mono = tt.physicalDeadline - time.Nanosecond
			store.Expire()
			assertRoomState(t, store, "room", roomStateEmpty)
			fakeClock.reading.Mono = tt.physicalDeadline
			store.Expire()
			assertTombstoneOnly(t, store, "room", tt.physicalDeadline+limits.TombstoneTTL)
			assertStoreCounts(t, store, 1, 0, 0, 0)
		})
	}

	t.Run("room and grant exact deadline", func(t *testing.T) {
		fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
		store := newTestStore(t, DefaultLimits(), fakeClock, newScriptedReader(filled(0xb1, 16), filled(0xc1, 32)))
		roomSpec := RoomSpec{
			Capacity:  1,
			ExpiresAt: testWall.Add(10 * time.Second),
			Participants: []ParticipantSpec{{
				ParticipantID:  "participant",
				SessionID:      "session",
				GrantExpiresAt: testWall.Add(10 * time.Second),
			}},
		}
		if _, _, err := store.CreateRoom("room", roomSpec); err != nil {
			t.Fatalf("CreateRoom(): %v", err)
		}
		fakeClock.reading.Mono = 10*time.Second - time.Nanosecond
		store.Expire()
		assertRoomState(t, store, "room", roomStateOpen)
		fakeClock.reading.Mono = 10 * time.Second
		store.Expire()
		assertTombstoneOnly(t, store, "room", 10*time.Second+HardMaxTombstoneTTL)
	})
}

func TestTombstoneDeadlineAllowsExactSameIDRecreationWithoutRefresh(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOpenRooms = 1
	limits.MaxRoomRecords = 1
	limits.MaxRoomCapacity = 1
	limits.MaxActiveSessions = 1
	limits.TombstoneTTL = 3 * time.Second
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
	random := &sequenceReader{}
	store := newTestStore(t, limits, fakeClock, random)
	roomSpec := validRoomSpec(testWall, 1)
	first, _, err := store.CreateRoom("room", roomSpec)
	if err != nil {
		t.Fatalf("CreateRoom(): %v", err)
	}
	if err := store.EndRoom("room"); err != nil {
		t.Fatalf("EndRoom(): %v", err)
	}
	deadline := limits.TombstoneTTL
	assertTombstoneOnly(t, store, "room", deadline)
	reads := random.reads

	fakeClock.reading.Mono = deadline - time.Nanosecond
	store.Expire()
	if err := store.EndRoom("room"); err != nil {
		t.Fatalf("repeated EndRoom(): %v", err)
	}
	assertTombstoneOnly(t, store, "room", deadline)
	if _, _, err := store.CreateRoom("room", roomSpec); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateRoom(deadline - 1ns) error = %v, want ErrConflict", err)
	}
	if random.reads != reads {
		t.Fatalf("live tombstone used randomness: reads=%d want=%d", random.reads, reads)
	}

	fakeClock.reading.Mono = deadline
	second, created, err := store.CreateRoom("room", roomSpec)
	if err != nil || !created {
		t.Fatalf("CreateRoom(exact tombstone deadline) = (_, %t, %v), want created", created, err)
	}
	if second.Grants[0].GrantID == first.Grants[0].GrantID {
		t.Fatal("same-ID recreation reused the old grant ID")
	}
	assertStoreCounts(t, store, 1, 1, 1, 1)

	if err := store.EndRoom("room"); err != nil {
		t.Fatalf("EndRoom(recreated): %v", err)
	}
	secondDeadline := deadline + limits.TombstoneTTL
	fakeClock.reading.Mono = secondDeadline - time.Nanosecond
	store.Expire()
	assertTombstoneOnly(t, store, "room", secondDeadline)
	fakeClock.reading.Mono = secondDeadline
	store.Expire()
	assertStoreCounts(t, store, 0, 0, 0, 0)
}

func TestResidentRecordCapIncludesTerminalEmptyAndTombstoneStates(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOpenRooms = 2
	limits.MaxRoomRecords = 2
	limits.MaxRoomCapacity = 1
	limits.MaxActiveSessions = 2
	limits.EmptyGrace = 5 * time.Second
	limits.TombstoneTTL = 10 * time.Second
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
	random := &sequenceReader{}
	store := newTestStore(t, limits, fakeClock, random)
	roomSpec := RoomSpec{
		Capacity:  1,
		ExpiresAt: testWall.Add(time.Minute),
		Participants: []ParticipantSpec{{
			ParticipantID:  "participant",
			SessionID:      "session",
			GrantExpiresAt: testWall.Add(10 * time.Second),
		}},
	}
	for _, roomID := range []string{"room-a", "room-b"} {
		if _, _, err := store.CreateRoom(roomID, roomSpec); err != nil {
			t.Fatalf("CreateRoom(%s): %v", roomID, err)
		}
	}
	assertStoreCounts(t, store, 2, 2, 2, 2)

	fakeClock.reading.Mono = 10 * time.Second
	if _, _, err := store.CreateRoom("room-c", roomSpec); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new room with terminal pre-sweep records error = %v, want ErrCapacity", err)
	}
	assertStoreCounts(t, store, 2, 2, 2, 2)
	store.Expire()
	assertRoomState(t, store, "room-a", roomStateEmpty)
	assertRoomState(t, store, "room-b", roomStateEmpty)
	assertStoreCounts(t, store, 2, 0, 0, 0)
	if _, _, err := store.CreateRoom("room-c", roomSpec); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new room with empty-grace residents error = %v, want ErrCapacity", err)
	}

	if err := store.EndRoom("room-b"); err != nil {
		t.Fatalf("EndRoom(room-b): %v", err)
	}
	assertTombstoneOnly(t, store, "room-b", 20*time.Second)
	if _, _, err := store.CreateRoom("room-c", roomSpec); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new room with tombstone resident error = %v, want ErrCapacity", err)
	}

	fakeClock.reading.Mono = 20 * time.Second
	store.Expire()
	assertTombstoneOnly(t, store, "room-a", 30*time.Second)
	if _, created, err := store.CreateRoom("room-c", roomSpec); err != nil || !created {
		t.Fatalf("CreateRoom after one resident removal = (_, %t, %v), want created", created, err)
	}
	assertStoreCounts(t, store, 2, 1, 1, 1)
	assertStoreInvariants(t, store)
}

func TestLifecycleChurnReturnsAllStateToBaseline(t *testing.T) {
	limits := DefaultLimits()
	fakeClock := &manualClock{}
	random := &sequenceReader{}
	store := newTestStore(t, limits, fakeClock, random)
	globalIngressPackets, globalIngressBytes := store.authenticatedGlobalPackets, store.authenticatedGlobalBytes
	globalFanoutWrites, globalFanoutBytes := store.globalFanoutWrites, store.globalFanoutBytes
	for index := range 1000 {
		base := time.Duration(index) * (limits.TombstoneTTL + 2*time.Nanosecond)
		fakeClock.reading = clock.Reading{Wall: testWall.Add(base), Mono: base}
		roomSpec := RoomSpec{
			Capacity:  1,
			ExpiresAt: fakeClock.reading.Wall.Add(time.Nanosecond),
			Participants: []ParticipantSpec{{
				ParticipantID:  "participant",
				SessionID:      "session",
				GrantExpiresAt: fakeClock.reading.Wall.Add(time.Nanosecond),
			}},
		}
		allocation, created, err := store.CreateRoom("room", roomSpec)
		if err != nil || !created || allocation.Grants[0].GrantSecret == nil {
			t.Fatalf("cycle %d CreateRoom() = (_, %t, %v)", index, created, err)
		}
		endpoint := netip.MustParseAddrPort("192.0.2.1:4000")
		nonce := bytes16(byte(index))
		challenge, reason := store.AdmitHello(HelloRequest{
			RoomID: "room", SessionID: allocation.Grants[0].SessionID, GrantID: allocation.Grants[0].GrantID,
			ClientNonce: nonce, Endpoint: endpoint, InputBytes: 300,
		})
		if reason != RejectNone {
			t.Fatalf("cycle %d AdmitHello() = %q", index, reason)
		}
		secret := *allocation.Grants[0].GrantSecret
		authTag := protocol.AuthTag(secret, protocol.Revision, "room", allocation.Grants[0].SessionID,
			allocation.Grants[0].GrantID, challenge.CandidateID, nonce, challenge.ServerNonce)
		bound, reason := store.AdmitAuth(AuthRequest{
			RoomID: "room", SessionID: allocation.Grants[0].SessionID, CandidateID: challenge.CandidateID,
			Endpoint: endpoint, AuthTag: authTag, InputBytes: 100,
		})
		if reason != RejectNone {
			t.Fatalf("cycle %d AdmitAuth() = %q", index, reason)
		}
		grant := store.bindingsByID[bound.BindingID]
		room := store.roomsByID["room"]
		key := grant.binding.key
		request := ClientDataRequest{
			RoomID: "room", SessionID: allocation.Grants[0].SessionID, BindingID: bound.BindingID,
			Sequence: 1, Endpoint: endpoint,
		}
		request.AuthTag = protocol.ClientDataTag(key, protocol.Revision, request.RoomID, request.SessionID,
			request.BindingID, request.Sequence, request.Payload)
		admitted, reason := store.AdmitClientData(request, 1)
		if reason != RejectNone {
			t.Fatalf("cycle %d AdmitClientData() = %q", index, reason)
		}
		plan, reason := store.PlanFanout(admitted, 1)
		if reason != RejectNone || len(plan.Recipients) != 0 {
			t.Fatalf("cycle %d PlanFanout() = (%#v, %q)", index, plan, reason)
		}
		fakeClock.reading.Mono += time.Nanosecond
		store.Expire()
		if grant.ingressPackets != nil || grant.ingressBytes != nil || room.ingressPackets != nil ||
			room.ingressBytes != nil || room.fanoutWrites != nil || room.fanoutBytes != nil {
			t.Fatalf("cycle %d terminal cleanup retained limiter state", index)
		}
		fakeClock.reading.Mono += limits.TombstoneTTL
		store.Expire()
		assertStoreCounts(t, store, 0, 0, 0, 0)
		if len(store.preauthSources) != 0 {
			t.Fatalf("cycle %d retained %d preauth sources", index, len(store.preauthSources))
		}
	}
	if store.authenticatedGlobalPackets != globalIngressPackets || store.authenticatedGlobalBytes != globalIngressBytes ||
		store.globalFanoutWrites != globalFanoutWrites || store.globalFanoutBytes != globalFanoutBytes {
		t.Fatal("lifecycle churn replaced process-global limiter state")
	}
	if len(store.preauthSources) != 0 {
		t.Fatalf("lifecycle churn retained %d preauth sources", len(store.preauthSources))
	}
	assertStoreInvariants(t, store)
}

func TestConcurrentLifecycleMaintainsLinearizedState(t *testing.T) {
	t.Run("identical allocation", func(t *testing.T) {
		fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
		store := newTestStore(t, DefaultLimits(), fakeClock, &sequenceReader{})
		roomSpec := validRoomSpec(testWall, 1)
		type result struct {
			allocation RoomAllocation
			created    bool
			err        error
		}
		results := make(chan result, 32)
		var wait sync.WaitGroup
		for range 32 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				allocation, created, err := store.CreateRoom("room", roomSpec)
				results <- result{allocation: allocation, created: created, err: err}
			}()
		}
		wait.Wait()
		close(results)
		createdCount := 0
		var grantID protocol.Bytes16
		var secret protocol.Bytes32
		for result := range results {
			if result.err != nil || len(result.allocation.Grants) != 1 || result.allocation.Grants[0].GrantSecret == nil {
				t.Fatalf("concurrent CreateRoom() = %#v", result)
			}
			if result.created {
				createdCount++
				grantID = result.allocation.Grants[0].GrantID
				secret = *result.allocation.Grants[0].GrantSecret
			}
		}
		if createdCount != 1 {
			t.Fatalf("created count = %d, want 1", createdCount)
		}
		retry, _, err := store.CreateRoom("room", roomSpec)
		if err != nil || retry.Grants[0].GrantID != grantID || *retry.Grants[0].GrantSecret != secret {
			t.Fatalf("stable retry = %#v, %v", retry, err)
		}
		assertStoreInvariants(t, store)
	})

	t.Run("create get end expire race", func(t *testing.T) {
		fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
		store := newTestStore(t, DefaultLimits(), fakeClock, &sequenceReader{})
		roomSpec := validRoomSpec(testWall, 1)
		if _, _, err := store.CreateRoom("room", roomSpec); err != nil {
			t.Fatalf("CreateRoom(): %v", err)
		}
		different := cloneRoomSpec(roomSpec)
		different.ExpiresAt = different.ExpiresAt.Add(time.Second)
		errorsSeen := make(chan error, 200)
		var wait sync.WaitGroup
		for index := range 200 {
			wait.Add(1)
			go func(operation int) {
				defer wait.Done()
				switch operation % 5 {
				case 0:
					_, _, err := store.CreateRoom("room", roomSpec)
					if err != nil && !errors.Is(err, ErrConflict) {
						errorsSeen <- err
					}
				case 1:
					_, _, err := store.CreateRoom("room", different)
					if !errors.Is(err, ErrConflict) {
						errorsSeen <- errors.New("different definition did not conflict")
					}
				case 2:
					_, err := store.GetRoom("room")
					if err != nil && !errors.Is(err, ErrNotFound) {
						errorsSeen <- err
					}
				case 3:
					if err := store.EndRoom("room"); err != nil {
						errorsSeen <- err
					}
				case 4:
					store.Expire()
				}
			}(index)
		}
		wait.Wait()
		close(errorsSeen)
		for err := range errorsSeen {
			t.Errorf("concurrent lifecycle: %v", err)
		}
		if err := store.EndRoom("room"); err != nil {
			t.Fatalf("final EndRoom(): %v", err)
		}
		if _, err := store.GetRoom("room"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("final GetRoom() error = %v, want ErrNotFound", err)
		}
		assertTombstoneOnly(t, store, "room", DefaultLimits().TombstoneTTL)
		assertStoreInvariants(t, store)
	})

	t.Run("resident cap race", func(t *testing.T) {
		limits := DefaultLimits()
		limits.MaxOpenRooms = 4
		limits.MaxRoomRecords = 4
		limits.MaxRoomCapacity = 1
		limits.MaxActiveSessions = 4
		fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
		store := newTestStore(t, limits, fakeClock, &sequenceReader{})
		roomSpec := validRoomSpec(testWall, 1)
		results := make(chan error, 32)
		var wait sync.WaitGroup
		for index := range 32 {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				_, _, err := store.CreateRoom("room-"+strconv.Itoa(index), roomSpec)
				results <- err
			}(index)
		}
		wait.Wait()
		close(results)
		created := 0
		for err := range results {
			switch {
			case err == nil:
				created++
			case errors.Is(err, ErrCapacity):
			default:
				t.Fatalf("CreateRoom() error = %v", err)
			}
		}
		if created != limits.MaxRoomRecords {
			t.Fatalf("created rooms = %d, want %d", created, limits.MaxRoomRecords)
		}
		assertStoreInvariants(t, store)
	})
}

type sequenceReader struct {
	nextID uint64
	reads  int
}

func (reader *sequenceReader) Read(buffer []byte) (int, error) {
	reader.reads++
	for index := range buffer {
		buffer[index] = 0
	}
	switch len(buffer) {
	case len(protocol.Bytes16{}):
		reader.nextID++
		value := reader.nextID
		for index := range 8 {
			buffer[len(buffer)-1-index] = byte(value)
			value >>= 8
		}
	case len(protocol.Bytes32{}):
		for index := range buffer {
			buffer[index] = byte(reader.nextID)
		}
	default:
		return 0, errors.New("unexpected random read size")
	}
	return len(buffer), nil
}

type signalClock struct {
	reading clock.Reading
	called  chan struct{}
}

func (fakeClock *signalClock) now() clock.Reading {
	select {
	case fakeClock.called <- struct{}{}:
	default:
	}
	return fakeClock.reading
}

func assertSnapshotParticipant(
	t *testing.T,
	participant ParticipantSnapshot,
	participantID, sessionID string,
	expiresAt time.Time,
	grantState GrantState,
	bindingState BindingState,
) {
	t.Helper()
	if participant.ParticipantID != participantID || participant.SessionID != sessionID ||
		participant.GrantExpiresAt != expiresAt.UTC() || participant.GrantState != grantState || participant.BindingState != bindingState {
		t.Fatalf("participant snapshot = %#v, want %q/%q/%v/%q/%q",
			participant, participantID, sessionID, expiresAt.UTC(), grantState, bindingState)
	}
}

func assertSnapshotTypeHasNoSecrets(t *testing.T) {
	t.Helper()
	forbidden := []string{"secret", "key", "challenge", "grantid", "bindingid", "endpoint"}
	for _, value := range []any{RoomSnapshot{}, ParticipantSnapshot{}} {
		typeOf := reflect.TypeOf(value)
		for index := range typeOf.NumField() {
			name := strings.ToLower(typeOf.Field(index).Name)
			for _, fragment := range forbidden {
				if strings.Contains(name, fragment) {
					t.Fatalf("%s contains forbidden field %s", typeOf.Name(), typeOf.Field(index).Name)
				}
			}
		}
	}
}

func assertRoomState(t *testing.T, store *Store, roomID string, want roomRecordState) {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	record := store.roomsByID[roomID]
	if record == nil || record.state != want {
		t.Fatalf("room %q state = %#v, want %v", roomID, record, want)
	}
}

func assertTombstoneOnly(t *testing.T, store *Store, roomID string, deadline time.Duration) {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	record := store.roomsByID[roomID]
	if record == nil {
		t.Fatalf("room %q is absent, want tombstone", roomID)
	}
	if record.state != roomStateTombstone || record.tombstoneDeadline != deadline || record.capacity != 0 ||
		!record.createdAt.IsZero() || !record.expiresAt.IsZero() || record.monoDeadline != 0 || record.grants != nil ||
		record.ingressPackets != nil || record.ingressBytes != nil || record.fanoutWrites != nil || record.fanoutBytes != nil {
		t.Fatalf("room %q retained non-tombstone state: %#v, want deadline %v", roomID, record, deadline)
	}
}

func assertStoreInvariants(t *testing.T, store *Store) {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.roomsByID) > store.limits.MaxRoomRecords || store.openRooms < 0 || store.activeSessions < 0 {
		t.Fatalf("invalid bounded counters: rooms=%d open=%d active=%d", len(store.roomsByID), store.openRooms, store.activeSessions)
	}
	openRooms := 0
	activeSessions := 0
	indexed := make(map[protocol.Bytes16]*grantRecord)
	indexedCandidates := make(map[protocol.Bytes16]*grantRecord)
	indexedBindings := make(map[protocol.Bytes16]*grantRecord)
	for roomID, room := range store.roomsByID {
		switch room.state {
		case roomStateOpen:
			openRooms++
		case roomStateEmpty:
		case roomStateTombstone:
			if room.tombstoneDeadline == 0 || room.capacity != 0 || !room.createdAt.IsZero() ||
				!room.expiresAt.IsZero() || room.monoDeadline != 0 || room.grants != nil ||
				room.ingressPackets != nil || room.ingressBytes != nil || room.fanoutWrites != nil || room.fanoutBytes != nil {
				t.Fatalf("tombstone %q retained room/grant data: %#v", roomID, room)
			}
			continue
		default:
			t.Fatalf("room %q has invalid state %v", roomID, room.state)
		}
		if room.ingressPackets == nil || room.ingressBytes == nil || room.fanoutWrites == nil || room.fanoutBytes == nil {
			t.Fatalf("live room %q has incomplete limiter state", roomID)
		}
		for _, grant := range room.grants {
			if grant == nil {
				t.Fatalf("room %q contains nil grant", roomID)
			}
			switch grant.state {
			case GrantStateIssued, GrantStateBound:
				activeSessions++
				if grant.secret == nil || grant.ingressPackets == nil || grant.ingressBytes == nil {
					t.Fatalf("live grant %x has incomplete secret/limiter state", grant.id)
				}
				indexed[grant.id] = grant
			case GrantStateExpired, GrantStateRevoked:
				if grant.secret != nil || grant.ingressPackets != nil || grant.ingressBytes != nil {
					t.Fatalf("terminal grant %x retained secret/limiter state", grant.id)
				}
				if _, exists := store.grantsByID[grant.id]; exists {
					t.Fatalf("terminal grant %x retained reverse index", grant.id)
				}
			default:
				t.Fatalf("grant %x has invalid state %q", grant.id, grant.state)
			}
			if grant.pendingChallenge != nil {
				if grant.pendingChallenge.candidateID == (protocol.Bytes16{}) || grant.pendingChallenge.serverNonce == (protocol.Bytes32{}) ||
					!grant.pendingChallenge.endpoint.IsValid() || grant.pendingChallenge.deadline == 0 || store.candidatesByID[grant.pendingChallenge.candidateID] != grant {
					t.Fatalf("grant %x has inconsistent pending challenge", grant.id)
				}
				indexedCandidates[grant.pendingChallenge.candidateID] = grant
			}
			if grant.lastHandshake != nil {
				if grant.lastHandshake.candidateID == (protocol.Bytes16{}) || grant.lastHandshake.serverNonce == (protocol.Bytes32{}) ||
					!grant.lastHandshake.endpoint.IsValid() || grant.lastHandshake.deadline == 0 || store.candidatesByID[grant.lastHandshake.candidateID] != grant {
					t.Fatalf("grant %x has inconsistent recent completion", grant.id)
				}
				indexedCandidates[grant.lastHandshake.candidateID] = grant
			}
			if grant.binding != nil {
				if grant.binding.id == (protocol.Bytes16{}) || grant.binding.key == (protocol.Bytes32{}) ||
					!grant.binding.endpoint.IsValid() || grant.binding.deadline == 0 || grant.binding.generation == 0 ||
					store.bindingsByID[grant.binding.id] != grant || grant.state != GrantStateBound ||
					(grant.bindingState != BindingStateBound && grant.bindingState != BindingStateRebindPending) {
					t.Fatalf("grant %x has inconsistent current binding", grant.id)
				}
				indexedBindings[grant.binding.id] = grant
			} else if grant.state == GrantStateBound {
				t.Fatalf("bound grant %x has no current binding", grant.id)
			}
			if !grantLive(grant) && (grant.pendingChallenge != nil || grant.lastHandshake != nil || grant.binding != nil) {
				t.Fatalf("terminal grant %x retained relay state", grant.id)
			}
		}
	}
	if openRooms != store.openRooms || activeSessions != store.activeSessions || len(indexed) != len(store.grantsByID) ||
		len(indexedCandidates) != len(store.candidatesByID) || len(indexedBindings) != len(store.bindingsByID) {
		t.Fatalf("recomputed counts = open:%d active:%d grants:%d candidates:%d bindings:%d; stored = %d/%d/%d/%d/%d",
			openRooms, activeSessions, len(indexed), len(indexedCandidates), len(indexedBindings),
			store.openRooms, store.activeSessions, len(store.grantsByID), len(store.candidatesByID), len(store.bindingsByID))
	}
	for grantID, grant := range store.grantsByID {
		if indexed[grantID] != grant {
			t.Fatalf("reverse index %x points outside authoritative room grants", grantID)
		}
	}
	for candidateID, grant := range store.candidatesByID {
		if indexedCandidates[candidateID] != grant {
			t.Fatalf("candidate index %x points outside authoritative grants", candidateID)
		}
	}
	for bindingID, grant := range store.bindingsByID {
		if indexedBindings[bindingID] != grant {
			t.Fatalf("binding index %x points outside authoritative grants", bindingID)
		}
	}
	if len(store.preauthSources) > HardMaxPreauthSources || store.preauthGlobalPackets == nil || store.preauthGlobalBytes == nil ||
		store.authenticatedGlobalPackets == nil || store.authenticatedGlobalBytes == nil ||
		store.globalFanoutWrites == nil || store.globalFanoutBytes == nil {
		t.Fatalf("invalid pre-auth state: sources=%d packet=%p bytes=%p", len(store.preauthSources), store.preauthGlobalPackets, store.preauthGlobalBytes)
	}
	for key, source := range store.preauthSources {
		if (key.Addr().Is4() && key.Bits() != 32) || (key.Addr().Is6() && key.Bits() != 64) ||
			source == nil || source.packets == nil || source.bytes == nil {
			t.Fatalf("invalid pre-auth source %v: %#v", key, source)
		}
	}
}

type manualClock struct {
	mu      sync.Mutex
	reading clock.Reading
	calls   int
}

func (fakeClock *manualClock) now() clock.Reading {
	fakeClock.mu.Lock()
	defer fakeClock.mu.Unlock()
	fakeClock.calls++
	return fakeClock.reading
}

type scriptedReader struct {
	chunks  [][]byte
	calls   []int
	failAt  int
	failure error
}

func newScriptedReader(chunks ...[]byte) *scriptedReader {
	reader := &scriptedReader{}
	reader.reset(chunks...)
	return reader
}

func (reader *scriptedReader) reset(chunks ...[]byte) {
	reader.chunks = append(reader.chunks[:0], chunks...)
	reader.calls = nil
	reader.failAt = 0
	reader.failure = nil
}

func (reader *scriptedReader) Read(buffer []byte) (int, error) {
	reader.calls = append(reader.calls, len(buffer))
	if reader.failAt == len(reader.calls) {
		return 0, reader.failure
	}
	if len(reader.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := reader.chunks[0]
	reader.chunks = reader.chunks[1:]
	read := copy(buffer, chunk)
	if read != len(buffer) {
		return read, io.EOF
	}
	return read, nil
}

func newTestStore(t *testing.T, limits Limits, fakeClock *manualClock, random io.Reader) *Store {
	t.Helper()
	store, err := New(Config{Limits: limits, Now: fakeClock.now, Random: random})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return store
}

func validRoomSpec(now time.Time, participants int) RoomSpec {
	roomSpec := RoomSpec{
		Capacity:     uint32(participants),
		ExpiresAt:    now.Add(time.Hour),
		Participants: make([]ParticipantSpec, participants),
	}
	for index := range roomSpec.Participants {
		roomSpec.Participants[index] = ParticipantSpec{
			ParticipantID:  "participant-" + string(rune('a'+index)),
			SessionID:      "session-" + string(rune('a'+index)),
			GrantExpiresAt: now.Add(30 * time.Minute),
		}
	}
	return roomSpec
}

func cloneRoomSpec(roomSpec RoomSpec) RoomSpec {
	clone := roomSpec
	clone.Participants = append([]ParticipantSpec(nil), roomSpec.Participants...)
	return clone
}

func duplicateParticipantID(roomSpec *RoomSpec) {
	*roomSpec = validRoomSpec(testWall, 2)
	roomSpec.Participants[1].ParticipantID = roomSpec.Participants[0].ParticipantID
}

func duplicateSessionID(roomSpec *RoomSpec) {
	*roomSpec = validRoomSpec(testWall, 2)
	roomSpec.Participants[1].SessionID = roomSpec.Participants[0].SessionID
}

func threeParticipants(roomSpec *RoomSpec) {
	*roomSpec = validRoomSpec(testWall, 3)
}

func seventeenParticipants(roomSpec *RoomSpec) {
	*roomSpec = validRoomSpec(testWall, HardMaxRoomCapacity+1)
}

func withLimit(limits Limits, mutate func(*Limits)) Limits {
	mutate(&limits)
	return limits
}

func filled(value byte, size int) []byte {
	result := make([]byte, size)
	for index := range result {
		result[index] = value
	}
	return result
}

func bytes16(value byte) (result protocol.Bytes16) {
	for index := range result {
		result[index] = value
	}
	return result
}

func bytes32(value byte) (result protocol.Bytes32) {
	for index := range result {
		result[index] = value
	}
	return result
}

func assertGrant(
	t *testing.T,
	grant ParticipantGrant,
	participantID, sessionID string,
	grantID protocol.Bytes16,
	grantSecret protocol.Bytes32,
	expiresAt time.Time,
	state GrantState,
) {
	t.Helper()
	if grant.ParticipantID != participantID || grant.SessionID != sessionID || grant.GrantID != grantID ||
		grant.GrantSecret == nil || *grant.GrantSecret != grantSecret || grant.GrantExpiresAt != expiresAt.UTC() || grant.State != state {
		t.Fatalf("grant = %#v, want participant=%q session=%q id=%x secret=%x expiry=%v state=%q",
			grant, participantID, sessionID, grantID, grantSecret, expiresAt.UTC(), state)
	}
}

func assertReads(t *testing.T, got []int, want ...int) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("random read sizes = %v, want %v", got, want)
	}
}

func assertStoreCounts(t *testing.T, store *Store, rooms, grants, openRooms, activeSessions int) {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.roomsByID) != rooms || len(store.grantsByID) != grants ||
		store.openRooms != openRooms || store.activeSessions != activeSessions {
		t.Fatalf("store counts = rooms:%d grants:%d open:%d active:%d, want %d/%d/%d/%d",
			len(store.roomsByID), len(store.grantsByID), store.openRooms, store.activeSessions,
			rooms, grants, openRooms, activeSessions)
	}
}

func limitValueName(value int) string {
	switch {
	case value == 0:
		return "zero"
	case value < 0:
		return "negative"
	default:
		return "over"
	}
}
