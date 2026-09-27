package store

import (
	"errors"
	"math"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"golang.org/x/time/rate"
)

func TestNewRejectsInvalidPreauthLimits(t *testing.T) {
	rateFields := []struct {
		name string
		max  rate.Limit
		set  func(*Limits, rate.Limit)
	}{
		{"source packet rate", HardMaxPreauthSourcePacketRate, func(l *Limits, value rate.Limit) { l.PreauthSourcePacketRate = value }},
		{"source byte rate", HardMaxPreauthSourceByteRate, func(l *Limits, value rate.Limit) { l.PreauthSourceByteRate = value }},
		{"global packet rate", HardMaxPreauthGlobalPacketRate, func(l *Limits, value rate.Limit) { l.PreauthGlobalPacketRate = value }},
		{"global byte rate", HardMaxPreauthGlobalByteRate, func(l *Limits, value rate.Limit) { l.PreauthGlobalByteRate = value }},
		{"session packet rate", HardMaxSessionPacketRate, func(l *Limits, value rate.Limit) { l.SessionPacketRate = value }},
		{"session byte rate", HardMaxSessionByteRate, func(l *Limits, value rate.Limit) { l.SessionByteRate = value }},
		{"room packet rate", HardMaxRoomPacketRate, func(l *Limits, value rate.Limit) { l.RoomPacketRate = value }},
		{"room byte rate", HardMaxRoomByteRate, func(l *Limits, value rate.Limit) { l.RoomByteRate = value }},
		{"authenticated global packet rate", HardMaxAuthenticatedGlobalPacketRate, func(l *Limits, value rate.Limit) { l.AuthenticatedGlobalPacketRate = value }},
		{"authenticated global byte rate", HardMaxAuthenticatedGlobalByteRate, func(l *Limits, value rate.Limit) { l.AuthenticatedGlobalByteRate = value }},
		{"room fanout write rate", HardMaxRoomFanoutWriteRate, func(l *Limits, value rate.Limit) { l.RoomFanoutWriteRate = value }},
		{"room fanout byte rate", HardMaxRoomFanoutByteRate, func(l *Limits, value rate.Limit) { l.RoomFanoutByteRate = value }},
		{"global fanout write rate", HardMaxGlobalFanoutWriteRate, func(l *Limits, value rate.Limit) { l.GlobalFanoutWriteRate = value }},
		{"global fanout byte rate", HardMaxGlobalFanoutByteRate, func(l *Limits, value rate.Limit) { l.GlobalFanoutByteRate = value }},
	}
	for _, field := range rateFields {
		for _, value := range []rate.Limit{0, -1, rate.Limit(math.Inf(1)), rate.Limit(math.NaN()), field.max + 1} {
			limits := DefaultLimits()
			field.set(&limits, value)
			if _, err := New(Config{Limits: limits}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("New(%s=%v) error = %v, want ErrInvalid", field.name, value, err)
			}
		}
	}
	burstFields := []struct {
		name string
		max  int
		set  func(*Limits, int)
	}{
		{"source packet burst", HardMaxPreauthSourcePacketBurst, func(l *Limits, value int) { l.PreauthSourcePacketBurst = value }},
		{"source byte burst", HardMaxPreauthSourceByteBurst, func(l *Limits, value int) { l.PreauthSourceByteBurst = value }},
		{"global packet burst", HardMaxPreauthGlobalPacketBurst, func(l *Limits, value int) { l.PreauthGlobalPacketBurst = value }},
		{"global byte burst", HardMaxPreauthGlobalByteBurst, func(l *Limits, value int) { l.PreauthGlobalByteBurst = value }},
		{"session packet burst", HardMaxSessionPacketBurst, func(l *Limits, value int) { l.SessionPacketBurst = value }},
		{"session byte burst", HardMaxSessionByteBurst, func(l *Limits, value int) { l.SessionByteBurst = value }},
		{"room packet burst", HardMaxRoomPacketBurst, func(l *Limits, value int) { l.RoomPacketBurst = value }},
		{"room byte burst", HardMaxRoomByteBurst, func(l *Limits, value int) { l.RoomByteBurst = value }},
		{"authenticated global packet burst", HardMaxAuthenticatedGlobalPacketBurst, func(l *Limits, value int) { l.AuthenticatedGlobalPacketBurst = value }},
		{"authenticated global byte burst", HardMaxAuthenticatedGlobalByteBurst, func(l *Limits, value int) { l.AuthenticatedGlobalByteBurst = value }},
		{"room fanout write burst", HardMaxRoomFanoutWriteBurst, func(l *Limits, value int) { l.RoomFanoutWriteBurst = value }},
		{"room fanout byte burst", HardMaxRoomFanoutByteBurst, func(l *Limits, value int) { l.RoomFanoutByteBurst = value }},
		{"global fanout write burst", HardMaxGlobalFanoutWriteBurst, func(l *Limits, value int) { l.GlobalFanoutWriteBurst = value }},
		{"global fanout byte burst", HardMaxGlobalFanoutByteBurst, func(l *Limits, value int) { l.GlobalFanoutByteBurst = value }},
	}
	for _, field := range burstFields {
		for _, value := range []int{0, -1, field.max + 1} {
			limits := DefaultLimits()
			field.set(&limits, value)
			if _, err := New(Config{Limits: limits}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("New(%s=%d) error = %v, want ErrInvalid", field.name, value, err)
			}
		}
	}
}

func TestD04LimiterEqualityOneOverAndExactRefill(t *testing.T) {
	specs := []struct {
		name  string
		rate  rate.Limit
		burst int
		pick  func(*Store) *rate.Limiter
	}{
		{"preauth source packets", 16, 160, func(s *Store) *rate.Limiter { return testPreauthSource(s).packets }},
		{"preauth source bytes", 19_200, 192_000, func(s *Store) *rate.Limiter { return testPreauthSource(s).bytes }},
		{"preauth global packets", 128, 1_280, func(s *Store) *rate.Limiter { return s.preauthGlobalPackets }},
		{"preauth global bytes", 153_600, 1_536_000, func(s *Store) *rate.Limiter { return s.preauthGlobalBytes }},
		{"session packets", 40, 40, func(s *Store) *rate.Limiter { return s.roomsByID["room"].grants[0].ingressPackets }},
		{"session bytes", 20_480, 20_480, func(s *Store) *rate.Limiter { return s.roomsByID["room"].grants[0].ingressBytes }},
		{"room packets", 160, 160, func(s *Store) *rate.Limiter { return s.roomsByID["room"].ingressPackets }},
		{"room bytes", 81_920, 81_920, func(s *Store) *rate.Limiter { return s.roomsByID["room"].ingressBytes }},
		{"authenticated global packets", 1_280, 1_280, func(s *Store) *rate.Limiter { return s.authenticatedGlobalPackets }},
		{"authenticated global bytes", 655_360, 655_360, func(s *Store) *rate.Limiter { return s.authenticatedGlobalBytes }},
		{"room fanout writes", 480, 480, func(s *Store) *rate.Limiter { return s.roomsByID["room"].fanoutWrites }},
		{"room fanout bytes", 245_760, 245_760, func(s *Store) *rate.Limiter { return s.roomsByID["room"].fanoutBytes }},
		{"global fanout writes", 3_840, 3_840, func(s *Store) *rate.Limiter { return s.globalFanoutWrites }},
		{"global fanout bytes", 1_966_080, 1_966_080, func(s *Store) *rate.Limiter { return s.globalFanoutBytes }},
	}
	for _, spec := range specs {
		t.Run(spec.name+"/equality", func(t *testing.T) {
			limiter := newD04Limiter(t, spec.pick)
			if limiter.Limit() != spec.rate || limiter.Burst() != spec.burst {
				t.Fatalf("limiter = %v/%d, want %v/%d", limiter.Limit(), limiter.Burst(), spec.rate, spec.burst)
			}
			if !allowAtomic(limiterTime(0), limiterCharge{limiter, spec.burst}) {
				t.Fatal("exact burst was rejected")
			}
		})
		t.Run(spec.name+"/one-over", func(t *testing.T) {
			limiter := newD04Limiter(t, spec.pick)
			now := limiterTime(0)
			if allowAtomic(now, limiterCharge{limiter, spec.burst + 1}) {
				t.Fatal("one-over burst was accepted")
			}
			if got := limiter.TokensAt(now); got != float64(spec.burst) {
				t.Fatalf("one-over changed tokens to %v, want %d", got, spec.burst)
			}
		})
		for _, refill := range []struct {
			name string
			at   time.Duration
			want bool
		}{
			{"refill-1ns", time.Second - time.Nanosecond, false},
			{"exact-refill", time.Second, true},
			{"refill+1ns", time.Second + time.Nanosecond, true},
		} {
			t.Run(spec.name+"/"+refill.name, func(t *testing.T) {
				limiter := newD04Limiter(t, spec.pick)
				if !allowAtomic(limiterTime(0), limiterCharge{limiter, spec.burst}) {
					t.Fatal("could not exhaust initial burst")
				}
				if got := allowAtomic(limiterTime(refill.at), limiterCharge{limiter, int(spec.rate)}); got != refill.want {
					t.Fatalf("rate-sized charge at %v = %t, want %t", refill.at, got, refill.want)
				}
			})
		}
	}
}

func TestReplayWindowMatrix(t *testing.T) {
	var window replayWindow
	for _, sequence := range []uint64{1, 2, 65, 3, math.MaxUint64} {
		if !window.accept(sequence) {
			t.Fatalf("fresh sequence %d was rejected", sequence)
		}
	}
	if window.accept(math.MaxUint64) {
		t.Fatal("duplicate MaxUint64 was accepted")
	}
	if !window.accept(math.MaxUint64 - 63) {
		t.Fatal("highest-63 unseen sequence was rejected")
	}
	if window.accept(math.MaxUint64 - 63) {
		t.Fatal("highest-63 duplicate was accepted")
	}
	if window.accept(math.MaxUint64 - 64) {
		t.Fatal("highest-64 sequence was accepted")
	}
	if window.highest != math.MaxUint64 {
		t.Fatalf("highest = %d, want MaxUint64", window.highest)
	}
}

func TestBeginChallengeValidatesAuthorityAndIsIdempotent(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	endpoint := netip.MustParseAddrPort("192.0.2.10:4000")
	clientNonce := bytes16(0x31)
	fixture.random.reset(filled(0x41, 16), filled(0x51, 32))
	request := fixture.challengeRequest(0, clientNonce, endpoint)

	challenge, reason := fixture.store.BeginChallenge(request)
	if reason != RejectNone {
		t.Fatalf("BeginChallenge() reason = %q", reason)
	}
	if challenge.CandidateID != bytes16(0x41) || challenge.ServerNonce != bytes32(0x51) ||
		challenge.ExpiresUnixMS != testWall.Add(3*time.Second).UnixMilli() {
		t.Fatalf("challenge = %#v", challenge)
	}
	assertReads(t, fixture.random.calls, 16, 32)
	assertNoSecretFields(t, ChallengeResult{})
	if challenge.ServerNonce == fixture.secret(0) {
		t.Fatal("challenge exposed the grant secret")
	}

	duplicate, reason := fixture.store.BeginChallenge(request)
	if reason != RejectNone || duplicate != challenge {
		t.Fatalf("duplicate BeginChallenge() = (%#v, %q), want same challenge", duplicate, reason)
	}
	assertReads(t, fixture.random.calls, 16, 32)

	differentNonce := request
	differentNonce.ClientNonce = bytes16(0x32)
	if got, reason := fixture.store.BeginChallenge(differentNonce); reason != RejectAuthFailed || got != (ChallengeResult{}) {
		t.Fatalf("different nonce while pending = (%#v, %q), want auth_failed", got, reason)
	}
	differentEndpoint := request
	differentEndpoint.Endpoint = netip.MustParseAddrPort("192.0.2.10:4001")
	if got, reason := fixture.store.BeginChallenge(differentEndpoint); reason != RejectWrongEndpoint || got != (ChallengeResult{}) {
		t.Fatalf("different endpoint while pending = (%#v, %q), want wrong_endpoint", got, reason)
	}

	wrongRoom := request
	wrongRoom.RoomID = "other-room"
	if _, reason := fixture.store.BeginChallenge(wrongRoom); reason != RejectWrongRoom {
		t.Fatalf("wrong room reason = %q", reason)
	}
	wrongSession := request
	wrongSession.SessionID = "other-session"
	if _, reason := fixture.store.BeginChallenge(wrongSession); reason != RejectAuthFailed {
		t.Fatalf("wrong session reason = %q", reason)
	}
	unknown := request
	unknown.GrantID = bytes16(0xee)
	if _, reason := fixture.store.BeginChallenge(unknown); reason != RejectUnknownGrant {
		t.Fatalf("unknown grant reason = %q", reason)
	}
	assertStoreInvariants(t, fixture.store)
}

func TestHandshakeWireExpiryCeilsWithoutRoundingAuthority(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Nanosecond, time.Nanosecond, 1)
	endpoint := netip.MustParseAddrPort("192.0.2.91:9000")
	nonce := bytes16(0x21)
	fixture.random.reset(filled(0x31, 16), filled(0x41, 32), filled(0x51, 16))

	challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
	if reason != RejectNone {
		t.Fatalf("BeginChallenge() reason = %q", reason)
	}
	wantExpiry := testWall.UnixMilli() + 1
	if challenge.ExpiresUnixMS != wantExpiry {
		t.Errorf("CHALLENGE expiry = %d, want ceiling millisecond %d", challenge.ExpiresUnixMS, wantExpiry)
	}
	if deadline := fixture.grant(0).pending.deadline; deadline != time.Nanosecond {
		t.Errorf("challenge monotonic deadline = %v, want exact 1ns", deadline)
	}

	bound, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, nonce, endpoint))
	if reason != RejectNone {
		t.Fatalf("Authenticate() reason = %q", reason)
	}
	if bound.ExpiresUnixMS != wantExpiry {
		t.Errorf("BOUND expiry = %d, want ceiling millisecond %d", bound.ExpiresUnixMS, wantExpiry)
	}
	if deadline := fixture.grant(0).binding.deadline; deadline != time.Nanosecond {
		t.Errorf("binding monotonic deadline = %v, want exact 1ns", deadline)
	}
	key := protocol.BindingKey(fixture.secret(0), protocol.Revision, "room", fixture.session(0),
		fixture.grantID(0), challenge.CandidateID, nonce, challenge.ServerNonce)
	wantTag := protocol.BoundTag(key, protocol.Revision, "room", fixture.session(0),
		challenge.CandidateID, bound.BindingID, wantExpiry)
	if bound.AuthTag != wantTag {
		t.Error("BOUND tag did not authenticate the ceiling-rounded wire expiry")
	}
}

func TestBeginChallengeRejectsRememberedNonceAcrossEndpoints(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	oldEndpoint := netip.MustParseAddrPort("192.0.2.14:4000")
	newEndpoint := netip.MustParseAddrPort("192.0.2.15:4000")
	nonce := bytes16(0x35)
	fixture.random.reset(filled(0x44, 16), filled(0x54, 32), filled(0x64, 16))
	challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, oldEndpoint))
	if reason != RejectNone {
		t.Fatalf("BeginChallenge(): %q", reason)
	}
	if _, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, nonce, oldEndpoint)); reason != RejectNone {
		t.Fatalf("Authenticate(): %q", reason)
	}
	grant := fixture.grant(0)
	oldBinding, oldRecent := grant.binding, grant.recent
	oldBindingValue, oldRecentValue := *oldBinding, *oldRecent
	fixture.random.reset(filled(0x45, 16), filled(0x55, 32))

	if got, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, newEndpoint)); reason != RejectAuthFailed || got != (ChallengeResult{}) {
		t.Fatalf("remembered nonce from new endpoint = (%#v, %q), want auth_failed", got, reason)
	}
	if len(fixture.random.calls) != 0 || grant.pending != nil || grant.binding != oldBinding || grant.recent != oldRecent ||
		*oldBinding != oldBindingValue || *oldRecent != oldRecentValue {
		t.Fatal("remembered nonce rejection used randomness or changed current state")
	}
}

