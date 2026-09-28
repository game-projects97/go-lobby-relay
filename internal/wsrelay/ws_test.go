package wsrelay

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"github.com/gyungsubLee/go-lobby-relay/internal/relaytransport"
)

type received struct {
	datagram []byte
	endpoint netip.AddrPort
}

type sink struct {
	mu       sync.Mutex
	messages []received
	notify   chan struct{}
	err      error
}

func newSink() *sink { return &sink{notify: make(chan struct{}, 64)} }

func (target *sink) deliver(datagram []byte, endpoint netip.AddrPort) error {
	target.mu.Lock()
	target.messages = append(target.messages, received{append([]byte(nil), datagram...), endpoint})
	err := target.err
	target.mu.Unlock()
	target.notify <- struct{}{}
	return err
}

func (target *sink) wait(t *testing.T, count int) []received {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		target.mu.Lock()
		if len(target.messages) >= count {
			messages := append([]received(nil), target.messages...)
			target.mu.Unlock()
			return messages
		}
		target.mu.Unlock()
		select {
		case <-target.notify:
		case <-deadline:
			t.Fatalf("waited for %d messages", count)
		}
	}
}

func startServer(t *testing.T, config Config) (*Server, string) {
	t.Helper()
	relay, err := New(config)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	httpServer := httptest.NewServer(relay)
	t.Cleanup(func() {
		_ = relay.Close()
		httpServer.Close()
	})
	return relay, "ws" + strings.TrimPrefix(httpServer.URL, "http") + Path
}

func dial(t *testing.T, url string, protocols ...string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if protocols == nil {
		protocols = []string{Subprotocol}
	}
	return websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: protocols})
}

func TestNewValidatesConfig(t *testing.T) {
	deliver := func([]byte, netip.AddrPort) error { return nil }
	for name, config := range map[string]Config{
		"nil deliver":     {},
		"negative max":    {Deliver: deliver, MaxConnections: -1},
		"max above hard":  {Deliver: deliver, MaxConnections: HardMaxConnections + 1},
		"source above":    {Deliver: deliver, MaxPerSource: HardMaxConnectionsPerSource + 1},
		"wildcard origin": {Deliver: deliver, AllowedOrigins: []string{"*"}},
		"empty origin":    {Deliver: deliver, AllowedOrigins: []string{""}},
		"whitespace":      {Deliver: deliver, AllowedOrigins: []string{"a b"}},
	} {
		if server, err := New(config); err == nil || server != nil {
			t.Fatalf("%s: New() = (%v, %v), want error", name, server, err)
		}
	}
}

