package playerauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
)

const (
	HardTokenTTL = 15 * time.Minute

	tokenVersion    = byte(1)
	tokenHeaderSize = 10
	tokenTagSize    = sha256.Size
	maxPlayerIDSize = 64
)

var (
	ErrInvalid     = errors.New("playerauth: invalid")
	ErrExpired     = errors.New("playerauth: expired")
	ErrFatalRandom = errors.New("playerauth: fatal random")
)

type Config struct {
	OperatorSecret [32]byte
	Now            func() time.Time
	Random         io.Reader
	TokenTTL       time.Duration
}

type Claims struct {
	PlayerID  string
	ExpiresAt time.Time
}

type Issuer struct {
	key      [32]byte
	now      func() time.Time
	tokenTTL time.Duration
}

func New(config Config) (*Issuer, error) {
	if config.OperatorSecret == ([32]byte{}) || config.TokenTTL <= 0 || config.TokenTTL > HardTokenTTL {
		return nil, ErrInvalid
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	random := config.Random
	if random == nil {
		random = rand.Reader
	}

	var nonce [32]byte
	if _, err := io.ReadFull(random, nonce[:]); err != nil {
		return nil, ErrFatalRandom
	}
	mac := hmac.New(sha256.New, config.OperatorSecret[:])
	_, _ = mac.Write([]byte("go-lobby-relay/player-token/v1\x00"))
	_, _ = mac.Write(nonce[:])
	var key [32]byte
	copy(key[:], mac.Sum(nil))
	return &Issuer{key: key, now: now, tokenTTL: config.TokenTTL}, nil
}

func (auth *Issuer) Issue(playerID string) (string, Claims, error) {
	if auth == nil || !protocol.ValidID(playerID) {
		return "", Claims{}, ErrInvalid
	}
	expiresAt := auth.now().UTC().Add(auth.tokenTTL)
	expiresUnixNano := expiresAt.UnixNano()
	if expiresUnixNano <= 0 || !time.Unix(0, expiresUnixNano).UTC().Equal(expiresAt) {
		return "", Claims{}, ErrInvalid
	}

	payload := make([]byte, tokenHeaderSize+len(playerID), tokenHeaderSize+len(playerID)+tokenTagSize)
	payload[0] = tokenVersion
	binary.BigEndian.PutUint64(payload[1:9], uint64(expiresUnixNano))
	payload[9] = byte(len(playerID))
	copy(payload[tokenHeaderSize:], playerID)
	mac := hmac.New(sha256.New, auth.key[:])
	_, _ = mac.Write(payload)
	raw := mac.Sum(payload)
	return base64.RawURLEncoding.EncodeToString(raw), Claims{PlayerID: playerID, ExpiresAt: expiresAt}, nil
}

func (auth *Issuer) Verify(token string) (Claims, error) {
	if auth == nil {
		return Claims{}, ErrInvalid
	}
	minimumRaw := tokenHeaderSize + 1 + tokenTagSize
	maximumRaw := tokenHeaderSize + maxPlayerIDSize + tokenTagSize
	if len(token) < base64.RawURLEncoding.EncodedLen(minimumRaw) || len(token) > base64.RawURLEncoding.EncodedLen(maximumRaw) {
		return Claims{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token || len(raw) < minimumRaw || len(raw) > maximumRaw {
		return Claims{}, ErrInvalid
	}
	playerIDLength := int(raw[9])
	if raw[0] != tokenVersion || playerIDLength < 1 || playerIDLength > maxPlayerIDSize || len(raw) != tokenHeaderSize+playerIDLength+tokenTagSize {
		return Claims{}, ErrInvalid
	}
	payload := raw[:len(raw)-tokenTagSize]
	mac := hmac.New(sha256.New, auth.key[:])
	_, _ = mac.Write(payload)
	if !hmac.Equal(raw[len(payload):], mac.Sum(nil)) {
		return Claims{}, ErrInvalid
	}
	playerID := string(payload[tokenHeaderSize:])
	expiresUnixNano := int64(binary.BigEndian.Uint64(payload[1:9]))
	if !protocol.ValidID(playerID) || expiresUnixNano <= 0 {
		return Claims{}, ErrInvalid
	}
	expiresAt := time.Unix(0, expiresUnixNano).UTC()
	if !auth.now().UTC().Before(expiresAt) {
		return Claims{}, ErrExpired
	}
	return Claims{PlayerID: playerID, ExpiresAt: expiresAt}, nil
}