func TestChallengeExactDeadlineAndTerminalGrantPaths(t *testing.T) {
	t.Run("challenge deadline", func(t *testing.T) {
		fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
		endpoint := netip.MustParseAddrPort("192.0.2.11:4000")
		fixture.random.reset(filled(0x42, 16), filled(0x52, 32))
		challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, bytes16(0x33), endpoint))
		if reason != RejectNone {
			t.Fatalf("BeginChallenge(): %q", reason)
		}
		fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(3 * time.Second), Mono: 3 * time.Second}
		if _, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, bytes16(0x33), endpoint)); reason != RejectExpired {
			t.Fatalf("Authenticate(exact challenge deadline) reason = %q", reason)
		}
		fixture.store.mu.RLock()
		defer fixture.store.mu.RUnlock()
		if fixture.grant(0).pending != nil || fixture.store.candidatesByID[challenge.CandidateID] != nil {
			t.Fatal("expired candidate remained indexed")
		}
	})

	t.Run("grant deadline", func(t *testing.T) {
		fixture := newHandshakeFixture(t, time.Hour, 2*time.Second, 1)
		fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(2 * time.Second), Mono: 2 * time.Second}
		if _, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, bytes16(1), netip.MustParseAddrPort("192.0.2.12:4000"))); reason != RejectExpired {
			t.Fatalf("BeginChallenge(exact grant deadline) reason = %q", reason)
		}
		if fixture.grant(0).secret != nil || fixture.grant(0).state != GrantStateExpired {
			t.Fatal("expired grant retained authority")
		}
	})

	t.Run("revoked challenge", func(t *testing.T) {
		fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
		endpoint := netip.MustParseAddrPort("192.0.2.13:4000")
		fixture.random.reset(filled(0x43, 16), filled(0x53, 32))
		challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, bytes16(0x34), endpoint))
		if reason != RejectNone {
			t.Fatalf("BeginChallenge(): %q", reason)
		}
		if err := fixture.store.EndRoom("room"); err != nil {
			t.Fatalf("EndRoom(): %v", err)
		}
		if _, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, bytes16(0x34), endpoint)); reason != RejectAuthFailed {
			t.Fatalf("Authenticate(revoked candidate) reason = %q, want auth_failed", reason)
		}
		assertStoreInvariants(t, fixture.store)
	})
}

func TestChallengeRandomnessIsStagedAndCollisionBounded(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 3)
	endpoint := netip.MustParseAddrPort("198.51.100.1:5000")
	collision := bytes16(0x61)
	fixture.random.reset(filled(0x61, 16), filled(0x71, 32))
	if _, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, bytes16(1), endpoint)); reason != RejectNone {
		t.Fatalf("first challenge reason = %q", reason)
	}

	chunks := make([][]byte, 0, 10)
	for range 8 {
		chunks = append(chunks, filled(0x61, 16))
	}
	chunks = append(chunks, filled(0x62, 16), filled(0x72, 32))
	fixture.random.reset(chunks...)
	second, reason := fixture.store.BeginChallenge(fixture.challengeRequest(1, bytes16(2), netip.MustParseAddrPort("198.51.100.2:5000")))
	if reason != RejectNone || second.CandidateID != bytes16(0x62) {
		t.Fatalf("ninth-draw collision success = (%#v, %q)", second, reason)
	}
	assertReads(t, fixture.random.calls, 16, 16, 16, 16, 16, 16, 16, 16, 16, 32)

	chunks = chunks[:0]
	for range 9 {
		chunks = append(chunks, filled(collision[0], 16))
	}
	fixture.random.reset(chunks...)
	beforeCandidates := len(fixture.store.candidatesByID)
	if got, reason := fixture.store.BeginChallenge(fixture.challengeRequest(2, bytes16(3), netip.MustParseAddrPort("198.51.100.3:5000"))); reason != RejectFatalRandom || got != (ChallengeResult{}) {
		t.Fatalf("collision exhaustion = (%#v, %q)", got, reason)
	}
	if len(fixture.store.candidatesByID) != beforeCandidates || fixture.grant(2).pending != nil {
		t.Fatal("collision exhaustion committed partial candidate state")
	}

	for _, tt := range []struct {
		name   string
		chunks [][]byte
		failAt int
	}{
		{name: "short candidate", chunks: [][]byte{filled(1, 15)}},
		{name: "short nonce", chunks: [][]byte{filled(0x63, 16), filled(1, 31)}},
		{name: "reader error", failAt: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fresh := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
			fresh.random.reset(tt.chunks...)
			fresh.random.failAt = tt.failAt
			fresh.random.failure = errors.New("random failed")
			if _, reason := fresh.store.BeginChallenge(fresh.challengeRequest(0, bytes16(4), endpoint)); reason != RejectFatalRandom {
				t.Fatalf("BeginChallenge() reason = %q", reason)
			}
			if len(fresh.store.candidatesByID) != 0 || fresh.grant(0).pending != nil {
				t.Fatal("random failure committed partial challenge")
			}
		})
	}
}

func TestAuthenticateBindsAndReplaysCurrentBound(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	endpoint := netip.MustParseAddrPort("203.0.113.10:6000")
	clientNonce := bytes16(0x81)
	fixture.random.reset(filled(0x82, 16), filled(0x83, 32), filled(0x84, 16))
	challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, clientNonce, endpoint))
	if reason != RejectNone {
		t.Fatalf("BeginChallenge(): %q", reason)
	}
	auth := fixture.authRequest(0, challenge, clientNonce, endpoint)

	wrongEndpoint := auth
	wrongEndpoint.Endpoint = netip.MustParseAddrPort("203.0.113.10:6001")
	if _, reason := fixture.store.Authenticate(wrongEndpoint); reason != RejectWrongEndpoint {
		t.Fatalf("wrong endpoint reason = %q", reason)
	}
	badTag := auth
	badTag.AuthTag[0] ^= 1
	if _, reason := fixture.store.Authenticate(badTag); reason != RejectAuthFailed {
		t.Fatalf("bad HMAC reason = %q", reason)
	}
	if fixture.grant(0).pending == nil {
		t.Fatal("failed AUTH consumed candidate")
	}

	bound, reason := fixture.store.Authenticate(auth)
	if reason != RejectNone {
		t.Fatalf("Authenticate() reason = %q", reason)
	}
	wantKey := protocol.BindingKey(fixture.secret(0), protocol.Revision, "room", fixture.session(0),
		fixture.grantID(0), challenge.CandidateID, clientNonce, challenge.ServerNonce)
	wantTag := protocol.BoundTag(wantKey, protocol.Revision, "room", fixture.session(0),
		challenge.CandidateID, bytes16(0x84), testWall.Add(60*time.Second).UnixMilli())
	if bound.BindingID != bytes16(0x84) || bound.ExpiresUnixMS != testWall.Add(60*time.Second).UnixMilli() || bound.AuthTag != wantTag {
		t.Fatalf("BOUND = %#v, want id=%x expiry=%d tag=%x", bound, bytes16(0x84), testWall.Add(60*time.Second).UnixMilli(), wantTag)
	}
	assertNoSecretFields(t, BoundResult{})
	grant := fixture.grant(0)
	if grant.pending != nil || grant.recent == nil || grant.binding == nil || grant.binding.key != wantKey ||
		grant.binding.endpoint != endpoint || grant.binding.replay != (replayWindow{}) || grant.state != GrantStateBound {
		t.Fatalf("bound grant state = %#v", grant)
	}
	if fixture.store.bindingsByID[bound.BindingID] != grant || fixture.store.candidatesByID[challenge.CandidateID] != grant {
		t.Fatal("current binding/recent completion indexes are inconsistent")
	}

	reads := append([]int(nil), fixture.random.calls...)
	duplicate, reason := fixture.store.Authenticate(auth)
	if reason != RejectNone || duplicate != bound {
		t.Fatalf("duplicate AUTH = (%#v, %q), want same BOUND", duplicate, reason)
	}
	if !reflect.DeepEqual(fixture.random.calls, reads) {
		t.Fatalf("duplicate AUTH used randomness: %v -> %v", reads, fixture.random.calls)
	}
	assertStoreInvariants(t, fixture.store)
}

func TestBindingsAllowSessionsToShareOneNATEndpoint(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 2)
	endpoint := netip.MustParseAddrPort("203.0.113.15:6000")
	for index := range 2 {
		nonce := bytes16(byte(0x85 + index))
		fixture.random.reset(filled(byte(0x87+index), 16), filled(byte(0x89+index), 32), filled(byte(0x8b+index), 16))
		challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(index, nonce, endpoint))
		if reason != RejectNone {
			t.Fatalf("BeginChallenge(%d): %q", index, reason)
		}
		if _, reason := fixture.store.Authenticate(fixture.authRequest(index, challenge, nonce, endpoint)); reason != RejectNone {
			t.Fatalf("Authenticate(%d): %q", index, reason)
		}
	}
	if fixture.grant(0).binding.endpoint != endpoint || fixture.grant(1).binding.endpoint != endpoint || len(fixture.store.bindingsByID) != 2 {
		t.Fatal("shared NAT endpoint displaced one session binding")
	}
}

func TestAuthenticateBindingDeadlineUsesMinimumAuthority(t *testing.T) {
	tests := []struct {
		name       string
		roomTTL    time.Duration
		grantTTL   time.Duration
		wantExpiry time.Duration
	}{
		{name: "binding TTL", roomTTL: time.Hour, grantTTL: 30 * time.Minute, wantExpiry: 60 * time.Second},
		{name: "grant TTL", roomTTL: time.Hour, grantTTL: 30 * time.Second, wantExpiry: 30 * time.Second},
		{name: "room TTL", roomTTL: 20 * time.Second, grantTTL: 20 * time.Second, wantExpiry: 20 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHandshakeFixture(t, tt.roomTTL, tt.grantTTL, 1)
			if tt.name == "grant TTL" {
				fixture.limits.BindingTTL = 60 * time.Second
			}
			endpoint := netip.MustParseAddrPort("203.0.113.20:6000")
			nonce := bytes16(0x91)
			fixture.random.reset(filled(0x92, 16), filled(0x93, 32), filled(0x94, 16))
			challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
			if reason != RejectNone {
				t.Fatalf("BeginChallenge(): %q", reason)
			}
			bound, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, nonce, endpoint))
			if reason != RejectNone || bound.ExpiresUnixMS != testWall.Add(tt.wantExpiry).UnixMilli() {
				t.Fatalf("Authenticate() = (%#v, %q), want expiry %v", bound, reason, tt.wantExpiry)
			}
		})
	}
}

func TestAuthenticateBindingRandomFailureRollsBack(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 3)
	firstEndpoint := netip.MustParseAddrPort("203.0.113.30:6000")
	firstNonce := bytes16(0xa1)
	fixture.random.reset(filled(0xa2, 16), filled(0xa3, 32), filled(0xa4, 16))
	firstChallenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(0, firstNonce, firstEndpoint))
	firstBound, reason := fixture.store.Authenticate(fixture.authRequest(0, firstChallenge, firstNonce, firstEndpoint))
	if reason != RejectNone {
		t.Fatalf("first Authenticate(): %q", reason)
	}

	secondEndpoint := netip.MustParseAddrPort("203.0.113.31:6000")
	secondNonce := bytes16(0xb1)
	fixture.random.reset(filled(0xb2, 16), filled(0xb3, 32))
	secondChallenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(1, secondNonce, secondEndpoint))
	chunks := make([][]byte, 0, 9)
	for range 8 {
		chunks = append(chunks, filled(0xa4, 16))
	}
	chunks = append(chunks, filled(0xb4, 16))
	fixture.random.reset(chunks...)
	secondBound, reason := fixture.store.Authenticate(fixture.authRequest(1, secondChallenge, secondNonce, secondEndpoint))
	if reason != RejectNone || secondBound.BindingID != bytes16(0xb4) {
		t.Fatalf("ninth-draw binding collision success = (%#v, %q)", secondBound, reason)
	}
	assertReads(t, fixture.random.calls, 16, 16, 16, 16, 16, 16, 16, 16, 16)

	thirdEndpoint := netip.MustParseAddrPort("203.0.113.32:6000")
	thirdNonce := bytes16(0xc1)
	fixture.random.reset(filled(0xc2, 16), filled(0xc3, 32))
	thirdChallenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(2, thirdNonce, thirdEndpoint))
	chunks = make([][]byte, 9)
	for index := range chunks {
		chunks[index] = filled(0xa4, 16)
	}
	fixture.random.reset(chunks...)
	if _, reason := fixture.store.Authenticate(fixture.authRequest(2, thirdChallenge, thirdNonce, thirdEndpoint)); reason != RejectFatalRandom {
		t.Fatalf("binding collision exhaustion reason = %q", reason)
	}
	if fixture.store.bindingsByID[firstBound.BindingID] != fixture.grant(0) ||
		fixture.store.bindingsByID[secondBound.BindingID] != fixture.grant(1) || fixture.grant(2).binding != nil || fixture.grant(2).pending == nil {
		t.Fatal("binding collision failure changed existing or pending state")
	}

	fixture.random.reset(filled(0xb4, 15))
	if _, reason := fixture.store.Authenticate(fixture.authRequest(2, thirdChallenge, thirdNonce, thirdEndpoint)); reason != RejectFatalRandom {
		t.Fatalf("short binding ID reason = %q", reason)
	}
	if fixture.grant(2).binding != nil || fixture.grant(2).pending == nil {
		t.Fatal("short binding read partially committed state")
	}
}

