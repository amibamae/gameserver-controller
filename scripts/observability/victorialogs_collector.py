#!/usr/bin/env python3
"""Optional one-shot controller journal collector with durable at-least-once delivery.

Run only with an explicitly enabled systemd timer. Credentials come from a
systemd LoadCredential file; never put secrets in flags, URLs or environment.
The journal cursor moves only after VictoriaLogs acknowledges the batch.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

import victorialogs_bridge as bridge

MAX_RECORDS = 250
MAX_LINE = 262144
UNIT = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}\\.service$")
CURSOR = re.compile(r"^[A-Za-z0-9_.;:=-]{1,4096}$")


def journal_event(record):
    if not isinstance(record, dict):
        return None
    message = record.get("MESSAGE")
    if not isinstance(message, str):
        return None
    return bridge.sanitized_event(message)


def decode_records(lines):
    """Return sanitized events and the last valid cursor from bounded lines."""
    events, cursor = [], None
    for line in lines:
        if len(line) > MAX_LINE:
            continue
        try:
            record = json.loads(line)
        except (UnicodeError, ValueError, TypeError):
            continue
        if not isinstance(record, dict):
            continue
        value = record.get("__CURSOR")
        if not isinstance(value, str) or not CURSOR.fullmatch(value):
            continue
        cursor = value
        event = journal_event(record)
        if event is not None:
            events.append(event)
    return events, cursor


def read_journal(unit, cursor):
    if not UNIT.fullmatch(unit):
        raise ValueError("invalid controller service name")
    command = ["journalctl", "--no-pager", "--output=json", "--unit=" + unit]
    command += ["--after-cursor=" + cursor] if cursor else ["--since=1 hour ago"]
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    records = []
    stopped = False
    try:
        for _ in range(MAX_RECORDS):
            line = process.stdout.readline(MAX_LINE + 1)
            if not line:
                break
            if not line.endswith(b"\\n"):
                # This message is too large. Drain the remainder without
                # retaining it or leaking it to the destination.
                while line and not line.endswith(b"\\n"):
                    line = process.stdout.readline(MAX_LINE + 1)
                continue
            records.append(line)
    finally:
        if process.poll() is None:
            process.terminate()
            stopped = True
        try:
            code = process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
            raise ValueError("journal reader timed out")
    if code != 0 and not (stopped and code == -15):
        raise ValueError("journal reader failed; cursor retained")
    return decode_records(records)


def read_cursor(path):
    if not path.exists():
        return ""
    cursor = path.read_text(encoding="utf-8").strip()
    if not CURSOR.fullmatch(cursor):
        raise ValueError("invalid saved journal cursor; manual recovery required")
    return cursor


def save_cursor(path, cursor):
    if not cursor or not CURSOR.fullmatch(cursor):
        return
    path.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    fd, temp_path = tempfile.mkstemp(prefix=".cursor-", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            output.write(cursor + "\\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temp_path, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temp_path):
            os.unlink(temp_path)


def collect_once(endpoint, token, path, unit, reader=read_journal, sender=bridge.deliver):
    """At-least-once: network failure never advances the durable cursor."""
    cursor = read_cursor(path)
    events, next_cursor = reader(unit, cursor)
    sender(events, endpoint, token)
    save_cursor(path, next_cursor)
    return len(events)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--endpoint", required=True, help="HTTPS /insert/jsonline gateway")
    parser.add_argument("--unit", default="gameserver-controller.service")
    options = parser.parse_args()
    try:
        if not UNIT.fullmatch(options.unit):
            raise ValueError("invalid unit")
        endpoint = bridge.endpoint_url(options.endpoint)
        credentials = Path(os.environ.get("CREDENTIALS_DIRECTORY", ""))
        state = Path(os.environ.get("STATE_DIRECTORY", ""))
        if not credentials.is_absolute() or not state.is_absolute():
            raise ValueError("systemd credential and state directories required")
        token = (credentials / "token").read_text(encoding="utf-8").strip()
        if not token or any(ch.isspace() for ch in token):
            raise ValueError("invalid private ingestion token")
        os.umask(0o077)
        collect_once(endpoint, token, state / "cursor", options.unit)
    except (OSError, ValueError, subprocess.SubprocessError):
        print("controller VictoriaLogs forwarding unavailable; cursor retained for retry", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
