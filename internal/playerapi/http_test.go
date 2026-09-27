package playerapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/clock"
	"github.com/gyungsubLee/go-lobby-relay/internal/lobby"
	"github.com/gyungsubLee/go-lobby-relay/internal/playerauth"
	"github.com/gyungsubLee/go-lobby-relay/internal/store"
)

var apiWall = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func TestPlayerLobbyLifecycle(t *testing.T) {
	fixture := newFixture(t)
	owner := fixture.token(t, "owner")
	member := fixture.token(t, "member")
	outsider := fixture.token(t, "outsider")

	created := fixture.request(t, owner, http.MethodPost, "/v1/lobbies", `{"visibility":"private","queue_key":"duo","capacity":2}`)
	assertStatus(t, created, http.StatusCreated)
	var lobbyBody lobbyResponse
	decode(t, created, &lobbyBody)
	if lobbyBody.OwnerPlayerID != "owner" || len(lobbyBody.Members) != 1 || lobbyBody.Assignment != nil {
		t.Fatalf("created lobby = %+v raw=%s", lobbyBody, created.Body.String())
	}

	privateGet := fixture.request(t, outsider, http.MethodGet, "/v1/lobbies/"+lobbyBody.LobbyID, "")
	assertStatus(t, privateGet, http.StatusNotFound)
	list := fixture.request(t, outsider, http.MethodGet, "/v1/lobbies?queue_key=duo&limit=20", "")
	assertStatus(t, list, http.StatusOK)
	if got := list.Body.String(); got != "{\"lobbies\":[],\"next_cursor\":\"\"}\n" {
		t.Fatalf("private list leaked: %q", got)
	}

	joined := fixture.request(t, member, http.MethodPost, "/v1/lobbies/"+lobbyBody.LobbyID+"/join", fmt.Sprintf(`{"revision":%d}`, lobbyBody.Revision))
	assertStatus(t, joined, http.StatusOK)
	decode(t, joined, &lobbyBody)
	stale := fixture.request(t, owner, http.MethodPut, "/v1/lobbies/"+lobbyBody.LobbyID+"/members/me/ready", `{"revision":1,"ready":true}`)
	assertStatus(t, stale, http.StatusConflict)

	for _, actor := range []string{owner, member} {
		ready := fixture.request(t, actor, http.MethodPut, "/v1/lobbies/"+lobbyBody.LobbyID+"/members/me/ready", fmt.Sprintf(`{"revision":%d,"ready":true}`, lobbyBody.Revision))
		assertStatus(t, ready, http.StatusOK)
		decode(t, ready, &lobbyBody)
	}
	nonOwner := fixture.request(t, member, http.MethodPost, "/v1/lobbies/"+lobbyBody.LobbyID+"/start", fmt.Sprintf(`{"revision":%d}`, lobbyBody.Revision))
	assertStatus(t, nonOwner, http.StatusForbidden)
	started := fixture.request(t, owner, http.MethodPost, "/v1/lobbies/"+lobbyBody.LobbyID+"/start", fmt.Sprintf(`{"revision":%d}`, lobbyBody.Revision))
	assertStatus(t, started, http.StatusOK)
	var ownerAssignment assignmentResponse
	decode(t, started, &ownerAssignment)
	if ownerAssignment.PlayerID != "owner" || ownerAssignment.RelayEndpoint.Port != 30000 || ownerAssignment.GrantSecret == "" {
		t.Fatalf("owner assignment = %+v", ownerAssignment)
	}
	memberGet := fixture.request(t, member, http.MethodGet, "/v1/lobbies/"+lobbyBody.LobbyID, "")
	assertStatus(t, memberGet, http.StatusOK)
	decode(t, memberGet, &lobbyBody)
	if lobbyBody.Assignment == nil || lobbyBody.Assignment.PlayerID != "member" || lobbyBody.Assignment.GrantSecret == ownerAssignment.GrantSecret {
		t.Fatalf("member assignment = %+v", lobbyBody.Assignment)
	}
}

