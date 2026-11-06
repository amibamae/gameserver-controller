#!/usr/bin/env python3
"""Validate the language-neutral Game Server Controller protocol package."""

from __future__ import annotations

import json
import sys
from pathlib import Path, PurePosixPath
from typing import Any

import yaml
from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource

ROOT = Path(__file__).resolve().parents[1]
SCHEMA_ROOT = ROOT / "protocol" / "schemas"
INDEX_PATH = SCHEMA_ROOT / "index.json"

EXAMPLE_SCHEMA_IDS = {
    "adapter-descriptor.hosting.json": "urn:gameserver-controller:0.1.0-draft:common:capability-descriptor",
    "adapter-descriptor.systemd.json": "urn:gameserver-controller:0.1.0-draft:common:capability-descriptor",
    "adapter-descriptor.velocity.json": "urn:gameserver-controller:0.1.0-draft:common:capability-descriptor",
    "adapter-invoke-request.json": "urn:gameserver-controller:0.1.0-draft:rpc:adapter-invoke-request",
    "backup-manifest.json": "urn:gameserver-controller:0.1.0-draft:storage:backup-manifest",
    "operation-accepted.json": "urn:gameserver-controller:0.1.0-draft:operations:operation",
    "operation-succeeded.json": "urn:gameserver-controller:0.1.0-draft:operations:operation",
    "operation-result.succeeded.json": "urn:gameserver-controller:0.1.0-draft:operations:server-provision-result",
    "operation-result.backup.succeeded.json": "urn:gameserver-controller:0.1.0-draft:operations:server-backup-result",
    "operation-result.restore.succeeded.json": "urn:gameserver-controller:0.1.0-draft:operations:server-restore-result",
    "operation-result.destroy.succeeded.json": "urn:gameserver-controller:0.1.0-draft:operations:server-destroy-result",
    "operation-event.json": "urn:gameserver-controller:0.1.0-draft:operations:operation-event",
    "provision-request.json": "urn:gameserver-controller:0.1.0-draft:hosting:provision-request",
    "velocity-player-transfer-request.json": "urn:gameserver-controller:0.1.0-draft:integrations:velocity-player-transfer-request",
    "velocity-player-transfer-result.json": "urn:gameserver-controller:0.1.0-draft:integrations:velocity-player-transfer-result",
}


def reject_constant(value: str) -> None:
    raise ValueError(f"non-finite JSON number is not permitted: {value}")


