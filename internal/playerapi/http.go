package playerapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/lobby"
	"github.com/gyungsubLee/go-lobby-relay/internal/playerauth"
	"golang.org/x/time/rate"
)

const (
	HardPlayerRequestRate  = rate.Limit(100)
	HardPlayerRequestBurst = 200
	HardPlayerConcurrent   = 64
	maxBodyBytes           = 64 << 10
)

var errInvalidConfig = errors.New("invalid player API config")

type Config struct {
	PlayerTokens   *playerauth.Auth
	Lobbies        *lobby.Manager
	AdvertisedHost string
	AdvertisedPort uint16
	RequestRate    rate.Limit
	RequestBurst   int
	MaxConcurrent  int
	Now            func() time.Time
	Fatal          func()
}

type handler struct {
	playerTokens   *playerauth.Auth
	lobbies        *lobby.Manager
	advertisedHost string
	advertisedPort uint16
	limiter        *rate.Limiter
	semaphore      chan struct{}
	now            func() time.Time
	fatal          func()
}

func NewHandler(config Config) (http.Handler, error) {
	if config.PlayerTokens == nil || config.Lobbies == nil || config.AdvertisedHost == "" || config.AdvertisedPort == 0 ||
		config.RequestRate <= 0 || config.RequestRate > HardPlayerRequestRate ||
		config.RequestBurst <= 0 || config.RequestBurst > HardPlayerRequestBurst ||
		config.MaxConcurrent <= 0 || config.MaxConcurrent > HardPlayerConcurrent {
		return nil, errInvalidConfig
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &handler{
		playerTokens: config.PlayerTokens, lobbies: config.Lobbies,
		advertisedHost: config.AdvertisedHost, advertisedPort: config.AdvertisedPort,
		limiter:   rate.NewLimiter(config.RequestRate, config.RequestBurst),
		semaphore: make(chan struct{}, config.MaxConcurrent), now: now, fatal: config.Fatal,
	}, nil
}

func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, DisableGeneralOptionsHandler: true, MaxHeaderBytes: 16 << 10,
		ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
}

func (handler *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	claims, ok := handler.authorize(request)
	if !ok {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeError(writer, http.StatusUnauthorized, "unauthorized", "valid player bearer token required")
		return
	}
	if !handler.limiter.AllowN(handler.now(), 1) {
		writeError(writer, http.StatusTooManyRequests, "rate_limited", "request rate or concurrency limit exceeded")
		return
	}
	select {
	case handler.semaphore <- struct{}{}:
		defer func() { <-handler.semaphore }()
	default:
		writeError(writer, http.StatusTooManyRequests, "rate_limited", "request rate or concurrency limit exceeded")
		return
	}
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
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch request.URL.Path {
	case "/v1/lobbies":
		handler.lobbiesRoute(writer, request, playerID)
		return
	case "/v1/matchmaking/tickets":
		handler.ticketsRoute(writer, request, playerID)
		return
	case "/v1/matchmaking/tickets/me":
		handler.myTicketRoute(writer, request, playerID)
		return
	}
	const prefix = "/v1/lobbies/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	tail := strings.TrimPrefix(request.URL.Path, prefix)
	parts := strings.Split(tail, "/")
	if len(parts) < 1 || parts[0] == "" {
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	lobbyID := parts[0]
	switch {
	case len(parts) == 1:
		handler.oneLobbyRoute(writer, request, playerID, lobbyID)
	case len(parts) == 2 && parts[1] == "join":
		handler.revisionRoute(writer, request, http.MethodPost, func(revision uint64) (any, error) { return handler.lobbies.Join(playerID, lobbyID, revision) })
	case len(parts) == 2 && parts[1] == "start":
		handler.revisionRoute(writer, request, http.MethodPost, func(revision uint64) (any, error) { return handler.lobbies.Start(playerID, lobbyID, revision) })
	case len(parts) == 3 && parts[1] == "members" && parts[2] == "me":
		handler.revisionRoute(writer, request, http.MethodDelete, func(revision uint64) (any, error) { return handler.lobbies.Leave(playerID, lobbyID, revision) })
	case len(parts) == 4 && parts[1] == "members" && parts[2] == "me" && parts[3] == "ready":
		handler.readyRoute(writer, request, playerID, lobbyID)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler *handler) lobbiesRoute(writer http.ResponseWriter, request *http.Request, playerID string) {
	switch request.Method {
	case http.MethodPost:
		if !noQuery(request.URL.Query()) {
			invalid(writer)
			return
		}
		var body struct {
			Visibility lobby.Visibility `json:"visibility"`
			QueueKey   string           `json:"queue_key"`
			Capacity   uint32           `json:"capacity"`
		}
		if !decodeExact(writer, request, &body, "visibility", "queue_key", "capacity") {
			return
		}
		result, err := handler.lobbies.Create(playerID, lobby.CreateRequest{Visibility: body.Visibility, QueueKey: body.QueueKey, Capacity: body.Capacity})
		handler.writeResult(writer, http.StatusCreated, encodeLobby(result, handler.advertisedHost, handler.advertisedPort), err)
	case http.MethodGet:
		if requestHasBody(request) {
			invalid(writer)
			return
		}
		query := request.URL.Query()
		if !exactQuery(query, "queue_key", "cursor", "limit") || len(query["queue_key"]) != 1 {
			invalid(writer)
			return
		}
		limit := 20
		var err error
		if values, exists := query["limit"]; exists {
			if len(values) != 1 {
				invalid(writer)
				return
			}
			limit, err = strconv.Atoi(values[0])
			if err != nil {
				invalid(writer)
				return
			}
		}
		cursor := ""
		if values, exists := query["cursor"]; exists {
			if len(values) != 1 {
				invalid(writer)
				return
			}
			cursor = values[0]
		}
		page, listErr := handler.lobbies.List(query.Get("queue_key"), cursor, limit)
		handler.writeResult(writer, http.StatusOK, pageResponse(page), listErr)
	default:
		methodNotAllowed(writer, "POST, GET")
	}
}

func (handler *handler) oneLobbyRoute(writer http.ResponseWriter, request *http.Request, playerID, lobbyID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, "GET")
		return
	}
	if requestHasBody(request) || !noQuery(request.URL.Query()) {
		invalid(writer)
		return
	}
	result, err := handler.lobbies.Get(playerID, lobbyID)
	handler.writeResult(writer, http.StatusOK, encodeLobby(result, handler.advertisedHost, handler.advertisedPort), err)
}

