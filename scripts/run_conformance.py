#!/usr/bin/env python3
"""Execute the protocol's language-neutral conformance vectors."""
from __future__ import annotations

import json
import sys
from datetime import datetime
from pathlib import Path
from typing import Any

from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource

ROOT = Path(__file__).resolve().parents[1]
PROTOCOL = ROOT / "protocol"
VECTORS = PROTOCOL / "conformance" / "test-vectors"
INDEX = PROTOCOL / "schemas" / "index.json"
PROVISION_SCHEMA_ID = "urn:gameserver-controller:0.1.0-draft:hosting:provision-request"

LEGAL_TRANSITIONS = {
    ("unprovisioned", "provisioning"),
    ("provisioning", "preparing"), ("provisioning", "failed"),
    ("preparing", "restoring"), ("preparing", "starting"), ("preparing", "failed"),
    ("restoring", "starting"), ("restoring", "stopped"), ("restoring", "failed"),
    ("starting", "validating"), ("starting", "failed"),
    ("validating", "ready"), ("validating", "degraded"), ("validating", "failed"),
    ("ready", "degraded"), ("ready", "draining"), ("ready", "stopping"),
    ("degraded", "validating"), ("degraded", "draining"), ("degraded", "stopping"), ("degraded", "failed"),
    ("draining", "ready"), ("draining", "stopping"), ("draining", "failed"),
    ("stopping", "backing_up"), ("stopping", "stopped"), ("stopping", "failed"),
    ("backing_up", "starting"), ("backing_up", "stopped"), ("backing_up", "destroying"), ("backing_up", "failed"),
    ("stopped", "starting"), ("stopped", "restoring"), ("stopped", "backing_up"), ("stopped", "destroying"),
    ("destroying", "destroyed"), ("destroying", "failed"),
    ("destroyed", "provisioning"),
    ("failed", "provisioning"), ("failed", "preparing"), ("failed", "restoring"),
    ("failed", "stopping"), ("failed", "destroying"),
}
OUTCOMES = ("transferred", "already_at_destination", "disconnected", "rejected", "failed", "outcome_unknown")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def parse_time(value: str) -> datetime:
    require(value.endswith("Z"), f"expected UTC Z timestamp, got {value!r}")
    return datetime.fromisoformat(value[:-1] + "+00:00")


def validate_registered_schema(schema_id: str, value: Any) -> bool:
    index = json.loads(INDEX.read_text(encoding="utf-8"))
    schemas: dict[str, dict[str, Any]] = {}
    for entry in index["schemas"]:
        schema = json.loads((PROTOCOL / "schemas" / entry["path"]).read_text(encoding="utf-8"))
        require(schema.get("$id") == entry["id"], f"schema index ID mismatch for {entry['path']}")
        schemas[entry["id"]] = schema
    require(schema_id in schemas, f"schema is not registered: {schema_id}")
    registry = Registry()
    for registered_id, schema in schemas.items():
        registry = registry.with_resource(registered_id, Resource.from_contents(schema))
    validator = Draft202012Validator(schemas[schema_id], registry=registry, format_checker=FormatChecker())
    errors = sorted(validator.iter_errors(value), key=lambda error: (list(map(str, error.absolute_path)), error.message))
    if errors:
        details = "; ".join(f"{list(error.absolute_path)}: {error.message}" for error in errors)
        raise ValueError(f"schema validation failed: {details}")
    return True