func TestBinaryMessagesRoundTripThroughTaggedEndpoints(t *testing.T) {
	messages := newSink()
	relay, url := startServer(t, Config{Deliver: messages.deliver})
	first, _, err := dial(t, url)
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	defer first.CloseNow()
	second, _, err := dial(t, url)
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}
	defer second.CloseNow()

	ctx := context.Background()
	if err := first.Write(ctx, websocket.MessageBinary, []byte("from-first")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if err := second.Write(ctx, websocket.MessageBinary, []byte("from-second")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	got := messages.wait(t, 2)
	endpoints := map[string]netip.AddrPort{}
	for _, message := range got {
		if !relay.Owns(message.endpoint) || !relaytransport.Tagged(message.endpoint, Zone) {
			t.Fatalf("endpoint %v not owned", message.endpoint)
		}
		endpoints[string(message.datagram)] = message.endpoint
	}
	if endpoints["from-first"] == endpoints["from-second"] {
		t.Fatal("two connections share an endpoint")
	}
	if relay.Owns(netip.MustParseAddrPort("127.0.0.1:1")) {
		t.Fatal("untagged endpoint reported as owned")
	}

	if !relay.Send([]byte("to-second"), endpoints["from-second"]) {
		t.Fatal("Send to live connection failed")
	}
	readContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	kind, data, err := second.Read(readContext)
	if err != nil || kind != websocket.MessageBinary || !bytes.Equal(data, []byte("to-second")) {
		t.Fatalf("second read = %v %q %v", kind, data, err)
	}
	if relay.Send([]byte("nobody"), relaytransport.Tag(netip.MustParseAddrPort("127.0.0.1:9"), Zone)) {
		t.Fatal("Send to unknown endpoint succeeded")
	}
}

func TestRejectsMissingSubprotocolTextAndOversizedMessages(t *testing.T) {
	messages := newSink()
	relay, url := startServer(t, Config{Deliver: messages.deliver})

	plain, _, err := dial(t, url, "other")
	if err == nil {
		readContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _, readErr := plain.Read(readContext)
		cancel()
		if websocket.CloseStatus(readErr) != websocket.StatusPolicyViolation {
			t.Fatalf("missing subprotocol close = %v", readErr)
		}
	}

	for _, test := range []struct {
		name   string
		kind   websocket.MessageType
		data   []byte
		status websocket.StatusCode
	}{
		{"text", websocket.MessageText, []byte("hello"), websocket.StatusUnsupportedData},
		{"oversized", websocket.MessageBinary, make([]byte, protocol.MaxDatagramBytes+1), websocket.StatusMessageTooBig},
	} {
		conn, _, err := dial(t, url)
		if err != nil {
			t.Fatalf("%s dial: %v", test.name, err)
		}
		_ = conn.Write(context.Background(), test.kind, test.data)
		readContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _, readErr := conn.Read(readContext)
		cancel()
		if websocket.CloseStatus(readErr) != test.status {
			t.Fatalf("%s close = %v, want %v", test.name, readErr, test.status)
		}
		conn.CloseNow()
	}
	deadline := time.Now().Add(2 * time.Second)
	for relay.Connections() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if relay.Connections() != 0 {
		t.Fatalf("connections after rejects = %d", relay.Connections())
	}
	messages.mu.Lock()
	defer messages.mu.Unlock()
	if len(messages.messages) != 0 {
		t.Fatalf("rejected frames reached the relay: %d", len(messages.messages))
	}
}

func TestPerSourceConnectionCap(t *testing.T) {
	messages := newSink()
	_, url := startServer(t, Config{Deliver: messages.deliver, MaxPerSource: 1})
	first, _, err := dial(t, url)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	defer first.CloseNow()
	if _, response, err := dial(t, url); err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second dial = %v %v, want 503", response, err)
	}
}

func TestCrossOriginRequiresAllowlist(t *testing.T) {
	messages := newSink()
	_, url := startServer(t, Config{Deliver: messages.deliver, AllowedOrigins: []string{"game.example"}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	header := http.Header{"Origin": []string{"https://evil.example"}}
	if _, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{Subprotocol}, HTTPHeader: header}); err == nil ||
		response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin dial = %v %v, want 403", response, err)
	}
	header.Set("Origin", "https://game.example")
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{Subprotocol}, HTTPHeader: header})
	if err != nil {
		t.Fatalf("allowed origin dial: %v", err)
	}
	conn.CloseNow()
}

func TestDeliverErrorIsFatalAndClosesConnection(t *testing.T) {
	messages := newSink()
	messages.err = errors.New("relay internal failure")
	fatal := make(chan struct{}, 1)
	_, url := startServer(t, Config{Deliver: messages.deliver, Fatal: func() { fatal <- struct{}{} }})
	conn, _, err := dial(t, url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	_ = conn.Write(context.Background(), websocket.MessageBinary, []byte("x"))
	select {
	case <-fatal:
	case <-time.After(2 * time.Second):
		t.Fatal("fatal not signalled")
	}
}

func TestCloseEndsConnections(t *testing.T) {
	messages := newSink()
	relay, url := startServer(t, Config{Deliver: messages.deliver})
	conn, _, err := dial(t, url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	done := make(chan error, 1)
	go func() { done <- relay.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close(): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}
	readContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := conn.Read(readContext); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("read after Close = %v", err)
	}
	if _, response, err := dial(t, url); err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("dial after Close = %v %v", response, err)
	}
}
