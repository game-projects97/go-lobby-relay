package playerapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/httpapi/httpx"
	"github.com/gyungsubLee/go-lobby-relay/internal/matchmaking"
	"github.com/gyungsubLee/go-lobby-relay/internal/playerauth"
	"golang.org/x/time/rate"
)

const (
	HardPlayerRequestRate  = rate.Limit(100)
	HardPlayerRequestBurst = 200
	HardPlayerConcurrent   = 64
)

var errInvalidConfig = errors.New("invalid player API config")

type Config struct {
	PlayerTokens   *playerauth.Issuer
	Lobbies        *matchmaking.Manager
	AdvertisedHost string
	AdvertisedPort uint16
	RequestRate    rate.Limit
	RequestBurst   int
	MaxConcurrent  int
	Now            func() time.Time
	Fatal          func()
}

type handler struct {
	playerTokens   *playerauth.Issuer
	lobbies        *matchmaking.Manager
	advertisedHost string
	advertisedPort uint16
	admission      *httpx.Admission
	fatal          func()
}

func NewHandler(config Config) (http.Handler, error) {
	if config.PlayerTokens == nil || config.Lobbies == nil || config.AdvertisedHost == "" || config.AdvertisedPort == 0 ||
		config.RequestRate <= 0 || config.RequestRate > HardPlayerRequestRate ||
		config.RequestBurst <= 0 || config.RequestBurst > HardPlayerRequestBurst ||
		config.MaxConcurrent <= 0 || config.MaxConcurrent > HardPlayerConcurrent {
		return nil, errInvalidConfig
	}
	return &handler{
		playerTokens: config.PlayerTokens, lobbies: config.Lobbies,
		advertisedHost: config.AdvertisedHost, advertisedPort: config.AdvertisedPort,
		admission: httpx.NewAdmission(config.RequestRate, config.RequestBurst, config.MaxConcurrent, config.Now),
		fatal:     config.Fatal,
	}, nil
}

func NewServer(addr string, handler http.Handler) *http.Server {
	return httpx.NewServer(addr, handler)
}

func (handler *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	claims, ok := handler.authorize(request)
	if !ok {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		httpx.WriteError(writer, http.StatusUnauthorized, "unauthorized", "valid player bearer token required")
		return
	}
	release, admitted := handler.admission.Enter(writer)
	if !admitted {
		return
	}
	defer release()
	handler.route(writer, request, claims.PlayerID)
}

func (handler *handler) authorize(request *http.Request) (playerauth.Claims, bool) {
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return playerauth.Claims{}, false
	}
	token, found := strings.CutPrefix(values[0], "Bearer ")
	if !found {
		return playerauth.Claims{}, false
	}
	claims, err := handler.playerTokens.Verify(token)
	return claims, err == nil
}

