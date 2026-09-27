#!/usr/bin/env python3
"""Exercise the real deployment transaction with fake VM services in a temp root."""
import hashlib
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

SOURCE = Path(__file__).resolve().parents[1] / 'deploy-backend.sh'
SHA = 'a' * 40

def executable(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    path.chmod(0o755)

def scenario(fail_health=False, marker=False, bad_env=False, first=False):
    with tempfile.TemporaryDirectory(prefix='lobby-relay-deploy-test-') as tmp:
        root=Path(tmp); opt=root/'opt/lobby-relay'; state=root/'var/lib/lobby-relay'; etc=root/'etc/lobby-relay'
        for p in [opt/'releases',state,etc,root/'var/lock',root/'etc/systemd/system',root/'bin']:p.mkdir(parents=True,exist_ok=True)
        port='80' if bad_env else '7777'
        (etc/'server.env').write_text(f'# comment\nRELAY_ADVERTISED_HOST=relay.example.com\nRELAY_UDP_PORT={port}\n')
        (state/'traffic.json').write_text('{}')
        if marker:(state/'traffic-stopped').touch()
        unit_names=['lobby-relay.service','cloudflared.service','traffic-guard.service','traffic-guard.timer']
        def release(path, is_new):
            path.mkdir(parents=True)
            executable(path/'lobby-relay','#!/bin/sh\nexit 0\n')
            executable(path/'cloudflared','#!/bin/sh\nexit 0\n')
            for n in unit_names:
                p=path/'ops'/n;p.parent.mkdir(exist_ok=True);p.write_text('[Unit]\nDescription=fixture\n')
            check='#!/bin/sh\n'
            if is_new and fail_health:check+='exit 1\n'
            executable(path/'scripts/check-deployment.sh',check+'echo healthy\n')
            for n in ['check-traffic-budget.sh','traffic_budget.py']:executable(path/'scripts'/n,'#!/bin/sh\nexit 0\n')
            files=sorted(p for p in path.rglob('*') if p.is_file())
            (path/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+str(p.relative_to(path))+'\n' for p in files))
        old=opt/'releases'/'old'
        if not first:release(old,False);(opt/'current').symlink_to(old)
        bundle=root/'bundle';release(bundle,True);archive=root/'release.tar.gz'
        with tarfile.open(archive,'w:gz') as tar:
            for p in bundle.rglob('*'):tar.add(p,arcname=str(p.relative_to(bundle)),recursive=False)
        executable(root/'bin/id','#!/bin/sh\necho 0\n')
        executable(root/'bin/flock','#!/bin/sh\nexit 0\n')
        executable(root/'bin/sleep','#!/bin/sh\nexit 0\n')
        executable(root/'bin/systemctl','#!/bin/sh\necho "$@" >> "'+str(root/'systemctl-calls')+'"\nexit 0\n')
        executable(root/'bin/mv', '#!/usr/bin/env python3\nimport os,sys\na=[x for x in sys.argv[1:] if not x.startswith("-")];os.replace(a[-2],a[-1])\n')
        executable(root/'bin/sha256sum', '#!/usr/bin/env python3\nimport hashlib,pathlib,sys\nfor line in pathlib.Path(sys.argv[-1]).read_text().splitlines():\n h,p=line.split("  ",1)\n if hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest()!=h:sys.exit(1)\n')
        source=SOURCE.read_text()
        for prefix in ['/opt/lobby-relay','/var/lib/lobby-relay','/var/lock/lobby-relay-deploy.lock','/etc/lobby-relay','/etc/systemd/system']:source=source.replace(prefix,str(root/prefix.lstrip('/')))
        script=root/'deploy.sh';executable(script,source)
        env={**os.environ,'PATH':str(root/'bin')+os.pathsep+os.environ['PATH']}
        result=subprocess.run(['bash',str(script),str(archive),SHA],env=env,text=True,capture_output=True,timeout=20)
        expected_failure=fail_health or marker or bad_env
        assert (result.returncode!=0)==expected_failure,(result.stdout,result.stderr)
        calls=(root/'systemctl-calls').read_text() if (root/'systemctl-calls').exists() else ''
        if expected_failure and not first:
            assert (opt/'current').resolve()==old.resolve(),(result.stdout,result.stderr)
        if not expected_failure:
            assert (opt/'current').resolve()==(opt/'releases'/SHA).resolve(),(result.stdout,result.stderr)
            assert 'restart lobby-relay.service' in calls and 'enable --now cloudflared.service' in calls, calls
        if fail_health and not first:assert 'previous release restored' in result.stderr,result.stderr
        if fail_health and first:assert 'first deployment failed' in result.stderr and 'stop lobby-relay.service' in calls,result.stderr
        if marker:assert 'reconcile cost guard' in result.stderr and calls=='',result.stderr
        if bad_env:assert 'server.env requires' in result.stderr and 'restart' not in calls,result.stderr

for options in [{},{'first':True},{'fail_health':True},{'fail_health':True,'first':True},{'marker':True},{'bad_env':True}]:scenario(**options)
print('PASS deploy: promotion, first install, failed-health rollback, first-install stop, cost-stop refusal, invalid settings refusal (service stubs).')
