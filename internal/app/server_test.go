package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	relayv1 "github.com/gyungsubLee/go-lobby-relay/gen/go/relay/v1"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"google.golang.org/protobuf/proto"
)

var serverTestToken = func() (token [32]byte) {
	for index := range token {
		token[index] = 0x42
	}
	return token
}()

func TestNewValidatesBeforeBinding(t *testing.T) {
	valid := testServerConfig()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty operator listen", func(config *Config) { config.OperatorListen = "" }},
		{"invalid operator listen", func(config *Config) { config.OperatorListen = "127.0.0.1" }},
		{"empty player listen", func(config *Config) { config.PlayerListen = "" }},
		{"invalid player listen", func(config *Config) { config.PlayerListen = "127.0.0.1" }},
		{"invalid relay network", func(config *Config) { config.RelayNetwork = "udp" }},
		{"empty relay listen", func(config *Config) { config.RelayListen = "" }},
		{"invalid relay listen", func(config *Config) { config.RelayListen = "127.0.0.1" }},
		{"relay family mismatch", func(config *Config) { config.RelayListen = "[::1]:0" }},
		{"empty advertised host", func(config *Config) { config.AdvertisedHost = "" }},
		{"zero advertised port for fixed relay port", func(config *Config) {
			config.RelayListen = "127.0.0.1:30000"
			config.AdvertisedPort = 0
		}},
		{"all-zero operator token", func(config *Config) { config.OperatorToken = [32]byte{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := valid
			tt.mutate(&config)
			var tcpCalls, udpCalls int
			deps := defaultDependencies()
			deps.listenTCP = func(string, string) (net.Listener, error) {
				tcpCalls++
				return nil, errors.New("must not bind")
			}
			deps.listenUDP = func(string, *net.UDPAddr) (*net.UDPConn, error) {
				udpCalls++
				return nil, errors.New("must not bind")
			}
			if server, err := newWithDependencies(config, deps); err == nil || server != nil {
				t.Fatalf("newWithDependencies() = (%#v, %v), want nil/error", server, err)
			}
			if tcpCalls != 0 || udpCalls != 0 {
				t.Fatalf("invalid config opened sockets: tcp=%d udp=%d", tcpCalls, udpCalls)
			}
		})
	}
}

func TestNewBindsSeparatePlayerListenerWithoutServing(t *testing.T) {
	server, err := New(testServerConfig())
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer server.Close()
	if server.PlayerAddr() == nil || server.PlayerAddr().String() == server.OperatorAddr().String() {
		t.Fatalf("player/operator addresses = %v/%v", server.PlayerAddr(), server.OperatorAddr())
	}
	connection, err := net.DialTimeout("tcp", server.PlayerAddr().String(), time.Second)
	if err != nil {
		t.Fatalf("player listener not bound: %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	_, _ = io.WriteString(connection, "GET /v1/lobbies?queue_key=duo HTTP/1.1\r\nHost: test\r\n\r\n")
	var one [1]byte
	if _, err := connection.Read(one[:]); err == nil {
		t.Fatal("player API served before Run")
	}
	_ = connection.Close()
}

func TestNewRollsBackTCPWhenUDPBindFails(t *testing.T) {
	config := testServerConfig()
	deps := defaultDependencies()
	var tcpAddresses []string
	deps.listenTCP = func(network, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err == nil {
			tcpAddresses = append(tcpAddresses, listener.Addr().String())
		}
		return listener, err
	}
	deps.listenUDP = func(string, *net.UDPAddr) (*net.UDPConn, error) {
		return nil, errors.New("injected UDP bind failure")
	}

	if server, err := newWithDependencies(config, deps); err == nil || server != nil {
		t.Fatalf("newWithDependencies() = (%#v, %v), want nil/error", server, err)
	}
	if len(tcpAddresses) != 2 {
		t.Fatalf("TCP listeners opened before UDP = %d, want 2", len(tcpAddresses))
	}
	for _, address := range tcpAddresses {
		rebound, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("TCP listener %s was not rolled back: %v", address, err)
		}
		_ = rebound.Close()
	}
}

func TestRunSharesHTTPStoreWithRelayAndCancelsCleanly(t *testing.T) {
	config := testServerConfig()
	server, err := New(config)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	operatorAddr, relayAddr := server.OperatorAddr(), server.RelayAddr()
	if operatorAddr == nil || relayAddr == nil {
		t.Fatalf("bound addresses = %v/%v", operatorAddr, relayAddr)
	}
	if server.runStarted {
		t.Fatal("New started work before Run")
	}
	assertBoundButNotServing(t, operatorAddr.String(), relayAddr.(*net.UDPAddr), config.RelayNetwork)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(ctx) }()
	waitForOperator(t, operatorAddr.String(), serverTestToken)

	allocation := createRoom(t, operatorAddr.String(), serverTestToken, "room", 2)
	actualPort := uint16(relayAddr.(*net.UDPAddr).Port)
	if allocation.RelayEndpoint.Host != config.AdvertisedHost || allocation.RelayEndpoint.Port != actualPort {
		t.Fatalf("advertised endpoint = %#v, want %s:%d", allocation.RelayEndpoint, config.AdvertisedHost, actualPort)
	}
	if allocation.ProtocolRevision != protocol.Revision || allocation.MaxDatagramBytes != protocol.MaxDatagramBytes ||
		allocation.MaxPayloadBytes != protocol.MaxPayloadBytes || len(allocation.Grants) != 2 {
		t.Fatalf("allocation response = %#v", allocation)
	}

	relayEndpoint := relayAddr.(*net.UDPAddr).AddrPort()
	first := newAllocatedClient(t)
	second := newAllocatedClient(t)
	first.bind(t, relayEndpoint, "room", allocation.Grants[0], 0x71)
	second.bind(t, relayEndpoint, "room", allocation.Grants[1], 0x72)
	payload := []byte{0, 1, 2, 0xfe, 0xff, 'x'}
	first.sendData(t, relayEndpoint, 1, payload)
	second.expectData(t, first.participantID, 1, payload)
	first.expectSilence(t)
	_ = first.conn.Close()
	_ = second.conn.Close()

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() after context cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not join after context cancellation")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close() after Run: %v", err)
	}
	assertAddressesRebind(t, operatorAddr, relayAddr, config.RelayNetwork)
}

