#!/usr/bin/env python3
"""Validate the declarative supervised-pilot VM capacity policy and examples."""

import json
from pathlib import Path
import sys

pilot_dir = Path(__file__).resolve().parent


def read_json(path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"cannot read {path.name}: {exc}", file=sys.stderr)
        raise SystemExit(1)


policy = read_json(pilot_dir / "capacity-policy.json")
cases_document = read_json(pilot_dir / "capacity-policy-cases.json")
errors = []


def expect(actual, expected, label):
    if actual != expected or type(actual) is not type(expected):
        errors.append(f"{label}: expected {expected!r}, got {actual!r}")


expect(policy.get("policy_id"), "supervised-pilot-vm-capacity", "policy_id")
expect(policy.get("policy_version"), 1, "policy_version")
expect(policy.get("max_instances_total"), 2, "max_instances_total")

enforcement = policy.get("enforcement", {})
expect(enforcement.get("mode"), "declarative", "enforcement.mode")
expect(enforcement.get("runtime_enforced"), False, "enforcement.runtime_enforced")
reason = enforcement.get("reason", "")
if not isinstance(reason, str) or "no VM-create operation" not in reason:
    errors.append("enforcement.reason must explain that the CLI has no VM-create operation")

groups = policy.get("capacity_groups", {})
expected_groups = {
    "production": {"max_instances": 1, "accepted_environments": ["production"]},
    "shared_nonproduction": {
        "max_instances": 1,
        "accepted_environments": ["staging", "testing"],
    },
}
expect(groups, expected_groups, "capacity_groups")
if isinstance(groups, dict):
    capacities = [
        value.get("max_instances")
        for value in groups.values()
        if isinstance(value, dict) and type(value.get("max_instances")) is int
    ]
    expect(sum(capacities), policy.get("max_instances_total"), "sum of capacity groups")
    environments = [
        environment
        for value in groups.values()
        if isinstance(value, dict)
        and isinstance(value.get("accepted_environments"), list)
        for environment in value["accepted_environments"]
    ]
    expect(sorted(environments), ["production", "staging", "testing"], "accepted environments")
    expect(len(environments), len(set(environments)), "environment assignments")

create_policy = policy.get("create_policy", {})
required_create_guards = (
    "reserve_slot_atomically_before_provider_create",
    "reservation_counts_as_capacity",
    "reject_duplicate_or_occupied_slot",
    "reject_if_total_would_exceed_limit",
    "reject_unsupported_environment",
)
for key in required_create_guards:
    expect(create_policy.get(key), True, f"create_policy.{key}")

occupancy = create_policy.get("occupancy", {})
for key in (
    "count_every_instance_without_provider_confirmed_absence_or_destruction",
    "count_unresolved_create_outcome",
    "count_unresolved_destroy_outcome",
):
    expect(occupancy.get(key), True, f"create_policy.occupancy.{key}")
expect(
    occupancy.get("release_only_after_provider_confirmation"),
    ["absent", "destroyed"],
    "create_policy.occupancy.release_only_after_provider_confirmation",
)
expect(cases_document.get("policy_id"), policy.get("policy_id"), "cases.policy_id")

static_policy_is_valid = not errors
cases = cases_document.get("cases")
if not isinstance(cases, list):
    errors.append("cases must be a list")
    cases = []

release_states = set(occupancy.get("release_only_after_provider_confirmation", []))
seen_case_ids = set()


def decide(environment, instances):
    requested_group = None
    requested_capacity = None
    for group_id, group in groups.items():
        if environment in group["accepted_environments"]:
            requested_group = group_id
            requested_capacity = group["max_instances"]
            break
    if requested_group is None:
        return {"allowed": False, "reason": "unsupported_environment"}

    occupied = [
        instance
        for instance in instances
        if instance["provider_state"] not in release_states
    ]
    if len(occupied) >= policy["max_instances_total"]:
        return {"allowed": False, "reason": "total_limit_reached"}

    group_environments = set(groups[requested_group]["accepted_environments"])
    same_group_count = sum(
        instance["environment"] in group_environments for instance in occupied
    )
    if same_group_count >= requested_capacity:
        return {"allowed": False, "reason": "slot_occupied"}
    return {"allowed": True, "reason": "slot_available"}


for index, case in enumerate(cases):
    label = f"cases[{index}]"
    if not isinstance(case, dict):
        errors.append(f"{label} must be an object")
        continue

    case_id = case.get("id")
    if not isinstance(case_id, str) or not case_id:
        errors.append(f"{label}.id must be a non-empty string")
        continue
    if case_id in seen_case_ids:
        errors.append(f"duplicate case id: {case_id}")
        continue
    seen_case_ids.add(case_id)

    environment = case.get("request_environment")
    instances = case.get("existing_instances")
    expected = case.get("expected")
    if not isinstance(environment, str):
        errors.append(f"{case_id}.request_environment must be a string")
        continue
    if not isinstance(instances, list):
        errors.append(f"{case_id}.existing_instances must be a list")
        continue
    if not isinstance(expected, dict):
        errors.append(f"{case_id}.expected must be an object")
        continue
    if any(
        not isinstance(instance, dict)
        or not isinstance(instance.get("environment"), str)
        or not isinstance(instance.get("provider_state"), str)
        for instance in instances
    ):
        errors.append(f"{case_id}.existing_instances entries need environment and provider_state strings")
        continue
    if static_policy_is_valid:
        actual = decide(environment, instances)
        if actual != expected:
            errors.append(f"{case_id}: expected {expected!r}, got {actual!r}")

if not cases:
    errors.append("at least one capacity case is required")

if errors:
    print("capacity policy validation failed:", file=sys.stderr)
    for error in errors:
        print(f"- {error}", file=sys.stderr)
    raise SystemExit(1)

print(
    f"capacity policy and {len(cases)} cases are valid "
    "(declarative reference checks; not runtime-enforced)"
)
