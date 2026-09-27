package operatorapi

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/playerauth"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"github.com/gyungsubLee/go-lobby-relay/internal/relayroom"
	"golang.org/x/time/rate"
)

const (
	testBodyLimit = 64 << 10

	invalidMessage      = "request is invalid"
	unauthorizedMessage = "valid bearer token required"
	notFoundMessage     = "room not found"
	methodMessage       = "method not allowed"
	conflictMessage     = "room_id already exists with a different immutable definition"
	tooLargeMessage     = "request body exceeds 65536 bytes"
	mediaTypeMessage    = "Content-Type must be application/json"
	capacityMessage     = "capacity limit exceeded"
	rateLimitedMessage  = "request rate or concurrency limit exceeded"
	internalMessage     = "internal server error"
)

var (
	controlTestWall  = time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC)
	controlTestToken = repeatedToken(0x42)
	controlBearer    = "Bearer " + base64.RawURLEncoding.EncodeToString(controlTestToken[:])
)

func TestParseOperatorTokenIsStrictAndRejectsZero(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString(controlTestToken[:])
	if got, err := ParseOperatorToken(valid); err != nil || got != controlTestToken {
		t.Fatalf("ParseOperatorToken(valid) = (%x, %v)", got, err)
	}

	tests := []struct {
		name  string
		value string
	}{
		{"short", valid[:len(valid)-1]},
		{"long", valid + "A"},
		{"invalid alphabet", strings.Repeat("!", 43)},
		{"noncanonical trailing bits", nonCanonicalRawURL(controlTestToken)},
		{"all zero", base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := ParseOperatorToken(tt.value); err == nil || got != ([32]byte{}) {
				t.Fatalf("ParseOperatorToken(%q) = (%x, %v), want zero/error", tt.name, got, err)
			}
		})
	}
}