func TestPlayerHTTPToUDPFlows(t *testing.T) {
	for _, flow := range []struct {
		name   string
		assign func(*testing.T, string, string, string) (playerAssignment, playerAssignment)
	}{
		{"lobby", lobbyAssignments},
		{"quick match", quickMatchAssignments},
	} {
		t.Run(flow.name, func(t *testing.T) {
			config := testServerConfig()
			server, err := New(config)
			if err != nil {
				t.Fatalf("New(): %v", err)
			}
			operator, player, relayAddress := server.OperatorAddr().String(), server.PlayerAddr().String(), server.RelayAddr().(*net.UDPAddr).AddrPort()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- server.Run(ctx) }()
			waitForOperator(t, operator, serverTestToken)
			firstToken := issuePlayerToken(t, operator, "player-a")
			secondToken := issuePlayerToken(t, operator, "player-b")
			firstAssignment, secondAssignment := flow.assign(t, player, firstToken, secondToken)
			if firstAssignment.RoomID != secondAssignment.RoomID || firstAssignment.PlayerID != "player-a" || secondAssignment.PlayerID != "player-b" || firstAssignment.GrantSecret == secondAssignment.GrantSecret {
				t.Fatalf("assignments = %+v / %+v", firstAssignment, secondAssignment)
			}
			first := newAllocatedClient(t)
			second := newAllocatedClient(t)
			first.bind(t, relayAddress, firstAssignment.RoomID, firstAssignment.grant(), 0x81)
			second.bind(t, relayAddress, secondAssignment.RoomID, secondAssignment.grant(), 0x82)
			payload := []byte("m1-http-to-udp")
			first.sendData(t, relayAddress, 1, payload)
			second.expectData(t, "player-a", 1, payload)
			_ = first.conn.Close()
			_ = second.conn.Close()
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run(): %v", err)
			}
			if err := server.Close(); err != nil {
				t.Fatalf("Close(): %v", err)
			}
			assertThreeAddressesRebind(t, server.OperatorAddr(), server.PlayerAddr(), server.RelayAddr(), config.RelayNetwork)
		})
	}
}

type playerAssignment struct {
	MatchID     string `json:"match_id"`
	RoomID      string `json:"room_id"`
	PlayerID    string `json:"player_id"`
	SessionID   string `json:"session_id"`
	GrantID     string `json:"grant_id"`
	GrantSecret string `json:"grant_secret"`
}

