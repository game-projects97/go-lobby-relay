// Package wsrelay carries Relay datagrams over WebSocket for clients that
// cannot open UDP sockets, such as browsers and WebViews.
//
// Each binary message is exactly one relay.v1 Envelope, byte-identical to a
// UDP datagram. The connection only transports bytes: handshake, HMAC,
// replay, rate limits and fan-out stay in the shared Relay core, and delivery
// stays best-effort (a full per-connection queue drops, it never blocks).
package wsrelay

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gyungsubLee/go-lobby-relay/internal/protocol"
	"github.com/gyungsubLee/go-lobby-relay/internal/relaytransport"
)

const (
	// Zone tags WebSocket endpoints inside the shared Relay endpoint space.
	Zone = "~ws"
	// Subprotocol must be offered by clients; it versions the framing.
	Subprotocol = "relay.v1"
	// Path is the only upgrade route.
	Path = "/v1/relay"

	HardMaxConnections          = 4096
	HardMaxConnectionsPerSource = 256
	DefaultMaxConnections       = 1024
	DefaultMaxPerSource         = 16

	sendQueueDepth = 32
	// idleTimeout exceeds the 60s binding TTL: live clients ping or rebind sooner.
	idleTimeout  = 75 * time.Second
	writeTimeout = time.Second
)

var errInvalidConfig = errors.New("wsrelay: invalid configuration")

type Config struct {
	// Deliver hands one received message to the Relay core. A non-nil error is fatal.
	Deliver func(datagram []byte, endpoint netip.AddrPort) error
	// AllowedOrigins lists cross-origin host patterns (path.Match syntax) that
	// may connect, e.g. "game.example.com" or "https://*.example.com".
	// Same-host origins are always allowed.
	AllowedOrigins []string
	// MaxConnections and MaxPerSource bound open connections overall and per
	// peer IP. Zero selects the defaults. Behind a reverse proxy every client
	// shares the proxy address, so raise MaxPerSource accordingly.
	MaxConnections int
	MaxPerSource   int
	Fatal          func()
}

type peer struct {
	conn  *websocket.Conn
	queue chan []byte
}

type Server struct {
	deliver       func([]byte, netip.AddrPort) error
	fatal         func()
	accept        websocket.AcceptOptions
	maxConns      int
	maxPerSource  int
	baseContext   context.Context
	cancelContext context.CancelFunc

	mu        sync.Mutex
	closed    bool
	peers     map[netip.AddrPort]*peer
	perSource map[netip.Addr]int
	reserved  int
	wg        sync.WaitGroup
}

func New(config Config) (*Server, error) {
	if config.Deliver == nil || config.MaxConnections < 0 || config.MaxConnections > HardMaxConnections ||
		config.MaxPerSource < 0 || config.MaxPerSource > HardMaxConnectionsPerSource {
		return nil, errInvalidConfig
	}
	for _, origin := range config.AllowedOrigins {
		if origin == "" || origin == "*" || strings.ContainsAny(origin, " \t\r\n") {
			return nil, errInvalidConfig
		}
	}
	if config.MaxConnections == 0 {
		config.MaxConnections = DefaultMaxConnections
	}
	if config.MaxPerSource == 0 {
		config.MaxPerSource = DefaultMaxPerSource
	}
	if config.Fatal == nil {
		config.Fatal = func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		deliver: config.Deliver,
		fatal:   config.Fatal,
		accept: websocket.AcceptOptions{
			Subprotocols:    []string{Subprotocol},
			OriginPatterns:  append([]string(nil), config.AllowedOrigins...),
			CompressionMode: websocket.CompressionDisabled,
		},
		maxConns:      config.MaxConnections,
		maxPerSource:  config.MaxPerSource,
		baseContext:   ctx,
		cancelContext: cancel,
		peers:         make(map[netip.AddrPort]*peer),
		perSource:     make(map[netip.Addr]int),
	}, nil
}

// NewHTTPServer returns a server without body/write timeouts: net/http leaves
// those deadlines on hijacked connections, which would cut live WebSockets.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:                         addr,
		Handler:                      handler,
		DisableGeneralOptionsHandler: true,
		MaxHeaderBytes:               16 << 10,
		ReadHeaderTimeout:            2 * time.Second,
	}
}

func (server *Server) Owns(endpoint netip.AddrPort) bool {
	return relaytransport.Tagged(endpoint, Zone)
}

