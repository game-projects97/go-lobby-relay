#!/usr/bin/env python3
"""Initial/admin secret delivery. Run as root with IDs and a private API-token FILE."""
import argparse
import base64
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import stat
import sys
import tempfile
import urllib.request


def private_file(path, owner=None):
    owner = os.geteuid() if owner is None else owner
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or stat.S_IMODE(before.st_mode) != 0o600 or before.st_uid != owner:
        raise ValueError("credential requires an owner-controlled regular 0600 file")
    descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    with os.fdopen(descriptor, "rb") as source:
        opened = os.fstat(source.fileno())
        if not os.path.samestat(before, opened):
            raise ValueError("credential changed during read")
        body = source.read(8193)
    if len(body) > 8192 or not os.path.samestat(before, path.lstat()):
        raise ValueError("credential size or replacement rejected")
    return body


def credential(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9._~+/=-]{1,8192}", value):
        raise ValueError("invalid credential response")
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def fetch_value(path, token):
    request = urllib.request.Request("https://api.cloudflare.com/client/v4" + path, headers={"Authorization": "Bearer " + token})
    # Avoid forwarding Authorization through redirects or ambient proxy settings.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with opener.open(request, timeout=15) as response:
        body = response.read(65537)
        if response.status != 200 or len(body) > 65536:
            raise ValueError("Cloudflare response rejected")
    result = json.loads(body)
    if result.get("success") is not True:
        raise ValueError("Cloudflare request failed")
    return result["result"]


def write_private(path, value, owner, create_only=False):
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, mode="w", delete=False) as file:
            temporary = file.name
            os.fchmod(file.fileno(), 0o600)
            os.fchown(file.fileno(), *owner)
            file.write(value)
            file.flush()
            os.fsync(file.fileno())
        if create_only:
            os.link(temporary, path)  # Never overwrite an existing operator identity.
            os.unlink(temporary)
        else:
            os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if temporary and os.path.exists(temporary):
            os.unlink(temporary)


def install_secrets(account, tunnel, api_file, directory=Path("/etc/lobby-relay"), owners=None, fetch=fetch_value):
    if not re.fullmatch(r"[0-9a-f]{32}", account) or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", tunnel):
        raise ValueError("invalid Cloudflare resource identifier")
    info = directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
        raise ValueError("runtime directory must be owner-controlled without writable group/other permissions")
    if owners is None:
        owners = {name: (pwd.getpwnam(name).pw_uid, pwd.getpwnam(name).pw_gid) for name in ("lobby-relay", "cloudflared")}
    token = credential(private_file(api_file).decode("ascii").strip())
    operator = directory / "operator-token"
    if operator.exists() or operator.is_symlink():
        existing = private_file(operator, owners["lobby-relay"][0]).removesuffix(b"\n").removesuffix(b"\r")
        if not re.fullmatch(rb"[A-Za-z0-9_-]{43}", existing):
            raise ValueError("existing operator token invalid; do not silently replace it")
        decoded = base64.urlsafe_b64decode(existing + b"=")
        if not any(decoded) or base64.urlsafe_b64encode(decoded).rstrip(b"=") != existing:
            raise ValueError("existing operator token is not canonical")
    # Obtain and validate before replacing any existing runtime credential.
    tunnel_value = credential(fetch(f"/accounts/{account}/cfd_tunnel/{tunnel}/token", token))
    write_private(directory / "tunnel-token", tunnel_value, owners["cloudflared"])
    if not operator.exists():
        # Same format as operatorapi.ParseOperatorToken: 32 nonzero bytes, raw base64url.
        value = bytes(32)
        while not any(value):
            value = secrets.token_bytes(32)
        write_private(operator, base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii"), owners["lobby-relay"], create_only=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("account_id")
    parser.add_argument("tunnel_id")
    parser.add_argument("api_token_file", type=Path)
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("run as root; the API credential is read from a root-owned 0600 file")
    os.umask(0o077)
    try:
        install_secrets(args.account_id, args.tunnel_id, args.api_token_file)
    except Exception:
        # HTTP errors may contain response data. Never print raw exception details.
        print("Runtime secret installation failed; check private file, IDs and scoped API permissions.", file=sys.stderr)
        return 1
    print("Runtime credentials installed; operator identity preserved. No services were restarted.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
