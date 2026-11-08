# Cross-Language Protocol Interoperability Plan

**Status:** Draft planning document. This plan does not change protocol semantics or make a second implementation a release gate.

## Objective

Check that independently built implementations assign the same meaning to a pinned protocol package. Keep repository-owned validation and conformance tooling in Go.

## Package under test

An interoperability exercise must name an immutable package using all of these values:

- `protocol_version`
- Source commit or immutable tag
- `schema_registry_digest`
- `fixture_suite_digest`

Do not treat an untagged branch or open pull request as a compatibility target. Use the [package manifest](../protocol/conformance/package-manifest.json), [schema index](../protocol/schemas/index.json), and [conformance requirements](../protocol/conformance/requirements.md) from the same snapshot. See [compatibility rules](../protocol/COMPATIBILITY.md).

## Initial interoperability profile

After the active draft schema slices are reviewed, use a limited offline profile that covers:

- JSON Schema 2020-12 validation for the package schemas and examples, with local references only.
- Duplicate JSON object member names are rejected before ordinary JSON parsing.
- The protocol's interoperable integer and Unicode rules.
- `game.configure` RFC 8785 configuration-digest vectors, including secret references, number and string serialization, UTF-16 property ordering, array order, and member-order invariance.
- Release-pinned artifact checksum evidence for game installation.
- Execution start and proxy registration outcomes where acceptance alone does not prove readiness or routability.
- Session-scoped reconciliation for unknown Velocity transfers, including exact binding, observation freshness, already-at-destination resolution, and source-only retries under current readiness.
- Synthetic privacy canaries in transfer requests, schema-valid error details, and diagnostic logs.
- Standard operation-result type binding, including partial failure and cancellation when `operation_data` is absent.

A limited profile reports exactly which requirements and fixture IDs it covers. It does not claim full controller or adapter conformance.

## Independent implementation

Use an implementation and validator runtime independent from the Go conformance evaluator. Generated bindings are acceptable when they come from the pinned schemas. The independent consumer may live outside this repository; do not add a second maintained protocol-check tool here, so repository-owned tooling remains Go. The independent implementation must consume the same package files and fixture inputs, without importing Go evaluator code or relying on Go-generated expected outputs.

Compare parsed JSON values for ordinary protocol results. Require exact digest values for RFC 8785 vectors. A schema-valid message alone does not prove behavioral interoperability.

All runs use synthetic fixtures and have no provider calls, production credentials, or production data.

## Evidence to retain

Each run records:

- Package version, commit or tag, and both package-manifest digests.
- Claimed conformance profile.
- Implementation language, runtime, and validator versions.
- Pass or fail status for each included fixture ID.
- Excluded requirements, known limitations, and any disagreement.

Resolve disagreements in the normative protocol text, schemas, examples, or fixtures, then rerun Go CI and the independent implementation against the same package snapshot. Do not special-case one implementation.

## Sequence

1. Finish review of the current draft protocol slices and keep them under the draft compatibility policy.
2. Select and identify the immutable package snapshot for the exercise.
3. Choose an independent implementation owner and language.
4. Run the initial profile and publish the evidence with the package identity.
5. Expand coverage only after the first profile agrees; report unsupported profiles explicitly.

The language and implementation owner remain undecided. Current project policy keeps other-language implementations optional and does not make them a release requirement; this plan preserves that policy.