func TestRebindRotatesBindingAtomicallyAndKeepsRecentUntilReplacement(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	oldEndpoint := netip.MustParseAddrPort("203.0.113.40:6000")
	oldNonce := bytes16(0xc1)
	fixture.random.reset(filled(0xc2, 16), filled(0xc3, 32), filled(0xc4, 16))
	oldChallenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(0, oldNonce, oldEndpoint))
	oldAuth := fixture.authRequest(0, oldChallenge, oldNonce, oldEndpoint)
	oldBound, reason := fixture.store.Authenticate(oldAuth)
	if reason != RejectNone {
		t.Fatalf("old Authenticate(): %q", reason)
	}
	oldBinding := fixture.grant(0).binding
	oldBinding.replay.accept(99)
	oldID, oldKey, oldEndpointValue := oldBinding.id, oldBinding.key, oldBinding.endpoint
	oldDeadline, oldGeneration, oldReplay := oldBinding.deadline, oldBinding.generation, oldBinding.replay
	if snapshot, err := fixture.store.GetRoom("room"); err != nil || snapshot.Participants[0].BindingState != BindingStateBound {
		t.Fatalf("bound snapshot = (%#v, %v)", snapshot, err)
	}

	newEndpoint := netip.MustParseAddrPort("203.0.113.41:6000")
	newNonce := bytes16(0xd1)
	fixture.random.reset(filled(0xd2, 16), filled(0xd3, 32), filled(0xd4, 16))
	newChallenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, newNonce, newEndpoint))
	if reason != RejectNone {
		t.Fatalf("rebind BeginChallenge(): %q", reason)
	}
	if fixture.grant(0).binding != oldBinding || fixture.store.bindingsByID[oldBound.BindingID] != fixture.grant(0) ||
		fixture.grant(0).recent == nil || fixture.grant(0).bindingState != BindingStateRebindPending {
		t.Fatal("pending rebind invalidated the old current binding/recent completion")
	}
	if snapshot, err := fixture.store.GetRoom("room"); err != nil || snapshot.Participants[0].BindingState != BindingStateRebindPending {
		t.Fatalf("rebind-pending snapshot = (%#v, %v)", snapshot, err)
	}
	if replay, reason := fixture.store.Authenticate(oldAuth); reason != RejectNone || replay != oldBound {
		t.Fatalf("old duplicate AUTH during pending rebind = (%#v, %q)", replay, reason)
	}
	pending := fixture.grant(0).pending
	assertOldBinding := func(label string) {
		t.Helper()
		grant := fixture.grant(0)
		if grant.binding != oldBinding || fixture.store.bindingsByID[oldID] != grant || oldBinding.id != oldID ||
			oldBinding.key != oldKey || oldBinding.endpoint != oldEndpointValue || oldBinding.deadline != oldDeadline ||
			oldBinding.generation != oldGeneration || oldBinding.replay != oldReplay || grant.state != GrantStateBound ||
			grant.bindingState != BindingStateRebindPending || grant.pending != pending {
			t.Fatalf("%s changed the old binding or pending rebind", label)
		}
	}
	collisions := make([][]byte, 9)
	for index := range collisions {
		collisions[index] = append([]byte(nil), oldID[:]...)
	}
	fixture.random.reset(collisions...)
	if _, reason := fixture.store.Authenticate(fixture.authRequest(0, newChallenge, newNonce, newEndpoint)); reason != RejectFatalRandom {
		t.Fatalf("rebind collision exhaustion reason = %q", reason)
	}
	assertOldBinding("collision exhaustion")
	fixture.random.reset(filled(0xee, 15))
	if _, reason := fixture.store.Authenticate(fixture.authRequest(0, newChallenge, newNonce, newEndpoint)); reason != RejectFatalRandom {
		t.Fatalf("rebind short read reason = %q", reason)
	}
	assertOldBinding("short read")

	fixture.random.reset(filled(0xd4, 16))
	newBound, reason := fixture.store.Authenticate(fixture.authRequest(0, newChallenge, newNonce, newEndpoint))
	if reason != RejectNone {
		t.Fatalf("new Authenticate(): %q", reason)
	}
	current := fixture.grant(0).binding
	if current == oldBinding || current.id != newBound.BindingID || current.id == oldID || current.endpoint != newEndpoint ||
		current.endpoint == oldEndpointValue || current.key == oldKey || current.replay != (replayWindow{}) ||
		current.generation != oldGeneration+1 {
		t.Fatalf("new binding did not rotate all binding-scoped state: old=%#v new=%#v", oldBinding, current)
	}
	if _, exists := fixture.store.bindingsByID[oldID]; exists || oldBinding.id != (protocol.Bytes16{}) ||
		oldBinding.key != (protocol.Bytes32{}) || oldBinding.endpoint.IsValid() || oldBinding.replay != (replayWindow{}) {
		t.Fatal("old binding remained usable or retained secret/index state")
	}
	if _, reason := fixture.store.Authenticate(oldAuth); reason != RejectAuthFailed {
		t.Fatalf("old duplicate AUTH after newer completion reason = %q", reason)
	}
	if fixture.grant(0).recent == nil || fixture.grant(0).recent.result != newBound || fixture.grant(0).bindingState != BindingStateBound {
		t.Fatal("new completion was not current")
	}
	if snapshot, err := fixture.store.GetRoom("room"); err != nil || snapshot.Participants[0].BindingState != BindingStateBound {
		t.Fatalf("rebound snapshot = (%#v, %v)", snapshot, err)
	}
	assertStoreInvariants(t, fixture.store)
}

func TestGetRoomProjectsBindingAndPendingDeadlinesBeforeSweep(t *testing.T) {
	t.Run("binding", func(t *testing.T) {
		fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
		endpoint := netip.MustParseAddrPort("203.0.113.44:6000")
		nonce := bytes16(0xdd)
		fixture.random.reset(filled(0xde, 16), filled(0xdf, 32), filled(0xe0, 16))
		challenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
		bound, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, nonce, endpoint))
		if reason != RejectNone {
			t.Fatalf("Authenticate(): %q", reason)
		}
		binding := fixture.grant(0).binding
		for _, tt := range []struct {
			name string
			at   time.Duration
			want BindingState
		}{
			{name: "before", at: 60*time.Second - time.Nanosecond, want: BindingStateBound},
			{name: "exact", at: 60 * time.Second, want: BindingStateExpired},
			{name: "after", at: 60*time.Second + time.Nanosecond, want: BindingStateExpired},
		} {
			fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(tt.at), Mono: tt.at}
			snapshot, err := fixture.store.GetRoom("room")
			if err != nil || snapshot.Participants[0].BindingState != tt.want {
				t.Fatalf("GetRoom(%s) = (%#v, %v), want binding %q", tt.name, snapshot, err, tt.want)
			}
			wantGrant := GrantStateBound
			if tt.want == BindingStateExpired {
				wantGrant = GrantStateIssued
			}
			if snapshot.Participants[0].GrantState != wantGrant {
				t.Fatalf("GetRoom(%s) grant = %q, want %q", tt.name, snapshot.Participants[0].GrantState, wantGrant)
			}
		}
		if fixture.grant(0).binding != binding || fixture.store.bindingsByID[bound.BindingID] != fixture.grant(0) {
			t.Fatal("deadline-aware snapshot mutated store under read access")
		}
		fixture.store.Expire()
		if fixture.grant(0).binding != nil || fixture.store.bindingsByID[bound.BindingID] != nil || binding.key != (protocol.Bytes32{}) {
			t.Fatal("Expire did not clear the projected-expired binding")
		}
	})

	t.Run("rebind pending", func(t *testing.T) {
		fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
		oldEndpoint := netip.MustParseAddrPort("203.0.113.45:6000")
		newEndpoint := netip.MustParseAddrPort("203.0.113.46:6000")
		oldNonce, newNonce := bytes16(0xe1), bytes16(0xe2)
		fixture.random.reset(filled(0xe3, 16), filled(0xe4, 32), filled(0xe5, 16))
		oldChallenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(0, oldNonce, oldEndpoint))
		if _, reason := fixture.store.Authenticate(fixture.authRequest(0, oldChallenge, oldNonce, oldEndpoint)); reason != RejectNone {
			t.Fatalf("Authenticate(): %q", reason)
		}
		fixture.random.reset(filled(0xe6, 16), filled(0xe7, 32))
		newChallenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, newNonce, newEndpoint))
		if reason != RejectNone {
			t.Fatalf("rebind BeginChallenge(): %q", reason)
		}
		for _, tt := range []struct {
			name string
			at   time.Duration
			want BindingState
		}{
			{name: "before", at: 3*time.Second - time.Nanosecond, want: BindingStateRebindPending},
			{name: "exact", at: 3 * time.Second, want: BindingStateBound},
			{name: "after", at: 3*time.Second + time.Nanosecond, want: BindingStateBound},
		} {
			fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(tt.at), Mono: tt.at}
			snapshot, err := fixture.store.GetRoom("room")
			if err != nil || snapshot.Participants[0].BindingState != tt.want || snapshot.Participants[0].GrantState != GrantStateBound {
				t.Fatalf("GetRoom(%s) = (%#v, %v), want bound/%q", tt.name, snapshot, err, tt.want)
			}
		}
		if fixture.grant(0).pending == nil || fixture.store.candidatesByID[newChallenge.CandidateID] != fixture.grant(0) {
			t.Fatal("deadline-aware snapshot mutated pending state")
		}
		fixture.store.Expire()
		if fixture.grant(0).pending != nil || fixture.store.candidatesByID[newChallenge.CandidateID] != nil ||
			fixture.grant(0).binding == nil || fixture.grant(0).bindingState != BindingStateBound {
			t.Fatal("Expire did not clear projected-expired pending state")
		}
	})
}

func TestIdempotentCreateRoomProjectsBindingDeadlineBeforeSweep(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	endpoint := netip.MustParseAddrPort("203.0.113.47:6000")
	nonce := bytes16(0xe8)
	fixture.random.reset(filled(0xe9, 16), filled(0xea, 32), filled(0xeb, 16))
	challenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
	if _, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, nonce, endpoint)); reason != RejectNone {
		t.Fatalf("Authenticate(): %q", reason)
	}
	definition := validDefinition(testWall, 1)
	for _, tt := range []struct {
		name string
		at   time.Duration
		want GrantState
	}{
		{name: "before", at: 60*time.Second - time.Nanosecond, want: GrantStateBound},
		{name: "exact", at: 60 * time.Second, want: GrantStateIssued},
		{name: "after", at: 60*time.Second + time.Nanosecond, want: GrantStateIssued},
	} {
		fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(tt.at), Mono: tt.at}
		allocation, created, err := fixture.store.CreateRoom("room", definition)
		if err != nil || created || allocation.Grants[0].State != tt.want || allocation.Grants[0].GrantSecret == nil {
			t.Fatalf("CreateRoom(%s) = (%#v, %t, %v), want state %q with live secret", tt.name, allocation, created, err, tt.want)
		}
	}
}

func TestExpiredBindingRemainsExpiredWithLivePendingRebind(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	fixture.store.limits.BindingTTL = 2 * time.Second
	oldEndpoint := netip.MustParseAddrPort("203.0.113.48:6000")
	newEndpoint := netip.MustParseAddrPort("203.0.113.49:6000")
	oldNonce, newNonce := bytes16(0xec), bytes16(0xed)
	fixture.random.reset(filled(0xee, 16), filled(0xef, 32), filled(0xf0, 16))
	oldChallenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(0, oldNonce, oldEndpoint))
	oldBound, reason := fixture.store.Authenticate(fixture.authRequest(0, oldChallenge, oldNonce, oldEndpoint))
	if reason != RejectNone {
		t.Fatalf("old Authenticate(): %q", reason)
	}
	oldBinding := fixture.grant(0).binding

	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(time.Second), Mono: time.Second}
	fixture.random.reset(filled(0xf1, 16), filled(0xf2, 32))
	newChallenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, newNonce, newEndpoint))
	if reason != RejectNone {
		t.Fatalf("rebind BeginChallenge(): %q", reason)
	}
	pending := fixture.grant(0).pending
	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(2 * time.Second), Mono: 2 * time.Second}
	before, err := fixture.store.GetRoom("room")
	if err != nil || before.Participants[0].GrantState != GrantStateIssued || before.Participants[0].BindingState != BindingStateExpired {
		t.Fatalf("pre-sweep snapshot = (%#v, %v), want issued/expired", before, err)
	}
	fixture.store.Expire()
	after, err := fixture.store.GetRoom("room")
	if err != nil || after.Participants[0].GrantState != GrantStateIssued || after.Participants[0].BindingState != BindingStateExpired {
		t.Fatalf("post-sweep snapshot = (%#v, %v), want issued/expired", after, err)
	}
	if fixture.grant(0).pending != pending || fixture.store.candidatesByID[newChallenge.CandidateID] != fixture.grant(0) ||
		fixture.grant(0).binding != nil || fixture.store.bindingsByID[oldBound.BindingID] != nil || oldBinding.key != (protocol.Bytes32{}) {
		t.Fatal("binding expiry removed the live pending rebind or retained old authority")
	}

	fixture.random.reset(filled(0xf3, 16))
	if _, reason := fixture.store.Authenticate(fixture.authRequest(0, newChallenge, newNonce, newEndpoint)); reason != RejectNone {
		t.Fatalf("pending Authenticate after old binding expiry: %q", reason)
	}
	rebound, err := fixture.store.GetRoom("room")
	if err != nil || rebound.Participants[0].GrantState != GrantStateBound || rebound.Participants[0].BindingState != BindingStateBound {
		t.Fatalf("rebound snapshot = (%#v, %v), want bound/bound", rebound, err)
	}
}

func TestRecentCompletionExpiresExactlyAtChallengeTTL(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	endpoint := netip.MustParseAddrPort("203.0.113.42:6000")
	nonce := bytes16(0xd5)
	fixture.random.reset(filled(0xd6, 16), filled(0xd7, 32), filled(0xd8, 16))
	challenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
	auth := fixture.authRequest(0, challenge, nonce, endpoint)
	bound, reason := fixture.store.Authenticate(auth)
	if reason != RejectNone {
		t.Fatalf("Authenticate(): %q", reason)
	}
	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(3*time.Second - time.Nanosecond), Mono: 3*time.Second - time.Nanosecond}
	if duplicate, reason := fixture.store.Authenticate(auth); reason != RejectNone || duplicate != bound {
		t.Fatalf("duplicate before recent deadline = (%#v, %q)", duplicate, reason)
	}
	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(3 * time.Second), Mono: 3 * time.Second}
	if _, reason := fixture.store.Authenticate(auth); reason != RejectExpired {
		t.Fatalf("duplicate at recent deadline reason = %q", reason)
	}
	if fixture.grant(0).recent != nil || fixture.store.candidatesByID[challenge.CandidateID] != nil || fixture.grant(0).binding == nil {
		t.Fatal("recent expiry removed the current binding or retained candidate state")
	}
}

func TestDuplicateAuthAtBindingDeadlineClearsCurrentAuthority(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	fixture.store.limits.BindingTTL = 2 * time.Second
	endpoint := netip.MustParseAddrPort("203.0.113.43:6000")
	nonce := bytes16(0xd9)
	fixture.random.reset(filled(0xda, 16), filled(0xdb, 32), filled(0xdc, 16))
	challenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
	auth := fixture.authRequest(0, challenge, nonce, endpoint)
	bound, reason := fixture.store.Authenticate(auth)
	if reason != RejectNone {
		t.Fatalf("Authenticate(): %q", reason)
	}
	binding := fixture.grant(0).binding
	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(2 * time.Second), Mono: 2 * time.Second}
	if _, reason := fixture.store.Authenticate(auth); reason != RejectExpired {
		t.Fatalf("duplicate at binding deadline reason = %q", reason)
	}
	if fixture.grant(0).binding != nil || fixture.grant(0).recent != nil || fixture.store.bindingsByID[bound.BindingID] != nil ||
		binding.key != (protocol.Bytes32{}) {
		t.Fatal("duplicate AUTH at binding deadline retained current authority")
	}
}

func TestClientIngressClassificationReplayAndPingCharging(t *testing.T) {
	limits := DefaultLimits()
	limits.SessionPacketRate = 1
	limits.SessionPacketBurst = 1
	fixture := newRelayStoreFixture(t, limits)
	client := fixture.addBoundRoom(t, "room", 1, 1)[0]

	request := client.dataRequest(1, []byte("one"))
	admitted, reason := fixture.store.AdmitClientIngress(request, 100)
	if reason != RejectNone || admitted.Sequence() != 1 || admitted.RoomID() != client.roomID ||
		admitted.SessionID() != client.sessionID || admitted.SenderParticipantID() != client.participantID {
		t.Fatalf("first ingress = (%#v, %q)", admitted, reason)
	}
	if _, reason := fixture.store.AdmitClientIngress(request, 100); reason != RejectRateLimited {
		t.Fatalf("rate-limited duplicate reason = %q, want rate_limited", reason)
	}
	second := client.dataRequest(2, []byte("two"))
	if _, reason := fixture.store.AdmitClientIngress(second, 100); reason != RejectRateLimited {
		t.Fatalf("fresh over limit reason = %q", reason)
	}

	fixture.setMono(time.Second)
	if _, reason := fixture.store.AdmitClientIngress(second, 100); reason != RejectReplay {
		t.Fatalf("consumed fresh sequence retry reason = %q, want replay", reason)
	}
	third := client.dataRequest(3, []byte("three"))
	if _, reason := fixture.store.AdmitClientIngress(third, 100); reason != RejectRateLimited {
		t.Fatalf("duplicate replay charge did not consume authenticated ingress: %q", reason)
	}

	fixture.setMono(2 * time.Second)
	fourth := client.dataRequest(4, []byte("four"))
	bad := fourth
	bad.AuthTag[0] ^= 1
	before := authenticatedBalancesAt(fixture.store, fixture.store.bindingsByID[client.bindingID], limiterTime(2*time.Second))
	if _, reason := fixture.store.AdmitClientIngress(bad, 100); reason != RejectAuthFailed {
		t.Fatalf("bad HMAC reason = %q", reason)
	}
	after := authenticatedBalancesAt(fixture.store, fixture.store.bindingsByID[client.bindingID], limiterTime(2*time.Second))
	if after != before {
		t.Fatalf("bad HMAC used authenticated budget: before=%#v after=%#v", before, after)
	}
	if _, reason := fixture.store.AdmitClientIngress(fourth, 100); reason != RejectNone {
		t.Fatalf("good packet after bad HMAC reason = %q", reason)
	}

	fixture.setMono(3 * time.Second)
	ping := client.pingRequest(5)
	if reason := fixture.store.AdmitPing(ping, 90); reason != RejectNone {
		t.Fatalf("Ping reason = %q", reason)
	}
	pingOver := client.pingRequest(6)
	if reason := fixture.store.AdmitPing(pingOver, 90); reason != RejectRateLimited {
		t.Fatalf("Ping over session limit reason = %q", reason)
	}
	fixture.setMono(4 * time.Second)
	if reason := fixture.store.AdmitPing(pingOver, 90); reason != RejectReplay {
		t.Fatalf("rate-rejected Ping replay reason = %q", reason)
	}
}