func (handler *handler) route(writer http.ResponseWriter, request *http.Request, playerID string) {
	if request.URL.EscapedPath() != request.URL.Path {
		httpx.WriteError(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch request.URL.Path {
	case "/v1/lobbies":
		handler.handleLobbies(writer, request, playerID)
		return
	case "/v1/matchmaking/tickets":
		handler.handleTickets(writer, request, playerID)
		return
	case "/v1/matchmaking/tickets/me":
		handler.handleMyTicket(writer, request, playerID)
		return
	}
	const prefix = "/v1/lobbies/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		httpx.WriteError(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	tail := strings.TrimPrefix(request.URL.Path, prefix)
	parts := strings.Split(tail, "/")
	if len(parts) < 1 || parts[0] == "" {
		httpx.WriteError(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	lobbyID := parts[0]
	switch {
	case len(parts) == 1:
		handler.handleLobby(writer, request, playerID, lobbyID)
	case len(parts) == 2 && parts[1] == "join":
		handler.handleRevisionAction(writer, request, http.MethodPost, func(revision uint64) (any, error) { return handler.lobbies.JoinLobby(playerID, lobbyID, revision) })
	case len(parts) == 2 && parts[1] == "start":
		handler.handleRevisionAction(writer, request, http.MethodPost, func(revision uint64) (any, error) { return handler.lobbies.StartMatch(playerID, lobbyID, revision) })
	case len(parts) == 3 && parts[1] == "members" && parts[2] == "me":
		handler.handleRevisionAction(writer, request, http.MethodDelete, func(revision uint64) (any, error) { return handler.lobbies.LeaveLobby(playerID, lobbyID, revision) })
	case len(parts) == 4 && parts[1] == "members" && parts[2] == "me" && parts[3] == "ready":
		handler.handleReady(writer, request, playerID, lobbyID)
	default:
		httpx.WriteError(writer, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler *handler) handleLobbies(writer http.ResponseWriter, request *http.Request, playerID string) {
	switch request.Method {
	case http.MethodPost:
		if len(request.URL.Query()) != 0 {
			httpx.WriteInvalid(writer)
			return
		}
		var body struct {
			Visibility matchmaking.Visibility `json:"visibility"`
			QueueKey   string                 `json:"queue_key"`
			Capacity   uint32                 `json:"capacity"`
		}
		if !httpx.DecodeExact(writer, request, &body, "visibility", "queue_key", "capacity") {
			return
		}
		result, err := handler.lobbies.CreateLobby(playerID, matchmaking.CreateLobbyRequest{Visibility: body.Visibility, QueueKey: body.QueueKey, Capacity: body.Capacity})
		handler.writeResult(writer, http.StatusCreated, encodeLobby(result, handler.advertisedHost, handler.advertisedPort), err)
	case http.MethodGet:
		if httpx.RequestHasBody(request) {
			httpx.WriteInvalid(writer)
			return
		}
		query := request.URL.Query()
		if !httpx.HasOnlyQueryKeys(query, "queue_key", "cursor", "limit") || len(query["queue_key"]) != 1 {
			httpx.WriteInvalid(writer)
			return
		}
		limit := 20
		var err error
		if values, exists := query["limit"]; exists {
			if len(values) != 1 {
				httpx.WriteInvalid(writer)
				return
			}
			limit, err = strconv.Atoi(values[0])
			if err != nil {
				httpx.WriteInvalid(writer)
				return
			}
		}
		cursor := ""
		if values, exists := query["cursor"]; exists {
			if len(values) != 1 {
				httpx.WriteInvalid(writer)
				return
			}
			cursor = values[0]
		}
		page, listErr := handler.lobbies.ListLobbies(query.Get("queue_key"), cursor, limit)
		handler.writeResult(writer, http.StatusOK, lobbyPageResponse(page), listErr)
	default:
		httpx.WriteMethodNotAllowed(writer, "POST, GET")
	}
}

func (handler *handler) handleLobby(writer http.ResponseWriter, request *http.Request, playerID, lobbyID string) {
	if request.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(writer, "GET")
		return
	}
	if httpx.RequestHasBody(request) || len(request.URL.Query()) != 0 {
		httpx.WriteInvalid(writer)
		return
	}
	result, err := handler.lobbies.GetLobby(playerID, lobbyID)
	handler.writeResult(writer, http.StatusOK, encodeLobby(result, handler.advertisedHost, handler.advertisedPort), err)
}

func (handler *handler) handleRevisionAction(writer http.ResponseWriter, request *http.Request, method string, action func(uint64) (any, error)) {
	if request.Method != method {
		httpx.WriteMethodNotAllowed(writer, method)
		return
	}
	if len(request.URL.Query()) != 0 {
		httpx.WriteInvalid(writer)
		return
	}
	var body struct {
		Revision uint64 `json:"revision"`
	}
	if !httpx.DecodeExact(writer, request, &body, "revision") {
		return
	}
	result, err := action(body.Revision)
	handler.writeResult(writer, http.StatusOK, handler.encodeActionResult(result), err)
}

func (handler *handler) handleReady(writer http.ResponseWriter, request *http.Request, playerID, lobbyID string) {
	if request.Method != http.MethodPut {
		httpx.WriteMethodNotAllowed(writer, "PUT")
		return
	}
	if len(request.URL.Query()) != 0 {
		httpx.WriteInvalid(writer)
		return
	}
	var body struct {
		Revision uint64 `json:"revision"`
		Ready    bool   `json:"ready"`
	}
	if !httpx.DecodeExact(writer, request, &body, "revision", "ready") {
		return
	}
	result, err := handler.lobbies.SetReady(playerID, lobbyID, body.Revision, body.Ready)
	handler.writeResult(writer, http.StatusOK, encodeLobby(result, handler.advertisedHost, handler.advertisedPort), err)
}

func (handler *handler) handleTickets(writer http.ResponseWriter, request *http.Request, playerID string) {
	if request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer, "POST")
		return
	}
	if len(request.URL.Query()) != 0 {
		httpx.WriteInvalid(writer)
		return
	}
	var body struct {
		QueueKey string `json:"queue_key"`
		Capacity uint32 `json:"capacity"`
	}
	if !httpx.DecodeExact(writer, request, &body, "queue_key", "capacity") {
		return
	}
	result, err := handler.lobbies.CreateTicket(playerID, matchmaking.CreateTicketRequest{QueueKey: body.QueueKey, Capacity: body.Capacity})
	handler.writeResult(writer, http.StatusCreated, encodeTicket(result, handler.advertisedHost, handler.advertisedPort), err)
}

func (handler *handler) handleMyTicket(writer http.ResponseWriter, request *http.Request, playerID string) {
	switch request.Method {
	case http.MethodGet:
		if httpx.RequestHasBody(request) || len(request.URL.Query()) != 0 {
			httpx.WriteInvalid(writer)
			return
		}
		result, err := handler.lobbies.GetTicket(playerID)
		handler.writeResult(writer, http.StatusOK, encodeTicket(result, handler.advertisedHost, handler.advertisedPort), err)
	case http.MethodDelete:
		if len(request.URL.Query()) != 0 {
			httpx.WriteInvalid(writer)
			return
		}
		var body struct {
			Revision uint64 `json:"revision"`
		}
		if !httpx.DecodeExact(writer, request, &body, "revision") {
			return
		}
		result, err := handler.lobbies.CancelTicket(playerID, body.Revision)
		handler.writeResult(writer, http.StatusOK, encodeTicket(result, handler.advertisedHost, handler.advertisedPort), err)
	default:
		httpx.WriteMethodNotAllowed(writer, "GET, DELETE")
	}
}

func (handler *handler) encodeActionResult(value any) any {
	switch typed := value.(type) {
	case matchmaking.LobbySnapshot:
		return encodeLobby(typed, handler.advertisedHost, handler.advertisedPort)
	case matchmaking.Assignment:
		return encodeAssignment(typed, handler.advertisedHost, handler.advertisedPort)
	default:
		return value
	}
}

func (handler *handler) writeResult(writer http.ResponseWriter, status int, value any, err error) {
	if err == nil {
		httpx.WriteJSON(writer, status, value)
		return
	}
	switch {
	case errors.Is(err, matchmaking.ErrInvalid):
		httpx.WriteInvalid(writer)
	case errors.Is(err, matchmaking.ErrNotFound):
		httpx.WriteError(writer, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, matchmaking.ErrConflict):
		httpx.WriteError(writer, http.StatusConflict, "conflict", "state or revision conflict")
	case errors.Is(err, matchmaking.ErrForbidden):
		httpx.WriteError(writer, http.StatusForbidden, "forbidden", "operation is not allowed")
	case errors.Is(err, matchmaking.ErrCapacity):
		httpx.WriteError(writer, http.StatusConflict, "capacity", "capacity limit exceeded")
	case errors.Is(err, matchmaking.ErrUnavailable):
		httpx.WriteError(writer, http.StatusServiceUnavailable, "unavailable", "service temporarily unavailable")
	default:
		httpx.WriteError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		if errors.Is(err, matchmaking.ErrFatalRandom) {
			httpx.NotifyFatal(writer, handler.fatal)
		}
	}
}

type relayEndpointResponse struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}
type assignmentResponse struct {
	MatchID        string                `json:"match_id"`
	RoomID         string                `json:"room_id"`
	PlayerID       string                `json:"player_id"`
	SessionID      string                `json:"session_id"`
	GrantID        string                `json:"grant_id"`
	GrantSecret    string                `json:"grant_secret"`
	GrantExpiresAt string                `json:"grant_expires_at"`
	RelayEndpoint  relayEndpointResponse `json:"relay_endpoint"`
}
type lobbyResponse struct {
	LobbyID       string                 `json:"lobby_id"`
	OwnerPlayerID string                 `json:"owner_player_id"`
	QueueKey      string                 `json:"queue_key"`
	Visibility    matchmaking.Visibility `json:"visibility"`
	Capacity      uint32                 `json:"capacity"`
	Revision      uint64                 `json:"revision"`
	State         matchmaking.LobbyState `json:"state"`
	Members       []memberResponse       `json:"members"`
	ExpiresAt     string                 `json:"expires_at"`
	Assignment    *assignmentResponse    `json:"assignment,omitempty"`
}
type memberResponse struct {
	PlayerID string `json:"player_id"`
	Ready    bool   `json:"ready"`
}
type lobbySummaryResponse struct {
	LobbyID       string                 `json:"lobby_id"`
	OwnerPlayerID string                 `json:"owner_player_id"`
	QueueKey      string                 `json:"queue_key"`
	Visibility    matchmaking.Visibility `json:"visibility"`
	Capacity      uint32                 `json:"capacity"`
	MemberCount   uint32                 `json:"member_count"`
	Revision      uint64                 `json:"revision"`
	ExpiresAt     string                 `json:"expires_at"`
}
type ticketResponse struct {
	TicketID   string                  `json:"ticket_id"`
	PlayerID   string                  `json:"player_id"`
	QueueKey   string                  `json:"queue_key"`
	State      matchmaking.TicketState `json:"state"`
	Capacity   uint32                  `json:"capacity"`
	Revision   uint64                  `json:"revision"`
	ExpiresAt  string                  `json:"expires_at"`
	Assignment *assignmentResponse     `json:"assignment,omitempty"`
}

func encodeAssignment(value matchmaking.Assignment, host string, port uint16) assignmentResponse {
	return assignmentResponse{MatchID: value.MatchID, RoomID: value.RoomID, PlayerID: value.PlayerID, SessionID: value.SessionID,
		GrantID: base64.RawURLEncoding.EncodeToString(value.GrantID[:]), GrantSecret: base64.RawURLEncoding.EncodeToString(value.GrantSecret[:]),
		GrantExpiresAt: value.GrantExpiresAt.UTC().Format(time.RFC3339Nano), RelayEndpoint: relayEndpointResponse{host, port}}
}
func encodeLobby(value matchmaking.LobbySnapshot, host string, port uint16) lobbyResponse {
	result := lobbyResponse{LobbyID: value.LobbyID, OwnerPlayerID: value.OwnerPlayerID, QueueKey: value.QueueKey, Visibility: value.Visibility,
		Capacity: value.Capacity, Revision: value.Revision, State: value.State, Members: make([]memberResponse, len(value.Members)), ExpiresAt: value.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	for index, member := range value.Members {
		result.Members[index] = memberResponse{PlayerID: member.PlayerID, Ready: member.Ready}
	}
	if value.Assignment != nil {
		item := encodeAssignment(*value.Assignment, host, port)
		result.Assignment = &item
	}
	return result
}
func encodeTicket(value matchmaking.TicketSnapshot, host string, port uint16) ticketResponse {
	result := ticketResponse{TicketID: value.TicketID, PlayerID: value.PlayerID, QueueKey: value.QueueKey, State: value.State,
		Capacity: value.Capacity, Revision: value.Revision, ExpiresAt: value.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	if value.Assignment != nil {
		item := encodeAssignment(*value.Assignment, host, port)
		result.Assignment = &item
	}
	return result
}
func lobbyPageResponse(value matchmaking.LobbyPage) any {
	lobbies := make([]lobbySummaryResponse, len(value.Lobbies))
	for index, item := range value.Lobbies {
		lobbies[index] = lobbySummaryResponse{LobbyID: item.LobbyID, OwnerPlayerID: item.OwnerPlayerID, QueueKey: item.QueueKey,
			Visibility: item.Visibility, Capacity: item.Capacity, MemberCount: item.MemberCount, Revision: item.Revision,
			ExpiresAt: item.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	}
	return struct {
		Lobbies    []lobbySummaryResponse `json:"lobbies"`
		NextCursor string                 `json:"next_cursor"`
	}{lobbies, value.NextCursor}
}
