#!/usr/bin/env python3
"""Run setup-host.sh against a temporary root with stubbed system commands."""
import os
from pathlib import Path
import subprocess
import tempfile

SOURCE = Path(__file__).resolve().parents[1] / 'setup-host.sh'
PREFIXES = ['/etc/os-release', '/etc/lobby-relay', '/etc/ssh/sshd_config.d', '/etc/systemd/journald.conf.d',
            '/var/lib/lobby-relay', '/opt/lobby-relay', '/root/.ssh', '/home/']
VALID = ['--api-hostname', 'api.example.com', '--relay-udp-port', '7777', '--admin-ssh-cidr', '192.0.2.1/32',
         '--traffic-warn-bytes', '100', '--traffic-stop-bytes', '300']

def executable(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    path.chmod(0o755)

def run(args, os_release='ID=ubuntu\nVERSION_ID="24.04"\n', key=True, ssh_client=None, ledger=False):
    with tempfile.TemporaryDirectory(prefix='lobby-relay-setup-test-') as tmp:
        root = Path(tmp); log = root / 'calls'
        (root / 'etc').mkdir(); (root / 'etc/os-release').write_text(os_release)
        (root / 'home/admin/.ssh').mkdir(parents=True)
        if key: (root / 'home/admin/.ssh/authorized_keys').write_text('ssh-ed25519 AAAA test\n')
        if ledger: (root / 'var/lib/lobby-relay').mkdir(parents=True); (root / 'var/lib/lobby-relay/traffic.json').write_text('{}')
        for name in ['apt-get', 'systemctl', 'ufw', 'sshd', 'useradd']:
            executable(root / 'bin' / name, f'#!/bin/sh\necho "{name} $*" >> "{log}"\n')
        executable(root / 'bin/getent', '#!/bin/sh\nexit 2\n')
        # CI runs unprivileged: report root and drop ownership flags, keep real modes.
        executable(root / 'bin/id', '#!/bin/sh\necho 0\n')
        executable(root / 'bin/install', '#!/usr/bin/env python3\nimport os,sys\na=sys.argv[1:];r=[]\nwhile a:\n x=a.pop(0)\n if x in("-o","-g"):a.pop(0);continue\n r.append(x)\nos.execv("/usr/bin/install",["install",*r])\n')
        executable(root / 'bin/ip', '#!/bin/sh\necho "1.1.1.1 via 10.0.0.1 dev ens5 src 10.0.0.2 uid 0"\n')
        scripts = root / 'release/scripts'
        source = SOURCE.read_text()
        for prefix in PREFIXES:
            source = source.replace(prefix, str(root) + prefix)
        executable(scripts / 'setup-host.sh', source)
        executable(scripts / 'install-operator-token.py', f'open("{log}","a").write("operator-token\\n")\n')
        executable(scripts / 'check-traffic-budget.sh', f'#!/bin/sh\necho "guard $* $TRAFFIC_INTERFACE $TRAFFIC_WARN_BYTES $TRAFFIC_STOP_BYTES" >> "{log}"\n')
        executable(scripts / 'traffic_budget.py', '')
        env = {**os.environ, 'PATH': str(root / 'bin') + os.pathsep + os.environ['PATH']}
        env.pop('SSH_CONNECTION', None)
        if ssh_client: env['SSH_CONNECTION'] = f'{ssh_client} 50000 10.0.0.2 22'
        result = subprocess.run(['bash', str(scripts / 'setup-host.sh'), *args], env=env, text=True, capture_output=True, timeout=20)
        calls = log.read_text() if log.exists() else ''
        files = {p: (root / p).read_text() for p in ['etc/lobby-relay/server.env', 'etc/lobby-relay/traffic.env', 'etc/ssh/sshd_config.d/10-lobby-relay.conf'] if (root / p).exists()}
        modes = {p: oct((root / p).stat().st_mode & 0o7777) for p in ['etc/lobby-relay', 'var/lib/lobby-relay', 'opt/lobby-relay/ops'] if (root / p).exists()}
        return result, calls, files, modes

result, calls, files, modes = run(VALID, ssh_client='192.0.2.1')
assert result.returncode == 0, result.stderr
assert files['etc/lobby-relay/server.env'] == 'API_HOSTNAME=api.example.com\nRELAY_ADVERTISED_HOST=api.example.com\nRELAY_UDP_PORT=7777\n', files
assert files['etc/lobby-relay/traffic.env'] == 'TRAFFIC_INTERFACE=ens5\nTRAFFIC_WARN_BYTES=100\nTRAFFIC_STOP_BYTES=300\n', files
assert 'PasswordAuthentication no' in files['etc/ssh/sshd_config.d/10-lobby-relay.conf']
assert modes == {'etc/lobby-relay': '0o711', 'var/lib/lobby-relay': '0o755', 'opt/lobby-relay/ops': '0o755'}, modes
ufw = [line for line in calls.splitlines() if line.startswith('ufw allow')]
assert ufw == ['ufw allow from 192.0.2.1/32 to any port 22 proto tcp', 'ufw allow 80/tcp', 'ufw allow 443/tcp', 'ufw allow 7777/udp'], ufw
assert 'systemctl disable --now caddy.service' in calls, 'packaged Caddy default site must not stay public'
assert not any(line.startswith(('systemctl start', 'systemctl enable')) for line in calls.splitlines()), 'setup must start no public service'
assert 'operator-token' in calls and 'guard --initialize ens5 100 300' in calls, calls
assert calls.index('ufw --force enable') < calls.index('operator-token')

result, calls, files, _ = run([*VALID, '--relay-hostname', 'relay.example.com'], ledger=True)
assert result.returncode == 0 and 'RELAY_ADVERTISED_HOST=relay.example.com' in files['etc/lobby-relay/server.env']
assert 'guard --initialize' not in calls, 'existing traffic ledger must never be reinitialized'

def refused(args, message, **options):
    result, calls, files, _ = run(args, **options)
    assert result.returncode != 0 and message in result.stderr, (args, options, result.stderr)
    assert 'apt-get' not in calls and not files, 'refusal must happen before any host change'

refused([*VALID[:4], '--admin-ssh-cidr', '0.0.0.0/0', *VALID[6:]], 'one IPv4 /32')
refused([*VALID[:2], '--relay-udp-port', '53', *VALID[4:]], 'unprivileged relay UDP port')
refused([*VALID[:2], '--relay-udp-port', '08080', *VALID[4:]], 'unprivileged relay UDP port')
refused([*VALID[:8], '--traffic-stop-bytes', '50'], 'warn < stop')
refused([*VALID[:8], '--traffic-stop-bytes', '3000000000000'], 'warn < stop')
refused(['--api-hostname', 'API.example.com', *VALID[2:]], 'lowercase DNS hostnames')
refused(VALID, 'Ubuntu 24.04 required', os_release='ID=debian\nVERSION_ID="12"\n')
refused(VALID, 'install an SSH public key', key=False)
refused(VALID, 'refusing to lock it out', ssh_client='198.51.100.7')
refused([*VALID, '--unknown', 'x'], 'usage')
print('PASS setup-host: settings, firewall, no public start, ledger preservation and input/lockout refusals (stubbed host).')