func TestClientIngressInvalidClassesUsePreauthExactlyOnce(t *testing.T) {
	tests := []struct {
		name string
		want RejectReason
		edit func(*relayStoreFixture, *boundTestClient, *ClientDataRequest)
	}{
		{"unknown binding", RejectNotBound, func(_ *relayStoreFixture, _ *boundTestClient, r *ClientDataRequest) { r.BindingID = bytes16(0xfe) }},
		{"zero binding", RejectNotBound, func(_ *relayStoreFixture, _ *boundTestClient, r *ClientDataRequest) { r.BindingID = protocol.Bytes16{} }},
		{"wrong room", RejectWrongRoom, func(_ *relayStoreFixture, _ *boundTestClient, r *ClientDataRequest) { r.RoomID = "other-room" }},
		{"wrong session", RejectAuthFailed, func(_ *relayStoreFixture, _ *boundTestClient, r *ClientDataRequest) { r.SessionID = "other-session" }},
		{"wrong endpoint", RejectWrongEndpoint, func(_ *relayStoreFixture, _ *boundTestClient, r *ClientDataRequest) {
			r.Endpoint = netip.MustParseAddrPort("198.18.1.1:4999")
		}},
		{"bad HMAC", RejectAuthFailed, func(_ *relayStoreFixture, _ *boundTestClient, r *ClientDataRequest) { r.AuthTag[0] ^= 1 }},
		{"expired", RejectExpired, func(f *relayStoreFixture, c *boundTestClient, _ *ClientDataRequest) {
			f.store.bindingsByID[c.bindingID].binding.deadline = f.fakeClock.reading.Mono
		}},
		{"revoked", RejectNotBound, func(f *relayStoreFixture, c *boundTestClient, _ *ClientDataRequest) { _ = f.store.EndRoom(c.roomID) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRelayStoreFixture(t, DefaultLimits())
			client := fixture.addBoundRoom(t, "room", 1, 1)[0]
			request := client.dataRequest(1, []byte("payload"))
			tt.edit(fixture, &client, &request)
			now := limiterTime(fixture.fakeClock.reading.Mono)
			source := fixture.store.preauthSources[sourceKey(request.Endpoint)]
			if source == nil {
				t.Fatal("classification endpoint unexpectedly has no canonical source record")
			}
			preBefore := preauthBalancesAt(fixture.store, source, now)
			grant := fixture.store.roomsByID[client.roomID]
			var authBefore authenticatedBalances
			if grant != nil && grant.state != roomStateTombstone {
				authBefore = authenticatedBalancesAt(fixture.store, grant.grants[0], now)
			}
			if _, reason := fixture.store.AdmitClientIngress(request, 77); reason != tt.want {
				t.Fatalf("reason = %q, want %q", reason, tt.want)
			}
			preAfter := preauthBalancesAt(fixture.store, source, now)
			wantPre := preauthBalances{preBefore.sourcePackets - 1, preBefore.sourceBytes - 77, preBefore.globalPackets - 1, preBefore.globalBytes - 77}
			if preAfter != wantPre {
				t.Fatalf("preauth charge = %#v -> %#v, want %#v", preBefore, preAfter, wantPre)
			}
			if grant != nil && grant.state != roomStateTombstone {
				if authAfter := authenticatedBalancesAt(fixture.store, grant.grants[0], now); authAfter != authBefore {
					t.Fatalf("invalid class used authenticated budget: %#v -> %#v", authBefore, authAfter)
				}
			}
		})
	}
}

func TestPreauthRateLimitWinsWithoutAuthenticatedDoubleCharge(t *testing.T) {
	limits := DefaultLimits()
	limits.PreauthSourcePacketRate, limits.PreauthSourcePacketBurst = 2, 2
	fixture := newRelayStoreFixture(t, limits)
	client := fixture.addBoundRoom(t, "room", 1, 1)[0]
	request := client.dataRequest(1, nil)
	request.AuthTag[0] ^= 1
	grant := fixture.store.bindingsByID[client.bindingID]
	now := limiterTime(0)
	before := authenticatedBalancesAt(fixture.store, grant, now)
	if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectRateLimited {
		t.Fatalf("bad HMAC with exhausted preauth reason = %q, want rate_limited", reason)
	}
	if after := authenticatedBalancesAt(fixture.store, grant, now); after != before {
		t.Fatalf("preauth rejection double-charged authenticated group: %#v -> %#v", before, after)
	}
	request = client.dataRequest(1, nil)
	if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectNone {
		t.Fatalf("valid authenticated packet was affected by preauth exhaustion: %q", reason)
	}
}

func TestFailedHelloAndAuthChargeOnlyPreauthOnce(t *testing.T) {
	for _, name := range []string{"hello unknown grant", "hello wrong room", "auth unknown candidate", "auth bad HMAC"} {
		t.Run(name, func(t *testing.T) {
			fixture := newRelayStoreFixture(t, DefaultLimits())
			client := fixture.addBoundRoom(t, "room", 1, 1)[0]
			grant := fixture.store.bindingsByID[client.bindingID]
			endpoint := client.endpoint
			inputBytes := 321
			want := RejectUnknownGrant
			var call func() RejectReason
			switch name {
			case "hello unknown grant":
				request := ChallengeRequest{
					RoomID: "room", SessionID: client.sessionID, GrantID: bytes16(0xfe),
					ClientNonce: bytes16(0x61), Endpoint: endpoint, InputBytes: inputBytes,
				}
				call = func() RejectReason { _, reason := fixture.store.BeginChallenge(request); return reason }
			case "hello wrong room":
				want = RejectWrongRoom
				request := ChallengeRequest{
					RoomID: "other-room", SessionID: client.sessionID, GrantID: client.grantID,
					ClientNonce: bytes16(0x62), Endpoint: endpoint, InputBytes: inputBytes,
				}
				call = func() RejectReason { _, reason := fixture.store.BeginChallenge(request); return reason }
			case "auth unknown candidate":
				inputBytes, want = 123, RejectAuthFailed
				request := AuthenticateRequest{
					RoomID: "room", SessionID: client.sessionID, CandidateID: bytes16(0xfd),
					Endpoint: endpoint, InputBytes: inputBytes,
				}
				call = func() RejectReason { _, reason := fixture.store.Authenticate(request); return reason }
			case "auth bad HMAC":
				inputBytes, want = 123, RejectAuthFailed
				rebindEndpoint := netip.AddrPortFrom(endpoint.Addr(), endpoint.Port()+1)
				nonce := bytes16(0x63)
				challenge, reason := fixture.store.BeginChallenge(ChallengeRequest{
					RoomID: "room", SessionID: client.sessionID, GrantID: client.grantID,
					ClientNonce: nonce, Endpoint: rebindEndpoint, InputBytes: 300,
				})
				if reason != RejectNone {
					t.Fatalf("rebind BeginChallenge(): %q", reason)
				}
				request := AuthenticateRequest{
					RoomID: "room", SessionID: client.sessionID, CandidateID: challenge.CandidateID,
					Endpoint: rebindEndpoint, InputBytes: inputBytes,
					AuthTag: protocol.AuthTag(client.secret, protocol.Revision, "room", client.sessionID,
						client.grantID, challenge.CandidateID, nonce, challenge.ServerNonce),
				}
				request.AuthTag[0] ^= 1
				endpoint = rebindEndpoint
				call = func() RejectReason { _, reason := fixture.store.Authenticate(request); return reason }
			}

			now := limiterTime(fixture.fakeClock.reading.Mono)
			source := fixture.store.preauthSources[sourceKey(endpoint)]
			preBefore := preauthBalancesAt(fixture.store, source, now)
			authBefore := authenticatedBalancesAt(fixture.store, grant, now)
			recordsBefore := snapshotRelayRecords(grant)
			if reason := call(); reason != want {
				t.Fatalf("reason = %q, want %q", reason, want)
			}
			wantPre := preauthBalances{
				preBefore.sourcePackets - 1, preBefore.sourceBytes - float64(inputBytes),
				preBefore.globalPackets - 1, preBefore.globalBytes - float64(inputBytes),
			}
			if preAfter := preauthBalancesAt(fixture.store, source, now); preAfter != wantPre {
				t.Fatalf("preauth charge = %#v, want %#v", preAfter, wantPre)
			}
			if authAfter := authenticatedBalancesAt(fixture.store, grant, now); authAfter != authBefore {
				t.Fatalf("failed %s used authenticated budget: %#v -> %#v", name, authBefore, authAfter)
			}
			assertRelayRecordsUnchanged(t, grant, recordsBefore)
		})
	}
}

func TestRateLimitedHelloAndAuthDoNotMutateChallengeOrBinding(t *testing.T) {
	for _, name := range []string{"hello", "auth"} {
		t.Run(name, func(t *testing.T) {
			limits := DefaultLimits()
			limits.PreauthSourcePacketRate = 0.1
			if name == "hello" {
				limits.PreauthSourcePacketBurst = 3
			} else {
				limits.PreauthSourcePacketBurst = 4
			}
			fixture := newRelayStoreFixture(t, limits)
			client := fixture.addBoundRoom(t, "room", 1, 1)[0]
			grant := fixture.store.bindingsByID[client.bindingID]
			var call func() RejectReason
			if name == "hello" {
				request := ChallengeRequest{
					RoomID: "room", SessionID: client.sessionID, GrantID: client.grantID,
					ClientNonce: bytes16(0x71), Endpoint: client.endpoint, InputBytes: 300,
				}
				call = func() RejectReason {
					result, reason := fixture.store.BeginChallenge(request)
					if result != (ChallengeResult{}) {
						t.Fatalf("rate-limited HELLO returned a challenge: %#v", result)
					}
					return reason
				}
			} else {
				rebindEndpoint := netip.AddrPortFrom(client.endpoint.Addr(), client.endpoint.Port()+1)
				nonce := bytes16(0x72)
				challenge, reason := fixture.store.BeginChallenge(ChallengeRequest{
					RoomID: "room", SessionID: client.sessionID, GrantID: client.grantID,
					ClientNonce: nonce, Endpoint: rebindEndpoint, InputBytes: 300,
				})
				if reason != RejectNone {
					t.Fatalf("rebind BeginChallenge(): %q", reason)
				}
				request := AuthenticateRequest{
					RoomID: "room", SessionID: client.sessionID, CandidateID: challenge.CandidateID,
					Endpoint: rebindEndpoint, InputBytes: 100,
					AuthTag: protocol.AuthTag(client.secret, protocol.Revision, "room", client.sessionID,
						client.grantID, challenge.CandidateID, nonce, challenge.ServerNonce),
				}
				call = func() RejectReason {
					result, reason := fixture.store.Authenticate(request)
					if result != (BoundResult{}) {
						t.Fatalf("rate-limited AUTH returned a binding: %#v", result)
					}
					return reason
				}
			}
			if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: client.endpoint, InputBytes: 1}); reason != RejectNone {
				t.Fatalf("exhaust preauth source: %q", reason)
			}
			now := limiterTime(fixture.fakeClock.reading.Mono)
			source := fixture.store.preauthSources[sourceKey(client.endpoint)]
			preBefore := preauthBalancesAt(fixture.store, source, now)
			authBefore := authenticatedBalancesAt(fixture.store, grant, now)
			recordsBefore := snapshotRelayRecords(grant)
			if reason := call(); reason != RejectRateLimited {
				t.Fatalf("rate-limited %s reason = %q", name, reason)
			}
			if preAfter := preauthBalancesAt(fixture.store, source, now); preAfter != preBefore {
				t.Fatalf("rate-limited %s partially charged/reset preauth: %#v -> %#v", name, preBefore, preAfter)
			}
			if authAfter := authenticatedBalancesAt(fixture.store, grant, now); authAfter != authBefore {
				t.Fatalf("rate-limited %s used authenticated budget: %#v -> %#v", name, authBefore, authAfter)
			}
			assertRelayRecordsUnchanged(t, grant, recordsBefore)
		})
	}
}

func TestExpiredBoundLikeRateLimitDoesNotMutateAuthorityBeforeAdmission(t *testing.T) {
	for _, target := range []string{"binding", "grant", "room"} {
		t.Run(target, func(t *testing.T) {
			limits := DefaultLimits()
			limits.PreauthSourcePacketRate = 0.1
			limits.PreauthSourcePacketBurst = 2
			fixture := newRelayStoreFixture(t, limits)
			client := fixture.addBoundRoom(t, "room", 1, 1)[0]
			room := fixture.store.roomsByID["room"]
			grant := fixture.store.bindingsByID[client.bindingID]
			binding := grant.binding
			switch target {
			case "binding":
				binding.deadline = 2 * time.Second
			case "grant":
				grant.monoDeadline = 2 * time.Second
			case "room":
				room.monoDeadline = 2 * time.Second
			}
			fixture.setMono(2 * time.Second)
			request := client.dataRequest(1, nil)
			if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectRateLimited {
				t.Fatalf("AdmitClientIngress(exhausted preauth, expired %s) reason = %q", target, reason)
			}
			if fixture.store.roomsByID["room"] != room || room.state != roomStateOpen ||
				fixture.store.bindingsByID[client.bindingID] != grant || grant.binding != binding ||
				grant.state != GrantStateBound || grant.secret == nil || binding.key != client.key {
				t.Fatalf("rate-limited expired %s input mutated authority: room=%#v grant=%#v binding=%#v", target, room, grant, binding)
			}
		})
	}
}

func TestHMACValidTooOldPacketChargesIngressWithoutReplayMutation(t *testing.T) {
	fixture := newRelayStoreFixture(t, DefaultLimits())
	client := fixture.addBoundRoom(t, "room", 1, 1)[0]
	request := client.dataRequest(65, nil)
	if _, reason := fixture.store.AdmitClientIngress(request, 10); reason != RejectNone {
		t.Fatalf("highest ingress: %q", reason)
	}
	grant := fixture.store.bindingsByID[client.bindingID]
	replayBefore := grant.binding.replay
	now := limiterTime(0)
	before := authenticatedBalancesAt(fixture.store, grant, now)
	tooOld := client.dataRequest(1, nil)
	if _, reason := fixture.store.AdmitClientIngress(tooOld, 10); reason != RejectReplay {
		t.Fatalf("highest-64 ingress reason = %q", reason)
	}
	if grant.binding.replay != replayBefore {
		t.Fatalf("too-old ingress changed replay: %#v -> %#v", replayBefore, grant.binding.replay)
	}
	after := authenticatedBalancesAt(fixture.store, grant, now)
	want := authenticatedBalances{
		before.sessionPackets - 1, before.sessionBytes - 10,
		before.roomPackets - 1, before.roomBytes - 10,
		before.globalPackets - 1, before.globalBytes - 10,
	}
	if after != want {
		t.Fatalf("too-old ingress charge = %#v, want %#v", after, want)
	}
}

