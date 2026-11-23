#!/usr/bin/env python3
"""Install or revoke a privacy-restricted VictoriaLogs exporter configuration.

Only the pinned-SSH controller handoff invokes this root-only script. The
payload travels on SSH stdin, never a shell argument, bootstrap or log line.
"""
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit

DEST = Path("/etc/gsc-observability")
IDENTIFIER = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$")
MAX_REQUEST = 8192


def read_request(stream):
    raw = stream.read(MAX_REQUEST + 1)
    if len(raw) > MAX_REQUEST:
        raise ValueError("request too large")
    request = json.loads(raw)
    if not isinstance(request, dict):
        raise ValueError("invalid request")
    if set(request) != {"endpoint", "environment_id", "instance_id", "privacy_mode", "token"}:
        raise ValueError("unexpected configuration fields")
    if request["privacy_mode"] not in ("disabled", "lifecycle"):
        raise ValueError("unsupported privacy mode")
    for key in ("environment_id", "instance_id"):
        value = request[key]
        if not isinstance(value, str) or not IDENTIFIER.fullmatch(value):
            raise ValueError("invalid identifier")
    if request["privacy_mode"] == "disabled":
        if request["endpoint"] or request["token"]:
            raise ValueError("disabled exporter cannot retain a credential")
    else:
        validate_endpoint(request["endpoint"])
        token = request["token"]
        if not isinstance(token, str) or not 16 <= len(token) <= 1024:
            raise ValueError("invalid token")
        if any(ord(ch) < 33 or ord(ch) > 126 for ch in token):
            raise ValueError("invalid token characters")
    return request


def validate_endpoint(value):
    if not isinstance(value, str) or len(value) > 2048:
        raise ValueError("invalid endpoint")
    parsed = urlsplit(value)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username or
            parsed.password or parsed.query or parsed.fragment or
            parsed.path != "/insert/jsonline" or
            any(ch.isspace() for ch in value)):
        raise ValueError("invalid endpoint")
    return value


def systemctl(*args):
    subprocess.run(["/usr/bin/systemctl", *args],
                   check=True, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL, timeout=20)


def atomic_private_file(path, contents):
    fd, name = tempfile.mkstemp(prefix=".gsc-", dir=DEST)
    try:
        with os.fdopen(fd, "wb") as output:
            output.write(contents)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(name, 0o600)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def configure(request):
    os.umask(0o077)
    DEST.mkdir(mode=0o700, parents=True, exist_ok=True)
    # Stop further forwarding before rotating either endpoint or credential.
    systemctl("disable", "--now", "gsc-victorialogs.timer")
    systemctl("stop", "gsc-victorialogs.service")
    if request["privacy_mode"] == "disabled":
        for name in ("victorialogs.env", "victorialogs.token"):
            (DEST / name).unlink(missing_ok=True)
        return
    env = (
        "GSC_VL_ENDPOINT=" + request["endpoint"] + "\n"
        "GSC_VL_ENVIRONMENT_ID=" + request["environment_id"] + "\n"
        "GSC_VL_INSTANCE_ID=" + request["instance_id"] + "\n"
        "GSC_VL_RAW_GAME_LOGS=0\n"
        "GSC_VL_PLAYER_LOG_CONSENT=0\n"
    )
    atomic_private_file(DEST / "victorialogs.token", request["token"].encode())
    atomic_private_file(DEST / "victorialogs.env", env.encode())
    systemctl("enable", "--now", "gsc-victorialogs.timer")


def main():
    try:
        request = read_request(sys.stdin.buffer)
        configure(request)
        print("victorialogs privacy configuration applied")
    except (ValueError, OSError, json.JSONDecodeError, subprocess.SubprocessError):
        print("victorialogs configuration failed; inspect service state", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
