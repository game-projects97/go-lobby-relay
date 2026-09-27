package playerauth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

var authTestNow = time.Date(2026, 8, 20, 12, 0, 0, 123, time.UTC)

type authTestClock struct{ now time.Time }

func (clock *authTestClock) read() time.Time { return clock.now }

func TestIssueAndVerifyPlayerToken(t *testing.T) {
	clock := &authTestClock{now: authTestNow}
	auth := newTestAuth(t, clock, 0x21, testOperatorSecret(0x41))

	token, issued, err := auth.Issue("player-a")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" || strings.Contains(token, "=") {
		t.Fatalf("token is not strict raw base64url: %q", token)
	}
	if issued.PlayerID != "player-a" || !issued.ExpiresAt.Equal(authTestNow.Add(HardTokenTTL)) {
		t.Fatalf("issued claims = %#v", issued)
	}

	verified, err := auth.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified != issued {
		t.Fatalf("verified = %#v, want %#v", verified, issued)
	}

	clock.now = issued.ExpiresAt.Add(-time.Nanosecond)
	if _, err := auth.Verify(token); err != nil {
		t.Fatalf("Verify one nanosecond before expiry: %v", err)
	}
	clock.now = issued.ExpiresAt
	if _, err := auth.Verify(token); !errors.Is(err, ErrExpired) {
		t.Fatalf("Verify at expiry = %v, want expired", err)
	}
}

func TestPlayerTokenRejectsTamperingAndNonCanonicalEncoding(t *testing.T) {
	clock := &authTestClock{now: authTestNow}
	auth := newTestAuth(t, clock, 0x22, testOperatorSecret(0x42))
	token, _, err := auth.Issue("player-a")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	raw[10] ^= 1
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	for name, candidate := range map[string]string{
		"tampered":         tampered,
		"padding":          token + "=",
		"invalid alphabet": token[:len(token)-1] + "+",
		"truncated":        token[:len(token)-1],
		"empty":            "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := auth.Verify(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Verify = %v, want invalid", err)
			}
		})
	}
}

func TestPlayerTokenIsScopedToSecretAndProcessNonce(t *testing.T) {
	clock := &authTestClock{now: authTestNow}
	issuer := newTestAuth(t, clock, 0x23, testOperatorSecret(0x43))
	token, _, err := issuer.Issue("player-a")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	for name, verifier := range map[string]*Issuer{
		"different process": newTestAuth(t, clock, 0x24, testOperatorSecret(0x43)),
		"different secret":  newTestAuth(t, clock, 0x23, testOperatorSecret(0x44)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(token); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Verify = %v, want invalid", err)
			}
		})
	}
}

func TestPlayerAuthValidationAndFatalRandom(t *testing.T) {
	valid := Config{
		OperatorSecret: testOperatorSecret(0x45),
		Now:            func() time.Time { return authTestNow },
		Random:         bytes.NewReader(bytes.Repeat([]byte{0x25}, 32)),
		TokenTTL:       HardTokenTTL,
	}
	for name, mutate := range map[string]func(*Config){
		"zero secret": func(config *Config) { config.OperatorSecret = [32]byte{} },
		"zero ttl":    func(config *Config) { config.TokenTTL = 0 },
		"over ttl":    func(config *Config) { config.TokenTTL = HardTokenTTL + time.Nanosecond },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if _, err := New(config); !errors.Is(err, ErrInvalid) {
				t.Fatalf("New = %v, want invalid", err)
			}
		})
	}

	failed := valid
	failed.Random = errorReader{}
	if _, err := New(failed); !errors.Is(err, ErrFatalRandom) {
		t.Fatalf("New random failure = %v, want fatal random", err)
	}
}

func TestPlayerAuthRejectsInvalidPlayerIDsWithoutLeakingInput(t *testing.T) {
	clock := &authTestClock{now: authTestNow}
	auth := newTestAuth(t, clock, 0x26, testOperatorSecret(0x46))
	for _, playerID := range []string{"", "-bad", "bad!", strings.Repeat("a", 65)} {
		if _, _, err := auth.Issue(playerID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Issue(%q) = %v, want invalid", playerID, err)
		} else if playerID != "" && strings.Contains(err.Error(), playerID) {
			t.Fatalf("error leaked player ID %q: %v", playerID, err)
		}
	}
}

func TestPlayerAuthErrorsDoNotLeakToken(t *testing.T) {
	clock := &authTestClock{now: authTestNow}
	auth := newTestAuth(t, clock, 0x27, testOperatorSecret(0x47))
	candidate := "sensitive-player-token"
	_, err := auth.Verify(candidate)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify = %v, want invalid", err)
	}
	if strings.Contains(err.Error(), candidate) {
		t.Fatalf("error leaked token: %v", err)
	}
}

func newTestAuth(t *testing.T, clock *authTestClock, nonceByte byte, secret [32]byte) *Issuer {
	t.Helper()
	auth, err := New(Config{
		OperatorSecret: secret,
		Now:            clock.read,
		Random:         bytes.NewReader(bytes.Repeat([]byte{nonceByte}, 32)),
		TokenTTL:       HardTokenTTL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return auth
}

func testOperatorSecret(value byte) [32]byte {
	var secret [32]byte
	for index := range secret {
		secret[index] = value
	}
	return secret
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
