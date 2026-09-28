#!/usr/bin/env bash
# Runs on the VM. Both HTTP API listeners must answer an unauthenticated probe with
# 401: that proves the process serves requests without sending any credential.
# The WebSocket Relay listener answers a plain GET (no Upgrade) with 426.
set -euo pipefail
[[ $# == 0 || $1 == health ]] || { echo 'usage: check-deployment.sh [health]' >&2; exit 2; }
probe() {
  local code
  code=$(curl --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 2 --max-time 6 "$2") || true
  [[ $code == "$1" ]] || { echo "unexpected status ${code:-none} from $2" >&2; return 1; }
}
probe 401 http://127.0.0.1:8080/v1/lobbies
probe 401 http://127.0.0.1:8081/v1/player-tokens
probe 426 http://127.0.0.1:8082/v1/relay
echo healthy
