#!/usr/bin/env python3
"""Validate the declarative supervised-pilot VM capacity policy."""

import json
from pathlib import Path
import sys

policy_path = Path(__file__).resolve().with_name("capacity-policy.json")
try:
    policy = json.loads(policy_path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError) as exc:
    print(f"cannot read capacity policy: {exc}", file=sys.stderr)
    raise SystemExit(1)

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
if "no VM-create operation" not in reason:
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
        for environment in value.get("accepted_environments", [])
    ]
    expect(sorted(environments), ["production", "staging", "testing"], "accepted environments")
    expect(len(environments), len(set(environments)), "environment assignments")

create_policy = policy.get("create_policy", {})
required_create_guards = (
    "reserve_slot_before_provider_create",
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

if errors:
    print("capacity policy validation failed:", file=sys.stderr)
    for error in errors:
        print(f"- {error}", file=sys.stderr)
    raise SystemExit(1)

print("capacity policy is valid (declarative; not runtime-enforced)")
