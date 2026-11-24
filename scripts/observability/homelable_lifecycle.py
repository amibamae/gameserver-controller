#!/usr/bin/env python3
"""Optional, non-destructive Homelable reconciliation from verified GSC state.

Only explicitly GSC-owned nodes may be updated. Retired instances are marked
offline, never deleted. A dry run never reads a token or contacts the API.
"""
import argparse
import json
from pathlib import Path
import re
import sys
from urllib.parse import urlencode

import homelable_sync as api

OPERATION_ID = re.compile(r"^[0-9a-f]{32}$")
OWNERSHIP_PREFIX = "gsc-managed-operation:"
STATES = frozenset({"active", "retired"})


def read_snapshot(path):
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or set(data) != {"design_id", "nodes"}:
        raise ValueError("invalid lifecycle snapshot")
    design = data["design_id"]
    if not isinstance(design, str) or not api.ID.fullmatch(design):
        raise ValueError("invalid design ID")
    nodes = data["nodes"]
    if not isinstance(nodes, list) or len(nodes) > 50:
        raise ValueError("invalid lifecycle inventory size")
    result, seen = [], set()
    for node in nodes:
        result.append(managed_node(node, design, seen))
    return design, result


def managed_node(node, design, seen):
    if not isinstance(node, dict) or set(node) != {"operation_id", "name", "type", "state"}:
        raise ValueError("invalid managed node fields")
    op, name, kind, state = (node[k] for k in ("operation_id", "name", "type", "state"))
    if not isinstance(op, str) or not OPERATION_ID.fullmatch(op) or op in seen:
        raise ValueError("duplicate or invalid operation identity")
    if not valid_name(name):
        raise ValueError("invalid lifecycle node name")
    if kind != "vm" or not isinstance(state, str) or state not in STATES:
        raise ValueError("unsupported node type or lifecycle state")
    seen.add(op)
    return {"label": (api.MANAGED_PREFIX + name)[:80], "type": "vm", "design_id": design,
            "notes": OWNERSHIP_PREFIX + op, "state": state}


def valid_name(name):
    if not isinstance(name, str) or not name or len(name) > 80:
        return False
    return not any(ord(ch) < 32 for ch in name)


def index_existing(existing):
    by_marker, by_label = {}, {}
    if not isinstance(existing, list):
        raise ValueError("Homelable node list is invalid")
    for node in existing:
        if not isinstance(node, dict):
            raise ValueError("invalid existing Homelable node")
        label, marker = node.get("label"), node.get("notes")
        if isinstance(label, str):
            by_label.setdefault(label, []).append(node)
        if isinstance(marker, str) and marker.startswith(OWNERSHIP_PREFIX):
            by_marker.setdefault(marker, []).append(node)
    return by_marker, by_label


def preflight(existing, nodes):
    """Validate every proposed update before performing any mutation."""
    labels = [node["label"] for node in nodes]
    if len(set(labels)) != len(labels):
        raise ValueError("duplicate desired node label")
    by_marker, by_label = index_existing(existing)
    for node in nodes:
        matches = by_marker.get(node["notes"], [])
        if len(matches) > 1:
            raise ValueError("duplicate managed node identity")
        collisions = by_label.get(node["label"], [])
        if len(collisions) > 1 or any(x.get("notes") != node["notes"] for x in collisions):
            raise ValueError("node label belongs to an unmanaged or ambiguous node")
        if matches and not api.ID.fullmatch(str(matches[0].get("id", ""))):
            raise ValueError("managed Homelable node has invalid server ID")
    return by_marker


def reconcile(base, design, nodes, token, apply=False):
    if not apply:
        return ["DRY RUN: reconcile " + n["label"] + " as " + n["state"] for n in nodes]
    existing = api.http_json(base, "/api/v1/nodes?" + urlencode({"design_id": design}), token)
    by_marker = preflight(existing, nodes)
    changes = []
    for node in nodes:
        matches = by_marker.get(node["notes"], [])
        target = matches[0] if matches else None
        changes.append(reconcile_node(base, node, token, target))
    return changes


def reconcile_node(base, node, token, target):
    desired = "online" if node["state"] == "active" else "offline"
    if target is None:
        if desired == "offline":
            return "retired and absent: " + node["label"]
        payload = {k: node[k] for k in ("label", "type", "design_id", "notes")}
        # A new topology node is not a source of actual Minecraft readiness.
        payload["status"] = desired
        created = api.http_json(base, "/api/v1/nodes", token, "POST", payload)
        if not isinstance(created, dict) or not isinstance(created.get("id"), str):
            raise ValueError("Homelable did not return the node ID")
        return "created: " + node["label"]
    if target.get("status") == desired:
        return "unchanged: " + node["label"]
    api.http_json(base, "/api/v1/nodes/" + target["id"], token, "PATCH", {"status": desired})
    return "updated: " + node["label"] + " => " + desired


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--snapshot", required=True, type=Path)
    parser.add_argument("--token-file", type=Path)
    parser.add_argument("--apply", action="store_true")
    options = parser.parse_args()
    try:
        base = api.validate_base(options.url)
        design, nodes = read_snapshot(options.snapshot)
        if options.apply and options.token_file is None:
            raise ValueError("explicit token file required")
        token = options.token_file.read_text(encoding="utf-8").strip() if options.apply else ""
        if options.apply and not token:
            raise ValueError("invalid Homelable JWT")
        for result in reconcile(base, design, nodes, token, options.apply):
            print(result)
    except (OSError, ValueError):
        print("optional Homelable reconciliation failed; no nodes were deleted",
              file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
