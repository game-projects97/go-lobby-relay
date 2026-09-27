#!/usr/bin/env python3
"""Conservative single-VM transfer guard. No third-party dependencies."""
import argparse
import datetime
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

# Public UDP reaches the relay directly, so stopping only the Tunnel would not
# stop the dominant traffic. The relay holds no durable state to drain.
GUARDED_UNITS = ("lobby-relay.service", "cloudflared.service")


class Uncertain(ValueError):
    pass


def validate_limits(limits):
    if not all(type(n) is int for n in limits) or not (0 < limits[0] < limits[1] < 3_000_000_000_000):
        raise ValueError("Require 0 < WARN < STOP < 3000000000000 bytes")


def load(path):
    return json.loads(path.read_text())


def durable_write(path, content, mode=0o600):
    name = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as file:
            name = file.name
            os.fchmod(file.fileno(), mode)
            file.write(content)
            file.flush()
            os.fsync(file.fileno())
        os.replace(name, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if name and os.path.exists(name):
            os.unlink(name)


def stop_public_services():
    # Stop running units; the durable marker and runtime mask also prevent restart.
    subprocess.run(["systemctl", "stop", *GUARDED_UNITS], check=True, timeout=30)
    subprocess.run(["systemctl", "mask", "--runtime", *GUARDED_UNITS], check=True, timeout=20)


class Guard:
    def __init__(self, root, interface, limits, stop=stop_public_services):
        validate_limits(limits)
        self.root, self.interface, self.limits = root, interface, limits
        self.stop = stop

    def latch(self, reason):
        # The marker survives both process and host restarts; never clear automatically.
        try:
            durable_write(self.root / "traffic-stopped", reason + "\n", 0o644)
        finally:
            self.stop()

    def sample(self, month, boot, rx, tx, initialize=False):
        path = self.root / "traffic.json"
        try:
            if not re.fullmatch(r"\d{4}-(0[1-9]|1[0-2])", month) or not boot or any(type(n) is not int or n < 0 for n in (rx, tx)):
                raise Uncertain("invalid counter sample")
            if initialize:
                if path.exists() or (self.root / "traffic-stopped").exists():
                    raise Uncertain("initialization cannot reset existing state or markers")
                total_rx, total_tx = rx, tx  # Include all traffic since boot, not zero.
            else:
                old = load(path)
                numeric = ("rx", "tx", "rx_bytes", "tx_bytes", "total_bytes")
                if old.get("version") != 1 or any(type(old.get(k)) is not int or old[k] < 0 for k in numeric):
                    raise Uncertain("invalid persisted counters")
                if old["total_bytes"] != old["rx_bytes"] + old["tx_bytes"] or not re.fullmatch(r"\d{4}-(0[1-9]|1[0-2])", old["month"]):
                    raise Uncertain("inconsistent persisted state")
                if old["interface"] != self.interface or old["boot"] != boot or rx < old["rx"] or tx < old["tx"]:
                    # ponytail: unsampled bytes before reset/reboot cannot be recovered
                    # locally; stop until an operator reconciles provider accounting.
                    raise Uncertain("interface, boot, or counters changed; reconcile provider totals")
                if month < old["month"]:
                    raise Uncertain("clock moved into an earlier month")
                total_rx, total_tx = rx - old["rx"], tx - old["tx"]
                if month == old["month"]:
                    total_rx += old["rx_bytes"]
                    total_tx += old["tx_bytes"]
                # Boundary delta is charged to the new month conservatively.
            record = dict(version=1, month=month, boot=boot, interface=self.interface, rx=rx, tx=tx, rx_bytes=total_rx, tx_bytes=total_tx, total_bytes=total_rx + total_tx)
            durable_write(path, json.dumps(record) + "\n")
        except (OSError, ValueError, KeyError, TypeError) as error:
            self.latch("uncertain traffic accounting")
            raise Uncertain("traffic state unavailable or uncertain; operator reconciliation required") from error

        total = record["total_bytes"]
        if total >= self.limits[0]:
            print(f"WARNING traffic total_bytes={total} rx_bytes={total_rx} tx_bytes={total_tx}", flush=True)
        if total >= self.limits[1] or (self.root / "traffic-stopped").exists():
            self.latch("traffic emergency latch")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--initialize", action="store_true", help="First boot only: refuse any existing state or safety marker")
    mode.add_argument("--check-start", action="store_true", help="Unit preflight: fail closed without systemctl calls to the starting unit")
    args = parser.parse_args()
    # The unit is not running yet; stopping its pending start job here would
    # deadlock ExecStartPre. The marker and a failing exit status prevent that start.
    stop = (lambda: None) if args.check_start else stop_public_services
    root = Path("/var/lib/lobby-relay")
    try:
        info = root.stat()
        if os.geteuid() != 0 or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError("state directory must be root-owned and not group/world writable")
        with (root / "traffic.lock").open("a") as lock:
            os.fchmod(lock.fileno(), 0o600)
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            interface = os.environ["TRAFFIC_INTERFACE"]
            if interface == "lo" or not re.fullmatch(r"[A-Za-z0-9_.-]{1,15}", interface):
                raise ValueError("explicit external interface required")
            limits = tuple(int(os.environ[f"TRAFFIC_{level}_BYTES"]) for level in ("WARN", "STOP"))
            guard = Guard(root, interface, limits, stop=stop)
            counters = Path("/sys/class/net") / interface / "statistics"
            rx = int((counters / "rx_bytes").read_text())
            tx = int((counters / "tx_bytes").read_text())
            boot = Path("/proc/sys/kernel/random/boot_id").read_text().strip()
            month = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m")
            guard.sample(month, boot, rx, tx, initialize=args.initialize)
            if args.check_start and (root / "traffic-stopped").exists():
                return 1
    except BlockingIOError:
        print("traffic guard: another sample owns the lock", flush=True)
        return 1 if args.check_start else 0
    except Exception as error:
        # Even malformed configuration or failed counter reads must fail closed.
        try:
            durable_write(root / "traffic-stopped", "traffic guard failure\n", 0o644)
        finally:
            stop()
        print(f"traffic guard stopped public services: {type(error).__name__}; inspect state/configuration", flush=True)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
