package relayroom

import (
	"crypto/rand"
	"errors"
	"io"
	"math"
	"net/netip"
	"sync"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"golang.org/x/time/rate"
)

var (
	ErrInvalid     = errors.New("invalid")
	ErrNotFound    = errors.New("not found")
	ErrConflict    = errors.New("conflict")
	ErrCapacity    = errors.New("capacity")
	ErrFatalRandom = errors.New("fatal random")
)

type Config struct {
	Limits Limits
	Now    func() clock.Reading
	Random io.Reader
}

type Store struct {
	mu sync.RWMutex

	limits Limits
	now    func() clock.Reading
	random io.Reader

	roomsByID                  map[string]*roomRecord
	grantsByID                 map[protocol.Bytes16]*grantRecord
	candidatesByID             map[protocol.Bytes16]*grantRecord
	bindingsByID               map[protocol.Bytes16]*grantRecord
	preauthSources             map[netip.Prefix]*preauthSource
	preauthGlobalPackets       *rate.Limiter
	preauthGlobalBytes         *rate.Limiter
	authenticatedGlobalPackets *rate.Limiter
	authenticatedGlobalBytes   *rate.Limiter
	globalFanoutWrites         *rate.Limiter
	globalFanoutBytes          *rate.Limiter
	openRooms                  int
	activeSessions             int
}

func New(config Config) (*Store, error) {
	if !validLimits(config.Limits) {
		return nil, ErrInvalid
	}

	now := config.Now
	if now == nil {
		now = clock.System()
	}
	random := config.Random
	if random == nil {
		random = rand.Reader
	}

	return &Store{
		limits:                     config.Limits,
		now:                        now,
		random:                     random,
		roomsByID:                  make(map[string]*roomRecord),
		grantsByID:                 make(map[protocol.Bytes16]*grantRecord),
		candidatesByID:             make(map[protocol.Bytes16]*grantRecord),
		bindingsByID:               make(map[protocol.Bytes16]*grantRecord),
		preauthSources:             make(map[netip.Prefix]*preauthSource),
		preauthGlobalPackets:       rate.NewLimiter(config.Limits.PreauthGlobalPacketRate, config.Limits.PreauthGlobalPacketBurst),
		preauthGlobalBytes:         rate.NewLimiter(config.Limits.PreauthGlobalByteRate, config.Limits.PreauthGlobalByteBurst),
		authenticatedGlobalPackets: rate.NewLimiter(config.Limits.AuthenticatedGlobalPacketRate, config.Limits.AuthenticatedGlobalPacketBurst),
		authenticatedGlobalBytes:   rate.NewLimiter(config.Limits.AuthenticatedGlobalByteRate, config.Limits.AuthenticatedGlobalByteBurst),
		globalFanoutWrites:         rate.NewLimiter(config.Limits.GlobalFanoutWriteRate, config.Limits.GlobalFanoutWriteBurst),
		globalFanoutBytes:          rate.NewLimiter(config.Limits.GlobalFanoutByteRate, config.Limits.GlobalFanoutByteBurst),
	}, nil
}

func (store *Store) Expire() {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().Mono
	for roomID, room := range store.roomsByID {
		if room.state == roomStateTombstone {
			if now >= room.tombstoneDeadline {
				delete(store.roomsByID, roomID)
			}
			continue
		}

		for _, grant := range room.grants {
			store.expireRelayState(grant, now)
			if grantLive(grant) && now >= grant.monoDeadline {
				store.terminalGrant(grant, GrantStateExpired)
			}
		}
		if now >= room.monoDeadline {
			store.tombstoneRoom(room, now, GrantStateExpired)
			continue
		}

		finalGrantDeadline := lastGrantDeadline(room)
		if now < finalGrantDeadline {
			continue
		}
		if room.state == roomStateOpen {
			room.state = roomStateEmpty
			store.openRooms--
		}
		if now >= saturatingAdd(finalGrantDeadline, store.limits.EmptyGrace) {
			store.tombstoneRoom(room, now, GrantStateExpired)
		}
	}
	for key, source := range store.preauthSources {
		if now >= saturatingAdd(source.lastObserved, preauthSourceIdleTTL) {
			delete(store.preauthSources, key)
		}
	}
}

func saturatingAdd(deadline, delta time.Duration) time.Duration {
	if deadline > time.Duration(math.MaxInt64)-delta {
		return time.Duration(math.MaxInt64)
	}
	return deadline + delta
}
