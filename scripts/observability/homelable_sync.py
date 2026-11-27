#!/usr/bin/env python3
"""Opt-in Homelable topology registration; creates missing GSC nodes only.

Existing nodes are never edited or deleted. A managed prefix separates these
entries from operator-maintained topology. Default execution is a dry run.
"""
import argparse
import json
from pathlib import Path
import re
import sys
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

MANAGED_PREFIX = "GSC / "
ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$")
TYPES = frozenset({"server", "vm", "router", "switch", "firewall", "nas",
                   "lxc", "docker_host", "docker_container", "ap"})


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def validate_base(value):
    parsed = urlsplit(value)
    host = (parsed.hostname or "").lower()
    if (not host or parsed.username or parsed.password or parsed.query or
            parsed.fragment or not value or len(value) > 2048):
        raise ValueError("invalid Homelable base URL")
    if parsed.scheme != "https" and not (
            parsed.scheme == "http" and host in ("localhost", "127.0.0.1", "::1")):
        raise ValueError("Homelable requires HTTPS or loopback")
    return value.rstrip("/")


def load_inventory(path):
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict) or set(value) != {"design_id", "nodes"}:
        raise ValueError("inventory needs a design_id and nodes only")
    design_id, nodes = value["design_id"], value["nodes"]
    if not isinstance(design_id, str) or not ID.fullmatch(design_id):
        raise ValueError("invalid design ID")
    if not isinstance(nodes, list) or len(nodes) > 50:
        raise ValueError("invalid node list")
    result, seen = [], set()
    for node in nodes:
        if not isinstance(node, dict) or set(node) != {"name", "type"}:
            raise ValueError("nodes require a name and type")
        name, kind = node["name"], node["type"]
        if not isinstance(name, str) or not 0 < len(name) <= 80 or any(
                ord(ch) < 32 for ch in name):
            raise ValueError("invalid node name")
        if kind not in TYPES or name in seen:
            raise ValueError("duplicate node name or unsupported type")
        seen.add(name)
        result.append({"label": MANAGED_PREFIX + name,
                       "type": kind, "design_id": design_id})
    return design_id, result


def http_json(base, path, token, method="GET", payload=None):
    data = None if payload is None else json.dumps(payload).encode()
    headers = {"Authorization": "Bearer " + token, "Accept": "application/json"}
    if data is not None:
        headers["Content-Type"] = "application/json"
    request = Request(base + path, data=data, headers=headers, method=method)
    with build_opener(NoRedirect).open(request, timeout=10) as response:
        body = response.read((1024 * 1024) + 1)
        if len(body) > 1024 * 1024:
            raise ValueError("Homelable API response too large")
        return json.loads(body) if body else None


def sync(base, design_id, nodes, token, apply=False):
    if not apply:
        return ["DRY RUN: ensure " + node["label"] for node in nodes]
    existing = http_json(base, "/api/v1/nodes?" +
                         urlencode({"design_id": design_id}), token)
    if not isinstance(existing, list):
        raise ValueError("Homelable nodes response is not a list")
    counts = {}
    for node in existing:
        if isinstance(node, dict) and isinstance(node.get("label"), str):
            label = node["label"]
            counts[label] = counts.get(label, 0) + 1
    actions = []
    for node in nodes:
        count = counts.get(node["label"], 0)
        if count > 1:
            raise ValueError("ambiguous existing Homelable node label")
        if count == 1:
            actions.append("existing: " + node["label"])
            continue
        created = http_json(base, "/api/v1/nodes", token, method="POST", payload=node)
        if not isinstance(created, dict) or not isinstance(created.get("id"), str):
            raise ValueError("Homelable did not return the new node ID")
        actions.append("created: " + node["label"])
    return actions


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--inventory", required=True, type=Path)
    parser.add_argument("--token-file", type=Path,
                        help="restricted file containing a short-lived Homelable JWT")
    parser.add_argument("--apply", action="store_true",
                        help="explicit permission to create missing nodes")
    args = parser.parse_args()
    try:
        base = validate_base(args.url)
        design, nodes = load_inventory(args.inventory)
        if args.apply and args.token_file is None:
            raise ValueError("token file required to apply")
        token = args.token_file.read_text(encoding="utf-8").strip() if args.apply else ""
        if args.apply and not token:
            raise ValueError("empty Homelable JWT")
        for action in sync(base, design, nodes, token, apply=args.apply):
            print(action)
    except (ValueError, OSError):
        print("Homelable topology sync failed; nothing was deleted", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
