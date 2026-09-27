#!/usr/bin/env bash
# Runs on the VM. Both HTTP listeners must answer an unauthenticated probe with
# 401: that proves the process serves requests without sending any credential.
set -euo pipefail
[[ $# == 0 || $1 == health ]] || { echo 'usage: check-deployment.sh [health]' >&2; exit 2; }
probe() {
  local code
  code=$(curl --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 2 --max-time 6 "$1") || true
  [[ $code == 401 ]] || { echo "unexpected status ${code:-none} from $1" >&2; return 1; }
}
probe http://127.0.0.1:8080/v1/lobbies
probe http://127.0.0.1:8081/v1/player-tokens
echo healthy