func (assignment playerAssignment) grant() testGrantResponse {
	secret := assignment.GrantSecret
	return testGrantResponse{ParticipantID: assignment.PlayerID, SessionID: assignment.SessionID, GrantID: assignment.GrantID, GrantSecret: &secret}
}

func issuePlayerToken(t *testing.T, operator, playerID string) string {
	t.Helper()
	response := httpJSON(t, operator, serverTestTokenBearer(), "POST", "/v1/player-tokens", `{"player_id":"`+playerID+`"}`)
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("issue token = %d %q", response.StatusCode, body)
	}
	defer response.Body.Close()
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result.Token
}

func lobbyAssignments(t *testing.T, player, firstToken, secondToken string) (playerAssignment, playerAssignment) {
	t.Helper()
	created := playerJSON(t, player, firstToken, "POST", "/v1/lobbies", `{"visibility":"private","queue_key":"duo","capacity":2}`)
	var state struct {
		LobbyID    string            `json:"lobby_id"`
		Revision   uint64            `json:"revision"`
		Assignment *playerAssignment `json:"assignment"`
	}
	decodeHTTP(t, created, http.StatusCreated, &state)
	joined := playerJSON(t, player, secondToken, "POST", "/v1/lobbies/"+state.LobbyID+"/join", fmt.Sprintf(`{"revision":%d}`, state.Revision))
	decodeHTTP(t, joined, http.StatusOK, &state)
	for _, token := range []string{firstToken, secondToken} {
		ready := playerJSON(t, player, token, "PUT", "/v1/lobbies/"+state.LobbyID+"/members/me/ready", fmt.Sprintf(`{"revision":%d,"ready":true}`, state.Revision))
		decodeHTTP(t, ready, http.StatusOK, &state)
	}
	started := playerJSON(t, player, firstToken, "POST", "/v1/lobbies/"+state.LobbyID+"/start", fmt.Sprintf(`{"revision":%d}`, state.Revision))
	var first playerAssignment
	decodeHTTP(t, started, http.StatusOK, &first)
	member := playerJSON(t, player, secondToken, "GET", "/v1/lobbies/"+state.LobbyID, "")
	decodeHTTP(t, member, http.StatusOK, &state)
	if state.Assignment == nil {
		t.Fatal("member assignment missing")
	}
	return first, *state.Assignment
}

func quickMatchAssignments(t *testing.T, player, firstToken, secondToken string) (playerAssignment, playerAssignment) {
	t.Helper()
	first := playerJSON(t, player, firstToken, "POST", "/v1/matchmaking/tickets", `{"queue_key":"duo","capacity":2}`)
	decodeHTTP(t, first, http.StatusCreated, &struct{}{})
	second := playerJSON(t, player, secondToken, "POST", "/v1/matchmaking/tickets", `{"queue_key":"duo","capacity":2}`)
	decodeHTTP(t, second, http.StatusCreated, &struct{}{})
	var tickets [2]struct {
		Assignment *playerAssignment `json:"assignment"`
	}
	for index, token := range []string{firstToken, secondToken} {
		response := playerJSON(t, player, token, "GET", "/v1/matchmaking/tickets/me", "")
		decodeHTTP(t, response, http.StatusOK, &tickets[index])
		if tickets[index].Assignment == nil {
			t.Fatalf("ticket %d assignment missing", index)
		}
	}
	return *tickets[0].Assignment, *tickets[1].Assignment
}

func playerJSON(t *testing.T, address, token, method, path, body string) *http.Response {
	t.Helper()
	return httpJSON(t, address, "Bearer "+token, method, path, body)
}

func httpJSON(t *testing.T, address, authorization, method, path, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", authorization)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return response
}

func decodeHTTP(t *testing.T, response *http.Response, status int, target any) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d body=%q want=%d", response.StatusCode, body, status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func serverTestTokenBearer() string {
	return "Bearer " + base64.RawURLEncoding.EncodeToString(serverTestToken[:])
}

func assertThreeAddressesRebind(t *testing.T, operator, player, relayAddress net.Addr, relayNetwork string) {
	t.Helper()
	for _, address := range []net.Addr{operator, player} {
		listener, err := net.Listen("tcp", address.String())
		if err != nil {
			t.Fatalf("TCP %s not reusable: %v", address, err)
		}
		_ = listener.Close()
	}
	socket, err := net.ListenUDP(relayNetwork, relayAddress.(*net.UDPAddr))
	if err != nil {
		t.Fatalf("UDP not reusable: %v", err)
	}
	_ = socket.Close()
}

