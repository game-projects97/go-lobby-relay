package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gyungsubLee/go-lobby-relay/internal/wsrelay"
)

func TestNewRejectsWebSocketOptionsWithoutListener(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"origins without listener":  func(config *Config) { config.WebSocketAllowedOrigins = []string{"game.test"} },
		"per-source without listen": func(config *Config) { config.WebSocketMaxPerSource = 4 },
		"bad listen address":        func(config *Config) { config.WebSocketListen = "not an address" },
		"wildcard origin": func(config *Config) {
			config.WebSocketListen = "127.0.0.1:0"
			config.WebSocketAllowedOrigins = []string{"*"}
		},
		"match TTL above hard max": func(config *Config) { config.MatchTTL = 3 * time.Hour },
	} {
		config := testServerConfig()
		mutate(&config)
		if server, err := New(config); err == nil || server != nil {
			t.Fatalf("%s: New() = (%v, %v), want error", name, server, err)
		}
	}
}

func TestQuickMatchMixesUDPAndWebSocketClients(t *testing.T) {
	config := testServerConfig()
	config.WebSocketListen = "127.0.0.1:0"
	config.MatchTTL = 30 * time.Minute
	server, err := New(config)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	if server.WebSocketAddr() == nil {
		t.Fatal("WebSocketAddr() = nil with listener configured")
	}
	operator, player, relayAddress := server.OperatorAddr().String(), server.PlayerAddr().String(), server.RelayAddr().(*net.UDPAddr).AddrPort()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	waitForOperator(t, operator, serverTestToken)

	udpAssignment, wsAssignment := quickMatchAssignments(t, player,
		issuePlayerToken(t, operator, "player-a"), issuePlayerToken(t, operator, "player-b"))

	udpClient := newAllocatedClient(t)
	defer udpClient.conn.Close()
	dialContext, dialCancel := context.WithTimeout(context.Background(), time.Second)
	defer dialCancel()
	wsConn, _, err := websocket.Dial(dialContext, "ws://"+server.WebSocketAddr().String()+wsrelay.Path,
		&websocket.DialOptions{Subprotocols: []string{wsrelay.Subprotocol}})
	if err != nil {
		t.Fatalf("websocket Dial(): %v", err)
	}
	wsClient := &allocatedClient{ws: wsConn}

	udpClient.bind(t, relayAddress, udpAssignment.RoomID, udpAssignment.grant(), 0x81)
	wsClient.bind(t, relayAddress, wsAssignment.RoomID, wsAssignment.grant(), 0x82)

	udpClient.sendData(t, relayAddress, 1, []byte("udp-to-browser"))
	wsClient.expectData(t, "player-a", 1, []byte("udp-to-browser"))
	wsClient.sendData(t, relayAddress, 1, []byte("browser-to-udp"))
	udpClient.expectData(t, "player-b", 1, []byte("browser-to-udp"))

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run(): %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	readContext, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	if _, _, err := wsConn.Read(readContext); err == nil {
		t.Fatal("WebSocket stayed open after server Close")
	}
}
