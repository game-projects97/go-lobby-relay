#!/usr/bin/env python3
"""Run: python3 infrastructure/scripts/tests/traffic-budget.py (stdlib only, no host mutations)."""
import sys
sys.dont_write_bytecode = True
import importlib.util
import tempfile
import os
import subprocess
from pathlib import Path

spec = importlib.util.spec_from_file_location("traffic", Path(__file__).parents[1] / "traffic_budget.py")
traffic = importlib.util.module_from_spec(spec)
spec.loader.exec_module(traffic)

assert traffic.GUARDED_UNITS == ("lobby-relay.service", "cloudflared.service"), "public UDP relay must be stopped with the Tunnel"

with tempfile.TemporaryDirectory() as d:
    root = Path(d)
    calls = []
    guard = traffic.Guard(root, "eth0", (100, 300), lambda: calls.append("stop"))
    guard.sample("2026-09", "boot1", 20, 30, initialize=True)
    assert traffic.load(root / "traffic.json")["total_bytes"] == 50
    guard.sample("2026-09", "boot1", 50, 50)
    assert traffic.load(root / "traffic.json")["total_bytes"] == 100 and calls == []
    guard.sample("2026-09", "boot1", 150, 150)
    assert (root / "traffic-stopped").exists() and calls == ["stop"]
    calls.clear()
    guard.sample("2026-10", "boot1", 160, 155)
    assert traffic.load(root / "traffic.json")["total_bytes"] == 15
    assert calls == ["stop"], "a new month must not clear the stop latch"

for failure in ("missing", "corrupt", "reboot", "reset", "interface"):
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        calls = []
        guard = traffic.Guard(root, "eth0", (100, 300), lambda: calls.append("stop"))
        if failure != "missing":
            guard.sample("2026-09", "boot1", 10, 10, initialize=True)
        if failure == "corrupt":
            (root / "traffic.json").write_text("broken")
        if failure == "interface":
            guard.interface = "eth1"
        try:
            guard.sample("2026-09", "boot2" if failure == "reboot" else "boot1", 1 if failure == "reset" else 20, 20)
        except traffic.Uncertain:
            pass
        else:
            raise AssertionError(f"{failure} must report uncertainty")
        assert (root / "traffic-stopped").exists() and "stop" in calls

for limits in [(0, 1), (2, 1), (1, 3_000_000_000_000)]:
    try:
        traffic.validate_limits(limits)
    except ValueError:
        pass
    else:
        raise AssertionError("unsafe thresholds accepted")
# A failed disk write must still stop public services even if no marker can persist.
with tempfile.TemporaryDirectory() as d:
    calls = []
    guard = traffic.Guard(Path(d) / "missing", "eth0", (100, 300), lambda: calls.append("stop"))
    try:
        guard.sample("2026-09", "boot", 1, 1)
    except OSError:
        pass
    assert calls == ["stop"]
# Execute startup mode in a fake VM filesystem. Only the root-UID requirement is
# omitted in this copied test script; counter, marker and CLI paths stay real.
for failure in (None, "reboot", "corrupt", "stopped", "over"):
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        state = root / "state"; state.mkdir()
        counters = root / "net/eth0/statistics"; counters.mkdir(parents=True)
        (counters / "rx_bytes").write_text("20")
        (counters / "tx_bytes").write_text("20")
        (root / "boot_id").write_text("boot2" if failure == "reboot" else "boot1")
        month = traffic.datetime.datetime.now(traffic.datetime.timezone.utc).strftime("%Y-%m")
        traffic.Guard(state, "eth0", (100, 300)).sample(month, "boot1", 10, 10, initialize=True)
        if failure == "corrupt": (state / "traffic.json").write_text("bad")
        if failure == "stopped": (state / "traffic-stopped").touch()
        if failure == "over": (counters / "rx_bytes").write_text("350")
        script = Path(spec.origin).read_text()
        for original, replacement in [("/var/lib/lobby-relay", str(state)), ("/sys/class/net", str(root / "net")), ("/proc/sys/kernel/random/boot_id", str(root / "boot_id"))]:
            script = script.replace(original, replacement)
        script = script.replace("os.geteuid() != 0 or info.st_uid != 0", "False")
        copied = root / "traffic.py"; copied.write_text(script)
        binary = root / "systemctl"
        binary.write_text('#!/bin/sh\necho called >> "'+str(root / 'systemctl-calls')+'"\nexit 1\n'); binary.chmod(0o755)
        env = {**os.environ, "PATH": str(root)+os.pathsep+os.environ["PATH"], "TRAFFIC_INTERFACE":"eth0", "TRAFFIC_WARN_BYTES":"100", "TRAFFIC_STOP_BYTES":"300"}
        result = subprocess.run([sys.executable, str(copied), "--check-start"], env=env, capture_output=True, text=True, timeout=10)
        assert result.returncode == (0 if failure is None else 1), (failure, result.stdout, result.stderr)
        assert not (root / "systemctl-calls").exists(), "startup preflight must never stop its own systemd job"
        if failure: assert (state / "traffic-stopped").exists()
print("traffic budget: accounting, persistent stop and cold-start gate assertions passed")
