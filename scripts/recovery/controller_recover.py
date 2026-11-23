#!/usr/bin/env python3
"""Controller-side, bounded Paper recovery. Never provisions or destroys hosts.

Run from a systemd timer on the controller with a pinned SSH host key.
No game logs or player data cross SSH: the VM probe emits reason codes only.
"""
import argparse
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

HOST = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9_.@:-]*$")
INVOCATION = re.compile(r"^[0-9a-f]{32}$")
PROBE = "sudo -n /usr/local/libexec/gsc/minecraft_probe.py"
RESTART = "sudo -n /usr/bin/systemctl restart minecraft-vanilla.service"
MAX_PER_DAY = 3
MAX_PER_HOUR = 2
COOLDOWN_SECONDS = 20 * 60
SAMPLE_WINDOW_SECONDS = 5 * 60
READY_WAIT_SECONDS = 8 * 60


def ssh(host, remote_command, timeout=20):
    result = subprocess.run(
        ["ssh", "-T", "-o", "BatchMode=yes",
         "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
         host, remote_command],
        capture_output=True, text=True, timeout=timeout, check=False)
    if result.returncode:
        raise RuntimeError("SSH observation or action failed")
    return result.stdout.strip()


def probe(host):
    try:
        data = json.loads(ssh(host, PROBE))
    except (ValueError, RuntimeError, subprocess.TimeoutExpired):
        return {"status": "unknown", "reason": "probe_unavailable", "invocation_id": ""}
    if not isinstance(data, dict):
        return {"status": "unknown", "reason": "invalid_probe", "invocation_id": ""}
    status = data.get("status", "unknown")
    reason = data.get("reason", "")
    invocation = data.get("invocation_id", "")
    if status not in ("healthy", "unhealthy", "unknown", "starting", "stopped"):
        status = "unknown"
    if not isinstance(reason, str) or not re.fullmatch(r"[a-z_]{1,64}", reason):
        reason = "invalid_probe"
        status = "unknown"
    if not isinstance(invocation, str) or not INVOCATION.fullmatch(invocation):
        invocation = ""
    return {"status": status, "reason": reason, "invocation_id": invocation}


@contextlib.contextmanager
def locked_state(directory, host):
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(directory, 0o700)
    name = hashlib.sha256(host.encode("utf-8")).hexdigest()
    lock_file = directory / (name + ".lock")
    with lock_file.open("a+", encoding="ascii") as lock:
        os.chmod(lock_file, 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        path = directory / (name + ".json")
        try:
            state = json.loads(path.read_text(encoding="utf-8"))
            if not isinstance(state, dict):
                raise ValueError("invalid state")
        except FileNotFoundError:
            state = {}
        yield state, path


def persist(state, path):
    temp = path.with_name(path.name + ".tmp." + str(os.getpid()))
    try:
        with temp.open("x", encoding="utf-8") as destination:
            os.chmod(temp, 0o600)
            json.dump(state, destination, separators=(",", ":"), sort_keys=True)
            destination.write("\n")
            destination.flush()
            os.fsync(destination.fileno())
        temp.replace(path)
    finally:
        temp.unlink(missing_ok=True)


def audit(directory, host, event, reason):
    # Fixed event fields: never include player log lines or credentials.
    path = directory / "recovery-audit.jsonl"
    record = {"at": int(time.time()), "host_hash": hashlib.sha256(
        host.encode("utf-8")).hexdigest(), "event": event, "reason": reason}
    with path.open("a", encoding="utf-8") as output:
        os.chmod(path, 0o600)
        output.write(json.dumps(record, separators=(",", ":")) + "\n")
        output.flush()
        os.fsync(output.fileno())


def allowed_attempt(state, now):
    times = [v for v in state.get("attempts", [])
             if isinstance(v, (int, float)) and 0 <= now - v < 86400]
    if sum(now - v < 3600 for v in times) >= MAX_PER_HOUR:
        return False, times
    if len(times) >= MAX_PER_DAY:
        return False, times
    if times and now - max(times) < COOLDOWN_SECONDS:
        return False, times
    return True, times


def observe(state, reading, now):
    """Require two same-invocation unhealthy samples in a five-minute window."""
    invocation = reading["invocation_id"]
    if reading["status"] != "unhealthy" or not invocation:
        state["samples"] = 0
        state["invocation"] = invocation
        state["last_sample"] = now
        return False
    recent = 0 <= now - state.get("last_sample", 0) <= SAMPLE_WINDOW_SECONDS
    if recent and state.get("invocation") == invocation:
        state["samples"] = min(2, state.get("samples", 0) + 1)
    else:
        state["samples"] = 1
    state["invocation"] = invocation
    state["last_sample"] = now
    return state["samples"] >= 2


def wait_for_new_ready(host, previous, wait=READY_WAIT_SECONDS):
    deadline = time.monotonic() + wait
    while time.monotonic() < deadline:
        time.sleep(10)
        reading = probe(host)
        if reading["status"] == "healthy" and reading["invocation_id"] != previous:
            return True
        if reading["status"] == "stopped":
            return False
    return False


def run(args):
    if not HOST.fullmatch(args.host) or args.host.startswith("-"):
        raise ValueError("--host must be a simple pinned SSH alias or user@host")
    if not args.execute:
        print("Dry run: no SSH calls, state changes, restarts, VM creation or destruction.")
        return 0
    if args.confirm_host != args.host:
        raise ValueError("--confirm-host must exactly match --host")
    directory = Path(args.state_dir)
    try:
        with locked_state(directory, args.host) as (state, path):
            return run_locked(args.host, state, path, directory)
    except BlockingIOError:
        print("Recovery already running for this host; no action taken.")
        return 1


def run_locked(host, state, path, directory):
    reading = probe(host)
    now = int(time.time())
    eligible = observe(state, reading, now)
    if not eligible:
        persist(state, path)
        print("No restart: " + reading["status"] + " (" + reading["reason"] + ").")
        return 0
    allowed, history = allowed_attempt(state, now)
    if not allowed:
        persist(state, path)
        audit(directory, host, "restart_suppressed", "retry_budget_exhausted")
        print("Restart suppressed by cooldown or retry budget; operator review required.")
        return 2

    # Persist intent BEFORE an external side effect. An uncertain SSH outcome
    # consumes the attempt; never issue an automatic retry for it.
    state["attempts"] = history + [now]
    state["samples"] = 0
    persist(state, path)
    audit(directory, host, "restart_requested", reading["reason"])
    try:
        ssh(host, RESTART, timeout=140)
    except (RuntimeError, subprocess.TimeoutExpired):
        audit(directory, host, "restart_outcome_unknown", "ssh_failed")
        print("Restart outcome unknown; no retry or VM replacement was attempted.")
        return 2
    if wait_for_new_ready(host, reading["invocation_id"]):
        audit(directory, host, "restart_verified", "paper_ready")
        print("Minecraft restarted and a new invocation reached Paper readiness.")
        return 0
    audit(directory, host, "restart_unverified", "readiness_unconfirmed")
    print("Restart not verified; manual VM diagnostics required, never auto-provision.")
    return 2


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", required=True, help="SSH host with pinned known_hosts key")
    parser.add_argument("--execute", action="store_true", help="enable controller-side recovery")
    parser.add_argument("--confirm-host", default="", help="repeat the exact host")
    parser.add_argument("--state-dir", default=str(
        Path.home() / ".local/state/gsc-paper-recovery"))
    args = parser.parse_args()
    try:
        return run(args)
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        print("Recovery unavailable; no remote mutation attempted: " + str(exc), file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
