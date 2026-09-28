package main

import (
	"reflect"
	"testing"
	"time"
)

func TestParseConfigOptionalWebSocketAndMatchTTL(t *testing.T) {
	tokenPath := writeTokenFile(t, "\n")
	base := validArgs(tokenPath)
	config, err := parseConfig(base)
	if err != nil || config.WebSocketListen != "" || config.WebSocketAllowedOrigins != nil ||
		config.WebSocketMaxPerSource != 0 || config.MatchTTL != 0 {
		t.Fatalf("defaults = %#v, %v", config, err)
	}

	args := append(append([]string(nil), base...),
		"--relay-ws-listen", "127.0.0.1:0",
		"--relay-ws-allowed-origin", "game.test",
		"--relay-ws-allowed-origin", "https://*.game.test",
		"--relay-ws-max-per-source", "64",
		"--match-ttl", "20m",
	)
	config, err = parseConfig(args)
	if err != nil {
		t.Fatalf("parseConfig(optional): %v", err)
	}
	if config.WebSocketListen != "127.0.0.1:0" || config.WebSocketMaxPerSource != 64 || config.MatchTTL != 20*time.Minute ||
		!reflect.DeepEqual(config.WebSocketAllowedOrigins, []string{"game.test", "https://*.game.test"}) {
		t.Fatalf("parseConfig(optional) = %#v", config)
	}

	for name, extra := range map[string][]string{
		"duplicate ws listen": {"--relay-ws-listen", "127.0.0.1:0", "--relay-ws-listen", "127.0.0.1:1"},
		"empty ws listen":     {"--relay-ws-listen", ""},
		"empty origin":        {"--relay-ws-allowed-origin", ""},
		"zero per source":     {"--relay-ws-max-per-source", "0"},
		"bad per source":      {"--relay-ws-max-per-source", "many"},
		"bad match ttl":       {"--match-ttl", "soon"},
		"negative match ttl":  {"--match-ttl", "-1m"},
		"zero match ttl":      {"--match-ttl", "0s"},
	} {
		if _, err := parseConfig(append(append([]string(nil), base...), extra...)); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