func TestAuthenticatedIngressAtomicGroupsAndIsolation(t *testing.T) {
	t.Run("session failure leaves room and global untouched", func(t *testing.T) {
		limits := DefaultLimits()
		limits.SessionPacketRate, limits.SessionPacketBurst = 1, 1
		limits.RoomPacketRate, limits.RoomPacketBurst = 3, 3
		limits.AuthenticatedGlobalPacketRate, limits.AuthenticatedGlobalPacketBurst = 3, 3
		fixture := newRelayStoreFixture(t, limits)
		clients := fixture.addBoundRoom(t, "room", 2, 1)
		first := clients[0].dataRequest(1, nil)
		if _, reason := fixture.store.AdmitClientIngress(first, 1); reason != RejectNone {
			t.Fatalf("first ingress: %q", reason)
		}
		grant := fixture.store.bindingsByID[clients[0].bindingID]
		now := limiterTime(0)
		before := authenticatedBalancesAt(fixture.store, grant, now)
		over := clients[0].dataRequest(2, nil)
		if _, reason := fixture.store.AdmitClientIngress(over, 1); reason != RejectRateLimited {
			t.Fatalf("session one-over reason = %q", reason)
		}
		after := authenticatedBalancesAt(fixture.store, grant, now)
		if after.roomPackets != before.roomPackets || after.roomBytes != before.roomBytes ||
			after.globalPackets != before.globalPackets || after.globalBytes != before.globalBytes {
			t.Fatalf("session block partially charged parent scopes: %#v -> %#v", before, after)
		}
		other := clients[1].dataRequest(1, nil)
		if _, reason := fixture.store.AdmitClientIngress(other, 1); reason != RejectNone {
			t.Fatalf("isolated session reason = %q", reason)
		}
	})

	t.Run("room failure leaves session and global untouched", func(t *testing.T) {
		limits := DefaultLimits()
		limits.SessionPacketRate, limits.SessionPacketBurst = 2, 2
		limits.RoomPacketRate, limits.RoomPacketBurst = 1, 1
		limits.AuthenticatedGlobalPacketRate, limits.AuthenticatedGlobalPacketBurst = 2, 2
		fixture := newRelayStoreFixture(t, limits)
		clients := fixture.addBoundRoom(t, "room", 2, 1)
		first := clients[0].dataRequest(1, nil)
		if _, reason := fixture.store.AdmitClientIngress(first, 1); reason != RejectNone {
			t.Fatalf("first ingress: %q", reason)
		}
		grant := fixture.store.bindingsByID[clients[1].bindingID]
		now := limiterTime(0)
		before := authenticatedBalancesAt(fixture.store, grant, now)
		second := clients[1].dataRequest(1, nil)
		if _, reason := fixture.store.AdmitClientIngress(second, 1); reason != RejectRateLimited {
			t.Fatalf("room one-over reason = %q", reason)
		}
		after := authenticatedBalancesAt(fixture.store, grant, now)
		if after.sessionPackets != before.sessionPackets || after.sessionBytes != before.sessionBytes ||
			after.globalPackets != before.globalPackets || after.globalBytes != before.globalBytes {
			t.Fatalf("room block partially charged sibling scopes: %#v -> %#v", before, after)
		}
	})

	t.Run("global failure leaves session and room untouched", func(t *testing.T) {
		limits := DefaultLimits()
		limits.SessionPacketRate, limits.SessionPacketBurst = 2, 2
		limits.RoomPacketRate, limits.RoomPacketBurst = 2, 2
		limits.AuthenticatedGlobalPacketRate, limits.AuthenticatedGlobalPacketBurst = 1, 1
		fixture := newRelayStoreFixture(t, limits)
		first := fixture.addBoundRoom(t, "room-a", 1, 1)[0]
		second := fixture.addBoundRoom(t, "room-b", 1, 2)[0]
		request := first.dataRequest(1, nil)
		if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectNone {
			t.Fatalf("first ingress: %q", reason)
		}
		grant := fixture.store.bindingsByID[second.bindingID]
		now := limiterTime(0)
		before := authenticatedBalancesAt(fixture.store, grant, now)
		request = second.dataRequest(1, nil)
		if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectRateLimited {
			t.Fatalf("global one-over reason = %q", reason)
		}
		after := authenticatedBalancesAt(fixture.store, grant, now)
		if after.sessionPackets != before.sessionPackets || after.sessionBytes != before.sessionBytes ||
			after.roomPackets != before.roomPackets || after.roomBytes != before.roomBytes {
			t.Fatalf("global block partially charged child scopes: %#v -> %#v", before, after)
		}
	})
}

func TestIngressChargesObservedBytesIncludingSaturatedRead(t *testing.T) {
	for _, tt := range []struct {
		name string
		cost int
		want RejectReason
	}{
		{"exact 1201", 1_201, RejectNone},
		{"one over configured bytes", 1_202, RejectRateLimited},
	} {
		t.Run(tt.name, func(t *testing.T) {
			limits := DefaultLimits()
			limits.SessionByteRate, limits.SessionByteBurst = 1_201, 1_201
			fixture := newRelayStoreFixture(t, limits)
			client := fixture.addBoundRoom(t, "room", 1, 1)[0]
			request := client.dataRequest(1, nil)
			if _, reason := fixture.store.AdmitClientIngress(request, tt.cost); reason != tt.want {
				t.Fatalf("reason = %q, want %q", reason, tt.want)
			}
		})
	}
}

func TestAdmittedValueIsOpaqueAndFanoutSnapshotsAuthoritativeRecipients(t *testing.T) {
	typeOf := reflect.TypeOf(AdmittedClientData{})
	for index := range typeOf.NumField() {
		field := typeOf.Field(index)
		if field.PkgPath == "" {
			t.Fatalf("AdmittedClientData exposes field %s", field.Name)
		}
		if field.Type == reflect.TypeOf((*grantRecord)(nil)) || field.Type == reflect.TypeOf(protocol.Bytes32{}) || field.Type.Kind() == reflect.Bool {
			t.Fatalf("AdmittedClientData contains forgeable/secret field %s %v", field.Name, field.Type)
		}
	}

	fixture := newRelayStoreFixture(t, DefaultLimits())
	clients := fixture.addBoundRoom(t, "room", 5, 1)
	_ = fixture.addBoundRoom(t, "other-room", 1, 2)
	fixture.store.clearBinding(fixture.store.bindingsByID[clients[2].bindingID])
	fixture.store.terminalGrant(fixture.store.bindingsByID[clients[3].bindingID], GrantStateExpired)
	fixture.store.terminalGrant(fixture.store.bindingsByID[clients[4].bindingID], GrantStateRevoked)

	request := clients[0].dataRequest(7, []byte("opaque"))
	admitted, reason := fixture.store.AdmitClientIngress(request, 100)
	if reason != RejectNone {
		t.Fatalf("AdmitClientIngress(): %q", reason)
	}
	plan, reason := fixture.store.AdmitFanout(admitted, 111)
	if reason != RejectNone {
		t.Fatalf("AdmitFanout(): %q", reason)
	}
	if plan.RoomID != "room" || plan.SessionID != clients[0].sessionID ||
		plan.SenderParticipantID != clients[0].participantID || plan.Sequence != 7 ||
		!reflect.DeepEqual(plan.Recipients, []netip.AddrPort{clients[1].endpoint}) {
		t.Fatalf("relay plan = %#v", plan)
	}
	plan.Recipients[0] = netip.MustParseAddrPort("203.0.113.250:9999")
	second, reason := fixture.store.AdmitFanout(admitted, 111)
	if reason != RejectNone || !reflect.DeepEqual(second.Recipients, []netip.AddrPort{clients[1].endpoint}) {
		t.Fatalf("recipient snapshot shared mutable state: (%#v, %q)", second, reason)
	}
	if _, reason := fixture.store.AdmitFanout(AdmittedClientData{}, 1); reason != RejectNotBound {
		t.Fatalf("zero admitted value reason = %q", reason)
	}
	other := newRelayStoreFixture(t, DefaultLimits())
	if _, reason := other.store.AdmitFanout(admitted, 1); reason != RejectNotBound {
		t.Fatalf("cross-store admitted value reason = %q", reason)
	}
}

func TestFanoutAtomicCostsAndNoIngressRefund(t *testing.T) {
	t.Run("room rejection charges neither room nor global", func(t *testing.T) {
		limits := DefaultLimits()
		limits.RoomFanoutWriteRate, limits.RoomFanoutWriteBurst = 1, 1
		limits.GlobalFanoutWriteRate, limits.GlobalFanoutWriteBurst = 2, 2
		fixture := newRelayStoreFixture(t, limits)
		clients := fixture.addBoundRoom(t, "room", 2, 1)
		request := clients[0].dataRequest(1, nil)
		admitted, _ := fixture.store.AdmitClientIngress(request, 1)
		if _, reason := fixture.store.AdmitFanout(admitted, 1); reason != RejectNone {
			t.Fatalf("first fanout: %q", reason)
		}
		now := limiterTime(0)
		before := fanoutBalancesAt(fixture.store, fixture.store.roomsByID["room"], now)
		if _, reason := fixture.store.AdmitFanout(admitted, 1); reason != RejectFanoutLimited {
			t.Fatalf("room one-over reason = %q", reason)
		}
		if after := fanoutBalancesAt(fixture.store, fixture.store.roomsByID["room"], now); after != before {
			t.Fatalf("fanout rejection partially charged: %#v -> %#v", before, after)
		}
	})

	t.Run("global rejection leaves room untouched", func(t *testing.T) {
		limits := DefaultLimits()
		limits.RoomFanoutWriteRate, limits.RoomFanoutWriteBurst = 2, 2
		limits.GlobalFanoutWriteRate, limits.GlobalFanoutWriteBurst = 1, 1
		fixture := newRelayStoreFixture(t, limits)
		a := fixture.addBoundRoom(t, "room-a", 2, 1)
		b := fixture.addBoundRoom(t, "room-b", 2, 2)
		request := a[0].dataRequest(1, nil)
		admitted, _ := fixture.store.AdmitClientIngress(request, 1)
		if _, reason := fixture.store.AdmitFanout(admitted, 1); reason != RejectNone {
			t.Fatalf("first fanout: %q", reason)
		}
		request = b[0].dataRequest(1, nil)
		admitted, _ = fixture.store.AdmitClientIngress(request, 1)
		now := limiterTime(0)
		before := fanoutBalancesAt(fixture.store, fixture.store.roomsByID["room-b"], now)
		if _, reason := fixture.store.AdmitFanout(admitted, 1); reason != RejectFanoutLimited {
			t.Fatalf("global one-over reason = %q", reason)
		}
		if after := fanoutBalancesAt(fixture.store, fixture.store.roomsByID["room-b"], now); after != before {
			t.Fatalf("global fanout rejection partially charged room: %#v -> %#v", before, after)
		}
	})

	t.Run("exact output bytes times recipient count", func(t *testing.T) {
		for _, tt := range []struct {
			name        string
			outputBytes int
			want        RejectReason
		}{
			{"equality", 100, RejectNone},
			{"one over", 101, RejectFanoutLimited},
		} {
			t.Run(tt.name, func(t *testing.T) {
				limits := DefaultLimits()
				limits.RoomFanoutByteRate, limits.RoomFanoutByteBurst = 200, 200
				limits.GlobalFanoutByteRate, limits.GlobalFanoutByteBurst = 200, 200
				fixture := newRelayStoreFixture(t, limits)
				clients := fixture.addBoundRoom(t, "room", 3, 1)
				request := clients[0].dataRequest(1, nil)
				admitted, _ := fixture.store.AdmitClientIngress(request, 1)
				if _, reason := fixture.store.AdmitFanout(admitted, tt.outputBytes); reason != tt.want {
					t.Fatalf("reason = %q, want %q", reason, tt.want)
				}
			})
		}
	})

	t.Run("output and fanout rejection never refund ingress", func(t *testing.T) {
		limits := DefaultLimits()
		limits.SessionPacketRate, limits.SessionPacketBurst = 1, 1
		limits.RoomFanoutWriteRate, limits.RoomFanoutWriteBurst = 1, 1
		fixture := newRelayStoreFixture(t, limits)
		clients := fixture.addBoundRoom(t, "room", 3, 1)
		request := clients[0].dataRequest(1, nil)
		admitted, _ := fixture.store.AdmitClientIngress(request, 1)
		if _, reason := fixture.store.AdmitFanout(admitted, 1); reason != RejectFanoutLimited {
			t.Fatalf("fanout reason = %q", reason)
		}
		request = clients[0].dataRequest(2, nil)
		if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectRateLimited {
			t.Fatalf("fanout failure refunded ingress: %q", reason)
		}
	})
}

func TestOutputAndFanoutRejectionKeepFreshReplaySpent(t *testing.T) {
	for _, tt := range []struct {
		name        string
		setup       func(*Limits)
		fanoutBytes int
		want        RejectReason
	}{
		{"output", func(*Limits) {}, protocol.MaxDatagramBytes + 1, RejectOversized},
		{"fanout", func(l *Limits) { l.RoomFanoutWriteRate, l.RoomFanoutWriteBurst = 1, 1 }, 1, RejectFanoutLimited},
	} {
		t.Run(tt.name, func(t *testing.T) {
			limits := DefaultLimits()
			tt.setup(&limits)
			fixture := newRelayStoreFixture(t, limits)
			clients := fixture.addBoundRoom(t, "room", 3, 1)
			request := clients[0].dataRequest(1, nil)
			admitted, reason := fixture.store.AdmitClientIngress(request, 1)
			if reason != RejectNone {
				t.Fatalf("AdmitClientIngress(): %q", reason)
			}
			if _, reason := fixture.store.AdmitFanout(admitted, tt.fanoutBytes); reason != tt.want {
				t.Fatalf("AdmitFanout() reason = %q, want %q", reason, tt.want)
			}
			if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectReplay {
				t.Fatalf("retry after %s rejection reason = %q, want replay", tt.name, reason)
			}
		})
	}
}

func TestFanoutRejectsInvalidCostsAndStaleAdmissionWithoutCharge(t *testing.T) {
	fixture := newRelayStoreFixture(t, DefaultLimits())
	clients := fixture.addBoundRoom(t, "room", 3, 1)
	request := clients[0].dataRequest(1, nil)
	admitted, _ := fixture.store.AdmitClientIngress(request, 1)
	now := limiterTime(0)
	room := fixture.store.roomsByID["room"]
	for _, outputBytes := range []int{-1, protocol.MaxDatagramBytes + 1, math.MaxInt} {
		before := fanoutBalancesAt(fixture.store, room, now)
		if _, reason := fixture.store.AdmitFanout(admitted, outputBytes); reason != RejectOversized {
			t.Fatalf("AdmitFanout(%d) reason = %q, want oversized", outputBytes, reason)
		}
		if after := fanoutBalancesAt(fixture.store, room, now); after != before {
			t.Fatalf("invalid output cost %d charged fanout: %#v -> %#v", outputBytes, before, after)
		}
	}

	fixture.rebind(t, &clients[0], netip.MustParseAddrPort("198.18.9.9:4999"))
	before := fanoutBalancesAt(fixture.store, room, now)
	if _, reason := fixture.store.AdmitFanout(admitted, 1); reason != RejectNotBound {
		t.Fatalf("generation-stale admitted value reason = %q", reason)
	}
	if after := fanoutBalancesAt(fixture.store, room, now); after != before {
		t.Fatalf("stale admitted value charged fanout: %#v -> %#v", before, after)
	}
}

func TestFanoutRechecksExactBindingDeadlineAfterMarshal(t *testing.T) {
	fixture := newRelayStoreFixture(t, DefaultLimits())
	clients := fixture.addBoundRoom(t, "room", 2, 1)
	request := clients[0].dataRequest(1, nil)
	admitted, reason := fixture.store.AdmitClientIngress(request, 1)
	if reason != RejectNone {
		t.Fatalf("AdmitClientIngress(): %q", reason)
	}
	fixture.setMono(HardMaxBindingTTL)
	now := limiterTime(HardMaxBindingTTL)
	room := fixture.store.roomsByID["room"]
	before := fanoutBalancesAt(fixture.store, room, now)
	if _, reason := fixture.store.AdmitFanout(admitted, 1); reason != RejectExpired {
		t.Fatalf("AdmitFanout(exact binding deadline) reason = %q", reason)
	}
	if after := fanoutBalancesAt(fixture.store, room, now); after != before {
		t.Fatalf("expired fanout charged budget: %#v -> %#v", before, after)
	}
}

