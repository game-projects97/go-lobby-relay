package operatorapi

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/httpapi/httpx"
	"github.com/gyungsubLee/go-lobby-relay/internal/playerauth"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"github.com/gyungsubLee/go-lobby-relay/internal/relayroom"
	"golang.org/x/time/rate"
)

const (
	HardOperatorRequestRate  = rate.Limit(20)
	HardOperatorRequestBurst = 40
	HardOperatorConcurrent   = 32
)

var errInvalidConfig = errors.New("invalid operator API config")

var errInvalidOperatorToken = errors.New("invalid operator token")

type Config struct {
	OperatorToken  [32]byte
	PlayerTokens   *playerauth.Issuer
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
	rooms          *relayroom.Store
	playerTokens   *playerauth.Issuer
	admission      *httpx.Admission
	fatal          func()
}

func NewHandler(config Config, rooms *relayroom.Store) (http.Handler, error) {
	if rooms == nil || config.PlayerTokens == nil || config.OperatorToken == [32]byte{} || config.AdvertisedHost == "" || config.AdvertisedPort == 0 ||
		!(config.RequestRate > 0 && config.RequestRate <= HardOperatorRequestRate) ||
		config.RequestBurst <= 0 || config.RequestBurst > HardOperatorRequestBurst ||
		config.MaxConcurrent <= 0 || config.MaxConcurrent > HardOperatorConcurrent {
		return nil, errInvalidConfig
	}
	return &handler{
		operatorToken:  config.OperatorToken,
		advertisedHost: config.AdvertisedHost,
		advertisedPort: config.AdvertisedPort,
		rooms:          rooms,
		playerTokens:   config.PlayerTokens,
		admission:      httpx.NewAdmission(config.RequestRate, config.RequestBurst, config.MaxConcurrent, config.Now),
		fatal:          config.Fatal,
	}, nil
}

func NewServer(addr string, handler http.Handler) *http.Server {
	return httpx.NewServer(addr, handler)
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
		httpx.WriteError(writer, http.StatusNotFound, "not_found", "room not found")
		return
	}
	if !authorized(request, handler.operatorToken) {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		httpx.WriteError(writer, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
		return
	}
	if roomRoute && !protocol.ValidID(roomID) {
		httpx.WriteInvalid(writer)
		return
	}
	if tokenRoute && request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer, "POST")
		return
	}
	if roomRoute && request.Method != http.MethodPut && request.Method != http.MethodGet && request.Method != http.MethodDelete {
		httpx.WriteMethodNotAllowed(writer, "PUT, GET, DELETE")
		return
	}
	release, admitted := handler.admission.Enter(writer)
	if !admitted {
		return
	}
	defer release()
	if tokenRoute {
		handler.postPlayerToken(writer, request)
		return
	}

	switch request.Method {
	case http.MethodPut:
		handler.putRoom(writer, request, roomID)
	case http.MethodGet:
		if httpx.RequestHasBody(request) {
			httpx.WriteInvalid(writer)
			return
		}
		handler.getRoom(writer, roomID)
	case http.MethodDelete:
		if httpx.RequestHasBody(request) {
			httpx.WriteInvalid(writer)
			return
		}
		handler.deleteRoom(writer, roomID)
	}
}

func (handler *handler) postPlayerToken(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		PlayerID string `json:"player_id"`
	}
	if !httpx.DecodeExact(writer, request, &body, "player_id") {
		return
	}
	if !protocol.ValidID(body.PlayerID) {
		httpx.WriteInvalid(writer)
		return
	}
	token, claims, err := handler.playerTokens.Issue(body.PlayerID)
	if err != nil {
		httpx.WriteError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		if errors.Is(err, playerauth.ErrFatalRandom) {
			httpx.NotifyFatal(writer, handler.fatal)
		}
		return
	}
	httpx.WriteJSON(writer, http.StatusCreated, struct {
		PlayerID  string `json:"player_id"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}{body.PlayerID, token, claims.ExpiresAt.UTC().Format(time.RFC3339Nano)})
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

func (handler *handler) putRoom(writer http.ResponseWriter, request *http.Request, roomID string) {
	body, ok := httpx.ReadJSONBody(writer, request)
	if !ok {
		return
	}
	roomSpec, ok := decodeRoomSpec(body)
	if !ok {
		httpx.WriteInvalid(writer)
		return
	}
	allocation, created, err := handler.rooms.CreateRoom(roomID, roomSpec)
	if err != nil {
		writeStoreError(writer, err)
		if errors.Is(err, relayroom.ErrFatalRandom) {
			httpx.NotifyFatal(writer, handler.fatal)
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(writer, status, allocationResponse(allocation, handler.advertisedHost, handler.advertisedPort))
}

func (handler *handler) getRoom(writer http.ResponseWriter, roomID string) {
	snapshot, err := handler.rooms.GetRoom(roomID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, snapshotResponse(snapshot, handler.advertisedHost, handler.advertisedPort))
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

func decodeRoomSpec(body []byte) (relayroom.RoomSpec, bool) {
	if !httpx.HasUniqueFields(body) || !hasExactRoomRequestFields(body) {
		return relayroom.RoomSpec{}, false
	}
	var request createRoomRequest
	if !httpx.DecodeStrict(body, &request) {
		return relayroom.RoomSpec{}, false
	}
	expiresAt, ok := canonicalUTCTime(request.ExpiresAt)
	if !ok {
		return relayroom.RoomSpec{}, false
	}
	roomSpec := relayroom.RoomSpec{
		Capacity:     request.Capacity,
		ExpiresAt:    expiresAt,
		Participants: make([]relayroom.ParticipantSpec, len(request.Participants)),
	}
	for index, participant := range request.Participants {
		grantExpiresAt, ok := canonicalUTCTime(participant.GrantExpiresAt)
		if !ok {
			return relayroom.RoomSpec{}, false
		}
		roomSpec.Participants[index] = relayroom.ParticipantSpec{
			ParticipantID:  participant.ParticipantID,
			SessionID:      participant.SessionID,
			GrantExpiresAt: grantExpiresAt,
		}
	}
	return roomSpec, true
}

func hasExactRoomRequestFields(body []byte) bool {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || !httpx.HasExactKeys(request, "capacity", "expires_at", "participants") {
		return false
	}
	var participants []map[string]json.RawMessage
	if json.Unmarshal(request["participants"], &participants) != nil {
		return false
	}
	for _, participant := range participants {
		if !httpx.HasExactKeys(participant, "participant_id", "session_id", "grant_expires_at") {
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

func allocationResponse(allocation relayroom.RoomAllocation, host string, port uint16) createRoomResponse {
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

func snapshotResponse(snapshot relayroom.RoomSnapshot, host string, port uint16) getRoomResponse {
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

func writeStoreError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, relayroom.ErrInvalid):
		httpx.WriteInvalid(writer)
	case errors.Is(err, relayroom.ErrNotFound):
		httpx.WriteError(writer, http.StatusNotFound, "not_found", "room not found")
	case errors.Is(err, relayroom.ErrConflict):
		httpx.WriteError(writer, http.StatusConflict, "conflict", "room_id already exists with a different immutable definition")
	case errors.Is(err, relayroom.ErrCapacity):
		httpx.WriteError(writer, http.StatusUnprocessableEntity, "capacity_exceeded", "capacity limit exceeded")
	default:
		httpx.WriteError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