func TestCloseBeforeDuringAndAfterRunIsIdempotent(t *testing.T) {
	t.Run("before Run", func(t *testing.T) {
		config := testServerConfig()
		server, err := New(config)
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		operatorAddr, relayAddr := server.OperatorAddr(), server.RelayAddr()
		var wait sync.WaitGroup
		for range 16 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if err := server.Close(); err != nil {
					t.Errorf("Close(): %v", err)
				}
			}()
		}
		wait.Wait()
		if err := server.Run(context.Background()); err != nil {
			t.Fatalf("Run() after pre-Close = %v", err)
		}
		if server.runStarted {
			t.Fatal("Run after pre-Close started owned work")
		}
		assertAddressesRebind(t, operatorAddr, relayAddr, config.RelayNetwork)
	})

	t.Run("during and after Run", func(t *testing.T) {
		config := testServerConfig()
		server, err := New(config)
		if err != nil {
			t.Fatalf("New(): %v", err)
		}
		operatorAddr, relayAddr := server.OperatorAddr(), server.RelayAddr()
		runDone := make(chan error, 1)
		go func() { runDone <- server.Run(context.Background()) }()
		waitForOperator(t, operatorAddr.String(), serverTestToken)

		var wait sync.WaitGroup
		for range 16 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if err := server.Close(); err != nil {
					t.Errorf("Close(): %v", err)
				}
			}()
		}
		wait.Wait()
		select {
		case err := <-runDone:
			if err != nil {
				t.Fatalf("Run() after local Close = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not join after local Close")
		}
		if err := server.Close(); err != nil {
			t.Fatalf("Close() after join: %v", err)
		}
		assertAddressesRebind(t, operatorAddr, relayAddr, config.RelayNetwork)
	})
}

func TestUnexpectedOwnedLoopFailureCancelsSiblings(t *testing.T) {
	config := testServerConfig()
	server, err := New(config)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	operatorAddr, relayAddr := server.OperatorAddr(), server.RelayAddr()
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(context.Background()) }()
	waitForOperator(t, operatorAddr.String(), serverTestToken)

	if err := server.operatorListener.Close(); err != nil {
		t.Fatalf("close owned listener: %v", err)
	}
	select {
	case err := <-runDone:
		if err == nil {
			t.Fatal("Run() unexpected listener failure = nil")
		}
		encoded := base64.RawURLEncoding.EncodeToString(serverTestToken[:])
		if strings.Contains(err.Error(), encoded) || strings.Contains(err.Error(), string(serverTestToken[:])) {
			t.Fatalf("Run error disclosed operator token: %q", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unexpected loop failure did not join siblings")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close() after unexpected failure: %v", err)
	}
	assertAddressesRebind(t, operatorAddr, relayAddr, config.RelayNetwork)
}

func TestQueuedUnexpectedLoopFailureWinsLaterCancellation(t *testing.T) {
	server := &Server{closeSignal: make(chan struct{})}
	runContext, cancel := context.WithCancel(context.Background())
	results := make(chan loopResult, 4)
	results <- server.classifyLoopResult(runContext, "operator", errors.New("sensitive cause"))
	cancel()
	results <- server.classifyLoopResult(runContext, "relay", net.ErrClosed)
	results <- server.classifyLoopResult(runContext, "sweeper", nil)
	results <- server.classifyLoopResult(runContext, "player", net.ErrClosed)

	err := coordinateLoopResults(runContext, server.closeSignal, make(chan struct{}), results, func() {})
	if err != errOwnedLoop {
		t.Fatalf("queued unexpected result after cancellation = %v, want generic %v", err, errOwnedLoop)
	}
}