func TestQuickMatchReturnsPrivateAssignments(t *testing.T) {
	fixture := newFixture(t)
	a := fixture.token(t, "player-a")
	b := fixture.token(t, "player-b")

	first := fixture.request(t, a, http.MethodPost, "/v1/matchmaking/tickets", `{"queue_key":"duo","capacity":2}`)
	assertStatus(t, first, http.StatusCreated)
	var firstTicket ticketResponse
	decode(t, first, &firstTicket)
	if firstTicket.State != "queued" || firstTicket.Assignment != nil {
		t.Fatalf("first ticket = %+v", firstTicket)
	}
	second := fixture.request(t, b, http.MethodPost, "/v1/matchmaking/tickets", `{"queue_key":"duo","capacity":2}`)
	assertStatus(t, second, http.StatusCreated)

	for player, token := range map[string]string{"player-a": a, "player-b": b} {
		got := fixture.request(t, token, http.MethodGet, "/v1/matchmaking/tickets/me", "")
		assertStatus(t, got, http.StatusOK)
		var ticket ticketResponse
		decode(t, got, &ticket)
		if ticket.State != "matched" || ticket.Assignment == nil || ticket.Assignment.PlayerID != player || ticket.Assignment.GrantSecret == "" {
			t.Fatalf("%s ticket = %+v", player, ticket)
		}
	}
	cancelMatched := fixture.request(t, a, http.MethodDelete, "/v1/matchmaking/tickets/me", `{"revision":2}`)
	assertStatus(t, cancelMatched, http.StatusConflict)
}

func TestRemainingPlayerRoutes(t *testing.T) {
	fixture := newFixture(t)
	owner := fixture.token(t, "owner")
	member := fixture.token(t, "member")

	created := fixture.request(t, owner, http.MethodPost, "/v1/lobbies", `{"visibility":"public","queue_key":"duo","capacity":2}`)
	assertStatus(t, created, http.StatusCreated)
	var body lobbyResponse
	decode(t, created, &body)
	listed := fixture.request(t, member, http.MethodGet, "/v1/lobbies?queue_key=duo", "")
	assertStatus(t, listed, http.StatusOK)
	if !bytes.Contains(listed.Body.Bytes(), []byte(body.LobbyID)) {
		t.Fatalf("public lobby missing: %s", listed.Body.String())
	}
	got := fixture.request(t, member, http.MethodGet, "/v1/lobbies/"+body.LobbyID, "")
	assertStatus(t, got, http.StatusOK)
	joined := fixture.request(t, member, http.MethodPost, "/v1/lobbies/"+body.LobbyID+"/join", fmt.Sprintf(`{"revision":%d}`, body.Revision))
	assertStatus(t, joined, http.StatusOK)
	decode(t, joined, &body)
	left := fixture.request(t, member, http.MethodDelete, "/v1/lobbies/"+body.LobbyID+"/members/me", fmt.Sprintf(`{"revision":%d}`, body.Revision))
	assertStatus(t, left, http.StatusOK)

	ticket := fixture.request(t, member, http.MethodPost, "/v1/matchmaking/tickets", `{"queue_key":"duo","capacity":2}`)
	assertStatus(t, ticket, http.StatusCreated)
	var ticketBody ticketResponse
	decode(t, ticket, &ticketBody)
	cancelled := fixture.request(t, member, http.MethodDelete, "/v1/matchmaking/tickets/me", fmt.Sprintf(`{"revision":%d}`, ticketBody.Revision))
	assertStatus(t, cancelled, http.StatusOK)
	decode(t, cancelled, &ticketBody)
	if ticketBody.State != "cancelled" {
		t.Fatalf("cancelled ticket = %+v", ticketBody)
	}
}

