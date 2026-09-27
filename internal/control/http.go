package control

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/playerauth"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"github.com/gyungsubLee/go-lobby-relay/internal/store"
	"golang.org/x/time/rate"
)

const (
	HardManagementRequestRate  = rate.Limit(20)
	HardManagementRequestBurst = 40
	HardManagementConcurrent   = 32

	maxRequestBodyBytes = 64 << 10
)

var errInvalidConfig = errors.New("invalid control config")

var errInvalidOperatorToken = errors.New("invalid operator token")

type Config struct {
	OperatorToken  [32]byte
	PlayerTokens   *playerauth.Auth
	AdvertisedHost string
	AdvertisedPort uint16
	RequestRate    rate.Limit
	RequestBurst   int
	MaxConcurrent  int
	Now            func() time.Time
	Fatal          func()
}

type handler struct {
	operatorToken  [32]byte
	advertisedHost string
	advertisedPort uint16
	rooms          *store.Store
	playerTokens   *playerauth.Auth
	limiter        *rate.Limiter
	semaphore      chan struct{}
	now            func() time.Time
	fatal          func()
}

func NewHandler(config Config, rooms *store.Store) (http.Handler, error) {
	if rooms == nil || config.PlayerTokens == nil || config.OperatorToken == [32]byte{} || config.AdvertisedHost == "" || config.AdvertisedPort == 0 ||
		!(config.RequestRate > 0 && config.RequestRate <= HardManagementRequestRate) ||
		config.RequestBurst <= 0 || config.RequestBurst > HardManagementRequestBurst ||
		config.MaxConcurrent <= 0 || config.MaxConcurrent > HardManagementConcurrent {
		return nil, errInvalidConfig
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &handler{
		operatorToken:  config.OperatorToken,
		advertisedHost: config.AdvertisedHost,
		advertisedPort: config.AdvertisedPort,
		rooms:          rooms,
		playerTokens:   config.PlayerTokens,
		limiter:        rate.NewLimiter(config.RequestRate, config.RequestBurst),
		semaphore:      make(chan struct{}, config.MaxConcurrent),
		now:            now,
		fatal:          config.Fatal,
	}, nil
}

func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:                         addr,
		Handler:                      handler,
		DisableGeneralOptionsHandler: true,
		MaxHeaderBytes:               16 << 10,
		ReadHeaderTimeout:            2 * time.Second,
		ReadTimeout:                  5 * time.Second,
		WriteTimeout:                 5 * time.Second,
		IdleTimeout:                  30 * time.Second,
	}
}

func ParseOperatorToken(encoded string) ([32]byte, error) {
	if len(encoded) != 43 {
		return [32]byte{}, errInvalidOperatorToken
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, errInvalidOperatorToken
	}
	var token [32]byte
	copy(token[:], decoded)
	if token == ([32]byte{}) {
		return [32]byte{}, errInvalidOperatorToken
	}
	return token, nil
}