func TestHTTPFatalRandomStopsServerAndJoinsSiblings(t *testing.T) {
	config := testServerConfig()
	deps := defaultDependencies()
	deps.random = failingReader{}
	server, err := newWithDependencies(config, deps)
	if err != nil {
		t.Fatalf("newWithDependencies(): %v", err)
	}
	operatorAddr, relayAddr := server.OperatorAddr(), server.RelayAddr()
	if server.runStarted {
		t.Fatal("New started work before Run")
	}
	assertBoundButNotServing(t, operatorAddr.String(), relayAddr.(*net.UDPAddr), config.RelayNetwork)

	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(context.Background()) }()
	waitForOperator(t, operatorAddr.String(), serverTestToken)
	now := time.Now().UTC()
	body := []byte(`{"capacity":1,"expires_at":"` + now.Add(time.Hour).Format(time.RFC3339Nano) +
		`","participants":[{"participant_id":"participant","session_id":"session","grant_expires_at":"` +
		now.Add(30*time.Minute).Format(time.RFC3339Nano) + `"}]}`)
	request, err := http.NewRequest(http.MethodPut, "http://"+operatorAddr.String()+"/v1/rooms/room", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest(): %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(serverTestToken[:]))
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatalf("fatal PUT room: %v", err)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatalf("read fatal response: %v", readErr)
	}
	wantBody := "{\"error\":{\"code\":\"internal_error\",\"message\":\"internal server error\"}}\n"
	if response.StatusCode != http.StatusInternalServerError || string(responseBody) != wantBody {
		t.Fatalf("fatal PUT room = %d %q", response.StatusCode, responseBody)
	}

	select {
	case err := <-runDone:
		if err != errOwnedLoop {
			t.Fatalf("Run() after HTTP fatal random = %v, want %v", err, errOwnedLoop)
		}
		encoded := base64.RawURLEncoding.EncodeToString(serverTestToken[:])
		if strings.Contains(err.Error(), encoded) || strings.Contains(err.Error(), string(serverTestToken[:])) {
			t.Fatalf("Run error disclosed operator token: %q", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP fatal random did not join owned loops")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close() after HTTP fatal random: %v", err)
	}
	assertAddressesRebind(t, operatorAddr, relayAddr, config.RelayNetwork)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("injected random failure") }

func testServerConfig() Config {
	return Config{
		OperatorListen: "127.0.0.1:0",
		PlayerListen:   "127.0.0.1:0",
		RelayNetwork:   "udp4",
		RelayListen:    "127.0.0.1:0",
		AdvertisedHost: "relay.test",
		AdvertisedPort: 0,
		OperatorToken:  serverTestToken,
	}
}

