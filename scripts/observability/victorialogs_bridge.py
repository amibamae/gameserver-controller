#!/usr/bin/env python3
"""Forward only allowlisted, privacy-safe controller events to VictoriaLogs.

Pipe the controller journald JSON lines to this tool. Unknown messages, raw
player logs, chat and raw error details are deliberately not forwarded.
"""
import argparse
from datetime import datetime
import json
from pathlib import Path
import sys
from urllib.parse import urlencode, urlsplit, urlunsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

EVENTS = frozenset({
    "integration.proxy.unreachable",
    "integration.proxy.identity_mismatch",
    "integration.proxy.recovered",
})
SEVERITIES = frozenset({"critical", "warning", "info"})
MAX_LINE = 65536


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def endpoint_url(raw):
    parsed = urlsplit(raw)
    host = (parsed.hostname or "").lower()
    if (not host or parsed.username or parsed.password or parsed.query or
            parsed.fragment or parsed.path != "/insert/jsonline"):
        raise ValueError("invalid ingestion endpoint")
    if parsed.scheme != "https" and not (
            parsed.scheme == "http" and host in ("127.0.0.1", "localhost", "::1")):
        raise ValueError("ingestion requires HTTPS or loopback")
    params = urlencode({
        "_time_field": "_time",
        "_msg_field": "_msg",
        "_stream_fields": "component,environment_id",
    })
    return urlunsplit((parsed.scheme, parsed.netloc, parsed.path, params, ""))


def allowed_event(original):
    event_type = original.get("event_type")
    severity = original.get("severity")
    return (isinstance(event_type, str) and isinstance(severity, str) and
            event_type in EVENTS and severity in SEVERITIES)


def sanitized_event(line):
    if len(line) > MAX_LINE:
        return None
    try:
        original = json.loads(line)
    except (ValueError, TypeError):
        return None
    if not isinstance(original, dict) or original.get("message") != "integration event":
        return None
    if not allowed_event(original):
        return None
    fields = ("event_id", "event_type", "severity",
              "environment_id", "adapter_id", "occurred_at")
    if any(not isinstance(original.get(key), str) or
           not 0 < len(original[key]) <= 255 for key in fields):
        return None
    try:
        stamp = datetime.fromisoformat(original["occurred_at"].replace("Z", "+00:00"))
    except ValueError:
        return None
    if stamp.tzinfo is None:
        return None
    result = {key: original[key] for key in fields}
    result["_time"] = original["occurred_at"]
    result["_msg"] = original["event_type"]
    result["component"] = "gameserver-controller"
    return result


def deliver(events, endpoint, token):
    if not events:
        return
    data = b"".join(json.dumps(event, separators=(",", ":")).encode() + b"\n"
                    for event in events)
    headers = {"Content-Type": "application/stream+json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = Request(endpoint, data=data, headers=headers, method="POST")
    with build_opener(NoRedirect).open(request, timeout=10) as response:
        if not 200 <= response.status < 300:
            raise ValueError("ingestion failed")


def run(source, raw_endpoint, token, batch_size):
    if not 1 <= batch_size <= 500:
        raise ValueError("batch size must be between 1 and 500")
    endpoint = endpoint_url(raw_endpoint)
    pending = []
    sent = 0
    for line in source:
        event = sanitized_event(line)
        if event is None:
            continue
        pending.append(event)
        if len(pending) >= batch_size:
            deliver(pending.copy(), endpoint, token)
            sent += len(pending)
            pending.clear()
    deliver(pending, endpoint, token)
    return sent + len(pending)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--token-file", type=Path)
    parser.add_argument("--batch-size", type=int, default=20)
    options = parser.parse_args()
    try:
        token = options.token_file.read_text(encoding="utf-8").strip() if options.token_file else ""
        count = run(sys.stdin, options.endpoint, token, options.batch_size)
    except (ValueError, OSError):
        print("VictoriaLogs forwarding failed; check privacy-safe collector state",
              file=sys.stderr)
        return 1
    print(f"Forwarded {count} allowlisted events", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