func (handler *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	roomID, roomRoute := canonicalRoomPath(request)
	tokenRoute := request.URL.EscapedPath() == request.URL.Path && request.URL.Path == "/v1/player-tokens"
	if !roomRoute && !tokenRoute {
		writeError(writer, http.StatusNotFound, "not_found", "room not found")
		return
	}
	if !authorized(request, handler.operatorToken) {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeError(writer, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
		return
	}
	if roomRoute && !protocol.ValidID(roomID) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	if tokenRoute && request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if roomRoute && request.Method != http.MethodPut && request.Method != http.MethodGet && request.Method != http.MethodDelete {
		writer.Header().Set("Allow", "PUT, GET, DELETE")
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
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
	if tokenRoute {
		handler.postPlayerToken(writer, request)
		return
	}

	switch request.Method {
	case http.MethodPut:
		handler.putRoom(writer, request, roomID)
	case http.MethodGet:
		if requestHasBody(request) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		handler.getRoom(writer, roomID)
	case http.MethodDelete:
		if requestHasBody(request) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		handler.deleteRoom(writer, roomID)
	}
}

func (handler *handler) postPlayerToken(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		PlayerID string `json:"player_id"`
	}
	if !decodeExactJSON(writer, request, &body, "player_id") {
		return
	}
	if !protocol.ValidID(body.PlayerID) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	token, claims, err := handler.playerTokens.Issue(body.PlayerID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		if errors.Is(err, playerauth.ErrFatalRandom) && handler.fatal != nil {
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			handler.fatal()
		}
		return
	}
	writeJSON(writer, http.StatusCreated, struct {
		PlayerID  string `json:"player_id"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}{body.PlayerID, token, claims.ExpiresAt.UTC().Format(time.RFC3339Nano)})
}

func decodeExactJSON(writer http.ResponseWriter, request *http.Request, target any, keys ...string) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 65536 bytes")
		} else {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		}
		return false
	}
	var object map[string]json.RawMessage
	if !hasUniqueJSONFields(body) || json.Unmarshal(body, &object) != nil || !hasExactKeys(object, keys...) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return false
	}
	return true
}

func canonicalRoomPath(request *http.Request) (string, bool) {
	const prefix = "/v1/rooms/"
	if request.URL.EscapedPath() != request.URL.Path || !strings.HasPrefix(request.URL.Path, prefix) {
		return "", false
	}
	roomID := strings.TrimPrefix(request.URL.Path, prefix)
	return roomID, roomID != "" && !strings.Contains(roomID, "/")
}

func authorized(request *http.Request, expected [32]byte) bool {
	var candidate [32]byte
	valid := 0
	values := request.Header.Values("Authorization")
	if len(values) == 1 {
		encoded, found := strings.CutPrefix(values[0], "Bearer ")
		if found {
			if parsed, err := ParseOperatorToken(encoded); err == nil {
				candidate = parsed
				valid = 1
			}
		}
	}
	equal := subtle.ConstantTimeCompare(candidate[:], expected[:])
	return valid&equal == 1
}

func requestHasBody(request *http.Request) bool {
	if request.Body == nil || request.ContentLength == 0 {
		return false
	}
	if request.ContentLength > 0 {
		return true
	}
	var oneByte [1]byte
	read, err := request.Body.Read(oneByte[:])
	return read != 0 || err != io.EOF
}

func (handler *handler) putRoom(writer http.ResponseWriter, request *http.Request, roomID string) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 65536 bytes")
			return
		}
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	definition, ok := decodeRoomDefinition(body)
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	allocation, created, err := handler.rooms.CreateRoom(roomID, definition)
	if err != nil {
		writeStoreError(writer, err)
		if errors.Is(err, store.ErrFatalRandom) && handler.fatal != nil {
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			handler.fatal()
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, allocationResponse(allocation, handler.advertisedHost, handler.advertisedPort))
}

func (handler *handler) getRoom(writer http.ResponseWriter, roomID string) {
	snapshot, err := handler.rooms.GetRoom(roomID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshotResponse(snapshot, handler.advertisedHost, handler.advertisedPort))
}

func (handler *handler) deleteRoom(writer http.ResponseWriter, roomID string) {
	if err := handler.rooms.EndRoom(roomID); err != nil {
		writeStoreError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

type createRoomRequest struct {
	Capacity     uint32 `json:"capacity"`
	ExpiresAt    string `json:"expires_at"`
	Participants []struct {
		ParticipantID  string `json:"participant_id"`
		SessionID      string `json:"session_id"`
		GrantExpiresAt string `json:"grant_expires_at"`
	} `json:"participants"`
}

func decodeRoomDefinition(body []byte) (store.RoomDefinition, bool) {
	if !hasUniqueJSONFields(body) || !hasExactRoomRequestFields(body) {
		return store.RoomDefinition{}, false
	}
	var request createRoomRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return store.RoomDefinition{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return store.RoomDefinition{}, false
	}
	expiresAt, ok := canonicalUTCTime(request.ExpiresAt)
	if !ok {
		return store.RoomDefinition{}, false
	}
	definition := store.RoomDefinition{
		Capacity:     request.Capacity,
		ExpiresAt:    expiresAt,
		Participants: make([]store.ParticipantDefinition, len(request.Participants)),
	}
	for index, participant := range request.Participants {
		grantExpiresAt, ok := canonicalUTCTime(participant.GrantExpiresAt)
		if !ok {
			return store.RoomDefinition{}, false
		}
		definition.Participants[index] = store.ParticipantDefinition{
			ParticipantID:  participant.ParticipantID,
			SessionID:      participant.SessionID,
			GrantExpiresAt: grantExpiresAt,
		}
	}
	return definition, true
}

func hasExactRoomRequestFields(body []byte) bool {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || !hasExactKeys(request, "capacity", "expires_at", "participants") {
		return false
	}
	var participants []map[string]json.RawMessage
	if json.Unmarshal(request["participants"], &participants) != nil {
		return false
	}
	for _, participant := range participants {
		if !hasExactKeys(participant, "participant_id", "session_id", "grant_expires_at") {
			return false
		}
	}
	return true
}

func hasExactKeys(object map[string]json.RawMessage, keys ...string) bool {
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

func canonicalUTCTime(value string) (time.Time, bool) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil && parsed.UTC().Format(time.RFC3339Nano) == value
}

func hasUniqueJSONFields(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if !scanJSONValue(decoder) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func scanJSONValue(decoder *json.Decoder) bool {
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
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, keyOK := keyToken.(string)
			if err != nil || !keyOK {
				return false
			}
			if _, exists := seen[key]; exists {
				return false
			}
			seen[key] = struct{}{}
			if !scanJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !scanJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

type relayEndpointResponse struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

type roomCommonResponse struct {
	RoomID           string                `json:"room_id"`
	State            string                `json:"state"`
	CreatedAt        string                `json:"created_at"`
	ExpiresAt        string                `json:"expires_at"`
	Capacity         uint32                `json:"capacity"`
	RelayEndpoint    relayEndpointResponse `json:"relay_endpoint"`
	ProtocolRevision uint32                `json:"protocol_revision"`
	MaxDatagramBytes uint32                `json:"max_datagram_bytes"`
	MaxPayloadBytes  uint32                `json:"max_payload_bytes"`
}

type createRoomResponse struct {
	roomCommonResponse
	Grants []grantResponse `json:"grants"`
}

type grantResponse struct {
	ParticipantID  string  `json:"participant_id"`
	SessionID      string  `json:"session_id"`
	GrantID        string  `json:"grant_id"`
	GrantSecret    *string `json:"grant_secret,omitempty"`
	GrantExpiresAt string  `json:"grant_expires_at"`
	State          string  `json:"state"`
}

type getRoomResponse struct {
	roomCommonResponse
	Participants []participantResponse `json:"participants"`
}

type participantResponse struct {
	ParticipantID  string `json:"participant_id"`
	SessionID      string `json:"session_id"`
	GrantState     string `json:"grant_state"`
	GrantExpiresAt string `json:"grant_expires_at"`
	BindingState   string `json:"binding_state"`
}

func allocationResponse(allocation store.Allocation, host string, port uint16) createRoomResponse {
	response := createRoomResponse{
		roomCommonResponse: commonResponse(allocation.RoomID, allocation.CreatedAt, allocation.ExpiresAt, allocation.Capacity, host, port),
		Grants:             make([]grantResponse, len(allocation.Grants)),
	}
	for index, grant := range allocation.Grants {
		item := grantResponse{
			ParticipantID:  grant.ParticipantID,
			SessionID:      grant.SessionID,
			GrantID:        base64.RawURLEncoding.EncodeToString(grant.GrantID[:]),
			GrantExpiresAt: grant.GrantExpiresAt.UTC().Format(time.RFC3339Nano),
			State:          string(grant.State),
		}
		if grant.GrantSecret != nil {
			secret := base64.RawURLEncoding.EncodeToString(grant.GrantSecret[:])
			item.GrantSecret = &secret
		}
		response.Grants[index] = item
	}
	return response
}

func snapshotResponse(snapshot store.RoomSnapshot, host string, port uint16) getRoomResponse {
	response := getRoomResponse{
		roomCommonResponse: commonResponse(snapshot.RoomID, snapshot.CreatedAt, snapshot.ExpiresAt, snapshot.Capacity, host, port),
		Participants:       make([]participantResponse, len(snapshot.Participants)),
	}
	for index, participant := range snapshot.Participants {
		response.Participants[index] = participantResponse{
			ParticipantID:  participant.ParticipantID,
			SessionID:      participant.SessionID,
			GrantState:     string(participant.GrantState),
			GrantExpiresAt: participant.GrantExpiresAt.UTC().Format(time.RFC3339Nano),
			BindingState:   string(participant.BindingState),
		}
	}
	return response
}

func commonResponse(roomID string, createdAt, expiresAt time.Time, capacity uint32, host string, port uint16) roomCommonResponse {
	return roomCommonResponse{
		RoomID:           roomID,
		State:            "open",
		CreatedAt:        createdAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:        expiresAt.UTC().Format(time.RFC3339Nano),
		Capacity:         capacity,
		RelayEndpoint:    relayEndpointResponse{Host: host, Port: port},
		ProtocolRevision: protocol.Revision,
		MaxDatagramBytes: protocol.MaxDatagramBytes,
		MaxPayloadBytes:  protocol.MaxPayloadBytes,
	}
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeStoreError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
	case errors.Is(err, store.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "room not found")
	case errors.Is(err, store.ErrConflict):
		writeError(writer, http.StatusConflict, "conflict", "room_id already exists with a different immutable definition")
	case errors.Is(err, store.ErrCapacity):
		writeError(writer, http.StatusUnprocessableEntity, "capacity_exceeded", "capacity limit exceeded")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	response := errorResponse{}
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(writer, status, response)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	encoded, _ := json.Marshal(value)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(encoded, '\n'))
}