func (server *Server) Send(datagram []byte, endpoint netip.AddrPort) bool {
	server.mu.Lock()
	target := server.peers[endpoint]
	server.mu.Unlock()
	if target == nil {
		return false
	}
	select {
	case target.queue <- datagram:
		return true
	default:
		return false
	}
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != Path {
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}
	remote, err := netip.ParseAddrPort(request.RemoteAddr)
	if err != nil {
		http.Error(writer, "bad peer address", http.StatusBadRequest)
		return
	}
	source := remote.Addr().WithZone("").Unmap()
	endpoint := relaytransport.Tag(remote, Zone)
	if !server.reserve(source, endpoint) {
		http.Error(writer, "relay connections exhausted", http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(writer, request, &server.accept)
	if err != nil {
		server.release(source, endpoint, nil)
		return
	}
	if conn.Subprotocol() != Subprotocol {
		_ = conn.Close(websocket.StatusPolicyViolation, "subprotocol "+Subprotocol+" required")
		server.release(source, endpoint, nil)
		return
	}
	conn.SetReadLimit(protocol.MaxDatagramBytes)
	target := &peer{conn: conn, queue: make(chan []byte, sendQueueDepth)}
	if !server.register(endpoint, target) {
		_ = conn.Close(websocket.StatusGoingAway, "relay closing")
		server.release(source, endpoint, nil)
		return
	}
	defer server.wg.Done()

	ctx, cancel := context.WithCancel(server.baseContext)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		server.writeLoop(ctx, target)
	}()
	status := server.readLoop(ctx, target, endpoint)
	server.release(source, endpoint, target)
	cancel()
	<-writerDone
	_ = conn.Close(status, "")
}

func (server *Server) readLoop(ctx context.Context, target *peer, endpoint netip.AddrPort) websocket.StatusCode {
	for {
		readContext, cancel := context.WithTimeout(ctx, idleTimeout)
		kind, message, err := target.conn.Read(readContext)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return websocket.StatusGoingAway
			}
			return websocket.StatusNormalClosure
		}
		if kind != websocket.MessageBinary {
			return websocket.StatusUnsupportedData
		}
		if err := server.deliver(message, endpoint); err != nil {
			server.fatal()
			return websocket.StatusInternalError
		}
	}
}

func (server *Server) writeLoop(ctx context.Context, target *peer) {
	for {
		select {
		case <-ctx.Done():
			return
		case datagram := <-target.queue:
			writeContext, cancel := context.WithTimeout(ctx, writeTimeout)
			err := target.conn.Write(writeContext, websocket.MessageBinary, datagram)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (server *Server) reserve(source netip.Addr, endpoint netip.AddrPort) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed || !endpoint.IsValid() || server.reserved >= server.maxConns ||
		server.perSource[source] >= server.maxPerSource {
		return false
	}
	if _, exists := server.peers[endpoint]; exists {
		return false
	}
	server.reserved++
	server.perSource[source]++
	server.peers[endpoint] = nil
	return true
}

func (server *Server) register(endpoint netip.AddrPort, target *peer) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return false
	}
	server.peers[endpoint] = target
	server.wg.Add(1)
	return true
}

func (server *Server) release(source netip.Addr, endpoint netip.AddrPort, target *peer) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if current, exists := server.peers[endpoint]; !exists || current != target {
		return
	}
	delete(server.peers, endpoint)
	server.reserved--
	if server.perSource[source]--; server.perSource[source] <= 0 {
		delete(server.perSource, source)
	}
}

// Connections reports open (and upgrading) connections.
func (server *Server) Connections() int {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.reserved
}

// Close ends every connection and waits for their handlers to finish.
func (server *Server) Close() error {
	server.mu.Lock()
	server.closed = true
	targets := make([]*peer, 0, len(server.peers))
	for _, target := range server.peers {
		if target != nil {
			targets = append(targets, target)
		}
	}
	server.mu.Unlock()
	// Close frames tell clients this is a shutdown, not a network loss; the
	// context cancel below bounds peers that never answer the handshake.
	var closing sync.WaitGroup
	for _, target := range targets {
		closing.Add(1)
		go func() {
			defer closing.Done()
			_ = target.conn.Close(websocket.StatusGoingAway, "relay closing")
		}()
	}
	closeWait := make(chan struct{})
	go func() {
		closing.Wait()
		close(closeWait)
	}()
	select {
	case <-closeWait:
	case <-time.After(writeTimeout):
	}
	server.cancelContext()
	server.wg.Wait()
	return nil
}