func TestFanoutRejectsAdmittedValueAfterDeleteRoomAndGrantExpiry(t *testing.T) {
	for _, target := range []string{"delete", "room expiry", "grant expiry"} {
		t.Run(target, func(t *testing.T) {
			fixture := newRelayStoreFixture(t, DefaultLimits())
			clients := fixture.addBoundRoom(t, "room", 2, 1)
			request := clients[0].dataRequest(1, nil)
			admitted, reason := fixture.store.AdmitClientIngress(request, 1)
			if reason != RejectNone {
				t.Fatalf("AdmitClientIngress(): %q", reason)
			}
			room := fixture.store.roomsByID["room"]
			grant := fixture.store.bindingsByID[clients[0].bindingID]
			want := RejectExpired
			switch target {
			case "delete":
				if err := fixture.store.EndRoom("room"); err != nil {
					t.Fatalf("EndRoom(): %v", err)
				}
				want = RejectNotBound
			case "room expiry":
				room.monoDeadline = time.Second
				fixture.setMono(time.Second)
			case "grant expiry":
				grant.monoDeadline = time.Second
				fixture.setMono(time.Second)
			}
			if _, reason := fixture.store.AdmitFanout(admitted, 1); reason != want {
				t.Fatalf("AdmitFanout(after %s) reason = %q, want %q", target, reason, want)
			}
		})
	}
}

func TestEmptyFanoutAndRoomIsolationUnderConcurrentTraffic(t *testing.T) {
	limits := DefaultLimits()
	limits.RoomPacketRate, limits.RoomPacketBurst = 1, 1
	fixture := newRelayStoreFixture(t, limits)
	a := fixture.addBoundRoom(t, "room-a", 1, 1)[0]
	b := fixture.addBoundRoom(t, "room-b", 1, 2)[0]
	type result struct {
		plan   RelayPlan
		reason RejectReason
	}
	results := make(chan result, 2)
	fanoutBefore := fanoutBalancesAt(fixture.store, fixture.store.roomsByID["room-a"], limiterTime(0))
	for _, client := range []boundTestClient{a, b} {
		client := client
		go func() {
			request := client.dataRequest(1, nil)
			admitted, reason := fixture.store.AdmitClientIngress(request, 1)
			if reason != RejectNone {
				results <- result{reason: reason}
				return
			}
			plan, reason := fixture.store.AdmitFanout(admitted, 1)
			results <- result{plan: plan, reason: reason}
		}()
	}
	for range 2 {
		got := <-results
		if got.reason != RejectNone || len(got.plan.Recipients) != 0 {
			t.Fatalf("concurrent isolated relay = (%#v, %q)", got.plan, got.reason)
		}
	}
	if fanoutAfter := fanoutBalancesAt(fixture.store, fixture.store.roomsByID["room-a"], limiterTime(0)); fanoutAfter != fanoutBefore {
		t.Fatalf("empty recipient plans charged fanout: %#v -> %#v", fanoutBefore, fanoutAfter)
	}
	request := a.dataRequest(2, nil)
	if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectRateLimited {
		t.Fatalf("room-a one-over reason = %q", reason)
	}
}

func TestSessionLimiterSurvivesRebind(t *testing.T) {
	limits := DefaultLimits()
	limits.SessionPacketRate, limits.SessionPacketBurst = 1, 1
	fixture := newRelayStoreFixture(t, limits)
	client := fixture.addBoundRoom(t, "room", 1, 1)[0]
	grant := fixture.store.bindingsByID[client.bindingID]
	limiter := grant.ingressPackets
	request := client.dataRequest(1, nil)
	if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectNone {
		t.Fatalf("first ingress: %q", reason)
	}
	fixture.rebind(t, &client, netip.MustParseAddrPort("198.18.1.2:4500"))
	if grant.ingressPackets != limiter {
		t.Fatal("rebind replaced the session limiter")
	}
	request = client.dataRequest(1, nil)
	if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectRateLimited {
		t.Fatalf("rebind reset the session burst: %q", reason)
	}
	fixture.setMono(time.Second)
	if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectReplay {
		t.Fatalf("rate-rejected new-binding sequence was not consumed: %q", reason)
	}
}

func TestPreauthSourceKeysAndAtomicAdmission(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	v4a := netip.MustParseAddrPort("192.0.2.1:1000")
	v4b := netip.MustParseAddrPort("192.0.2.1:2000")
	v6a := netip.MustParseAddrPort("[2001:db8:1:2::1]:1000")
	v6b := netip.MustParseAddrPort("[2001:db8:1:2:ffff::2]:2000")
	if sourceKey(v4a) != sourceKey(v4b) {
		t.Fatal("IPv4 source key included the port")
	}
	if sourceKey(v6a) != sourceKey(v6b) {
		t.Fatal("IPv6 source key was not a /64 with port excluded")
	}
	if sourceKey(v4a).Bits() != 32 || sourceKey(v6a).Bits() != 64 {
		t.Fatalf("source key widths = %d/%d, want 32/64", sourceKey(v4a).Bits(), sourceKey(v6a).Bits())
	}

	for range fixture.limits.PreauthSourcePacketBurst {
		if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: v4a, InputBytes: 1}); reason != RejectNone {
			t.Fatalf("burst admission reason = %q", reason)
		}
	}
	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(time.Nanosecond), Mono: time.Nanosecond}
	now := limiterTime(fixture.fakeClock.reading.Mono)
	globalBefore := fixture.store.preauthGlobalPackets.TokensAt(now)
	record := fixture.store.preauthSources[sourceKey(v4a)]
	if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: v4a, InputBytes: 1}); reason != RejectRateLimited {
		t.Fatalf("one-over source burst reason = %q", reason)
	}
	if got := fixture.store.preauthGlobalPackets.TokensAt(now); got != globalBefore {
		t.Fatalf("source rejection partially consumed global tokens: %v -> %v", globalBefore, got)
	}
	if fixture.store.preauthSources[sourceKey(v4a)] != record || record.lastObserved != fixture.fakeClock.reading.Mono {
		t.Fatal("rate-limited existing source was recreated or not refreshed")
	}
}

func TestPreauthGlobalBoundaryDoesNotPartiallyCommitSource(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	for index := range fixture.limits.PreauthGlobalPacketBurst {
		endpoint := netip.AddrPortFrom(netip.AddrFrom4([4]byte{172, 16, byte(index >> 8), byte(index)}), 1000)
		if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: endpoint, InputBytes: 1}); reason != RejectNone {
			t.Fatalf("global burst packet %d reason = %q", index, reason)
		}
	}
	newEndpoint := netip.MustParseAddrPort("172.17.0.1:1000")
	if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: newEndpoint, InputBytes: 1}); reason != RejectRateLimited {
		t.Fatalf("one-over global burst reason = %q", reason)
	}
	if fixture.store.preauthSources[sourceKey(newEndpoint)] != nil || len(fixture.store.preauthSources) != fixture.limits.PreauthGlobalPacketBurst {
		t.Fatal("global rejection partially committed a new source record")
	}
}

func TestPreauthByteBoundariesAreAtomicAcrossAllFourLimiters(t *testing.T) {
	t.Run("source byte", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			finalCost int
			want      RejectReason
		}{
			{name: "equality", finalCost: 1_041, want: RejectNone},
			{name: "one over", finalCost: 1_042, want: RejectRateLimited},
		} {
			t.Run(tt.name, func(t *testing.T) {
				fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
				endpoint := netip.MustParseAddrPort("192.0.2.80:8000")
				for index := range 159 {
					if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: endpoint, InputBytes: 1_201}); reason != RejectNone {
						t.Fatalf("setup packet %d reason = %q", index, reason)
					}
				}
				now := limiterTime(fixture.fakeClock.reading.Mono)
				source := fixture.store.preauthSources[sourceKey(endpoint)]
				before := preauthBalancesAt(fixture.store, source, now)
				if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: endpoint, InputBytes: tt.finalCost}); reason != tt.want {
					t.Fatalf("final admission reason = %q, want %q", reason, tt.want)
				}
				after := preauthBalancesAt(fixture.store, source, now)
				if tt.want == RejectRateLimited {
					if after != before {
						t.Fatalf("source-byte block partially consumed: before=%#v after=%#v", before, after)
					}
				} else if after != (preauthBalances{before.sourcePackets - 1, before.sourceBytes - float64(tt.finalCost), before.globalPackets - 1, before.globalBytes - float64(tt.finalCost)}) {
					t.Fatalf("source-byte equality balances: before=%#v after=%#v", before, after)
				}
			})
		}
	})

	t.Run("global byte", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			finalCost int
			want      RejectReason
		}{
			{name: "equality", finalCost: 1_122, want: RejectNone},
			{name: "one over", finalCost: 1_123, want: RejectRateLimited},
		} {
			t.Run(tt.name, func(t *testing.T) {
				fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
				var target netip.AddrPort
				for index := range 1_278 {
					endpoint := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 20, byte(index >> 8), byte(index)}), 8000)
					if index == 0 {
						target = endpoint
					}
					if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: endpoint, InputBytes: 1_201}); reason != RejectNone {
						t.Fatalf("setup packet %d reason = %q", index, reason)
					}
				}
				now := limiterTime(fixture.fakeClock.reading.Mono)
				source := fixture.store.preauthSources[sourceKey(target)]
				before := preauthBalancesAt(fixture.store, source, now)
				if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: target, InputBytes: tt.finalCost}); reason != tt.want {
					t.Fatalf("final admission reason = %q, want %q", reason, tt.want)
				}
				after := preauthBalancesAt(fixture.store, source, now)
				if tt.want == RejectRateLimited {
					if after != before {
						t.Fatalf("global-byte block partially consumed: before=%#v after=%#v", before, after)
					}
				} else if after != (preauthBalances{before.sourcePackets - 1, before.sourceBytes - float64(tt.finalCost), before.globalPackets - 1, before.globalBytes - float64(tt.finalCost)}) {
					t.Fatalf("global-byte equality balances: before=%#v after=%#v", before, after)
				}
			})
		}
	})
}

func TestHandshakeDeadlinesSaturateNearMaxMonotonicTime(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	maxDeadline := time.Duration(math.MaxInt64)
	now := maxDeadline - 2*time.Second
	fixture.store.roomsByID["room"].monoDeadline = maxDeadline
	fixture.grant(0).monoDeadline = maxDeadline
	fixture.fakeClock.reading = clock.Reading{Wall: testWall, Mono: now}
	endpoint := netip.MustParseAddrPort("192.0.2.90:9000")
	nonce := bytes16(0xf9)
	fixture.random.reset(filled(0xfa, 16), filled(0xfb, 32), filled(0xfc, 16))
	challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
	if reason != RejectNone || fixture.grant(0).pending == nil || fixture.grant(0).pending.deadline != maxDeadline ||
		challenge.ExpiresUnixMS != testWall.Add(2*time.Second).UnixMilli() {
		t.Fatalf("near-max challenge = (%#v, %q, %#v)", challenge, reason, fixture.grant(0).pending)
	}
	bound, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, nonce, endpoint))
	if reason != RejectNone || fixture.grant(0).binding == nil || fixture.grant(0).binding.deadline != maxDeadline ||
		fixture.grant(0).recent == nil || fixture.grant(0).recent.deadline != maxDeadline ||
		bound.ExpiresUnixMS != testWall.Add(2*time.Second).UnixMilli() {
		t.Fatalf("near-max bound = (%#v, %q, binding=%#v recent=%#v)", bound, reason, fixture.grant(0).binding, fixture.grant(0).recent)
	}
}

func TestPreauthIdleBoundaryLazilyReplacesOnlyAtExactDeadline(t *testing.T) {
	for _, tt := range []struct {
		name     string
		at       time.Duration
		wantSame bool
	}{
		{name: "before", at: 60*time.Second - time.Nanosecond, wantSame: true},
		{name: "exact", at: 60 * time.Second, wantSame: false},
		{name: "after", at: 60*time.Second + time.Nanosecond, wantSame: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
			endpoint := netip.MustParseAddrPort("192.0.2.50:7000")
			if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: endpoint, InputBytes: 1}); reason != RejectNone {
				t.Fatalf("initial admission = %q", reason)
			}
			old := fixture.store.preauthSources[sourceKey(endpoint)]
			fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(tt.at), Mono: tt.at}
			if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: endpoint, InputBytes: 1}); reason != RejectNone {
				t.Fatalf("boundary admission = %q", reason)
			}
			current := fixture.store.preauthSources[sourceKey(endpoint)]
			if (current == old) != tt.wantSame || current.lastObserved != tt.at {
				t.Fatalf("source identity/observation = same:%t last:%v", current == old, current.lastObserved)
			}
		})
	}
}

func TestPreauthRejectedHelloRefreshesExistingSource(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	endpoint := netip.MustParseAddrPort("192.0.2.60:7000")
	request := fixture.challengeRequest(0, bytes16(0xe1), endpoint)
	request.GrantID = bytes16(0xff)
	if _, reason := fixture.store.BeginChallenge(request); reason != RejectUnknownGrant {
		t.Fatalf("unknown grant reason = %q", reason)
	}
	record := fixture.store.preauthSources[sourceKey(endpoint)]
	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(time.Second), Mono: time.Second}
	if _, reason := fixture.store.BeginChallenge(request); reason != RejectUnknownGrant {
		t.Fatalf("second unknown grant reason = %q", reason)
	}
	if fixture.store.preauthSources[sourceKey(endpoint)] != record || record.lastObserved != time.Second {
		t.Fatal("rejected HELLO did not refresh the existing record")
	}
}

func TestExistingSourceRejectedAndRateLimitedObservationDoesNotResetLimiters(t *testing.T) {
	t.Run("admitted rejection charges once on existing limiter", func(t *testing.T) {
		fixture := newRelayStoreFixture(t, DefaultLimits())
		endpoint := netip.MustParseAddrPort("192.0.2.61:7000")
		if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: endpoint, InputBytes: 100}); reason != RejectNone {
			t.Fatalf("initial AdmitPreauth(): %q", reason)
		}
		source := fixture.store.preauthSources[sourceKey(endpoint)]
		packets, bytes := source.packets, source.bytes
		fixture.setMono(time.Second)
		now := limiterTime(time.Second)
		before := preauthBalancesAt(fixture.store, source, now)
		if _, reason := fixture.store.BeginChallenge(ChallengeRequest{
			RoomID: "room", SessionID: "session", GrantID: bytes16(0xfe), ClientNonce: bytes16(1),
			Endpoint: endpoint, InputBytes: 321,
		}); reason != RejectUnknownGrant {
			t.Fatalf("unknown HELLO reason = %q", reason)
		}
		want := preauthBalances{before.sourcePackets - 1, before.sourceBytes - 321, before.globalPackets - 1, before.globalBytes - 321}
		if after := preauthBalancesAt(fixture.store, source, now); after != want {
			t.Fatalf("rejected existing-source charge = %#v, want %#v", after, want)
		}
		if fixture.store.preauthSources[sourceKey(endpoint)] != source || source.packets != packets || source.bytes != bytes || source.lastObserved != time.Second {
			t.Fatal("rejected existing source recreated its record/limiters or missed observation refresh")
		}
	})

	t.Run("rate rejection refreshes idle deadline without partial charge or burst reset", func(t *testing.T) {
		limits := DefaultLimits()
		limits.PreauthSourcePacketRate = 0.01
		limits.PreauthSourcePacketBurst = 1
		fixture := newRelayStoreFixture(t, limits)
		endpoint := netip.MustParseAddrPort("192.0.2.62:7000")
		if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: endpoint, InputBytes: 100}); reason != RejectNone {
			t.Fatalf("initial AdmitPreauth(): %q", reason)
		}
		source := fixture.store.preauthSources[sourceKey(endpoint)]
		packets, bytes := source.packets, source.bytes
		refreshed := 30 * time.Second
		fixture.setMono(refreshed)
		now := limiterTime(refreshed)
		before := preauthBalancesAt(fixture.store, source, now)
		if _, reason := fixture.store.BeginChallenge(ChallengeRequest{
			RoomID: "room", SessionID: "session", GrantID: bytes16(0xfd), ClientNonce: bytes16(2),
			Endpoint: endpoint, InputBytes: 200,
		}); reason != RejectRateLimited {
			t.Fatalf("rate-limited HELLO reason = %q", reason)
		}
		if after := preauthBalancesAt(fixture.store, source, now); after != before {
			t.Fatalf("rate-limited existing source partially charged/reset: %#v -> %#v", before, after)
		}
		if fixture.store.preauthSources[sourceKey(endpoint)] != source || source.packets != packets || source.bytes != bytes || source.lastObserved != refreshed {
			t.Fatal("rate-limited existing source recreated limiters or missed observation refresh")
		}

		fixture.setMono(preauthSourceIdleTTL)
		fixture.store.Expire()
		if fixture.store.preauthSources[sourceKey(endpoint)] != source {
			t.Fatal("source was evicted at its original idle deadline despite refreshed observation")
		}
		fixture.setMono(refreshed + preauthSourceIdleTTL - time.Nanosecond)
		fixture.store.Expire()
		if fixture.store.preauthSources[sourceKey(endpoint)] != source {
			t.Fatal("source was evicted before refreshed idle deadline")
		}
		fixture.setMono(refreshed + preauthSourceIdleTTL)
		fixture.store.Expire()
		if fixture.store.preauthSources[sourceKey(endpoint)] != nil {
			t.Fatal("source survived exact refreshed idle deadline")
		}
	})
}