func assertBoundButNotServing(t *testing.T, operator string, relayAddress *net.UDPAddr, relayNetwork string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", operator, time.Second)
	if err != nil {
		t.Fatalf("operator listener was not bound: %v", err)
	}
	if _, err := io.WriteString(connection, "GET /v1/rooms/missing HTTP/1.1\r\nHost: "+operator+"\r\n\r\n"); err != nil {
		t.Fatalf("write pre-Run request: %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	var one [1]byte
	if _, err := connection.Read(one[:]); err == nil {
		t.Fatal("operator served a response before Run")
	} else if networkError, ok := err.(net.Error); !ok || !networkError.Timeout() {
		t.Fatalf("pre-Run read = %v, want timeout", err)
	}
	_ = connection.Close()

	if listener, err := net.Listen("tcp", operator); err == nil {
		_ = listener.Close()
		t.Fatal("operator address was not held after New")
	}
	if socket, err := net.ListenUDP(relayNetwork, relayAddress); err == nil {
		_ = socket.Close()
		t.Fatal("relay address was not held after New")
	}
}

func waitForOperator(t *testing.T, address string, token [32]byte) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, "http://"+address+"/v1/rooms/missing", nil)
		if err != nil {
			t.Fatalf("NewRequest(): %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(token[:]))
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusNotFound {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operator listener did not start serving")
}

type testAllocationResponse struct {
	RelayEndpoint struct {
		Host string `json:"host"`
		Port uint16 `json:"port"`
	} `json:"relay_endpoint"`
	ProtocolRevision uint32              `json:"protocol_revision"`
	MaxDatagramBytes uint32              `json:"max_datagram_bytes"`
	MaxPayloadBytes  uint32              `json:"max_payload_bytes"`
	Grants           []testGrantResponse `json:"grants"`
}

type testGrantResponse struct {
	ParticipantID string  `json:"participant_id"`
	SessionID     string  `json:"session_id"`
	GrantID       string  `json:"grant_id"`
	GrantSecret   *string `json:"grant_secret"`
}

func createRoom(t *testing.T, address string, token [32]byte, roomID string, participants int) testAllocationResponse {
	t.Helper()
	now := time.Now().UTC()
	requestBody := struct {
		Capacity     uint32 `json:"capacity"`
		ExpiresAt    string `json:"expires_at"`
		Participants []struct {
			ParticipantID  string `json:"participant_id"`
			SessionID      string `json:"session_id"`
			GrantExpiresAt string `json:"grant_expires_at"`
		} `json:"participants"`
	}{Capacity: uint32(participants), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano)}
	requestBody.Participants = make([]struct {
		ParticipantID  string `json:"participant_id"`
		SessionID      string `json:"session_id"`
		GrantExpiresAt string `json:"grant_expires_at"`
	}, participants)
	for index := range requestBody.Participants {
		requestBody.Participants[index].ParticipantID = "participant-" + string(rune('a'+index))
		requestBody.Participants[index].SessionID = "session-" + string(rune('a'+index))
		requestBody.Participants[index].GrantExpiresAt = now.Add(30 * time.Minute).Format(time.RFC3339Nano)
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	request, err := http.NewRequest(http.MethodPut, "http://"+address+"/v1/rooms/"+roomID, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest(): %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(token[:]))
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatalf("PUT room: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("PUT room = %d %q", response.StatusCode, responseBody)
	}
	var allocation testAllocationResponse
	if err := json.NewDecoder(response.Body).Decode(&allocation); err != nil {
		t.Fatalf("decode allocation: %v", err)
	}
	return allocation
}

type allocatedClient struct {
	conn                             *net.UDPConn
	roomID, sessionID, participantID string
	bindingID                        protocol.Bytes16
	key                              protocol.Bytes32
}

func newAllocatedClient(t *testing.T) *allocatedClient {
	t.Helper()
	connection, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatalf("ListenUDP(client): %v", err)
	}
	return &allocatedClient{conn: connection}
}

func (client *allocatedClient) bind(t *testing.T, relayEndpoint netip.AddrPort, roomID string, grant testGrantResponse, nonceByte byte) {
	t.Helper()
	grantID := decode16(t, grant.GrantID)
	if grant.GrantSecret == nil {
		t.Fatal("allocation omitted grant secret")
	}
	secret := decode32(t, *grant.GrantSecret)
	var nonce protocol.Bytes16
	for index := range nonce {
		nonce[index] = nonceByte
	}
	hello := &relayv1.Envelope{
		ProtocolRevision: protocol.Revision, RoomId: roomID, SessionId: grant.SessionID,
		Body: &relayv1.Envelope_Hello{Hello: &relayv1.Hello{
			GrantId: grantID[:], ClientNonce: nonce[:], Padding: make([]byte, protocol.MinHelloBytes),
		}},
	}
	client.send(t, relayEndpoint, marshalEnvelope(t, hello))
	challengeEnvelope := client.receive(t)
	challenge := challengeEnvelope.GetChallenge()
	if challenge == nil {
		t.Fatalf("HELLO response body = %T", challengeEnvelope.Body)
	}
	candidateID := bytesTo16(t, challenge.CandidateId)
	serverNonce := bytesTo32(t, challenge.ServerNonce)
	authTag := protocol.AuthTag(secret, protocol.Revision, roomID, grant.SessionID, grantID, candidateID, nonce, serverNonce)
	client.send(t, relayEndpoint, marshalEnvelope(t, &relayv1.Envelope{
		ProtocolRevision: protocol.Revision, RoomId: roomID, SessionId: grant.SessionID, AuthTag: authTag[:],
		Body: &relayv1.Envelope_Auth{Auth: &relayv1.Auth{CandidateId: candidateID[:]}},
	}))
	boundEnvelope := client.receive(t)
	bound := boundEnvelope.GetBound()
	if bound == nil {
		t.Fatalf("AUTH response body = %T", boundEnvelope.Body)
	}
	bindingID := bytesTo16(t, bound.BindingId)
	key := protocol.BindingKey(secret, protocol.Revision, roomID, grant.SessionID, grantID, candidateID, nonce, serverNonce)
	wantTag := protocol.BoundTag(key, protocol.Revision, roomID, grant.SessionID, candidateID, bindingID, bound.ExpiresUnixMs)
	if !protocol.EqualTag(wantTag, boundEnvelope.AuthTag) {
		t.Fatal("BOUND tag mismatch")
	}
	client.roomID, client.sessionID, client.participantID = roomID, grant.SessionID, grant.ParticipantID
	client.bindingID, client.key = bindingID, key
}

func (client *allocatedClient) sendData(t *testing.T, relayEndpoint netip.AddrPort, sequence uint64, payload []byte) {
	t.Helper()
	tag := protocol.ClientDataTag(client.key, protocol.Revision, client.roomID, client.sessionID, client.bindingID, sequence, payload)
	client.send(t, relayEndpoint, marshalEnvelope(t, &relayv1.Envelope{
		ProtocolRevision: protocol.Revision, Sequence: sequence, RoomId: client.roomID, SessionId: client.sessionID,
		AuthTag: tag[:], Body: &relayv1.Envelope_ClientData{ClientData: &relayv1.ClientData{
			BindingId: client.bindingID[:], Payload: append([]byte(nil), payload...),
		}},
	}))
}

func (client *allocatedClient) send(t *testing.T, endpoint netip.AddrPort, datagram []byte) {
	t.Helper()
	_ = client.conn.SetWriteDeadline(time.Now().Add(time.Second))
	written, err := client.conn.WriteToUDPAddrPort(datagram, endpoint)
	if err != nil || written != len(datagram) {
		t.Fatalf("WriteToUDPAddrPort() = (%d, %v), want %d", written, err, len(datagram))
	}
}

func (client *allocatedClient) receive(t *testing.T) *relayv1.Envelope {
	t.Helper()
	_ = client.conn.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, protocol.MaxDatagramBytes+1)
	read, _, err := client.conn.ReadFromUDPAddrPort(buffer)
	if err != nil {
		t.Fatalf("ReadFromUDPAddrPort(): %v", err)
	}
	envelope := new(relayv1.Envelope)
	if err := proto.Unmarshal(buffer[:read], envelope); err != nil {
		t.Fatalf("proto.Unmarshal(): %v", err)
	}
	return envelope
}

func (client *allocatedClient) expectData(t *testing.T, participantID string, sequence uint64, payload []byte) {
	t.Helper()
	envelope := client.receive(t)
	data := envelope.GetServerData()
	if data == nil || data.SenderParticipantId != participantID || envelope.Sequence != sequence || !bytes.Equal(data.Payload, payload) {
		t.Fatalf("ServerData = %#v", envelope)
	}
}

func (client *allocatedClient) expectSilence(t *testing.T) {
	t.Helper()
	_ = client.conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	var buffer [protocol.MaxDatagramBytes + 1]byte
	if read, endpoint, err := client.conn.ReadFromUDPAddrPort(buffer[:]); err == nil {
		t.Fatalf("unexpected datagram: %d bytes from %v", read, endpoint)
	} else if networkError, ok := err.(net.Error); !ok || !networkError.Timeout() {
		t.Fatalf("ReadFromUDPAddrPort() = %v, want timeout", err)
	}
}

func marshalEnvelope(t *testing.T, envelope *relayv1.Envelope) []byte {
	t.Helper()
	datagram, err := (proto.MarshalOptions{Deterministic: true}).Marshal(envelope)
	if err != nil {
		t.Fatalf("proto.Marshal(): %v", err)
	}
	return datagram
}

func decode16(t *testing.T, encoded string) protocol.Bytes16 {
	t.Helper()
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode 16 bytes: %v", err)
	}
	return bytesTo16(t, decoded)
}

