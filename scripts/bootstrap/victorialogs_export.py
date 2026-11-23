#!/usr/bin/env python3
"""Opt-in, bounded journald exporter for a disposable Minecraft VM.

Uses Python's standard library and a credential supplied after bootstrap.
Ordinary operation exports only classified lifecycle events, not raw player logs.
"""
import datetime
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

UNITS = ("minecraft-vanilla.service", "minecraft-restore.service")
MAX_EVENTS = 250
MAX_JOURNAL_LINE = 262144
ID_PATTERN = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$")


def ingest_endpoint(value):
    parsed = urllib.parse.urlsplit(value)
    if (parsed.scheme != "https" or not parsed.hostname or
            parsed.username or parsed.password or parsed.query or parsed.fragment or
            not parsed.path.endswith("/insert/jsonline")):
        raise ValueError("an HTTPS VictoriaLogs /insert/jsonline endpoint is required")
    return value + "?_msg_field=_msg&_time_field=_time&_stream_fields=source_unit,environment_id,instance_id"


def lifecycle_message(unit, message):
    if unit == "gsc-bootstrap":
        if message == "gsc bootstrap complete":
            return "bootstrap_complete"
        if message == "gsc bootstrap failed":
            return "bootstrap_failed"
        return None
    if unit == "minecraft-vanilla.service":
        if message.startswith("Done ("):
            return "paper_ready"
        if message.startswith("Starting minecraft server version"):
            return "paper_starting"
        if message.startswith("Stopping server"):
            return "paper_stopping"
        if "ERROR" in message or "Exception" in message:
            return "paper_error"
        if "WARN" in message:
            return "paper_warning"
    if unit == "minecraft-restore.service":
        if "ERROR" in message or "failed" in message.lower():
            return "restore_failed"
        if "restore complete" in message.lower():
            return "restore_complete"
    return None


def convert_event(record, environment, instance):
    unit = record.get("_SYSTEMD_UNIT", "")
    if record.get("SYSLOG_IDENTIFIER") == "gsc-bootstrap":
        unit = "gsc-bootstrap"
    if unit not in UNITS and unit != "gsc-bootstrap":
        return None
    message = record.get("MESSAGE", "")
    timestamp = record.get("__REALTIME_TIMESTAMP")
    if not isinstance(message, str) or not isinstance(timestamp, str) or not timestamp.isdigit():
        return None
    code = lifecycle_message(unit, message)
    if code is None or len(timestamp) > 20:
        return None
    try:
        time_value = datetime.datetime.fromtimestamp(
            int(timestamp) / 1000000, datetime.timezone.utc
        ).isoformat()
    except (ValueError, OverflowError):
        return None
    event = {
        "_time": time_value,
        "_msg": code,
        "event_type": code,
        "source_unit": unit,
        "environment_id": environment,
        "instance_id": instance,
    }
    return event


def collect(cursor, environment, instance):
    command = ["journalctl", "--no-pager", "--output=json",
               "--unit=minecraft-vanilla.service",
               "--unit=minecraft-restore.service", "--identifier=gsc-bootstrap"]
    command += ["--after-cursor=" + cursor] if cursor else ["--since=1 hour ago"]
    events = []
    last_cursor = cursor
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    try:
        for line in process.stdout:
            if len(line) > MAX_JOURNAL_LINE:
                continue
            try:
                record = json.loads(line)
            except (ValueError, UnicodeError):
                continue
            if not isinstance(record, dict):
                continue
            new_cursor = record.get("__CURSOR")
            if not isinstance(new_cursor, str) or len(new_cursor) > 4096:
                continue
            event = convert_event(record, environment, instance)
            last_cursor = new_cursor
            if event is not None:
                events.append(event)
            if len(events) >= MAX_EVENTS:
                break
    finally:
        if process.poll() is None:
            process.terminate()
        process.wait(timeout=5)
    return events, last_cursor


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        return None


def upload(endpoint, token, events):
    if not events:
        return
    body = ("\n".join(json.dumps(event, separators=(",", ":")) for event in events) + "\n").encode()
    request = urllib.request.Request(
        endpoint, body, method="POST",
        headers={"Content-Type": "application/stream+json",
                 "Authorization": "Bearer " + token}
    )
    with urllib.request.build_opener(NoRedirect).open(request, timeout=10) as response:
        if response.status != 200 and response.status != 204:
            raise ValueError("unexpected log ingestion response")


def save_cursor(path, cursor):
    if not cursor:
        return
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    descriptor, temp_path = tempfile.mkstemp(prefix=".cursor-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            output.write(cursor)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temp_path, path)
    finally:
        if os.path.exists(temp_path):
            os.unlink(temp_path)


def main():
    environment = os.environ.get("GSC_VL_ENVIRONMENT_ID", "")
    instance = os.environ.get("GSC_VL_INSTANCE_ID", "")
    endpoint = ingest_endpoint(os.environ.get("GSC_VL_ENDPOINT", ""))
    if not ID_PATTERN.fullmatch(environment) or not ID_PATTERN.fullmatch(instance):
        raise ValueError("environment and instance identifiers are required")
    credentials_dir = Path(os.environ.get("CREDENTIALS_DIRECTORY", ""))
    if not credentials_dir.is_absolute():
        raise ValueError("systemd credential directory is required")
    token = (credentials_dir / "token").read_text(encoding="utf-8").strip()
    if not token or "\n" in token or "\r" in token:
        raise ValueError("valid log ingestion token required")
    state_dir = Path(os.environ.get("STATE_DIRECTORY", "/var/lib/gsc-victorialogs"))
    cursor_file = state_dir / "cursor"
    cursor = cursor_file.read_text(encoding="utf-8").strip() if cursor_file.exists() else ""
    events, next_cursor = collect(cursor, environment, instance)
    upload(endpoint, token, events)
    save_cursor(cursor_file, next_cursor)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, urllib.error.URLError, subprocess.SubprocessError):
        # Do not expose tokens, endpoints, player messages or raw request errors.
        print("victorialogs export unavailable; journal cursor retained for retry", file=sys.stderr)
        sys.exit(1)
