#!/usr/bin/env bash
# Run via sudo on the VM: deploy-backend.sh RELEASE_ARCHIVE COMMIT_SHA
# All lobby, ticket, token and relay state is in memory: every restart ends it.
set -euo pipefail
umask 077
[[ $(id -u) == 0 && $# == 2 && $2 =~ ^[a-f0-9]{40}$ ]] || { echo 'root, archive and full commit SHA required' >&2; exit 2; }
archive=$(realpath "$1")
release="/opt/lobby-relay/releases/$2"
[[ ! -e /var/lib/lobby-relay/traffic-stopped ]] || { echo 'reconcile cost guard before deployment' >&2; exit 1; }
exec 9>/var/lock/lobby-relay-deploy.lock
flock -n 9 || { echo 'another deployment is running' >&2; exit 1; }
stage=$(mktemp -d /opt/lobby-relay/releases/.stage.XXXXXX)
old=$(readlink -e /opt/lobby-relay/current || true)
changed=false
cleanup() {
  code=$?
  trap - EXIT
  if [[ $code != 0 && $changed == true ]]; then
    if [[ -n $old && -x $old/lobby-relay ]]; then
      activate "$old"
      systemctl try-reload-or-restart caddy.service || true
      restored=false
      if systemctl restart lobby-relay.service; then
        for _ in {1..15}; do
          if "$old/scripts/check-deployment.sh" health >/dev/null 2>&1; then restored=true;break;fi
          sleep 1
        done
      fi
      if [[ $restored == true ]]; then echo 'deployment failed; previous release restored (in-memory state cannot be restored)' >&2
      else echo 'rollback health check failed; direct administrator recovery required' >&2; fi
    else
      systemctl stop lobby-relay.service || true
      echo 'first deployment failed; relay stopped' >&2
    fi
  fi
  rm -rf -- "$stage"
  exit "$code"
}
trap cleanup EXIT
# The protected CI archive is still constrained: no links/traversal/devices/oversize.
python3 - "$archive" "$stage" <<'PY'
import tarfile,sys
from pathlib import PurePosixPath
with tarfile.open(sys.argv[1]) as tar:
    members=tar.getmembers()
    if len(members)>30 or sum(m.size for m in members)>150*1024*1024: raise SystemExit('invalid release size')
    for m in members:
        p=PurePosixPath(m.name)
        if p.is_absolute() or '..' in p.parts or not (m.isfile() or m.isdir()) or m.mode&0o7000: raise SystemExit('invalid archive entry')
    tar.extractall(sys.argv[2],filter='data')
PY
(cd "$stage" && sha256sum --check SHA256SUMS)
# The binary has no dry-run mode; validate the non-secret settings it will receive.
python3 - /etc/lobby-relay/server.env <<'PY'
import re,sys
values=dict(line.split('=',1) for line in open(sys.argv[1]).read().splitlines() if line and not line.startswith('#'))
name=r'[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?'
port=values.get('RELAY_UDP_PORT','')
if not all(re.fullmatch(name,values.get(key,'')) for key in ('API_HOSTNAME','RELAY_ADVERTISED_HOST')) or not re.fullmatch(r'[1-9][0-9]{3,4}',port) or not 1024<=int(port)<=65535:
    raise SystemExit('server.env requires API_HOSTNAME, RELAY_ADVERTISED_HOST and an unprivileged RELAY_UDP_PORT')
PY
chmod 0755 "$stage" "$stage/lobby-relay"
if [[ -e $release ]]; then
  cmp "$stage/SHA256SUMS" "$release/SHA256SUMS" || { echo 'release ID already has different content' >&2; exit 1; }
else
  mv "$stage" "$release"
  stage=$(mktemp -d /opt/lobby-relay/releases/.stage.XXXXXX)
fi
activate() {
  local target=$1
  install -d -m 0755 /opt/lobby-relay/ops
  install -m 0755 "$target/scripts/check-traffic-budget.sh" "$target/scripts/traffic_budget.py" /opt/lobby-relay/ops/
  install -m 0644 "$target/ops/lobby-relay.service" "$target/ops/traffic-guard.service" "$target/ops/traffic-guard.timer" /etc/systemd/system/
  install -d -m 0755 /etc/systemd/system/caddy.service.d
  install -m 0644 "$target/ops/caddy-lobby-relay.conf" /etc/systemd/system/caddy.service.d/lobby-relay.conf
  install -m 0644 "$target/ops/Caddyfile" /etc/caddy/Caddyfile
  ln -sfn "$target" /opt/lobby-relay/current.next
  mv -Tf /opt/lobby-relay/current.next /opt/lobby-relay/current
  systemctl daemon-reload
}
[[ -e /var/lib/lobby-relay/traffic.json ]] || { echo 'run setup-host.sh before the first deployment' >&2; exit 1; }
changed=true
activate "$release"
systemctl enable lobby-relay.service >/dev/null
systemctl restart lobby-relay.service
healthy=false
for _ in {1..15}; do
  if "$release/scripts/check-deployment.sh" health >/dev/null 2>&1; then healthy=true;break;fi
  sleep 1
done
[[ $healthy == true ]] || { echo 'new server health check failed' >&2; exit 1; }
# Reconcile now: a timer alone leaves a window with stale/corrupt accounting.
systemctl start traffic-guard.service
[[ ! -e /var/lib/lobby-relay/traffic-stopped ]] || { echo 'cost guard stopped deployment' >&2; exit 1; }
systemctl enable --now traffic-guard.timer >/dev/null
# Reload applies a changed Caddyfile without dropping TLS; it starts Caddy on first deploy.
systemctl enable caddy.service >/dev/null
systemctl reload-or-restart caddy.service
echo "deployed $2; previous=${old:-none}"