func TestPlayerAPIRejectsInvalidAuthAndInput(t *testing.T) {
	fixture := newFixture(t)
	token := fixture.token(t, "player")
	tampered := token[:len(token)-1] + "A"
	for _, test := range []struct {
		name, auth, method, path, body, contentType string
		status                                      int
	}{
		{"missing auth", "", http.MethodGet, "/v1/lobbies?queue_key=duo", "", "", http.StatusUnauthorized},
		{"invalid auth", "Bearer invalid", http.MethodGet, "/v1/lobbies?queue_key=duo", "", "", http.StatusUnauthorized},
		{"tampered auth", "Bearer " + tampered, http.MethodGet, "/v1/lobbies?queue_key=duo", "", "", http.StatusUnauthorized},
		{"unknown route", "Bearer " + token, http.MethodGet, "/v1/nope", "", "", http.StatusNotFound},
		{"unknown field", "Bearer " + token, http.MethodPost, "/v1/lobbies", `{"visibility":"public","queue_key":"duo","capacity":2,"player_id":"admin"}`, "application/json", http.StatusBadRequest},
		{"duplicate field", "Bearer " + token, http.MethodPost, "/v1/lobbies", `{"visibility":"public","queue_key":"duo","capacity":2,"capacity":3}`, "application/json", http.StatusBadRequest},
		{"wrong content type", "Bearer " + token, http.MethodPost, "/v1/lobbies", `{}`, "text/plain", http.StatusUnsupportedMediaType},
		{"extra query", "Bearer " + token, http.MethodGet, "/v1/lobbies?queue_key=duo&admin=true", "", "", http.StatusBadRequest},
		{"wrong method", "Bearer " + token, http.MethodPatch, "/v1/lobbies", "", "", http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
			if test.auth != "" {
				request.Header.Set("Authorization", test.auth)
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			fixture.handler.ServeHTTP(response, request)
			assertStatus(t, response, test.status)
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
		})
	}

	expired := fixture.token(t, "expired")
	*fixture.now = fixture.now.Add(playerauth.HardTokenTTL)
	response := fixture.request(t, expired, http.MethodGet, "/v1/lobbies?queue_key=duo", "")
	assertStatus(t, response, http.StatusUnauthorized)
}

type fixture struct {
	handler http.Handler
	auth    *playerauth.Auth
	now     *time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	now := apiWall
	fakeClock := func() clock.Reading { return clock.Reading{Wall: now, Mono: time.Hour + now.Sub(apiWall)} }
	relayStore, err := store.New(store.Config{Limits: store.DefaultLimits(), Now: fakeClock, Random: &sequenceReader{}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := lobby.New(lobby.Config{Rooms: relayStore, Now: fakeClock, Random: &sequenceReader{next: 1}})
	if err != nil {
		t.Fatal(err)
	}
	secret := [32]byte{1}
	auth, err := playerauth.New(playerauth.Config{OperatorSecret: secret, Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{2}, 32)), TokenTTL: playerauth.HardTokenTTL})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(Config{PlayerTokens: auth, Lobbies: manager, AdvertisedHost: "relay.example.net", AdvertisedPort: 30000, RequestRate: HardPlayerRequestRate, RequestBurst: HardPlayerRequestBurst, MaxConcurrent: HardPlayerConcurrent, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{handler: handler, auth: auth, now: &now}
}

func (fixture fixture) token(t *testing.T, playerID string) string {
	t.Helper()
	token, _, err := fixture.auth.Issue(playerID)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (fixture fixture) request(t *testing.T, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

type sequenceReader struct {
	mu   sync.Mutex
	next uint64
}

func (reader *sequenceReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.next++
	for i := range buffer {
		buffer[i] = byte(reader.next + uint64(i))
	}
	return len(buffer), nil
}

type endpointResponse struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}
type assignmentResponse struct {
	MatchID        string           `json:"match_id"`
	RoomID         string           `json:"room_id"`
	PlayerID       string           `json:"player_id"`
	SessionID      string           `json:"session_id"`
	GrantID        string           `json:"grant_id"`
	GrantSecret    string           `json:"grant_secret"`
	GrantExpiresAt string           `json:"grant_expires_at"`
	RelayEndpoint  endpointResponse `json:"relay_endpoint"`
}
type memberResponse struct {
	PlayerID string `json:"player_id"`
	Ready    bool   `json:"ready"`
}
type lobbyResponse struct {
	LobbyID       string              `json:"lobby_id"`
	OwnerPlayerID string              `json:"owner_player_id"`
	QueueKey      string              `json:"queue_key"`
	Visibility    string              `json:"visibility"`
	Capacity      uint32              `json:"capacity"`
	Revision      uint64              `json:"revision"`
	State         string              `json:"state"`
	Members       []memberResponse    `json:"members"`
	Assignment    *assignmentResponse `json:"assignment,omitempty"`
}
type ticketResponse struct {
	TicketID   string              `json:"ticket_id"`
	PlayerID   string              `json:"player_id"`
	QueueKey   string              `json:"queue_key"`
	State      string              `json:"state"`
	Capacity   uint32              `json:"capacity"`
	Revision   uint64              `json:"revision"`
	Assignment *assignmentResponse `json:"assignment,omitempty"`
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d body=%q want=%d", response.Code, response.Body.String(), status)
	}
}
func decode(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %q: %v", response.Body.String(), err)
	}
}
