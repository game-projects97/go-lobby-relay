#!/usr/bin/env python3
"""No cloud requests or system-user changes: python3 infrastructure/scripts/tests/runtime-secrets.py."""
import base64
import importlib.util
import os
from pathlib import Path
import stat
import sys
import tempfile
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("secrets_installer", Path(__file__).parents[1] / "install-runtime-secrets.py")
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

account="1"*32
tunnel="11111111-1111-1111-1111-111111111111"
with tempfile.TemporaryDirectory() as temporary:
    root=Path(temporary)
    api=root/'api-token'; api.write_text('api-value'); api.chmod(0o600)
    target=root/'runtime'; target.mkdir(mode=0o711)
    owners={name:(os.getuid(),os.getgid()) for name in ('lobby-relay','cloudflared')}
    calls=[]
    def fetch(path, token):
        assert token=='api-value'
        calls.append(path)
        return 'eyJhIjoiIn0='
    installer.install_secrets(account,tunnel,api,target,owners,fetch)
    assert calls==[f'/accounts/{account}/cfd_tunnel/{tunnel}/token']
    original=(target/'operator-token').read_bytes()
    decoded=base64.urlsafe_b64decode(original+b'=')
    # Must satisfy operatorapi.ParseOperatorToken: 43 raw-base64url chars, 32 nonzero bytes.
    assert len(original)==43 and len(decoded)==32 and any(decoded)
    assert base64.urlsafe_b64encode(decoded).rstrip(b'=')==original
    assert (target/'tunnel-token').read_text()=='eyJhIjoiIn0='
    assert sorted(p.name for p in target.iterdir())==['operator-token','tunnel-token']
    for path in target.iterdir(): assert stat.S_IMODE(path.stat().st_mode)==0o600
    installer.install_secrets(account,tunnel,api,target,owners,fetch)
    assert (target/'operator-token').read_bytes()==original,'operator identity must survive reruns'
    for mode in (0o644,0o400):
        api.chmod(mode)
        try: installer.install_secrets(account,tunnel,api,target,owners,fetch)
        except ValueError: pass
        else: raise AssertionError('unsafe API credential mode accepted')
    api.chmod(0o600)
    link=root/'link';link.symlink_to(api)
    try: installer.private_file(link)
    except ValueError: pass
    else: raise AssertionError('credential symlink accepted')
    previous={p.name:p.read_bytes() for p in target.iterdir()}
    def failure(path, token):
        raise ValueError('API unavailable')
    try: installer.install_secrets(account,tunnel,api,target,owners,failure)
    except ValueError: pass
    else: raise AssertionError('API failure hidden')
    assert previous=={p.name:p.read_bytes() for p in target.iterdir()},'failed fetch must not rotate secrets'
    (target/'operator-token').write_text('A'*43)
    try: installer.install_secrets(account,tunnel,api,target,owners,fetch)
    except ValueError: pass
    else: raise AssertionError('non-canonical operator token silently accepted')
    for bad_account,bad_tunnel in [('bad',tunnel),(account,'../tunnel')]:
        try: installer.install_secrets(bad_account,bad_tunnel,api,target,owners,fetch)
        except ValueError: pass
        else: raise AssertionError('unsafe resource ID accepted')
print('runtime secrets: all assertions passed')
