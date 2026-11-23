#!/usr/bin/env python3
"""Local-only, read-only Paper health probe. Never exports game log contents."""
import json
import re
import subprocess
import sys
import time

UNIT = "minecraft-vanilla.service"
INVOCATION = re.compile(r"^[0-9a-f]{32}$")
DONE = re.compile(r"\bDone \([^\n)]{1,50}\)!")
FATAL = tuple(re.compile(p, re.IGNORECASE) for p in (
    r"Encountered an unexpected exception",
    r"Exception in server tick loop",
    r"Failed to start the minecraft server",
    r"Crash report saved to",
    r"java\.lang\.OutOfMemoryError",
    r"A single server tick took .*considered to be crashed",
))
STARTUP_GRACE_SECONDS = 600
MAX_JOURNAL_LINES = 250


def command(*args):
    result = subprocess.run(args, capture_output=True, text=True,
                            timeout=8, check=False)
    if result.returncode != 0:
        raise RuntimeError("local health observation failed")
    return result.stdout


def status_fields(output):
    return dict(line.split("=", 1) for line in output.splitlines() if "=" in line)


def classify(fields, messages, uptime_seconds):
    """Return an unambiguous status and safe reason code, never raw logs."""
    active = fields.get("ActiveState")
    invocation = fields.get("InvocationID", "")
    if active == "inactive":
        return "stopped", "service_inactive"
    if not INVOCATION.fullmatch(invocation):
        return "unknown", "invocation_unavailable"
    if active == "failed":
        return "unhealthy", "service_failed"
    if active != "active":
        return "unknown", "service_transitioning"
    if messages is None:
        return "unknown", "journal_unavailable"

    ready_at = -1
    fatal_at = -1
    for index, line in enumerate(messages):
        if DONE.search(line):
            ready_at = index
        if any(pattern.search(line) for pattern in FATAL):
            fatal_at = index
    if fatal_at > ready_at:
        return "unhealthy", "fatal_current_invocation"
    if ready_at >= 0:
        return "healthy", "paper_ready"
    since = fields.get("ActiveEnterTimestampMonotonic", "0")
    try:
        started = int(since) / 1_000_000
    except ValueError:
        return "unknown", "start_time_unavailable"
    if started <= 0 or uptime_seconds < started:
        return "unknown", "start_time_unavailable"
    if uptime_seconds - started < STARTUP_GRACE_SECONDS:
        return "starting", "awaiting_paper_ready"
    return "unknown", "readiness_unconfirmed"


def inspect():
    fields = status_fields(command(
        "systemctl", "show",
        "--property=ActiveState,SubState,Result,InvocationID,ActiveEnterTimestampMonotonic",
        UNIT))
    invocation = fields.get("InvocationID", "")
    messages = None
    if INVOCATION.fullmatch(invocation):
        try:
            text = command("journalctl", "--no-pager", "-q", "-n",
                           str(MAX_JOURNAL_LINES), "-o", "cat",
                           "_SYSTEMD_INVOCATION_ID=" + invocation)
            messages = text.splitlines()
        except (RuntimeError, subprocess.TimeoutExpired):
            pass
    with open("/proc/uptime", encoding="ascii") as source:
        uptime = float(source.read().split()[0])
    status, reason = classify(fields, messages, uptime)
    return {"status": status, "reason": reason,
            "invocation_id": invocation if INVOCATION.fullmatch(invocation) else ""}


def main():
    try:
        result = inspect()
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired):
        result = {"status": "unknown", "reason": "probe_unavailable",
                  "invocation_id": ""}
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