func (handler *handler) revisionRoute(writer http.ResponseWriter, request *http.Request, method string, action func(uint64) (any, error)) {
	if request.Method != method {
		methodNotAllowed(writer, method)
		return
	}
	if !noQuery(request.URL.Query()) {
		invalid(writer)
		return
	}
	var body struct {
		Revision uint64 `json:"revision"`
	}
	if !decodeExact(writer, request, &body, "revision") {
		return
	}
	result, err := action(body.Revision)
	handler.writeResult(writer, http.StatusOK, handler.response(result), err)
}

func (handler *handler) readyRoute(writer http.ResponseWriter, request *http.Request, playerID, lobbyID string) {
	if request.Method != http.MethodPut {
		methodNotAllowed(writer, "PUT")
		return
	}
	if !noQuery(request.URL.Query()) {
		invalid(writer)
		return
	}
	var body struct {
		Revision uint64 `json:"revision"`
		Ready    bool   `json:"ready"`
	}
	if !decodeExact(writer, request, &body, "revision", "ready") {
		return
	}
	result, err := handler.lobbies.SetReady(playerID, lobbyID, body.Revision, body.Ready)
	handler.writeResult(writer, http.StatusOK, encodeLobby(result, handler.advertisedHost, handler.advertisedPort), err)
}

func (handler *handler) ticketsRoute(writer http.ResponseWriter, request *http.Request, playerID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, "POST")
		return
	}
	if !noQuery(request.URL.Query()) {
		invalid(writer)
		return
	}
	var body struct {
		QueueKey string `json:"queue_key"`
		Capacity uint32 `json:"capacity"`
	}
	if !decodeExact(writer, request, &body, "queue_key", "capacity") {
		return
	}
	result, err := handler.lobbies.Enqueue(playerID, lobby.EnqueueRequest{QueueKey: body.QueueKey, Capacity: body.Capacity})
	handler.writeResult(writer, http.StatusCreated, encodeTicket(result, handler.advertisedHost, handler.advertisedPort), err)
}

