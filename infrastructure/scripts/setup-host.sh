#!/usr/bin/env bash
# One-time (re-runnable) host preparation for any Ubuntu 24.04 VM, independent of
# the hosting provider. Run as root from an extracted release archive:
#   scripts/setup-host.sh --api-hostname api.example.com --relay-udp-port 7777 \
#     --admin-ssh-cidr 203.0.113.4/32 --traffic-warn-bytes N --traffic-stop-bytes N
# It installs packages, users, directories, firewall, settings, the operator token
# and the traffic ledger. It starts no public service; deploy-backend.sh does.
set -euo pipefail
umask 022
usage() {
  echo 'usage: setup-host.sh --api-hostname HOST [--relay-hostname HOST] --relay-udp-port PORT --admin-ssh-cidr CIDR --traffic-warn-bytes N --traffic-stop-bytes N [--traffic-interface IFACE]' >&2
  exit 2
}
api_hostname='' relay_hostname='' relay_port='' admin_cidr='' warn_bytes='' stop_bytes='' interface=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || usage
  case $1 in
    --api-hostname) api_hostname=$2;;
    --relay-hostname) relay_hostname=$2;;
    --relay-udp-port) relay_port=$2;;
    --admin-ssh-cidr) admin_cidr=$2;;
    --traffic-warn-bytes) warn_bytes=$2;;
    --traffic-stop-bytes) stop_bytes=$2;;
    --traffic-interface) interface=$2;;
    *) usage;;
  esac
  shift 2
done
relay_hostname=${relay_hostname:-$api_hostname}
hostname_re='^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$'
[[ $api_hostname =~ $hostname_re && $relay_hostname =~ $hostname_re ]] || { echo 'lowercase DNS hostnames required' >&2; exit 2; }
[[ $relay_port =~ ^[1-9][0-9]{3,4}$ ]] && (( relay_port >= 1024 && relay_port <= 65535 )) || { echo 'unprivileged relay UDP port required' >&2; exit 2; }
# One administrator address only: SSH is never opened to the internet.
[[ $admin_cidr =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/32$ ]] || { echo 'admin SSH source must be one IPv4 /32' >&2; exit 2; }
# Same bound as traffic_budget.validate_limits; a rejected ledger would latch a stop marker.
[[ $warn_bytes =~ ^[1-9][0-9]{0,12}$ && $stop_bytes =~ ^[1-9][0-9]{0,12}$ ]] && (( warn_bytes < stop_bytes && stop_bytes < 3000000000000 )) || { echo 'require 0 < warn < stop < 3000000000000 traffic bytes' >&2; exit 2; }
[[ $(id -u) == 0 ]] || { echo 'run as root' >&2; exit 2; }
# Enabling the firewall must not cut off the session running this script.
if [[ -n ${SSH_CONNECTION:-} && ${SSH_CONNECTION%% *} != "${admin_cidr%/32}" ]]; then
  echo "current SSH client ${SSH_CONNECTION%% *} is not $admin_cidr; refusing to lock it out" >&2
  exit 1
fi
grep -qx 'VERSION_ID="24.04"' /etc/os-release && grep -qx 'ID=ubuntu' /etc/os-release || { echo 'Ubuntu 24.04 required' >&2; exit 2; }
if [[ -z $interface ]]; then
  interface=$(ip -o route get 1.1.1.1 | sed -n 's/.* dev \([^ ]*\).*/\1/p')
fi
[[ $interface =~ ^[A-Za-z0-9_.-]{1,15}$ && $interface != lo ]] || { echo 'external traffic interface not found; pass --traffic-interface' >&2; exit 2; }
# Refuse to disable password login unless a key already works for some account.
compgen -G '/root/.ssh/authorized_keys' >/dev/null || compgen -G '/home/*/.ssh/authorized_keys' >/dev/null || { echo 'install an SSH public key before hardening sshd' >&2; exit 1; }
here=$(CDPATH= cd -- "$(dirname "$0")" && pwd)

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ufw caddy python3 curl ca-certificates unattended-upgrades
# The package starts Caddy with its default site; keep every public service off
# until deploy-backend.sh installs the real configuration.
systemctl disable --now caddy.service

getent passwd lobby-relay >/dev/null || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin lobby-relay
install -d -m 0755 /opt/lobby-relay /opt/lobby-relay/releases /opt/lobby-relay/ops
install -d -m 0711 -o root -g root /etc/lobby-relay
# Root-owned so services cannot delete traffic safety state.
install -d -m 0755 -o root -g root /var/lib/lobby-relay

install -d -m 0755 /etc/ssh/sshd_config.d /etc/systemd/journald.conf.d
printf '%s\n' 'PasswordAuthentication no' 'KbdInteractiveAuthentication no' 'PermitRootLogin prohibit-password' > /etc/ssh/sshd_config.d/10-lobby-relay.conf
printf '%s\n' '[Journal]' 'SystemMaxUse=100M' 'RuntimeMaxUse=32M' 'MaxRetentionSec=7day' > /etc/systemd/journald.conf.d/lobby-relay.conf
printf '%s\n' "API_HOSTNAME=$api_hostname" "RELAY_ADVERTISED_HOST=$relay_hostname" "RELAY_UDP_PORT=$relay_port" > /etc/lobby-relay/server.env
printf '%s\n' "TRAFFIC_INTERFACE=$interface" "TRAFFIC_WARN_BYTES=$warn_bytes" "TRAFFIC_STOP_BYTES=$stop_bytes" > /etc/lobby-relay/traffic.env
chmod 0644 /etc/lobby-relay/server.env /etc/lobby-relay/traffic.env

ufw default deny incoming
ufw default allow outgoing
ufw allow from "$admin_cidr" to any port 22 proto tcp
ufw allow 80/tcp
ufw allow 443/tcp
ufw allow "$relay_port/udp"
ufw --force enable
sshd -t
systemctl restart ssh
systemctl restart systemd-journald

python3 "$here/install-operator-token.py"
install -m 0755 "$here/check-traffic-budget.sh" "$here/traffic_budget.py" /opt/lobby-relay/ops/
if [[ ! -e /var/lib/lobby-relay/traffic.json ]]; then
  env TRAFFIC_INTERFACE="$interface" TRAFFIC_WARN_BYTES="$warn_bytes" TRAFFIC_STOP_BYTES="$stop_bytes" /opt/lobby-relay/ops/check-traffic-budget.sh --initialize
fi
echo "host ready: point $api_hostname and $relay_hostname at this host, open 22/tcp (admin), 80/tcp, 443/tcp and $relay_port/udp in any provider firewall, then run deploy-backend.sh"
