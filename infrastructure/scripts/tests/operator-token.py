#!/usr/bin/env python3
"""No system-user changes: python3 infrastructure/scripts/tests/operator-token.py."""
import base64
import importlib.util
import os
from pathlib import Path
import stat
import sys
import tempfile
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("operator_token", Path(__file__).parents[1] / "install-operator-token.py")
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

owner=(os.getuid(),os.getgid())
with tempfile.TemporaryDirectory() as temporary:
    root=Path(temporary)
    target=root/'runtime'; target.mkdir(mode=0o711)
    assert installer.install_operator_token(target,owner) is True
    original=(target/'operator-token').read_bytes()
    decoded=base64.urlsafe_b64decode(original+b'=')
    # Must satisfy operatorapi.ParseOperatorToken: 43 raw-base64url chars, 32 nonzero bytes.
    assert len(original)==43 and len(decoded)==32 and any(decoded)
    assert base64.urlsafe_b64encode(decoded).rstrip(b'=')==original
    assert sorted(p.name for p in target.iterdir())==['operator-token']
    assert stat.S_IMODE((target/'operator-token').stat().st_mode)==0o600
    assert installer.install_operator_token(target,owner) is False
    assert (target/'operator-token').read_bytes()==original,'operator identity must survive reruns'
    (target/'operator-token').chmod(0o644)
    try: installer.install_operator_token(target,owner)
    except ValueError: pass
    else: raise AssertionError('world-readable operator token accepted')
    (target/'operator-token').chmod(0o600)
    (target/'operator-token').write_text('A'*43)
    try: installer.install_operator_token(target,owner)
    except ValueError: pass
    else: raise AssertionError('all-zero operator token silently accepted')
    (target/'operator-token').unlink()
    (target/'operator-token').symlink_to(root/'elsewhere')
    try: installer.install_operator_token(target,owner)
    except (ValueError,OSError): pass
    else: raise AssertionError('operator token symlink accepted')
    loose=root/'loose'; loose.mkdir(mode=0o777); loose.chmod(0o777)
    try: installer.install_operator_token(loose,owner)
    except ValueError: pass
    else: raise AssertionError('group/world-writable runtime directory accepted')
print('operator token: all assertions passed')