func (handler *handler) myTicketRoute(writer http.ResponseWriter, request *http.Request, playerID string) {
	switch request.Method {
	case http.MethodGet:
		if requestHasBody(request) || !noQuery(request.URL.Query()) {
			invalid(writer)
			return
		}
		result, err := handler.lobbies.GetTicket(playerID)
		handler.writeResult(writer, http.StatusOK, encodeTicket(result, handler.advertisedHost, handler.advertisedPort), err)
	case http.MethodDelete:
		if !noQuery(request.URL.Query()) {
			invalid(writer)
			return
		}
		var body struct {
			Revision uint64 `json:"revision"`
		}
		if !decodeExact(writer, request, &body, "revision") {
			return
		}
		result, err := handler.lobbies.CancelTicket(playerID, body.Revision)
		handler.writeResult(writer, http.StatusOK, encodeTicket(result, handler.advertisedHost, handler.advertisedPort), err)
	default:
		methodNotAllowed(writer, "GET, DELETE")
	}
}

func (handler *handler) response(value any) any {
	switch typed := value.(type) {
	case lobby.LobbySnapshot:
		return encodeLobby(typed, handler.advertisedHost, handler.advertisedPort)
	case lobby.Assignment:
		return encodeAssignment(typed, handler.advertisedHost, handler.advertisedPort)
	default:
		return value
	}
}

func (handler *handler) writeResult(writer http.ResponseWriter, status int, value any, err error) {
	if err == nil {
		writeJSON(writer, status, value)
		return
	}
	switch {
	case errors.Is(err, lobby.ErrInvalid):
		invalid(writer)
	case errors.Is(err, lobby.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, lobby.ErrConflict):
		writeError(writer, http.StatusConflict, "conflict", "state or revision conflict")
	case errors.Is(err, lobby.ErrForbidden):
		writeError(writer, http.StatusForbidden, "forbidden", "operation is not allowed")
	case errors.Is(err, lobby.ErrCapacity):
		writeError(writer, http.StatusConflict, "capacity", "capacity limit exceeded")
	case errors.Is(err, lobby.ErrUnavailable):
		writeError(writer, http.StatusServiceUnavailable, "unavailable", "service temporarily unavailable")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		if errors.Is(err, lobby.ErrFatalRandom) && handler.fatal != nil {
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			handler.fatal()
		}
	}
}

type relayEndpoint struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}
type assignmentJSON struct {
	MatchID        string        `json:"match_id"`
	RoomID         string        `json:"room_id"`
	PlayerID       string        `json:"player_id"`
	SessionID      string        `json:"session_id"`
	GrantID        string        `json:"grant_id"`
	GrantSecret    string        `json:"grant_secret"`
	GrantExpiresAt string        `json:"grant_expires_at"`
	RelayEndpoint  relayEndpoint `json:"relay_endpoint"`
}
type lobbyJSON struct {
	LobbyID       string           `json:"lobby_id"`
	OwnerPlayerID string           `json:"owner_player_id"`
	QueueKey      string           `json:"queue_key"`
	Visibility    lobby.Visibility `json:"visibility"`
	Capacity      uint32           `json:"capacity"`
	Revision      uint64           `json:"revision"`
	State         lobby.LobbyState `json:"state"`
	Members       []memberJSON     `json:"members"`
	ExpiresAt     string           `json:"expires_at"`
	Assignment    *assignmentJSON  `json:"assignment,omitempty"`
}
type memberJSON struct {
	PlayerID string `json:"player_id"`
	Ready    bool   `json:"ready"`
}
type summaryJSON struct {
	LobbyID       string           `json:"lobby_id"`
	OwnerPlayerID string           `json:"owner_player_id"`
	QueueKey      string           `json:"queue_key"`
	Visibility    lobby.Visibility `json:"visibility"`
	Capacity      uint32           `json:"capacity"`
	MemberCount   uint32           `json:"member_count"`
	Revision      uint64           `json:"revision"`
	ExpiresAt     string           `json:"expires_at"`
}
type ticketJSON struct {
	TicketID   string            `json:"ticket_id"`
	PlayerID   string            `json:"player_id"`
	QueueKey   string            `json:"queue_key"`
	State      lobby.TicketState `json:"state"`
	Capacity   uint32            `json:"capacity"`
	Revision   uint64            `json:"revision"`
	ExpiresAt  string            `json:"expires_at"`
	Assignment *assignmentJSON   `json:"assignment,omitempty"`
}