func TestPreauthFullTableUsesProcessOnlyAndCreatesNoRecord(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	for index := range HardMaxPreauthSources {
		endpoint := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(index >> 16), byte(index >> 8), byte(index)}), 1000)
		if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: endpoint, InputBytes: 1}); reason != RejectNone {
			t.Fatalf("fill source %d reason = %q", index, reason)
		}
		if index >= fixture.limits.PreauthGlobalPacketBurst-1 {
			fixture.fakeClock.reading.Mono += time.Second / time.Duration(fixture.limits.PreauthGlobalPacketRate)
			fixture.fakeClock.reading.Wall = testWall.Add(fixture.fakeClock.reading.Mono)
		}
	}
	if len(fixture.store.preauthSources) != HardMaxPreauthSources {
		t.Fatalf("source table size = %d", len(fixture.store.preauthSources))
	}
	fixture.fakeClock.reading.Mono += time.Second
	fixture.fakeClock.reading.Wall = testWall.Add(fixture.fakeClock.reading.Mono)
	newEndpoint := netip.MustParseAddrPort("11.0.0.1:1000")
	now := limiterTime(fixture.fakeClock.reading.Mono)
	packetsBefore := fixture.store.preauthGlobalPackets.TokensAt(now)
	bytesBefore := fixture.store.preauthGlobalBytes.TokensAt(now)
	if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: newEndpoint, InputBytes: 1_201}); reason != RejectRateLimited {
		t.Fatalf("full-table new source reason = %q", reason)
	}
	if len(fixture.store.preauthSources) != HardMaxPreauthSources || fixture.store.preauthSources[sourceKey(newEndpoint)] != nil {
		t.Fatal("full-table new source created state")
	}
	if got := fixture.store.preauthGlobalPackets.TokensAt(now); got != packetsBefore-1 {
		t.Fatalf("full-table process-only packet tokens = %v, want %v", got, packetsBefore-1)
	}
	if got := fixture.store.preauthGlobalBytes.TokensAt(now); got != bytesBefore-1_201 {
		t.Fatalf("full-table process-only byte tokens = %v, want %v", got, bytesBefore-1_201)
	}
	packetsBefore = fixture.store.preauthGlobalPackets.TokensAt(now)
	bytesBefore = fixture.store.preauthGlobalBytes.TokensAt(now)
	if reason := fixture.store.AdmitPreauth(PreauthRequest{
		Endpoint: netip.MustParseAddrPort("11.0.0.2:1000"), InputBytes: fixture.limits.PreauthGlobalByteBurst + 1,
	}); reason != RejectRateLimited {
		t.Fatalf("full-table process byte one-over reason = %q", reason)
	}
	if packetsAfter, bytesAfter := fixture.store.preauthGlobalPackets.TokensAt(now), fixture.store.preauthGlobalBytes.TokensAt(now); packetsAfter != packetsBefore || bytesAfter != bytesBefore {
		t.Fatalf("full-table process rejection partially charged: packets %v -> %v bytes %v -> %v",
			packetsBefore, packetsAfter, bytesBefore, bytesAfter)
	}
	existing := netip.MustParseAddrPort("10.0.0.1:2000")
	record := fixture.store.preauthSources[sourceKey(existing)]
	if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: existing, InputBytes: 1}); reason != RejectNone {
		t.Fatalf("full-table existing source reason = %q", reason)
	}
	if fixture.store.preauthSources[sourceKey(existing)] != record || len(fixture.store.preauthSources) != HardMaxPreauthSources {
		t.Fatal("full-table existing source was replaced")
	}
}

func TestExpireRemovesIdleSourcesAndBindingAtExactDeadlines(t *testing.T) {
	fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
	source := netip.MustParseAddrPort("192.0.2.71:7000")
	if reason := fixture.store.AdmitPreauth(PreauthRequest{Endpoint: source, InputBytes: 1}); reason != RejectNone {
		t.Fatalf("AdmitPreauth(): %q", reason)
	}
	endpoint := netip.MustParseAddrPort("192.0.2.72:7000")
	nonce := bytes16(0xf5)
	fixture.random.reset(filled(0xf6, 16), filled(0xf7, 32), filled(0xf8, 16))
	challenge, _ := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
	bound, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, nonce, endpoint))
	if reason != RejectNone {
		t.Fatalf("Authenticate(): %q", reason)
	}
	binding := fixture.grant(0).binding
	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(60*time.Second - time.Nanosecond), Mono: 60*time.Second - time.Nanosecond}
	fixture.store.Expire()
	if fixture.store.preauthSources[sourceKey(source)] == nil || fixture.store.bindingsByID[bound.BindingID] == nil {
		t.Fatal("relay state expired before its exact deadline")
	}
	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(60 * time.Second), Mono: 60 * time.Second}
	fixture.store.Expire()
	if fixture.store.preauthSources[sourceKey(source)] != nil || fixture.store.bindingsByID[bound.BindingID] != nil ||
		fixture.grant(0).binding != nil || binding.key != (protocol.Bytes32{}) {
		t.Fatal("relay state survived its exact deadline")
	}
	if snapshot, err := fixture.store.GetRoom("room"); err != nil || snapshot.Participants[0].GrantState != GrantStateIssued ||
		snapshot.Participants[0].BindingState != BindingStateExpired {
		t.Fatalf("binding-expired snapshot = (%#v, %v)", snapshot, err)
	}
	assertStoreInvariants(t, fixture.store)
}

func TestExpireAndEndRoomClearRelaySecretsAndIndexes(t *testing.T) {
	for _, tt := range []struct {
		name          string
		terminalState GrantState
		bindingState  BindingState
		terminate     func(*handshakeFixture) error
	}{
		{
			name:          "EndRoom",
			terminalState: GrantStateRevoked,
			bindingState:  BindingStateRevoked,
			terminate:     func(fixture *handshakeFixture) error { return fixture.store.EndRoom("room") },
		},
		{
			name:          "Expire",
			terminalState: GrantStateExpired,
			bindingState:  BindingStateExpired,
			terminate: func(fixture *handshakeFixture) error {
				fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(time.Second), Mono: time.Second}
				fixture.store.roomsByID["room"].monoDeadline = time.Second
				fixture.store.Expire()
				return nil
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHandshakeFixture(t, time.Hour, 30*time.Minute, 1)
			endpoint := netip.MustParseAddrPort("192.0.2.70:7000")
			nonce := bytes16(0xf1)
			fixture.random.reset(filled(0xf2, 16), filled(0xf3, 32), filled(0xf4, 16))
			challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
			if reason != RejectNone {
				t.Fatalf("BeginChallenge(): %q", reason)
			}
			bound, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, nonce, endpoint))
			if reason != RejectNone {
				t.Fatalf("Authenticate(): %q", reason)
			}

			grant := fixture.grant(0)
			binding := grant.binding
			request := ClientDataRequest{
				RoomID: "room", SessionID: fixture.session(0), BindingID: bound.BindingID, Sequence: 42,
				Endpoint: endpoint, Payload: []byte("replay state"),
			}
			request.AuthTag = protocol.ClientDataTag(binding.key, protocol.Revision, request.RoomID, request.SessionID,
				request.BindingID, request.Sequence, request.Payload)
			if _, reason := fixture.store.AdmitClientIngress(request, 123); reason != RejectNone {
				t.Fatalf("AdmitClientIngress(): %q", reason)
			}

			fixture.random.reset(filled(0xa2, 16), filled(0xa3, 32))
			pending, reason := fixture.store.BeginChallenge(fixture.challengeRequest(
				0, bytes16(0xa1), netip.MustParseAddrPort("192.0.2.73:7000"),
			))
			if reason != RejectNone {
				t.Fatalf("pending rebind BeginChallenge(): %q", reason)
			}

			room := fixture.store.roomsByID["room"]
			secret := grant.secret
			recent := grant.recent
			pendingRecord := grant.pending
			if secret == nil || *secret == (protocol.Bytes32{}) || binding.id == (protocol.Bytes16{}) ||
				binding.key == (protocol.Bytes32{}) || !binding.endpoint.IsValid() || binding.replay == (replayWindow{}) ||
				recent == nil || pendingRecord == nil || grant.ingressPackets == nil || grant.ingressBytes == nil ||
				room.ingressPackets == nil || room.ingressBytes == nil || room.fanoutWrites == nil || room.fanoutBytes == nil {
				t.Fatal("terminal cleanup fixture did not contain every relay-owned field")
			}
			grantID, bindingID := grant.id, binding.id
			recentID, pendingID := recent.candidateID, pendingRecord.candidateID

			if err := tt.terminate(fixture); err != nil {
				t.Fatalf("%s(): %v", tt.name, err)
			}
			if grant.state != tt.terminalState || grant.bindingState != tt.bindingState ||
				grant.secret != nil || grant.binding != nil || grant.pending != nil || grant.recent != nil ||
				grant.generation != 0 || grant.ingressPackets != nil || grant.ingressBytes != nil ||
				*secret != (protocol.Bytes32{}) {
				t.Fatalf("terminal grant retained ownership/state: %#v secret=%x", grant, *secret)
			}
			if binding.id != (protocol.Bytes16{}) || binding.key != (protocol.Bytes32{}) || binding.endpoint.IsValid() ||
				binding.generation != 0 || binding.replay != (replayWindow{}) {
				t.Fatalf("terminal binding retained authority: %#v", binding)
			}
			assertChallengeRecordZero(t, "pending", pendingRecord)
			assertCompletedHandshakeZero(t, recent)
			if fixture.store.grantsByID[grantID] != nil || fixture.store.bindingsByID[bindingID] != nil ||
				fixture.store.candidatesByID[recentID] != nil || fixture.store.candidatesByID[pendingID] != nil ||
				len(fixture.store.grantsByID) != 0 || len(fixture.store.bindingsByID) != 0 || len(fixture.store.candidatesByID) != 0 {
				t.Fatalf("terminal indexes retained grant=%x binding=%x recent=%x pending=%x", grantID, bindingID, recentID, pending.CandidateID)
			}
			assertTombstoneOnly(t, fixture.store, "room", fixture.fakeClock.reading.Mono+fixture.limits.TombstoneTTL)
			assertStoreInvariants(t, fixture.store)
		})
	}
}

func TestEndRoomClassifiesRetiredRelayCredentialsWithoutResurrection(t *testing.T) {
	fixture := newRelayStoreFixture(t, DefaultLimits())
	clients := fixture.addBoundRoom(t, "room", 2, 1)
	client := clients[0]
	grant := fixture.store.bindingsByID[client.bindingID]
	binding := grant.binding
	secret := grant.secret

	pendingEndpoint := netip.MustParseAddrPort("198.51.100.22:4022")
	pendingNonce := bytes16(0x72)
	pending, reason := fixture.store.BeginChallenge(ChallengeRequest{
		RoomID: "room", SessionID: client.sessionID, GrantID: client.grantID,
		ClientNonce: pendingNonce, Endpoint: pendingEndpoint, InputBytes: 300,
	})
	if reason != RejectNone {
		t.Fatalf("BeginChallenge(pending): %q", reason)
	}
	pendingRecord := grant.pending
	pendingAuth := AuthenticateRequest{
		RoomID: "room", SessionID: client.sessionID, CandidateID: pending.CandidateID,
		Endpoint: pendingEndpoint, InputBytes: 100,
		AuthTag: protocol.AuthTag(client.secret, protocol.Revision, "room", client.sessionID,
			client.grantID, pending.CandidateID, pendingNonce, pending.ServerNonce),
	}
	admitted, reason := fixture.store.AdmitClientIngress(client.dataRequest(1, []byte("admitted-before-delete")), 123)
	if reason != RejectNone {
		t.Fatalf("AdmitClientIngress(): %q", reason)
	}
	if err := fixture.store.EndRoom("room"); err != nil {
		t.Fatalf("EndRoom(): %v", err)
	}

	assertPreauthCharge := func(name string, endpoint netip.AddrPort, inputBytes int, want RejectReason, call func() RejectReason) {
		t.Helper()
		now := limiterTime(fixture.fakeClock.reading.Mono)
		source := fixture.store.preauthSources[sourceKey(endpoint)]
		if source == nil {
			t.Fatalf("%s source record missing", name)
		}
		before := preauthBalancesAt(fixture.store, source, now)
		if got := call(); got != want {
			t.Fatalf("%s reason = %q, want %q", name, got, want)
		}
		after := preauthBalancesAt(fixture.store, source, now)
		wantAfter := preauthBalances{
			before.sourcePackets - 1, before.sourceBytes - float64(inputBytes),
			before.globalPackets - 1, before.globalBytes - float64(inputBytes),
		}
		if after != wantAfter {
			t.Fatalf("%s preauth charge = %#v -> %#v, want %#v", name, before, after, wantAfter)
		}
	}

	assertPreauthCharge("stale HELLO", client.endpoint, 300, RejectUnknownGrant, func() RejectReason {
		result, reason := fixture.store.BeginChallenge(ChallengeRequest{
			RoomID: "room", SessionID: client.sessionID, GrantID: client.grantID,
			ClientNonce: bytes16(0x73), Endpoint: client.endpoint, InputBytes: 300,
		})
		if result != (ChallengeResult{}) {
			t.Fatalf("stale HELLO returned %#v", result)
		}
		return reason
	})
	assertPreauthCharge("stale AUTH", pendingEndpoint, pendingAuth.InputBytes, RejectAuthFailed, func() RejectReason {
		result, reason := fixture.store.Authenticate(pendingAuth)
		if result != (BoundResult{}) {
			t.Fatalf("stale AUTH returned %#v", result)
		}
		return reason
	})
	assertPreauthCharge("stale ClientData", client.endpoint, 111, RejectNotBound, func() RejectReason {
		result, reason := fixture.store.AdmitClientIngress(client.dataRequest(2, nil), 111)
		if result != (AdmittedClientData{}) {
			t.Fatalf("stale ClientData returned %#v", result)
		}
		return reason
	})
	assertPreauthCharge("stale Ping", client.endpoint, 99, RejectNotBound, func() RejectReason {
		return fixture.store.AdmitPing(client.pingRequest(3), 99)
	})
	if plan, reason := fixture.store.AdmitFanout(admitted, 1); reason != RejectNotBound ||
		plan.RoomID != "" || plan.SessionID != "" || plan.SenderParticipantID != "" ||
		plan.Sequence != 0 || len(plan.Recipients) != 0 {
		t.Fatalf("AdmitFanout(pre-delete admission) = (%#v, %q), want empty/not_bound", plan, reason)
	}

	if grant.secret != nil || *secret != (protocol.Bytes32{}) || grant.binding != nil || grant.pending != nil ||
		binding.key != (protocol.Bytes32{}) || binding.endpoint.IsValid() {
		t.Fatalf("retired credentials regained authority: grant=%#v binding=%#v", grant, binding)
	}
	assertChallengeRecordZero(t, "pending", pendingRecord)
	if len(fixture.store.grantsByID) != 0 || len(fixture.store.candidatesByID) != 0 || len(fixture.store.bindingsByID) != 0 {
		t.Fatal("stale traffic resurrected a credential index")
	}
	assertTombstoneOnly(t, fixture.store, "room", fixture.store.limits.TombstoneTTL)
	assertStoreInvariants(t, fixture.store)
}

