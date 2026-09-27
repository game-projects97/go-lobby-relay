package matchmaking

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/relayroom"
)

const (
	HardMaxOpenLobbies = 256
	HardMaxMatchSize   = 16
	HardMaxLobbyTTL    = 2 * time.Hour
	HardMaxListPage    = 50
	DefaultLobbyTTL    = 30 * time.Minute
	MatchTTL           = 2 * time.Minute

	maxIDDraws = 9
)

var (
	ErrInvalid     = errors.New("matchmaking: invalid")
	ErrNotFound    = errors.New("matchmaking: not found")
	ErrConflict    = errors.New("matchmaking: conflict")
	ErrForbidden   = errors.New("matchmaking: forbidden")
	ErrCapacity    = errors.New("matchmaking: capacity")
	ErrUnavailable = errors.New("matchmaking: unavailable")
	ErrFatalRandom = errors.New("matchmaking: fatal random")
)

type Config struct {
	Rooms  *relayroom.Store
	Now    func() clock.Reading
	Random io.Reader
}

type Manager struct {
	mu sync.Mutex

	rooms  *relayroom.Store
	now    func() clock.Reading
	random io.Reader

	lobbiesByID     map[string]*lobbyRecord
	lobbyByPlayer   map[string]string
	matchIDs        map[string]struct{}
	ticketsByPlayer map[string]*ticketRecord
	queues          map[queueBucket][]string
	nextSequence    uint64
}

func New(config Config) (*Manager, error) {
	if config.Rooms == nil {
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
	return &Manager{
		rooms:           config.Rooms,
		now:             now,
		random:          random,
		lobbiesByID:     make(map[string]*lobbyRecord),
		lobbyByPlayer:   make(map[string]string),
		matchIDs:        make(map[string]struct{}),
		ticketsByPlayer: make(map[string]*ticketRecord),
		queues:          make(map[queueBucket][]string),
		nextSequence:    1,
	}, nil
}

func (manager *Manager) Expire() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked(manager.now())
}

func (manager *Manager) takeSequenceLocked() uint64 {
	sequence := manager.nextSequence
	manager.nextSequence++
	return sequence
}

func (manager *Manager) uniqueIDLocked(prefix string, exists func(string) bool) (string, error) {
	for range maxIDDraws {
		candidate, err := manager.drawIDLocked(prefix)
		if err != nil {
			return "", err
		}
		if !exists(candidate) {
			return candidate, nil
		}
	}
	return "", ErrFatalRandom
}

func (manager *Manager) drawIDLocked(prefix string) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(manager.random, raw[:]); err != nil {
		return "", ErrFatalRandom
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (manager *Manager) expireLocked(reading clock.Reading) {
	manager.expireLobbiesLocked(reading)
	manager.expireTicketsLocked(reading)
}
