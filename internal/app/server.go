package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gyungsubLee/go-lobby-relay/internal/httpapi/operatorapi"
	"github.com/gyungsubLee/go-lobby-relay/internal/httpapi/playerapi"
	"github.com/gyungsubLee/go-lobby-relay/internal/matchmaking"
	"github.com/gyungsubLee/go-lobby-relay/internal/playerauth"
	"github.com/gyungsubLee/go-lobby-relay/internal/relayroom"
	"github.com/gyungsubLee/go-lobby-relay/internal/udprelay"
)

var (
	errInvalidConfig  = errors.New("app: invalid configuration")
	errBind           = errors.New("app: listener bind failed")
	errOwnedLoop      = errors.New("app: owned loop failed")
	errAlreadyRunning = errors.New("app: already running")
	errClose          = errors.New("app: close failed")
)

type Config struct {
	OperatorListen string
	PlayerListen   string
	RelayNetwork   string
	RelayListen    string
	AdvertisedHost string
	AdvertisedPort uint16
	OperatorToken  [32]byte
}

type dependencies struct {
	listenTCP        func(string, string) (net.Listener, error)
	listenUDP        func(string, *net.UDPAddr) (*net.UDPConn, error)
	random           io.Reader
	playerAuthRandom io.Reader
}

func defaultDependencies() dependencies {
	return dependencies{listenTCP: net.Listen, listenUDP: net.ListenUDP}
}

type Server struct {
	operatorListener net.Listener
	operatorServer   *http.Server
	playerListener   net.Listener
	playerServer     *http.Server
	udpRelay         *udprelay.Server
	rooms            *relayroom.Store
	lobbies          *matchmaking.Manager
	operatorAddr     net.Addr
	playerAddr       net.Addr
	relayAddr        net.Addr

	mu              sync.Mutex
	runStarted      bool
	closeRequested  bool
	finished        bool
	closeSignal     chan struct{}
	closeSignalOnce sync.Once
	fatalSignal     chan struct{}
	fatalSignalOnce sync.Once
	shutdownOnce    sync.Once
	shutdownErr     error
}

func New(config Config) (*Server, error) {
	return newWithDependencies(config, defaultDependencies())
}

func newWithDependencies(config Config, deps dependencies) (*Server, error) {
	relayAddress, err := validateConfig(config)
	if err != nil || deps.listenTCP == nil || deps.listenUDP == nil {
		return nil, errInvalidConfig
	}
	rooms, err := relayroom.New(relayroom.Config{Limits: relayroom.DefaultLimits(), Random: deps.random})
	if err != nil {
		return nil, errInvalidConfig
	}
	playerTokens, err := playerauth.New(playerauth.Config{OperatorSecret: config.OperatorToken, Random: deps.playerAuthRandom, TokenTTL: playerauth.HardTokenTTL})
	if err != nil {
		return nil, errInvalidConfig
	}
	lobbies, err := matchmaking.New(matchmaking.Config{Rooms: rooms, Random: deps.random})
	if err != nil {
		return nil, errInvalidConfig
	}
	operatorListener, err := deps.listenTCP("tcp", config.OperatorListen)
	if err != nil {
		return nil, errBind
	}
	playerListener, err := deps.listenTCP("tcp", config.PlayerListen)
	if err != nil {
		_ = operatorListener.Close()
		return nil, errBind
	}
	relaySocket, err := deps.listenUDP(config.RelayNetwork, relayAddress)
	if err != nil {
		_ = playerListener.Close()
		_ = operatorListener.Close()
		return nil, errBind
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = operatorListener.Close()
			_ = playerListener.Close()
			_ = relaySocket.Close()
		}
	}()

	advertisedPort := config.AdvertisedPort
	if advertisedPort == 0 {
		advertisedPort = uint16(relaySocket.LocalAddr().(*net.UDPAddr).Port)
	}
	server := &Server{
		operatorListener: operatorListener,
		playerListener:   playerListener,
		rooms:            rooms,
		lobbies:          lobbies,
		operatorAddr:     operatorListener.Addr(),
		playerAddr:       playerListener.Addr(),
		relayAddr:        relaySocket.LocalAddr(),
		closeSignal:      make(chan struct{}),
		fatalSignal:      make(chan struct{}),
	}
	handler, err := operatorapi.NewHandler(operatorapi.Config{
		OperatorToken:  config.OperatorToken,
		PlayerTokens:   playerTokens,
		AdvertisedHost: config.AdvertisedHost,
		AdvertisedPort: advertisedPort,
		RequestRate:    operatorapi.HardOperatorRequestRate,
		RequestBurst:   operatorapi.HardOperatorRequestBurst,
		MaxConcurrent:  operatorapi.HardOperatorConcurrent,
		Fatal:          server.notifyFatal,
	}, rooms)
	if err != nil {
		return nil, errInvalidConfig
	}
	playerHandler, err := playerapi.NewHandler(playerapi.Config{
		PlayerTokens: playerTokens, Lobbies: lobbies, AdvertisedHost: config.AdvertisedHost, AdvertisedPort: advertisedPort,
		RequestRate: playerapi.HardPlayerRequestRate, RequestBurst: playerapi.HardPlayerRequestBurst,
		MaxConcurrent: playerapi.HardPlayerConcurrent, Fatal: server.notifyFatal,
	})
	if err != nil {
		return nil, errInvalidConfig
	}
	udpRelay, err := udprelay.New(relaySocket, rooms, udprelay.Config{})
	if err != nil {
		return nil, errInvalidConfig
	}

	server.operatorServer = operatorapi.NewServer(operatorListener.Addr().String(), handler)
	server.playerServer = playerapi.NewServer(playerListener.Addr().String(), playerHandler)
	server.udpRelay = udpRelay
	cleanup = false
	return server, nil
}