func TestRelayAuthorityEndsAtExactRoomGrantAndBindingDeadlines(t *testing.T) {
	for _, tt := range []struct {
		name     string
		roomTTL  time.Duration
		grantTTL time.Duration
	}{
		{"room", 2 * time.Second, 2 * time.Second},
		{"grant", time.Hour, 2 * time.Second},
		{"binding", time.Hour, 30 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newHandshakeFixture(t, tt.roomTTL, tt.grantTTL, 1)
			if tt.name == "binding" {
				fixture.store.limits.BindingTTL = 2 * time.Second
			}
			endpoint := netip.MustParseAddrPort("192.0.2.74:7000")
			nonce := bytes16(0xb1)
			fixture.random.reset(filled(0xb2, 16), filled(0xb3, 32), filled(0xb4, 16))
			challenge, reason := fixture.store.BeginChallenge(fixture.challengeRequest(0, nonce, endpoint))
			if reason != RejectNone {
				t.Fatalf("BeginChallenge(): %q", reason)
			}
			bound, reason := fixture.store.Authenticate(fixture.authRequest(0, challenge, nonce, endpoint))
			if reason != RejectNone {
				t.Fatalf("Authenticate(): %q", reason)
			}
			key := fixture.grant(0).binding.key
			request := ClientDataRequest{
				RoomID: "room", SessionID: fixture.session(0), BindingID: bound.BindingID, Sequence: 1,
				Endpoint: endpoint,
			}
			request.AuthTag = protocol.ClientDataTag(key, protocol.Revision, request.RoomID, request.SessionID,
				request.BindingID, request.Sequence, request.Payload)
			fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(2 * time.Second), Mono: 2 * time.Second}
			if _, reason := fixture.store.AdmitClientIngress(request, 1); reason != RejectExpired {
				t.Fatalf("AdmitClientIngress(exact %s deadline) reason = %q", tt.name, reason)
			}
		})
	}
}

type preauthBalances struct {
	sourcePackets, sourceBytes, globalPackets, globalBytes float64
}

type relayRecordsSnapshot struct {
	state          GrantState
	bindingState   BindingState
	generation     uint64
	bindingPointer *bindingRecord
	binding        bindingRecord
	pendingPointer *challengeRecord
	pending        challengeRecord
	recentPointer  *completedHandshake
	recent         completedHandshake
}

func snapshotRelayRecords(grant *grantRecord) relayRecordsSnapshot {
	snapshot := relayRecordsSnapshot{
		state: grant.state, bindingState: grant.bindingState, generation: grant.generation,
		bindingPointer: grant.binding, pendingPointer: grant.pending, recentPointer: grant.recent,
	}
	if grant.binding != nil {
		snapshot.binding = *grant.binding
	}
	if grant.pending != nil {
		snapshot.pending = *grant.pending
	}
	if grant.recent != nil {
		snapshot.recent = *grant.recent
	}
	return snapshot
}

func assertRelayRecordsUnchanged(t *testing.T, grant *grantRecord, want relayRecordsSnapshot) {
	t.Helper()
	if grant.state != want.state || grant.bindingState != want.bindingState || grant.generation != want.generation ||
		grant.binding != want.bindingPointer || grant.pending != want.pendingPointer || grant.recent != want.recentPointer {
		t.Fatalf("relay ownership/state changed: grant=%#v want=%#v", grant, want)
	}
	if grant.binding != nil && *grant.binding != want.binding {
		t.Fatalf("binding changed: got=%#v want=%#v", *grant.binding, want.binding)
	}
	if grant.pending != nil && *grant.pending != want.pending {
		t.Fatalf("pending challenge changed: got=%#v want=%#v", *grant.pending, want.pending)
	}
	if grant.recent != nil && *grant.recent != want.recent {
		t.Fatalf("recent completion changed: got=%#v want=%#v", *grant.recent, want.recent)
	}
}

func assertChallengeRecordZero(t *testing.T, name string, record *challengeRecord) {
	t.Helper()
	if record.candidateID != (protocol.Bytes16{}) || record.clientNonce != (protocol.Bytes16{}) ||
		record.serverNonce != (protocol.Bytes32{}) || record.endpoint.IsValid() || record.result != (ChallengeResult{}) {
		t.Fatalf("%s challenge retained authority: %#v", name, record)
	}
}

func assertCompletedHandshakeZero(t *testing.T, record *completedHandshake) {
	t.Helper()
	if record.candidateID != (protocol.Bytes16{}) || record.clientNonce != (protocol.Bytes16{}) ||
		record.serverNonce != (protocol.Bytes32{}) || record.endpoint.IsValid() || record.result != (BoundResult{}) {
		t.Fatalf("recent completion retained authority: %#v", record)
	}
}

func preauthBalancesAt(store *Store, source *preauthSource, now time.Time) preauthBalances {
	return preauthBalances{
		sourcePackets: source.packets.TokensAt(now),
		sourceBytes:   source.bytes.TokensAt(now),
		globalPackets: store.preauthGlobalPackets.TokensAt(now),
		globalBytes:   store.preauthGlobalBytes.TokensAt(now),
	}
}

type handshakeFixture struct {
	store      *Store
	fakeClock  *manualClock
	random     *scriptedReader
	allocation Allocation
	secrets    []protocol.Bytes32
	limits     Limits
}

func newHandshakeFixture(t *testing.T, roomTTL, grantTTL time.Duration, participants int) *handshakeFixture {
	t.Helper()
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
	random := newScriptedReader()
	chunks := make([][]byte, 0, participants*2)
	secrets := make([]protocol.Bytes32, participants)
	for index := range participants {
		chunks = append(chunks, filled(byte(0x10+index), 16), filled(byte(0x20+index), 32))
		secrets[index] = bytes32(byte(0x20 + index))
	}
	random.reset(chunks...)
	limits := DefaultLimits()
	store := newTestStore(t, limits, fakeClock, random)
	definition := validDefinition(testWall, participants)
	definition.ExpiresAt = testWall.Add(roomTTL)
	for index := range definition.Participants {
		definition.Participants[index].GrantExpiresAt = testWall.Add(grantTTL)
	}
	allocation, _, err := store.CreateRoom("room", definition)
	if err != nil {
		t.Fatalf("CreateRoom(): %v", err)
	}
	return &handshakeFixture{store: store, fakeClock: fakeClock, random: random, allocation: allocation, secrets: secrets, limits: limits}
}

func (fixture *handshakeFixture) grant(index int) *grantRecord {
	return fixture.store.roomsByID["room"].grants[index]
}

func (fixture *handshakeFixture) grantID(index int) protocol.Bytes16 {
	return fixture.allocation.Grants[index].GrantID
}

func (fixture *handshakeFixture) secret(index int) protocol.Bytes32 {
	return fixture.secrets[index]
}

func (fixture *handshakeFixture) session(index int) string {
	return fixture.allocation.Grants[index].SessionID
}

func (fixture *handshakeFixture) challengeRequest(index int, nonce protocol.Bytes16, endpoint netip.AddrPort) ChallengeRequest {
	return ChallengeRequest{
		RoomID:      "room",
		SessionID:   fixture.session(index),
		GrantID:     fixture.grantID(index),
		ClientNonce: nonce,
		Endpoint:    endpoint,
		InputBytes:  300,
	}
}

func (fixture *handshakeFixture) authRequest(
	index int,
	challenge ChallengeResult,
	clientNonce protocol.Bytes16,
	endpoint netip.AddrPort,
) AuthenticateRequest {
	tag := protocol.AuthTag(fixture.secret(index), protocol.Revision, "room", fixture.session(index),
		fixture.grantID(index), challenge.CandidateID, clientNonce, challenge.ServerNonce)
	return AuthenticateRequest{
		RoomID:      "room",
		SessionID:   fixture.session(index),
		CandidateID: challenge.CandidateID,
		Endpoint:    endpoint,
		AuthTag:     tag,
		InputBytes:  100,
	}
}

func assertNoSecretFields(t *testing.T, value any) {
	t.Helper()
	forbidden := []string{"secret", "key", "grant"}
	typeOf := reflect.TypeOf(value)
	for index := range typeOf.NumField() {
		name := typeOf.Field(index).Name
		for _, fragment := range forbidden {
			if containsFold(name, fragment) {
				t.Fatalf("%s exposes forbidden field %s", typeOf.Name(), name)
			}
		}
	}
}

func containsFold(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		match := true
		for offset := range len(fragment) {
			left, right := value[index+offset], fragment[offset]
			if left >= 'A' && left <= 'Z' {
				left += 'a' - 'A'
			}
			if left != right {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func newD04Limiter(t *testing.T, pick func(*Store) *rate.Limiter) *rate.Limiter {
	t.Helper()
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
	store := newTestStore(t, DefaultLimits(), fakeClock, &sequenceReader{})
	if _, _, err := store.CreateRoom("room", validDefinition(testWall, 1)); err != nil {
		t.Fatalf("CreateRoom(): %v", err)
	}
	return pick(store)
}

func testPreauthSource(store *Store) *preauthSource {
	key := sourceKey(netip.MustParseAddrPort("192.0.2.1:4000"))
	source := store.preauthSources[key]
	if source == nil {
		source = &preauthSource{
			packets: rate.NewLimiter(store.limits.PreauthSourcePacketRate, store.limits.PreauthSourcePacketBurst),
			bytes:   rate.NewLimiter(store.limits.PreauthSourceByteRate, store.limits.PreauthSourceByteBurst),
		}
		store.preauthSources[key] = source
	}
	return source
}

type relayStoreFixture struct {
	store     *Store
	fakeClock *manualClock
	random    *sequenceReader
}

type boundTestClient struct {
	roomID, participantID, sessionID string
	grantID, bindingID               protocol.Bytes16
	secret, key                      protocol.Bytes32
	endpoint                         netip.AddrPort
}

func newRelayStoreFixture(t *testing.T, limits Limits) *relayStoreFixture {
	t.Helper()
	fakeClock := &manualClock{reading: clock.Reading{Wall: testWall, Mono: 0}}
	random := &sequenceReader{}
	return &relayStoreFixture{store: newTestStore(t, limits, fakeClock, random), fakeClock: fakeClock, random: random}
}

func (fixture *relayStoreFixture) setMono(now time.Duration) {
	fixture.fakeClock.reading = clock.Reading{Wall: testWall.Add(now), Mono: now}
}

func (fixture *relayStoreFixture) addBoundRoom(t *testing.T, roomID string, participants int, network byte) []boundTestClient {
	t.Helper()
	definition := RoomDefinition{
		Capacity:     uint32(participants),
		ExpiresAt:    testWall.Add(time.Hour),
		Participants: make([]ParticipantDefinition, participants),
	}
	for index := range definition.Participants {
		suffix := string(rune('a' + index))
		definition.Participants[index] = ParticipantDefinition{
			ParticipantID:  roomID + "-participant-" + suffix,
			SessionID:      roomID + "-session-" + suffix,
			GrantExpiresAt: testWall.Add(30 * time.Minute),
		}
	}
	allocation, created, err := fixture.store.CreateRoom(roomID, definition)
	if err != nil || !created {
		t.Fatalf("CreateRoom(%s) = (_, %t, %v)", roomID, created, err)
	}
	clients := make([]boundTestClient, participants)
	for index, grant := range allocation.Grants {
		endpoint := netip.AddrPortFrom(netip.AddrFrom4([4]byte{198, 18, network, byte(index + 1)}), uint16(4000+index))
		nonce := bytes16(byte(0x40 + index))
		challenge, reason := fixture.store.BeginChallenge(ChallengeRequest{
			RoomID: roomID, SessionID: grant.SessionID, GrantID: grant.GrantID,
			ClientNonce: nonce, Endpoint: endpoint, InputBytes: 300,
		})
		if reason != RejectNone {
			t.Fatalf("BeginChallenge(%s/%d): %q", roomID, index, reason)
		}
		secret := *grant.GrantSecret
		authTag := protocol.AuthTag(secret, protocol.Revision, roomID, grant.SessionID, grant.GrantID,
			challenge.CandidateID, nonce, challenge.ServerNonce)
		bound, reason := fixture.store.Authenticate(AuthenticateRequest{
			RoomID: roomID, SessionID: grant.SessionID, CandidateID: challenge.CandidateID,
			Endpoint: endpoint, AuthTag: authTag, InputBytes: 100,
		})
		if reason != RejectNone {
			t.Fatalf("Authenticate(%s/%d): %q", roomID, index, reason)
		}
		clients[index] = boundTestClient{
			roomID: roomID, participantID: grant.ParticipantID, sessionID: grant.SessionID,
			grantID: grant.GrantID, bindingID: bound.BindingID, secret: secret, endpoint: endpoint,
			key: protocol.BindingKey(secret, protocol.Revision, roomID, grant.SessionID, grant.GrantID,
				challenge.CandidateID, nonce, challenge.ServerNonce),
		}
	}
	return clients
}

func (fixture *relayStoreFixture) rebind(t *testing.T, client *boundTestClient, endpoint netip.AddrPort) {
	t.Helper()
	nonce := bytes16(0xe1)
	challenge, reason := fixture.store.BeginChallenge(ChallengeRequest{
		RoomID: client.roomID, SessionID: client.sessionID, GrantID: client.grantID,
		ClientNonce: nonce, Endpoint: endpoint, InputBytes: 300,
	})
	if reason != RejectNone {
		t.Fatalf("rebind BeginChallenge(): %q", reason)
	}
	tag := protocol.AuthTag(client.secret, protocol.Revision, client.roomID, client.sessionID, client.grantID,
		challenge.CandidateID, nonce, challenge.ServerNonce)
	bound, reason := fixture.store.Authenticate(AuthenticateRequest{
		RoomID: client.roomID, SessionID: client.sessionID, CandidateID: challenge.CandidateID,
		Endpoint: endpoint, AuthTag: tag, InputBytes: 100,
	})
	if reason != RejectNone {
		t.Fatalf("rebind Authenticate(): %q", reason)
	}
	client.bindingID = bound.BindingID
	client.endpoint = endpoint
	client.key = protocol.BindingKey(client.secret, protocol.Revision, client.roomID, client.sessionID, client.grantID,
		challenge.CandidateID, nonce, challenge.ServerNonce)
}

func (client boundTestClient) dataRequest(sequence uint64, payload []byte) ClientDataRequest {
	payload = append([]byte(nil), payload...)
	return ClientDataRequest{
		RoomID: client.roomID, SessionID: client.sessionID, BindingID: client.bindingID,
		Sequence: sequence, Payload: payload, Endpoint: client.endpoint,
		AuthTag: protocol.ClientDataTag(client.key, protocol.Revision, client.roomID, client.sessionID,
			client.bindingID, sequence, payload),
	}
}

func (client boundTestClient) pingRequest(sequence uint64) PingRequest {
	return PingRequest{
		RoomID: client.roomID, SessionID: client.sessionID, BindingID: client.bindingID,
		Sequence: sequence, Endpoint: client.endpoint,
		AuthTag: protocol.PingTag(client.key, protocol.Revision, client.roomID, client.sessionID,
			client.bindingID, sequence),
	}
}

type authenticatedBalances struct {
	sessionPackets, sessionBytes, roomPackets, roomBytes, globalPackets, globalBytes float64
}

func authenticatedBalancesAt(store *Store, grant *grantRecord, now time.Time) authenticatedBalances {
	room := store.roomsByID[grant.roomID]
	return authenticatedBalances{
		grant.ingressPackets.TokensAt(now), grant.ingressBytes.TokensAt(now),
		room.ingressPackets.TokensAt(now), room.ingressBytes.TokensAt(now),
		store.authenticatedGlobalPackets.TokensAt(now), store.authenticatedGlobalBytes.TokensAt(now),
	}
}

type fanoutBalances struct {
	roomWrites, roomBytes, globalWrites, globalBytes float64
}

func fanoutBalancesAt(store *Store, room *roomRecord, now time.Time) fanoutBalances {
	return fanoutBalances{
		room.fanoutWrites.TokensAt(now), room.fanoutBytes.TokensAt(now),
		store.globalFanoutWrites.TokensAt(now), store.globalFanoutBytes.TokensAt(now),
	}
}
