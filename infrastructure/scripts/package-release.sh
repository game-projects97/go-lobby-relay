#!/usr/bin/env bash
# Build on CI/admin workstation, never on the production VM.
set -euo pipefail
[[ $# == 1 ]] || { echo 'usage: package-release.sh OUTPUT.tar.gz' >&2; exit 2; }
repo=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
output=$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve())' "$1")
[[ ! -e $output ]] || { echo 'output already exists; choose a new archive path' >&2; exit 2; }
if [[ -n ${GO_BINARY:-} ]]; then go_binary=$GO_BINARY
elif [[ -x $repo/.tools/go/bin/go ]]; then go_binary=$repo/.tools/go/bin/go
else go_binary=$(command -v go); fi
# Release builds use exactly the toolchain pinned by go.mod.
go_version=$(sed -n 's/^go \([0-9.]*\)$/\1/p' "$repo/go.mod")
[[ $(GOTOOLCHAIN=local "$go_binary" version) == "go version go$go_version "* ]] || { echo "Go $go_version required" >&2; exit 2; }
stage=$(mktemp -d)
trap 'rm -rf -- "$stage"' EXIT
mkdir -p "$stage/ops" "$stage/scripts"
(cd "$repo" && GOTOOLCHAIN=local GOOS=linux GOARCH=amd64 CGO_ENABLED=0 "$go_binary" build -trimpath -ldflags='-s -w -buildid=' -o "$stage/lobby-relay" ./cmd/lobby-relay)
chmod 0755 "$stage/lobby-relay"
for name in lobby-relay.service traffic-guard.service traffic-guard.timer Caddyfile caddy-lobby-relay.conf; do install -m 0644 "$repo/infrastructure/ops/$name" "$stage/ops/$name"; done
for name in setup-host.sh install-operator-token.py check-deployment.sh deploy-backend.sh check-traffic-budget.sh traffic_budget.py; do install -m 0755 "$repo/infrastructure/scripts/$name" "$stage/scripts/$name"; done
python3 - "$stage" <<'PY'
import hashlib, pathlib, sys
root=pathlib.Path(sys.argv[1])
files=sorted(p for p in root.rglob('*') if p.is_file())
(root/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.relative_to(root).as_posix()+'\n' for p in files))
PY
# COPYFILE_DISABLE avoids macOS metadata entries in the Linux release archive.
COPYFILE_DISABLE=1 tar -czf "$output" -C "$stage" lobby-relay ops scripts SHA256SUMS
printf 'Release archive: %s\n' "$output"