func validateConfig(config Config) (*net.UDPAddr, error) {
	if config.OperatorListen == "" || config.PlayerListen == "" || config.RelayListen == "" || config.AdvertisedHost == "" ||
		config.OperatorToken == ([32]byte{}) || (config.RelayNetwork != "udp4" && config.RelayNetwork != "udp6") {
		return nil, errInvalidConfig
	}
	if _, err := net.ResolveTCPAddr("tcp", config.OperatorListen); err != nil {
		return nil, errInvalidConfig
	}
	if _, err := net.ResolveTCPAddr("tcp", config.PlayerListen); err != nil {
		return nil, errInvalidConfig
	}
	relayAddress, err := net.ResolveUDPAddr(config.RelayNetwork, config.RelayListen)
	if err != nil || relayAddress == nil || (config.AdvertisedPort == 0 && relayAddress.Port != 0) {
		return nil, errInvalidConfig
	}
	return relayAddress, nil
}

func (server *Server) OperatorAddr() net.Addr { return server.operatorAddr }

func (server *Server) PlayerAddr() net.Addr { return server.playerAddr }

func (server *Server) RelayAddr() net.Addr { return server.relayAddr }

type loopResult struct {
	name       string
	err        error
	unexpected bool
}

func (server *Server) Run(ctx context.Context) error {
	if ctx == nil {
		return errInvalidConfig
	}
	server.mu.Lock()
	if server.closeRequested {
		server.mu.Unlock()
		return nil
	}
	if server.runStarted || server.finished {
		server.mu.Unlock()
		return errAlreadyRunning
	}
	server.runStarted = true
	server.mu.Unlock()

	runContext, cancel := context.WithCancel(ctx)
	results := make(chan loopResult, 4)
	go func() {
		err := server.operatorServer.Serve(server.operatorListener)
		results <- server.classifyLoopResult(runContext, "operator", err)
	}()
	go func() {
		err := server.playerServer.Serve(server.playerListener)
		results <- server.classifyLoopResult(runContext, "player", err)
	}()
	go func() {
		err := server.udpRelay.Run()
		results <- server.classifyLoopResult(runContext, "relay", err)
	}()
	go func() {
		server.runSweeper(runContext)
		results <- server.classifyLoopResult(runContext, "sweeper", nil)
	}()

	runErr := coordinateLoopResults(runContext, server.closeSignal, server.fatalSignal, results, func() {
		cancel()
		_ = server.shutdown()
	})

	server.mu.Lock()
	server.finished = true
	server.mu.Unlock()
	return runErr
}

func (server *Server) runSweeper(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			server.rooms.Expire()
			server.lobbies.Expire()
		}
	}
}

func (server *Server) notifyFatal() {
	server.fatalSignalOnce.Do(func() { close(server.fatalSignal) })
}

func (server *Server) classifyLoopResult(ctx context.Context, name string, err error) loopResult {
	return loopResult{name: name, err: err, unexpected: !server.intentionalStop(ctx)}
}

func coordinateLoopResults(
	ctx context.Context,
	closeSignal, fatalSignal <-chan struct{},
	results <-chan loopResult,
	stop func(),
) error {
	received := 0
	unexpected := false
	select {
	case <-ctx.Done():
	case <-closeSignal:
	case <-fatalSignal:
		unexpected = true
	case result := <-results:
		received = 1
		unexpected = result.unexpected
	}
	stop()
	for received < 4 {
		if (<-results).unexpected {
			unexpected = true
		}
		received++
	}
	select {
	case <-fatalSignal:
		unexpected = true
	default:
	}
	if unexpected {
		return errOwnedLoop
	}
	return nil
}

func (server *Server) intentionalStop(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.closeRequested
}

func (server *Server) Close() error {
	server.mu.Lock()
	server.closeRequested = true
	server.mu.Unlock()
	server.closeSignalOnce.Do(func() { close(server.closeSignal) })
	return server.shutdown()
}

func (server *Server) shutdown() error {
	server.shutdownOnce.Do(func() {
		operatorErr := server.operatorServer.Close()
		playerErr := server.playerServer.Close()
		listenerErr := server.operatorListener.Close()
		playerListenerErr := server.playerListener.Close()
		relayErr := server.udpRelay.Close()
		if operatorErr != nil && !errors.Is(operatorErr, http.ErrServerClosed) && !errors.Is(operatorErr, net.ErrClosed) ||
			playerErr != nil && !errors.Is(playerErr, http.ErrServerClosed) && !errors.Is(playerErr, net.ErrClosed) ||
			listenerErr != nil && !errors.Is(listenerErr, net.ErrClosed) ||
			playerListenerErr != nil && !errors.Is(playerListenerErr, net.ErrClosed) || relayErr != nil {
			server.shutdownErr = errClose
		}
	})
	return server.shutdownErr
}