func decode32(t *testing.T, encoded string) protocol.Bytes32 {
	t.Helper()
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode 32 bytes: %v", err)
	}
	return bytesTo32(t, decoded)
}

func bytesTo16(t *testing.T, decoded []byte) (value protocol.Bytes16) {
	t.Helper()
	if len(decoded) != len(value) {
		t.Fatalf("decoded length = %d, want %d", len(decoded), len(value))
	}
	copy(value[:], decoded)
	return value
}

func bytesTo32(t *testing.T, decoded []byte) (value protocol.Bytes32) {
	t.Helper()
	if len(decoded) != len(value) {
		t.Fatalf("decoded length = %d, want %d", len(decoded), len(value))
	}
	copy(value[:], decoded)
	return value
}

func assertAddressesRebind(t *testing.T, operatorAddr, relayAddr net.Addr, relayNetwork string) {
	t.Helper()
	operator, err := net.Listen("tcp", operatorAddr.String())
	if err != nil {
		t.Fatalf("rebind operator %s: %v", operatorAddr, err)
	}
	_ = operator.Close()
	relay, err := net.ListenUDP(relayNetwork, relayAddr.(*net.UDPAddr))
	if err != nil {
		t.Fatalf("rebind relay %s: %v", relayAddr, err)
	}
	_ = relay.Close()
}