func TestOperatorCanIssuePlayerToken(t *testing.T) {
	fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
	response := serveHandler(t, fixture.handler, http.MethodPost, "/v1/player-tokens", strings.NewReader(`{"player_id":"player-a"}`), controlBearer, "application/json")
	if response.Code != http.StatusCreated {
		t.Fatalf("POST /v1/player-tokens = %d %q", response.Code, response.Body.String())
	}
	var body struct {
		PlayerID  string `json:"player_id"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	claims, err := fixture.playerTokens.Verify(body.Token)
	if err != nil || claims.PlayerID != "player-a" || body.PlayerID != claims.PlayerID || body.ExpiresAt != claims.ExpiresAt.Format(time.RFC3339Nano) {
		t.Fatalf("issued token response = %+v, claims=%+v err=%v", body, claims, err)
	}

	for _, test := range []struct {
		name, method, body, contentType, authorization string
		status                                         int
	}{
		{"unauthorized", http.MethodPost, `{"player_id":"player-a"}`, "application/json", "", http.StatusUnauthorized},
		{"wrong method", http.MethodGet, "", "", controlBearer, http.StatusMethodNotAllowed},
		{"unknown field", http.MethodPost, `{"player_id":"player-a","role":"admin"}`, "application/json", controlBearer, http.StatusBadRequest},
		{"duplicate field", http.MethodPost, `{"player_id":"player-a","player_id":"player-b"}`, "application/json", controlBearer, http.StatusBadRequest},
		{"invalid player", http.MethodPost, `{"player_id":"-bad"}`, "application/json", controlBearer, http.StatusBadRequest},
		{"wrong content type", http.MethodPost, `{"player_id":"player-a"}`, "text/plain", controlBearer, http.StatusUnsupportedMediaType},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := serveHandler(t, fixture.handler, test.method, "/v1/player-tokens", strings.NewReader(test.body), test.authorization, test.contentType)
			if got.Code != test.status {
				t.Fatalf("status = %d body=%q, want %d", got.Code, got.Body.String(), test.status)
			}
		})
	}
}

func TestHandlerAuthenticatesAndRoutesCanonically(t *testing.T) {
	wrongToken := repeatedToken(0x43)
	authTests := []struct {
		name   string
		header string
	}{
		{"absent", ""},
		{"wrong scheme", "Basic " + strings.Repeat("a", 43)},
		{"wrong bytes", "Bearer " + base64.RawURLEncoding.EncodeToString(wrongToken[:])},
		{"padded", "Bearer " + base64.URLEncoding.EncodeToString(controlTestToken[:])},
		{"short decoded value", "Bearer " + base64.RawURLEncoding.EncodeToString(controlTestToken[:31])},
		{"invalid alphabet", "Bearer " + strings.Repeat("!", 43)},
		{"noncanonical trailing bits", "Bearer " + nonCanonicalRawURL(controlTestToken)},
	}
	for _, tt := range authTests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
			response := serveHandler(t, fixture.handler, http.MethodGet, "/v1/rooms/room", nil, tt.header, "")
			assertErrorResponse(t, response, http.StatusUnauthorized, "unauthorized", unauthorizedMessage)
			if got := response.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
			}
		})
	}

	t.Run("wrong token does not disclose existence", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
		roomSpec := testRoomSpec(controlTestWall, 1)
		if _, _, err := fixture.store.CreateRoom("existing", roomSpec); err != nil {
			t.Fatalf("CreateRoom(): %v", err)
		}
		wrong := "Bearer " + base64.RawURLEncoding.EncodeToString(wrongToken[:])
		existing := serveHandler(t, fixture.handler, http.MethodGet, "/v1/rooms/existing", nil, wrong, "")
		missing := serveHandler(t, fixture.handler, http.MethodGet, "/v1/rooms/missing", nil, wrong, "")
		if existing.Code != http.StatusUnauthorized || missing.Code != http.StatusUnauthorized || existing.Body.String() != missing.Body.String() {
			t.Fatalf("unauthorized responses differ: existing=%d %q missing=%d %q",
				existing.Code, existing.Body.String(), missing.Code, missing.Body.String())
		}
	})

	t.Run("unauthorized PUT cannot mutate", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
		body := createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"participant", "session", time.Hour}})
		response := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), "", "application/json")
		assertErrorResponse(t, response, http.StatusUnauthorized, "unauthorized", unauthorizedMessage)
		if _, err := fixture.store.GetRoom("room"); !errors.Is(err, relayroom.ErrNotFound) {
			t.Fatalf("unauthorized PUT mutated store: %v", err)
		}
	})

	t.Run("correct token reaches store", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
		response := serveHandler(t, fixture.handler, http.MethodGet, "/v1/rooms/missing", nil, controlBearer, "")
		assertErrorResponse(t, response, http.StatusNotFound, "not_found", notFoundMessage)
	})

	routeTests := []struct {
		name   string
		method string
		path   string
		status int
		code   string
		msg    string
	}{
		{"listing", http.MethodGet, "/v1/rooms", http.StatusNotFound, "not_found", notFoundMessage},
		{"trailing slash", http.MethodGet, "/v1/rooms/", http.StatusNotFound, "not_found", notFoundMessage},
		{"room trailing slash", http.MethodGet, "/v1/rooms/room/", http.StatusNotFound, "not_found", notFoundMessage},
		{"extra segment", http.MethodGet, "/v1/rooms/room/extra", http.StatusNotFound, "not_found", notFoundMessage},
		{"percent encoded ID", http.MethodGet, "/v1/rooms/%72oom", http.StatusNotFound, "not_found", notFoundMessage},
		{"status excluded", http.MethodGet, "/v1/status", http.StatusNotFound, "not_found", notFoundMessage},
		{"invalid canonical ID", http.MethodGet, "/v1/rooms/-room", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"invalid canonical character", http.MethodGet, "/v1/rooms/room!", http.StatusBadRequest, "invalid_request", invalidMessage},
	}
	for _, tt := range routeTests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
			response := serveHandler(t, fixture.handler, tt.method, tt.path, nil, controlBearer, "")
			assertErrorResponse(t, response, tt.status, tt.code, tt.msg)
		})
	}

	t.Run("unsupported method advertises exact methods", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
		response := serveHandler(t, fixture.handler, http.MethodPost, "/v1/rooms/room", nil, controlBearer, "")
		assertErrorResponse(t, response, http.StatusMethodNotAllowed, "method_not_allowed", methodMessage)
		if got := response.Header().Get("Allow"); got != "PUT, GET, DELETE" {
			t.Fatalf("Allow = %q", got)
		}
	})

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method+" rejects body", func(t *testing.T) {
			fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
			response := serveHandler(t, fixture.handler, method, "/v1/rooms/room", strings.NewReader("{}"), controlBearer, "application/json")
			assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request", invalidMessage)
		})
	}

	t.Run("DELETE is idempotent and bodyless", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
		for range 2 {
			response := serveHandler(t, fixture.handler, http.MethodDelete, "/v1/rooms/never-created", nil, controlBearer, "")
			if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
				t.Fatalf("DELETE = %d %q, want bodyless 204", response.Code, response.Body.String())
			}
		}
	})
}

func TestPutRejectsNonCanonicalOrUnboundedJSON(t *testing.T) {
	roomExpiry := controlTestWall.Add(2 * time.Hour).Format(time.RFC3339Nano)
	grantExpiry := controlTestWall.Add(time.Hour).Format(time.RFC3339Nano)
	participant := `{"participant_id":"participant","session_id":"session","grant_expires_at":"` + grantExpiry + `"}`
	valid := `{"capacity":1,"expires_at":"` + roomExpiry + `","participants":[` + participant + `]}`

	tests := []struct {
		name        string
		body        string
		contentType string
		status      int
		code        string
		message     string
	}{
		{"missing media type", valid, "", http.StatusUnsupportedMediaType, "unsupported_media_type", mediaTypeMessage},
		{"wrong media type", valid, "text/plain", http.StatusUnsupportedMediaType, "unsupported_media_type", mediaTypeMessage},
		{"unknown top-level field", strings.TrimSuffix(valid, "}") + `,"metadata":{}}`, "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"unknown participant field", strings.Replace(valid, `"grant_expires_at":"`+grantExpiry+`"`, `"grant_expires_at":"`+grantExpiry+`","metadata":{}`, 1), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"case-variant top-level field", strings.Replace(valid, `"capacity"`, `"Capacity"`, 1), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"case-variant participant field", strings.Replace(valid, `"participant_id"`, `"Participant_ID"`, 1), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"duplicate top-level field", strings.Replace(valid, `"capacity":1`, `"capacity":1,"capacity":1`, 1), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"duplicate nested field", strings.Replace(valid, `"participant_id":"participant"`, `"participant_id":"participant","participant_id":"participant"`, 1), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"second JSON value", valid + `{}`, "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"trailing garbage", valid + `x`, "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"missing capacity", `{"expires_at":"` + roomExpiry + `","participants":[` + participant + `]}`, "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"missing expires at", `{"capacity":1,"participants":[` + participant + `]}`, "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"missing participants", `{"capacity":1,"expires_at":"` + roomExpiry + `"}`, "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"numeric room timestamp", strings.Replace(valid, `"`+roomExpiry+`"`, `123`, 1), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"numeric grant timestamp", strings.Replace(valid, `"`+grantExpiry+`"`, `123`, 1), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"UTC offset", strings.ReplaceAll(valid, "Z", "+09:00"), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"missing zone", strings.ReplaceAll(valid, "Z", ""), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"noncanonical fractional seconds", strings.ReplaceAll(valid, ":00Z", ":00.000Z"), "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
		{"empty body", "", "application/json", http.StatusBadRequest, "invalid_request", invalidMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
			response := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", strings.NewReader(tt.body), controlBearer, tt.contentType)
			assertErrorResponse(t, response, tt.status, tt.code, tt.message)
			if _, err := fixture.store.GetRoom("room"); !errors.Is(err, relayroom.ErrNotFound) {
				t.Fatalf("rejected request mutated store: %v", err)
			}
		})
	}

	t.Run("JSON media type parameters are accepted", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
		response := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", strings.NewReader(valid), controlBearer, "application/json; charset=utf-8")
		if response.Code != http.StatusCreated {
			t.Fatalf("PUT = %d %q", response.Code, response.Body.String())
		}
	})

	t.Run("exact body cap is accepted", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
		body := append([]byte(valid), bytes.Repeat([]byte{' '}, testBodyLimit-len(valid))...)
		response := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), controlBearer, "application/json")
		if response.Code != http.StatusCreated {
			t.Fatalf("exact-cap PUT = %d %q", response.Code, response.Body.String())
		}
	})

	t.Run("body cap plus one is rejected", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
		body := append([]byte(valid), bytes.Repeat([]byte{' '}, testBodyLimit+1-len(valid))...)
		response := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), controlBearer, "application/json")
		assertErrorResponse(t, response, http.StatusRequestEntityTooLarge, "body_too_large", tooLargeMessage)
	})
}

func TestPutValidatesIDsCapacityAndTTLs(t *testing.T) {
	tests := []struct {
		name   string
		roomID string
		body   func(*testing.T) []byte
		status int
		code   string
		msg    string
	}{
		{
			"64-byte ID", "a" + strings.Repeat("b", 63),
			func(t *testing.T) []byte {
				return createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"participant", "session", time.Hour}})
			},
			http.StatusCreated, "", "",
		},
		{
			"65-byte ID", "a" + strings.Repeat("b", 64),
			func(t *testing.T) []byte {
				return createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"participant", "session", time.Hour}})
			},
			http.StatusBadRequest, "invalid_request", invalidMessage,
		},
		{
			"capacity mismatch", "room",
			func(t *testing.T) []byte {
				body := createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"participant", "session", time.Hour}})
				return bytes.Replace(body, []byte(`"capacity":1`), []byte(`"capacity":2`), 1)
			},
			http.StatusBadRequest, "invalid_request", invalidMessage,
		},
		{
			"capacity over hard maximum", "room",
			func(t *testing.T) []byte {
				return createRoomBody(t, controlTestWall, 2*time.Hour, participantSpecs(17, time.Hour))
			},
			http.StatusUnprocessableEntity, "capacity_exceeded", capacityMessage,
		},
		{
			"room TTL exact maximum", "room",
			func(t *testing.T) []byte {
				return createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"participant", "session", 2 * time.Hour}})
			},
			http.StatusCreated, "", "",
		},
		{
			"room TTL over maximum", "room",
			func(t *testing.T) []byte {
				return createRoomBody(t, controlTestWall, 2*time.Hour+time.Nanosecond, []testParticipantSpec{{"participant", "session", time.Hour}})
			},
			http.StatusBadRequest, "invalid_request", invalidMessage,
		},
		{
			"room deadline not future", "room",
			func(t *testing.T) []byte {
				return createRoomBody(t, controlTestWall, 0, []testParticipantSpec{{"participant", "session", time.Hour}})
			},
			http.StatusBadRequest, "invalid_request", invalidMessage,
		},
		{
			"grant deadline past room", "room",
			func(t *testing.T) []byte {
				return createRoomBody(t, controlTestWall, time.Hour, []testParticipantSpec{{"participant", "session", time.Hour + time.Nanosecond}})
			},
			http.StatusBadRequest, "invalid_request", invalidMessage,
		},
		{
			"duplicate participant", "room",
			func(t *testing.T) []byte {
				return createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"same", "session-a", time.Hour}, {"same", "session-b", time.Hour}})
			},
			http.StatusBadRequest, "invalid_request", invalidMessage,
		},
		{
			"duplicate session", "room",
			func(t *testing.T) []byte {
				return createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"participant-a", "same", time.Hour}, {"participant-b", "same", time.Hour}})
			},
			http.StatusBadRequest, "invalid_request", invalidMessage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
			response := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/"+tt.roomID, bytes.NewReader(tt.body(t)), controlBearer, "application/json")
			if tt.status == http.StatusCreated {
				if response.Code != tt.status {
					t.Fatalf("PUT = %d %q", response.Code, response.Body.String())
				}
				return
			}
			assertErrorResponse(t, response, tt.status, tt.code, tt.msg)
		})
	}
}

func TestRoomHTTPResponsesAreExactIdempotentAndRedacted(t *testing.T) {
	random := &testSequenceReader{}
	fixture := newControlFixture(t, relayroom.DefaultLimits(), random, nil)
	participants := []testParticipantSpec{
		{"bob", "session-b", 90 * time.Minute},
		{"alice", "session-a", time.Hour},
	}
	body := createRoomBody(t, controlTestWall, 2*time.Hour, participants)
	firstRecorder := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), controlBearer, "application/json")
	if firstRecorder.Code != http.StatusCreated {
		t.Fatalf("first PUT = %d %q", firstRecorder.Code, firstRecorder.Body.String())
	}
	first := decodePutResponse(t, firstRecorder)
	assertPutResponse(t, first, "room", controlTestWall, controlTestWall.Add(2*time.Hour))
	assertExactPutKeys(t, firstRecorder.Body.Bytes(), true)
	reads := random.readCount()

	reversed := createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{participants[1], participants[0]})
	retryRecorder := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(reversed), controlBearer, "application/json")
	if retryRecorder.Code != http.StatusOK {
		t.Fatalf("retry PUT = %d %q", retryRecorder.Code, retryRecorder.Body.String())
	}
	retry := decodePutResponse(t, retryRecorder)
	if !reflect.DeepEqual(first, retry) || random.readCount() != reads {
		t.Fatalf("canonical retry changed allocation/randomness: first=%#v retry=%#v reads=%d/%d", first, retry, reads, random.readCount())
	}

	fixture.fakeClock.setMono(time.Hour)
	partialRecorder := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), controlBearer, "application/json")
	if partialRecorder.Code != http.StatusOK {
		t.Fatalf("partial retry = %d %q", partialRecorder.Code, partialRecorder.Body.String())
	}
	partial := decodePutResponse(t, partialRecorder)
	if partial.Grants[0].ParticipantID != "alice" || partial.Grants[0].State != "expired" || partial.Grants[0].GrantSecret != nil ||
		partial.Grants[0].GrantID != first.Grants[0].GrantID || partial.Grants[0].GrantExpiresAt != first.Grants[0].GrantExpiresAt {
		t.Fatalf("terminal grant was reissued or extended: %#v", partial.Grants[0])
	}
	if partial.Grants[1].State != "issued" || partial.Grants[1].GrantSecret == nil ||
		partial.Grants[1].GrantID != first.Grants[1].GrantID || *partial.Grants[1].GrantSecret != *first.Grants[1].GrantSecret ||
		partial.CreatedAt != first.CreatedAt || partial.ExpiresAt != first.ExpiresAt || random.readCount() != reads {
		t.Fatalf("live allocation changed on partial retry: %#v", partial)
	}
	assertExactPutKeys(t, partialRecorder.Body.Bytes(), false)

	getRecorder := serveHandler(t, fixture.handler, http.MethodGet, "/v1/rooms/room", nil, controlBearer, "")
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET = %d %q", getRecorder.Code, getRecorder.Body.String())
	}
	get := decodeGetResponse(t, getRecorder)
	if get.RoomID != "room" || get.State != "open" || get.RelayEndpoint.Host != "relay.example.net" || get.RelayEndpoint.Port != 30000 ||
		get.ProtocolRevision != protocol.Revision || get.MaxDatagramBytes != protocol.MaxDatagramBytes || get.MaxPayloadBytes != protocol.MaxPayloadBytes ||
		len(get.Participants) != 2 || get.Participants[0].GrantState != "expired" || get.Participants[0].BindingState != "expired" ||
		get.Participants[1].GrantState != "issued" || get.Participants[1].BindingState != "unbound" {
		t.Fatalf("GET response = %#v", get)
	}
	assertExactGetKeysAndRedaction(t, getRecorder.Body.Bytes())
	assertAllTimestampStringsEndInZ(t, getRecorder.Body.Bytes())

	conflicting := createRoomBody(t, controlTestWall, 2*time.Hour-time.Second, participants)
	conflict := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(conflicting), controlBearer, "application/json")
	assertErrorResponse(t, conflict, http.StatusConflict, "conflict", conflictMessage)

	deleted := serveHandler(t, fixture.handler, http.MethodDelete, "/v1/rooms/room", nil, controlBearer, "")
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Fatalf("DELETE = %d %q", deleted.Code, deleted.Body.String())
	}
	terminalGet := serveHandler(t, fixture.handler, http.MethodGet, "/v1/rooms/room", nil, controlBearer, "")
	assertErrorResponse(t, terminalGet, http.StatusNotFound, "not_found", notFoundMessage)
	tombstoneRetry := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), controlBearer, "application/json")
	assertErrorResponse(t, tombstoneRetry, http.StatusConflict, "conflict", conflictMessage)
	repeatedDelete := serveHandler(t, fixture.handler, http.MethodDelete, "/v1/rooms/room", nil, controlBearer, "")
	if repeatedDelete.Code != http.StatusNoContent || repeatedDelete.Body.Len() != 0 {
		t.Fatalf("repeated DELETE = %d %q", repeatedDelete.Code, repeatedDelete.Body.String())
	}
}

func TestTerminalAndMissingRoomsReturnNotFoundBeforeSweep(t *testing.T) {
	fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
	body := createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"participant", "session", time.Hour}})
	created := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), controlBearer, "application/json")
	if created.Code != http.StatusCreated {
		t.Fatalf("PUT = %d %q", created.Code, created.Body.String())
	}
	fixture.fakeClock.setMono(time.Hour)
	terminal := serveHandler(t, fixture.handler, http.MethodGet, "/v1/rooms/room", nil, controlBearer, "")
	assertErrorResponse(t, terminal, http.StatusNotFound, "not_found", notFoundMessage)
	retry := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), controlBearer, "application/json")
	assertErrorResponse(t, retry, http.StatusConflict, "conflict", conflictMessage)
	missing := serveHandler(t, fixture.handler, http.MethodGet, "/v1/rooms/missing", nil, controlBearer, "")
	assertErrorResponse(t, missing, http.StatusNotFound, "not_found", notFoundMessage)
}

func TestStoreErrorsHaveFixedHTTPMappings(t *testing.T) {
	t.Run("configured capacity", func(t *testing.T) {
		limits := relayroom.DefaultLimits()
		limits.MaxOpenRooms = 1
		limits.MaxRoomRecords = 1
		var fatalCalls atomic.Int32
		fixture := newControlFixture(t, limits, &testSequenceReader{}, func(config *Config) {
			config.Fatal = func() { fatalCalls.Add(1) }
		})
		body := createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"participant", "session", time.Hour}})
		first := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/one", bytes.NewReader(body), controlBearer, "application/json")
		if first.Code != http.StatusCreated {
			t.Fatalf("first PUT = %d %q", first.Code, first.Body.String())
		}
		second := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/two", bytes.NewReader(body), controlBearer, "application/json")
		assertErrorResponse(t, second, http.StatusUnprocessableEntity, "capacity_exceeded", capacityMessage)
		if fatalCalls.Load() != 0 {
			t.Fatalf("nonfatal capacity error sent %d fatal notifications", fatalCalls.Load())
		}
	})

	t.Run("fatal randomness leaves no partial room", func(t *testing.T) {
		random := &failOnceReader{delegate: &testSequenceReader{}}
		var failed *httptest.ResponseRecorder
		fatalCalls := 0
		fixture := newControlFixture(t, relayroom.DefaultLimits(), random, func(config *Config) {
			config.Fatal = func() {
				fatalCalls++
				assertErrorResponse(t, failed, http.StatusInternalServerError, "internal_error", internalMessage)
				if !failed.Flushed {
					t.Fatal("fatal notification ran before the 500 response was flushed")
				}
			}
		})
		body := createRoomBody(t, controlTestWall, 2*time.Hour, []testParticipantSpec{{"participant", "session", time.Hour}})
		failed = httptest.NewRecorder()
		fixture.handler.ServeHTTP(failed, authorizedTestRequest(http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), "application/json"))
		assertErrorResponse(t, failed, http.StatusInternalServerError, "internal_error", internalMessage)
		if fatalCalls != 1 {
			t.Fatalf("fatal random sent %d fatal notifications, want 1", fatalCalls)
		}
		if _, err := fixture.store.GetRoom("room"); !errors.Is(err, relayroom.ErrNotFound) {
			t.Fatalf("fatal random left partial room: %v", err)
		}
		retry := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", bytes.NewReader(body), controlBearer, "application/json")
		if retry.Code != http.StatusCreated {
			t.Fatalf("retry after random failure = %d %q", retry.Code, retry.Body.String())
		}
	})
}

func TestAdmissionRejectsBeforeReadingBody(t *testing.T) {
	t.Run("rate", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, func(config *Config) {
			config.RequestRate = 1
			config.RequestBurst = 1
		})
		first := serveHandler(t, fixture.handler, http.MethodGet, "/v1/rooms/missing", nil, controlBearer, "")
		assertErrorResponse(t, first, http.StatusNotFound, "not_found", notFoundMessage)
		body := &trackingReader{reader: strings.NewReader("{}")}
		limited := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/room", body, controlBearer, "application/json")
		assertErrorResponse(t, limited, http.StatusTooManyRequests, "rate_limited", rateLimitedMessage)
		if body.reads.Load() != 0 {
			t.Fatalf("rate-rejected body read %d times", body.reads.Load())
		}
	})

	t.Run("concurrency", func(t *testing.T) {
		fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, func(config *Config) {
			config.MaxConcurrent = 1
		})
		blocking := &blockingReader{started: make(chan struct{}), release: make(chan struct{})}
		firstRequest := authorizedTestRequest(http.MethodPut, "/v1/rooms/first", blocking, "application/json")
		firstResponse := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			fixture.handler.ServeHTTP(firstResponse, firstRequest)
			close(done)
		}()
		select {
		case <-blocking.started:
		case <-time.After(time.Second):
			t.Fatal("first request never began reading its body")
		}

		secondBody := &trackingReader{reader: strings.NewReader("{}")}
		second := serveHandler(t, fixture.handler, http.MethodPut, "/v1/rooms/second", secondBody, controlBearer, "application/json")
		assertErrorResponse(t, second, http.StatusTooManyRequests, "rate_limited", rateLimitedMessage)
		if secondBody.reads.Load() != 0 {
			t.Fatalf("concurrency-rejected body read %d times", secondBody.reads.Load())
		}
		close(blocking.release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("admitted request did not finish")
		}
		if firstResponse.Header().Get("Cache-Control") != "no-store" || firstResponse.Code != http.StatusBadRequest {
			t.Fatalf("first request = %d %q", firstResponse.Code, firstResponse.Body.String())
		}
	})
}

func TestNewHandlerRejectsInvalidConfigLimits(t *testing.T) {
	fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
	valid := fixture.config
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero rate", func(config *Config) { config.RequestRate = 0 }},
		{"negative rate", func(config *Config) { config.RequestRate = -1 }},
		{"rate above maximum", func(config *Config) { config.RequestRate = HardOperatorRequestRate + 0.01 }},
		{"infinite rate", func(config *Config) { config.RequestRate = rate.Inf }},
		{"NaN rate", func(config *Config) { config.RequestRate = rate.Limit(math.NaN()) }},
		{"zero burst", func(config *Config) { config.RequestBurst = 0 }},
		{"negative burst", func(config *Config) { config.RequestBurst = -1 }},
		{"burst above maximum", func(config *Config) { config.RequestBurst = HardOperatorRequestBurst + 1 }},
		{"zero concurrency", func(config *Config) { config.MaxConcurrent = 0 }},
		{"negative concurrency", func(config *Config) { config.MaxConcurrent = -1 }},
		{"concurrency above maximum", func(config *Config) { config.MaxConcurrent = HardOperatorConcurrent + 1 }},
		{"all-zero operator token", func(config *Config) { config.OperatorToken = [32]byte{} }},
		{"empty advertised host", func(config *Config) { config.AdvertisedHost = "" }},
		{"zero advertised port", func(config *Config) { config.AdvertisedPort = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := valid
			tt.mutate(&config)
			if handler, err := NewHandler(config, fixture.store); err == nil || handler != nil {
				t.Fatalf("NewHandler() = (%T, %v), want nil/error", handler, err)
			}
		})
	}
	if handler, err := NewHandler(valid, nil); err == nil || handler != nil {
		t.Fatalf("NewHandler(nil store) = (%T, %v), want nil/error", handler, err)
	}
	valid.Now = nil
	if handler, err := NewHandler(valid, fixture.store); err != nil || handler == nil {
		t.Fatalf("NewHandler(nil clock) = (%T, %v), want production default", handler, err)
	}
}

func TestNewServerHasFixedBounds(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := NewServer("127.0.0.1:0", handler)
	if server.Addr != "127.0.0.1:0" || reflect.ValueOf(server.Handler).Pointer() != reflect.ValueOf(handler).Pointer() || server.MaxHeaderBytes != 16<<10 ||
		server.ReadHeaderTimeout != 2*time.Second || server.ReadTimeout != 5*time.Second ||
		server.WriteTimeout != 5*time.Second || server.IdleTimeout != 30*time.Second {
		t.Fatalf("NewServer() = %#v", server)
	}
}

func TestBoundedServerRejectsOversizedHeaderBeforeHandler(t *testing.T) {
	var calls atomic.Int32
	address := startTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer connection.Close()
	request := "PUT /v1/rooms/room HTTP/1.1\r\nHost: " + address + "\r\nX-Oversized: " + strings.Repeat("a", 32<<10) + "\r\n\r\n"
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPut})
	if err != nil {
		t.Fatalf("ReadResponse(): %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("status = %d, want 431", response.StatusCode)
	}
	if calls.Load() != 0 {
		t.Fatalf("oversized header invoked handler %d times", calls.Load())
	}
}

func TestBoundedServerDoesNotExposeGeneralOptions(t *testing.T) {
	fixture := newControlFixture(t, relayroom.DefaultLimits(), &testSequenceReader{}, nil)
	address := startTestServer(t, fixture.handler)
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, "OPTIONS * HTTP/1.1\r\nHost: "+address+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodOptions})
	if err != nil {
		t.Fatalf("ReadResponse(): %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if response.StatusCode != http.StatusNotFound || response.Header.Get("Cache-Control") != "no-store" || !bytes.Contains(body, []byte(`"code":"not_found"`)) {
		t.Fatalf("OPTIONS * = %d headers=%v body=%q, want handler-owned 404/no-store", response.StatusCode, response.Header, body)
	}
}

func TestBoundedServerClosesIncompleteHeaderAtReadHeaderTimeout(t *testing.T) {
	var calls atomic.Int32
	address := startTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("Dial(): %v", err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, "GET /v1/rooms/room HTTP/1.1\r\nHost: "+address+"\r\nX-Hold:"); err != nil {
		t.Fatalf("write partial request: %v", err)
	}
	started := time.Now()
	if err := connection.SetReadDeadline(started.Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline(): %v", err)
	}
	if _, err := io.ReadAll(connection); err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			t.Fatalf("server did not close incomplete header: %v", err)
		}
	}
	elapsed := time.Since(started)
	if elapsed < 1500*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("incomplete header closed after %v, want approximately 2s", elapsed)
	}
	if calls.Load() != 0 {
		t.Fatalf("incomplete header invoked handler %d times", calls.Load())
	}
}

type testParticipantSpec struct {
	participantID string
	sessionID     string
	grantTTL      time.Duration
}

type testCreateParticipant struct {
	ParticipantID  string `json:"participant_id"`
	SessionID      string `json:"session_id"`
	GrantExpiresAt string `json:"grant_expires_at"`
}

type testCreateRequest struct {
	Capacity     uint32                  `json:"capacity"`
	ExpiresAt    string                  `json:"expires_at"`
	Participants []testCreateParticipant `json:"participants"`
}

type testEndpoint struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

type testCommonResponse struct {
	RoomID           string       `json:"room_id"`
	State            string       `json:"state"`
	CreatedAt        string       `json:"created_at"`
	ExpiresAt        string       `json:"expires_at"`
	Capacity         uint32       `json:"capacity"`
	RelayEndpoint    testEndpoint `json:"relay_endpoint"`
	ProtocolRevision uint32       `json:"protocol_revision"`
	MaxDatagramBytes uint32       `json:"max_datagram_bytes"`
	MaxPayloadBytes  uint32       `json:"max_payload_bytes"`
}

type testGrantResponse struct {
	ParticipantID  string  `json:"participant_id"`
	SessionID      string  `json:"session_id"`
	GrantID        string  `json:"grant_id"`
	GrantSecret    *string `json:"grant_secret"`
	GrantExpiresAt string  `json:"grant_expires_at"`
	State          string  `json:"state"`
}

type testPutResponse struct {
	testCommonResponse
	Grants []testGrantResponse `json:"grants"`
}

type testParticipantResponse struct {
	ParticipantID  string `json:"participant_id"`
	SessionID      string `json:"session_id"`
	GrantState     string `json:"grant_state"`
	GrantExpiresAt string `json:"grant_expires_at"`
	BindingState   string `json:"binding_state"`
}

type testGetResponse struct {
	testCommonResponse
	Participants []testParticipantResponse `json:"participants"`
}

type controlFixture struct {
	handler      http.Handler
	store        *relayroom.Store
	playerTokens *playerauth.Issuer
	fakeClock    *controlStoreClock
	config       Config
}

func newControlFixture(t *testing.T, limits relayroom.Limits, random io.Reader, mutate func(*Config)) controlFixture {
	t.Helper()
	fakeClock := &controlStoreClock{reading: clock.Reading{Wall: controlTestWall, Mono: 0}}
	roomStore, err := relayroom.New(relayroom.Config{Limits: limits, Now: fakeClock.now, Random: random})
	if err != nil {
		t.Fatalf("store.New(): %v", err)
	}
	playerTokens, err := playerauth.New(playerauth.Config{
		OperatorSecret: controlTestToken,
		Now:            func() time.Time { return controlTestWall },
		Random:         bytes.NewReader(bytes.Repeat([]byte{0x91}, 32)),
		TokenTTL:       playerauth.HardTokenTTL,
	})
	if err != nil {
		t.Fatalf("playerauth.New(): %v", err)
	}
	config := Config{
		OperatorToken:  controlTestToken,
		PlayerTokens:   playerTokens,
		AdvertisedHost: "relay.example.net",
		AdvertisedPort: 30000,
		RequestRate:    HardOperatorRequestRate,
		RequestBurst:   HardOperatorRequestBurst,
		MaxConcurrent:  HardOperatorConcurrent,
		Now:            func() time.Time { return controlTestWall },
	}
	if mutate != nil {
		mutate(&config)
	}
	handler, err := NewHandler(config, roomStore)
	if err != nil {
		t.Fatalf("NewHandler(): %v", err)
	}
	return controlFixture{handler: handler, store: roomStore, playerTokens: playerTokens, fakeClock: fakeClock, config: config}
}

func createRoomBody(t *testing.T, wall time.Time, roomTTL time.Duration, participants []testParticipantSpec) []byte {
	t.Helper()
	request := testCreateRequest{
		Capacity:     uint32(len(participants)),
		ExpiresAt:    wall.Add(roomTTL).UTC().Format(time.RFC3339Nano),
		Participants: make([]testCreateParticipant, len(participants)),
	}
	for index, participant := range participants {
		request.Participants[index] = testCreateParticipant{
			ParticipantID:  participant.participantID,
			SessionID:      participant.sessionID,
			GrantExpiresAt: wall.Add(participant.grantTTL).UTC().Format(time.RFC3339Nano),
		}
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	return body
}

func participantSpecs(count int, ttl time.Duration) []testParticipantSpec {
	participants := make([]testParticipantSpec, count)
	for index := range participants {
		participants[index] = testParticipantSpec{
			participantID: "participant-" + string(rune('a'+index)),
			sessionID:     "session-" + string(rune('a'+index)),
			grantTTL:      ttl,
		}
	}
	return participants
}

func testRoomSpec(wall time.Time, count int) relayroom.RoomSpec {
	participants := make([]relayroom.ParticipantSpec, count)
	for index := range participants {
		participants[index] = relayroom.ParticipantSpec{
			ParticipantID:  "participant-" + string(rune('a'+index)),
			SessionID:      "session-" + string(rune('a'+index)),
			GrantExpiresAt: wall.Add(time.Hour),
		}
	}
	return relayroom.RoomSpec{Capacity: uint32(count), ExpiresAt: wall.Add(2 * time.Hour), Participants: participants}
}

func serveHandler(t *testing.T, handler http.Handler, method, target string, body io.Reader, authorization, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, body)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	return response
}

func authorizedTestRequest(method, target string, body io.Reader, contentType string) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Header.Set("Authorization", controlBearer)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return request
}

func assertErrorResponse(t *testing.T, response *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d body=%q, want %d", response.Code, response.Body.String(), status)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid error JSON: %v; body=%q", err, response.Body.String())
	}
	assertObjectKeys(t, body, "error")
	errorObject, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error object = %#v", body["error"])
	}
	assertObjectKeys(t, errorObject, "code", "message")
	if errorObject["code"] != code || errorObject["message"] != message {
		t.Fatalf("error = %#v, want %q/%q", errorObject, code, message)
	}
}

func decodePutResponse(t *testing.T, response *httptest.ResponseRecorder) testPutResponse {
	t.Helper()
	var decoded testPutResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	return decoded
}

func decodeGetResponse(t *testing.T, response *httptest.ResponseRecorder) testGetResponse {
	t.Helper()
	var decoded testGetResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	return decoded
}

func assertPutResponse(t *testing.T, response testPutResponse, roomID string, createdAt, expiresAt time.Time) {
	t.Helper()
	if response.RoomID != roomID || response.State != "open" || response.CreatedAt != createdAt.Format(time.RFC3339Nano) ||
		response.ExpiresAt != expiresAt.Format(time.RFC3339Nano) || response.Capacity != 2 ||
		response.RelayEndpoint.Host != "relay.example.net" || response.RelayEndpoint.Port != 30000 ||
		response.ProtocolRevision != protocol.Revision || response.MaxDatagramBytes != protocol.MaxDatagramBytes ||
		response.MaxPayloadBytes != protocol.MaxPayloadBytes || len(response.Grants) != 2 {
		t.Fatalf("PUT response = %#v", response)
	}
	for _, grant := range response.Grants {
		if grant.State != "issued" || grant.GrantSecret == nil || strings.Contains(grant.GrantID, "=") || strings.Contains(*grant.GrantSecret, "=") {
			t.Fatalf("grant encoding/state = %#v", grant)
		}
		grantID, err := base64.RawURLEncoding.DecodeString(grant.GrantID)
		if err != nil || len(grantID) != 16 {
			t.Fatalf("grant_id = %q: bytes=%d err=%v", grant.GrantID, len(grantID), err)
		}
		secret, err := base64.RawURLEncoding.DecodeString(*grant.GrantSecret)
		if err != nil || len(secret) != 32 {
			t.Fatalf("grant_secret = %q: bytes=%d err=%v", *grant.GrantSecret, len(secret), err)
		}
	}
}

func assertExactPutKeys(t *testing.T, body []byte, allSecrets bool) {
	t.Helper()
	object := decodeJSONObject(t, body)
	assertObjectKeys(t, object, "room_id", "state", "created_at", "expires_at", "capacity", "relay_endpoint", "protocol_revision", "max_datagram_bytes", "max_payload_bytes", "grants")
	assertEndpointKeys(t, object)
	grants := object["grants"].([]any)
	for index, item := range grants {
		grant := item.(map[string]any)
		keys := []string{"participant_id", "session_id", "grant_id", "grant_expires_at", "state"}
		if allSecrets || index != 0 {
			keys = append(keys, "grant_secret")
		}
		assertObjectKeys(t, grant, keys...)
	}
	assertAllTimestampStringsEndInZ(t, body)
}

func assertExactGetKeysAndRedaction(t *testing.T, body []byte) {
	t.Helper()
	object := decodeJSONObject(t, body)
	assertObjectKeys(t, object, "room_id", "state", "created_at", "expires_at", "capacity", "relay_endpoint", "protocol_revision", "max_datagram_bytes", "max_payload_bytes", "participants")
	assertEndpointKeys(t, object)
	for _, item := range object["participants"].([]any) {
		participant := item.(map[string]any)
		assertObjectKeys(t, participant, "participant_id", "session_id", "grant_state", "grant_expires_at", "binding_state")
	}
	forbidden := []string{"grant_secret", "grant_id", "derived_key", "challenge_nonce", "binding_id", "observed_endpoint"}
	assertForbiddenKeysAbsent(t, object, forbidden)
}

func assertEndpointKeys(t *testing.T, object map[string]any) {
	t.Helper()
	endpoint, ok := object["relay_endpoint"].(map[string]any)
	if !ok {
		t.Fatalf("relay_endpoint = %#v", object["relay_endpoint"])
	}
	assertObjectKeys(t, endpoint, "host", "port")
}

func decodeJSONObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode JSON object: %v", err)
	}
	return object
}

func assertObjectKeys(t *testing.T, object map[string]any, keys ...string) {
	t.Helper()
	want := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		want[key] = struct{}{}
	}
	if len(object) != len(want) {
		t.Fatalf("object keys = %v, want %v", reflect.ValueOf(object).MapKeys(), keys)
	}
	for key := range object {
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected key %q in %#v", key, object)
		}
	}
}

func assertForbiddenKeysAbsent(t *testing.T, value any, forbidden []string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			for _, blocked := range forbidden {
				if key == blocked {
					t.Fatalf("forbidden response key %q", key)
				}
			}
			assertForbiddenKeysAbsent(t, child, forbidden)
		}
	case []any:
		for _, child := range typed {
			assertForbiddenKeysAbsent(t, child, forbidden)
		}
	}
}

func assertAllTimestampStringsEndInZ(t *testing.T, body []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode timestamps: %v", err)
	}
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if strings.HasSuffix(key, "_at") {
					text, ok := child.(string)
					if !ok || !strings.HasSuffix(text, "Z") {
						t.Fatalf("timestamp %s = %#v, want UTC Z string", key, child)
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
}

func repeatedToken(value byte) [32]byte {
	var token [32]byte
	for index := range token {
		token[index] = value
	}
	return token
}

func nonCanonicalRawURL(value [32]byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	encoded := base64.RawURLEncoding.EncodeToString(value[:])
	last := strings.IndexByte(alphabet, encoded[len(encoded)-1])
	return encoded[:len(encoded)-1] + string(alphabet[last+1])
}

type controlStoreClock struct {
	mu      sync.Mutex
	reading clock.Reading
}

func (fakeClock *controlStoreClock) now() clock.Reading {
	fakeClock.mu.Lock()
	defer fakeClock.mu.Unlock()
	return fakeClock.reading
}

func (fakeClock *controlStoreClock) setMono(value time.Duration) {
	fakeClock.mu.Lock()
	defer fakeClock.mu.Unlock()
	fakeClock.reading.Mono = value
}

type testSequenceReader struct {
	mu    sync.Mutex
	next  uint64
	reads int
}

func (reader *testSequenceReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.reads++
	reader.next++
	for index := range buffer {
		buffer[index] = byte(reader.next)
	}
	return len(buffer), nil
}

func (reader *testSequenceReader) readCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.reads
}

type failOnceReader struct {
	mu       sync.Mutex
	failed   bool
	delegate io.Reader
}

func (reader *failOnceReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if !reader.failed {
		reader.failed = true
		return 0, errors.New("injected random failure")
	}
	return reader.delegate.Read(buffer)
}

type trackingReader struct {
	reader io.Reader
	reads  atomic.Int32
}

func (reader *trackingReader) Read(buffer []byte) (int, error) {
	reader.reads.Add(1)
	return reader.reader.Read(buffer)
}

type blockingReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (reader *blockingReader) Read([]byte) (int, error) {
	reader.once.Do(func() { close(reader.started) })
	<-reader.release
	return 0, io.EOF
}

func startTestServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(): %v", err)
	}
	server := NewServer(listener.Addr().String(), handler)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("server.Close(): %v", err)
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				t.Errorf("Serve(): %v", err)
			}
		case <-time.After(time.Second):
			t.Error("server did not stop")
		}
	})
	return listener.Addr().String()
}