def reject_duplicate_members(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON object member: {key}")
        result[key] = value
    return result


def check_safe_integer_values(value: Any, source: str, location: str = "$") -> None:
    if isinstance(value, bool):
        return
    if isinstance(value, int):
        if abs(value) > 9_007_199_254_740_991:
            raise ValueError(f"{source}{location}: integer exceeds the interoperable JSON safe-integer range")
    elif isinstance(value, dict):
        for key, child in value.items():
            check_safe_integer_values(child, source, f"{location}.{key}")
    elif isinstance(value, list):
        for index, child in enumerate(value):
            check_safe_integer_values(child, source, f"{location}[{index}]")


def load_json(path: Path) -> Any:
    value = json.loads(
        path.read_text(encoding="utf-8"),
        object_pairs_hook=reject_duplicate_members,
        parse_constant=reject_constant,
    )
    check_safe_integer_values(value, str(path))
    return value


def collect_refs(value: Any):
    if isinstance(value, dict):
        ref = value.get("$ref")
        if isinstance(ref, str):
            yield ref
        for child in value.values():
            yield from collect_refs(child)
    elif isinstance(value, list):
        for child in value:
            yield from collect_refs(child)


def fail(errors: list[str], message: str) -> None:
    errors.append(message)


def main() -> int:
    errors: list[str] = []

    if not INDEX_PATH.is_file():
        print("ERROR: protocol/schemas/index.json is missing.", file=sys.stderr)
        return 1

    try:
        index = load_json(INDEX_PATH)
    except Exception as exc:
        print(f"ERROR: cannot parse schema index: {exc}", file=sys.stderr)
        return 1

    protocol_version = index.get("protocol_version")
    if not isinstance(protocol_version, str) or not protocol_version:
        fail(errors, "schema index must declare protocol_version")

    if index.get("json_schema_dialect") != "https://json-schema.org/draft/2020-12/schema":
        fail(errors, "schema index must select JSON Schema Draft 2020-12")

    entries = index.get("schemas")
    if not isinstance(entries, list) or not entries:
        fail(errors, "schema index must contain a non-empty schemas array")
        entries = []

    schemas_by_id: dict[str, dict[str, Any]] = {}
    indexed_paths: set[str] = set()

    for entry in entries:
        if not isinstance(entry, dict):
            fail(errors, "schema index entries must be objects")
            continue
        relative = entry.get("path")
        expected_id = entry.get("id")
        if not isinstance(relative, str) or not isinstance(expected_id, str):
            fail(errors, f"schema index entry is missing path or id: {entry!r}")
            continue

        posix_path = PurePosixPath(relative)
        if posix_path.is_absolute() or ".." in posix_path.parts:
            fail(errors, f"unsafe schema path in index: {relative}")
            continue
        if relative in indexed_paths:
            fail(errors, f"duplicate schema path in index: {relative}")
            continue
        indexed_paths.add(relative)

        path = SCHEMA_ROOT / Path(*posix_path.parts)
        if not path.is_file():
            fail(errors, f"indexed schema file does not exist: {relative}")
            continue
        try:
            schema = load_json(path)
        except Exception as exc:
            fail(errors, f"cannot parse schema {relative}: {exc}")
            continue

        schema_id = schema.get("$id")
        if schema_id != expected_id:
            fail(errors, f"{relative}: index id does not match schema $id ({expected_id!r} != {schema_id!r})")
        if not isinstance(schema_id, str) or not schema_id:
            fail(errors, f"{relative}: schema has no non-empty $id")
            continue
        if schema_id in schemas_by_id:
            fail(errors, f"duplicate schema $id: {schema_id}")
            continue
        if schema.get("$schema") != "https://json-schema.org/draft/2020-12/schema":
            fail(errors, f"{relative}: schema must declare Draft 2020-12")

        try:
            Draft202012Validator.check_schema(schema)
        except Exception as exc:
            fail(errors, f"{relative}: invalid JSON Schema: {exc}")
            continue
        schemas_by_id[schema_id] = schema

    actual_schema_paths = {
        path.relative_to(SCHEMA_ROOT).as_posix()
        for path in SCHEMA_ROOT.rglob("*.schema.json")
    }
    for missing in sorted(actual_schema_paths - indexed_paths):
        fail(errors, f"schema file is not listed in index.json: {missing}")
    for missing in sorted(indexed_paths - actual_schema_paths):
        fail(errors, f"index entry is not a discoverable schema file: {missing}")

    registry = Registry()
    for schema_id, schema in schemas_by_id.items():
        registry = registry.with_resource(schema_id, Resource.from_contents(schema))

    for relative, schema in (
        (entry.get("path", "<unknown>"), schemas_by_id.get(entry.get("id", "")))
        for entry in entries if isinstance(entry, dict)
    ):
        if schema is None:
            continue
        for ref in collect_refs(schema):
            base_ref = ref.split("#", 1)[0]
            if base_ref.startswith("urn:") and base_ref not in schemas_by_id:
                fail(errors, f"{relative}: unresolved local schema reference: {ref}")
            elif base_ref and not base_ref.startswith("urn:"):
                fail(errors, f"{relative}: external schema reference is not allowed in the closed registry: {ref}")

    format_checker = FormatChecker()
    validators = {
        schema_id: Draft202012Validator(schema, registry=registry, format_checker=format_checker)
        for schema_id, schema in schemas_by_id.items()
    }

    examples_dir = ROOT / "protocol" / "examples"
    actual_examples = {path.name for path in examples_dir.glob("*.json")}
    expected_examples = set(EXAMPLE_SCHEMA_IDS)
    for missing in sorted(actual_examples - expected_examples):
        fail(errors, f"example is not mapped to a schema in validator: {missing}")
    for missing in sorted(expected_examples - actual_examples):
        fail(errors, f"mapped example does not exist: {missing}")

    for filename, schema_id in EXAMPLE_SCHEMA_IDS.items():
        path = examples_dir / filename
        if not path.is_file():
            continue
        if schema_id not in validators:
            fail(errors, f"{filename}: mapped schema is missing from index.json: {schema_id}")
            continue
        try:
            document = load_json(path)
        except Exception as exc:
            fail(errors, f"{filename}: invalid JSON: {exc}")
            continue
        if isinstance(document, dict) and isinstance(document.get("capabilities"), list):
            for capability_index, capability in enumerate(document["capabilities"]):
                if not isinstance(capability, dict):
                    fail(errors, f"{filename}: capability {capability_index} must be an object")
                    continue
                for field in ("input_schema_uri", "output_schema_uri"):
                    schema_uri = capability.get(field)
                    if schema_uri not in schemas_by_id:
                        fail(
                            errors,
                            f"{filename}: capability {capability_index} {field} "
                            f"does not resolve in the local schema registry: {schema_uri!r}",
                        )

        validation_errors = sorted(
            validators[schema_id].iter_errors(document),
            key=lambda error: (list(map(str, error.absolute_path)), error.message),
        )
        for error in validation_errors:
            location = "$" + "".join(
                f"[{part}]" if isinstance(part, int) else f".{part}"
                for part in error.absolute_path
            )
            fail(errors, f"{filename}{location}: {error.message}")

    vectors_dir = ROOT / "protocol" / "conformance" / "test-vectors"
    fixture_ids: set[str] = set()
    for path in sorted(vectors_dir.glob("*.json")):
        try:
            fixture = load_json(path)
        except Exception as exc:
            fail(errors, f"{path.name}: invalid conformance fixture JSON: {exc}")
            continue
        fixture_id = fixture.get("fixture_id") if isinstance(fixture, dict) else None
        if not isinstance(fixture_id, str) or not fixture_id:
            fail(errors, f"{path.name}: fixture_id is required")
        elif fixture_id in fixture_ids:
            fail(errors, f"{path.name}: duplicate fixture_id: {fixture_id}")
        else:
            fixture_ids.add(fixture_id)
        if fixture.get("protocol_version") != protocol_version:
            fail(errors, f"{path.name}: fixture protocol_version must match schema index")
        if not isinstance(fixture.get("expected"), dict):
            fail(errors, f"{path.name}: expected must be an object")

    openapi_path = ROOT / "protocol" / "management-api" / "openapi.yaml"
    try:
        openapi_doc = yaml.safe_load(openapi_path.read_text(encoding="utf-8"))
        openapi_version = openapi_doc.get("openapi") if isinstance(openapi_doc, dict) else None
        if openapi_version != "3.2.1":
            fail(errors, f"OpenAPI document must declare 3.2.1, found {openapi_version!r}")
        components = openapi_doc.get("components", {})
        api_schemas = components.get("schemas", {}) if isinstance(components, dict) else {}
        expected_refs = {
            "Operation": "../schemas/operations/operation.schema.json",
            "Server": "../schemas/lifecycle/server.schema.json",
        }
        for name, expected_ref in expected_refs.items():
            actual = api_schemas.get(name, {}) if isinstance(api_schemas, dict) else {}
            if not isinstance(actual, dict) or actual.get("$ref") != expected_ref:
                fail(errors, f"OpenAPI component {name} must reference the canonical schema {expected_ref}")

        for reference in collect_refs(openapi_doc):
            if reference.startswith("#"):
                continue
            relative_reference = reference.split("#", 1)[0]
            if not relative_reference:
                continue
            target = (openapi_path.parent / relative_reference).resolve()
            try:
                target.relative_to(ROOT.resolve())
            except ValueError:
                fail(errors, f"OpenAPI reference escapes the repository: {reference}")
                continue
            if not target.is_file():
                fail(errors, f"OpenAPI reference target does not exist: {reference}")
                continue
            try:
                schema_relative = target.relative_to(SCHEMA_ROOT.resolve()).as_posix()
            except ValueError:
                fail(errors, f"OpenAPI external reference must point to the local schema package: {reference}")
                continue
            if schema_relative not in indexed_paths:
                fail(errors, f"OpenAPI reference target is not registered in schemas/index.json: {reference}")
    except Exception as exc:
        fail(errors, f"cannot parse OpenAPI document: {exc}")

    if errors:
        print("Protocol validation failed:")
        for error in errors:
            print(f" - {error}")
        return 1

    print(
        f"Protocol validation passed: {len(schemas_by_id)} schemas, "
        f"{len(EXAMPLE_SCHEMA_IDS)} examples, {len(fixture_ids)} conformance fixtures, "
        f"protocol {protocol_version}."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