def evaluate(fixture: dict[str, Any]) -> dict[str, Any]:
    fixture_id = fixture["fixture_id"]
    if fixture_id == "lifecycle.reject.unlisted-transition":
        transition = fixture["requested_transition"]
        allowed = (fixture["initial_state"], transition["to"]) in LEGAL_TRANSITIONS
        result = {"accepted": allowed, "side_effect_permitted": allowed}
        if not allowed:
            result["error_code"] = "core.lifecycle.invalid_transition"
        return result

    if fixture_id == "operations.reconcile.unknown-create-outcome":
        initial = fixture["initial"]
        unknown = initial["dispatch_recorded"] and not initial["provider_response_received"]
        return {
            "operation_status": "reconciling" if unknown else initial["operation_status"],
            "step_status": "outcome_unknown" if unknown else initial["step_status"],
            "retry_create_immediately": not unknown,
            "inspect_exact_target_or_search_by_idempotency_reference": unknown,
            "if_unresolvable": "needs_operator" if unknown else None,
        }

    if fixture_id in {
        "safety.destroy.blocked-stale-data-generation",
        "safety.destroy.blocked-unverified-backup",
    }:
        state = fixture["initial_state"]
        backup = state["backup"]
        if not backup["verified"]:
            code = "core.safety.backup_not_verified"
        elif state["data_generation_status"] != "known" or backup["data_generation_id"] != state["data_generation_id"]:
            code = "core.safety.backup_generation_mismatch"
        else:
            code = None
        return {
            "accepted": code is None,
            "error_code": code,
            "provider_destroy_calls": 1 if code is None else 0,
            "logical_server_state": "destroying" if code is None else state["lifecycle_state"],
        }

    if fixture_id == "security.staging.cannot-write-production-backup":
        actor = fixture["actor_environment"]
        target = fixture["target_environment"]
        required_scope = f"storage.backup.write:{target}"
        allowed = actor == target or required_scope in fixture["credential_scope"]
        return {
            "accepted": allowed,
            "side_effect_permitted": allowed,
            "error_category": None if allowed else "authorization",
        }

    if fixture_id == "readiness-required-before-transfer":
        assertion = fixture["input"]["readiness_assertion"]
        current = parse_time(fixture["input"]["validation_time"])
        expired = current >= parse_time(assertion["expires_at"])
        return {
            "outcome": "rejected" if expired else "accepted",
            "error_code": "readiness_assertion_expired" if expired else None,
            "proxy_transfer_calls": 0 if expired else len(fixture["input"]["player_session_ids"]),
        }

    if fixture_id == "velocity-transfer-partial-results":
        sessions = fixture["input"]["player_session_ids"]
        results = fixture["input"].get("session_outcomes", [])
        require(len(results) == len(sessions), "one session outcome is required for every requested session")
        require([item.get("session_id") for item in results] == sessions,
                "session outcomes must preserve the requested session list and order")
        counts = {outcome: 0 for outcome in OUTCOMES}
        for item in results:
            require(item.get("outcome") in counts, f"unknown transfer outcome: {item.get('outcome')!r}")
            counts[item["outcome"]] += 1
        has_unknown = counts["outcome_unknown"] > 0
        return {
            "requested_count": len(sessions),
            "outcome_counts": counts,
            "retry_unknown_without_reconciliation": not has_unknown,
        }

    if fixture_id == "operations.idempotency.same-key-different-request":
        first, second = fixture["first_request"], fixture["second_request"]
        same_binding = first["actor_id"] == second["actor_id"] and first["idempotency_key"] == second["idempotency_key"]
        conflict = same_binding and first != second
        return {
            "second_request_accepted": not conflict,
            "error_code": "core.operation.idempotency_conflict" if conflict else None,
            "operation_count": 1 if conflict else 2,
        }

    if fixture_id == "operations.events.monotonic-sequence":
        events = fixture["events"]
        require(bool(events), "event sequence must not be empty")
        operation_ids = {event["operation_id"] for event in events}
        sequences = [event["sequence"] for event in events]
        return {
            "schema_valid": all(isinstance(value, int) and not isinstance(value, bool) and value > 0 for value in sequences),
            "sequence_strictly_increasing": len(operation_ids) == 1 and all(
                left < right for left, right in zip(sequences, sequences[1:])
            ),
        }

    if fixture_id == "hosting.provision.valid.standard-os":
        valid = validate_registered_schema(PROVISION_SCHEMA_ID, fixture["input"])
        return {"schema_valid": valid, "side_effect_permitted": False}

    raise ValueError(f"no evaluator registered for fixture {fixture_id!r}")


def compare_expected(actual: Any, expected: Any, path: str = "$") -> None:
    if isinstance(expected, dict):
        require(isinstance(actual, dict), f"{path}: expected an object")
        for key, value in expected.items():
            compare_expected(actual.get(key), value, f"{path}.{key}")
    else:
        require(actual == expected, f"{path}: expected {expected!r}, got {actual!r}")


def main() -> int:
    paths = sorted(VECTORS.glob("*.json"))
    if not paths:
        print(f"ERROR: no conformance vectors found in {VECTORS}", file=sys.stderr)
        return 1

    failures: list[str] = []
    for path in paths:
        try:
            fixture = json.loads(path.read_text(encoding="utf-8"))
            compare_expected(evaluate(fixture), fixture["expected"])
        except Exception as exc:
            failures.append(f"{path.name}: {exc}")

    if failures:
        print("Conformance failed:")
        for failure in failures:
            print(f" - {failure}")
        return 1

    print(f"Conformance passed: {len(paths)} fixtures.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