func encodeAssignment(value lobby.Assignment, host string, port uint16) assignmentJSON {
	return assignmentJSON{MatchID: value.MatchID, RoomID: value.RoomID, PlayerID: value.PlayerID, SessionID: value.SessionID,
		GrantID: base64.RawURLEncoding.EncodeToString(value.GrantID[:]), GrantSecret: base64.RawURLEncoding.EncodeToString(value.GrantSecret[:]),
		GrantExpiresAt: value.GrantExpiresAt.UTC().Format(time.RFC3339Nano), RelayEndpoint: relayEndpoint{host, port}}
}
func encodeLobby(value lobby.LobbySnapshot, host string, port uint16) lobbyJSON {
	result := lobbyJSON{LobbyID: value.LobbyID, OwnerPlayerID: value.OwnerPlayerID, QueueKey: value.QueueKey, Visibility: value.Visibility,
		Capacity: value.Capacity, Revision: value.Revision, State: value.State, Members: make([]memberJSON, len(value.Members)), ExpiresAt: value.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	for index, member := range value.Members {
		result.Members[index] = memberJSON{PlayerID: member.PlayerID, Ready: member.Ready}
	}
	if value.Assignment != nil {
		item := encodeAssignment(*value.Assignment, host, port)
		result.Assignment = &item
	}
	return result
}
func encodeTicket(value lobby.TicketSnapshot, host string, port uint16) ticketJSON {
	result := ticketJSON{TicketID: value.TicketID, PlayerID: value.PlayerID, QueueKey: value.QueueKey, State: value.State,
		Capacity: value.Capacity, Revision: value.Revision, ExpiresAt: value.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	if value.Assignment != nil {
		item := encodeAssignment(*value.Assignment, host, port)
		result.Assignment = &item
	}
	return result
}
func pageResponse(value lobby.LobbyPage) any {
	lobbies := make([]summaryJSON, len(value.Lobbies))
	for index, item := range value.Lobbies {
		lobbies[index] = summaryJSON{LobbyID: item.LobbyID, OwnerPlayerID: item.OwnerPlayerID, QueueKey: item.QueueKey,
			Visibility: item.Visibility, Capacity: item.Capacity, MemberCount: item.MemberCount, Revision: item.Revision,
			ExpiresAt: item.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	}
	return struct {
		Lobbies    []summaryJSON `json:"lobbies"`
		NextCursor string        `json:"next_cursor"`
	}{lobbies, value.NextCursor}
}

func decodeExact(writer http.ResponseWriter, request *http.Request, target any, keys ...string) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		if _, ok := err.(*http.MaxBytesError); ok {
			writeError(writer, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 65536 bytes")
		} else {
			invalid(writer)
		}
		return false
	}
	var object map[string]json.RawMessage
	if !uniqueJSON(body) || json.Unmarshal(body, &object) != nil || !exactKeys(object, keys...) {
		invalid(writer)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		invalid(writer)
		return false
	}
	return true
}
func uniqueJSON(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if !scanValue(decoder) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
func scanValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return false
			}
			if _, exists := seen[key]; exists {
				return false
			}
			seen[key] = struct{}{}
			if !scanValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !scanValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	}
	return false
}
func exactKeys(object map[string]json.RawMessage, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}
func exactQuery(query url.Values, allowed ...string) bool {
	allowedSet := map[string]bool{}
	for _, key := range allowed {
		allowedSet[key] = true
	}
	for key, values := range query {
		if !allowedSet[key] || len(values) != 1 {
			return false
		}
	}
	return true
}
func noQuery(query url.Values) bool { return len(query) == 0 }
func requestHasBody(request *http.Request) bool {
	if request.Body == nil || request.ContentLength == 0 {
		return false
	}
	if request.ContentLength > 0 {
		return true
	}
	var one [1]byte
	read, err := request.Body.Read(one[:])
	return read != 0 || err != io.EOF
}
func invalid(writer http.ResponseWriter) {
	writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
}
func methodNotAllowed(writer http.ResponseWriter, allow string) {
	writer.Header().Set("Allow", allow)
	writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}
func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message}})
}
func writeJSON(writer http.ResponseWriter, status int, value any) {
	encoded, _ := json.Marshal(value)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(encoded, '\n'))
}
