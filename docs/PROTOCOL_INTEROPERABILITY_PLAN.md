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
- Release-pinned installation evidence for the exact artifact set, including per-artifact expected and observed checksums.
- Execution lifecycle outcomes: exact unit inspection, unknown start and graceful-stop reconciliation, and the distinction between process-running and game-ready.
- Proxy player-count freshness and registration outcomes where acceptance alone does not prove routability.
- Session-scoped reconciliation for unknown Velocity transfers, including exact binding, observation freshness, already-at-destination resolution, and source-only retries under current readiness.
- Synthetic privacy canaries in transfer requests, schema-valid error details, and diagnostic logs.
- Standard operation-result type binding, including partial failure and cancellation when `operation_data` is absent.

A limited profile reports exactly which requirements and fixture IDs it covers. It does not claim full controller or adapter conformance.

## Required fixture selection

Use every case and expected assertion in each fixture below. The fixture's `expected` object is the published oracle; an independent consumer computes its own results from the inputs. A negative message that correctly fails validation is a passing case, not a skipped fixture.

| Fixture ID | Required evidence |
|---|---|
| `game.release.describe.integrity-and-identity` | Exact release identity, unique artifacts, and supported checksum representation |
| `game.configure.pinned-schema-and-result-binding` | All configuration cases, including digest mismatch, secret references, RFC 8785 serialization, and permuted member order |
| `game.install.release-binding-and-reconciliation` | Exact artifact set, release-pinned expected/observed checksums, and uncertain outcomes |
| `execution.unit.start.accepted-not-ready` | Accepted process startup does not establish game readiness |
| `execution.unit.graceful-stop.unknown-requires-inspection` | Unknown stop does not confirm termination or permit blind retry |
| `execution.unit.inspect.exact-target-state` | Exact running/stopped observations; unknown and mismatched unit results remain inconclusive |
| `execution.unit.logs.byte-bound-and-redaction` | UTF-8 bytes plus newline separators, exact unit binding, and synthetic secret detection |
| `execution.unit.health.diagnostic-redaction-and-binding` | Diagnostic canaries, target binding, and unknown health |
| `integration.proxy.register.accepted-is-not-routable` | Registration acceptance alone does not authorize routing |
| `integration.proxy.players.inspect.fresh-count-and-target-binding` | Count/timestamp pairing, exact target, age boundary, stale/future evidence, and unknown state |
| `velocity-transfer-outcome-unknown-reconciliation` | Every reconciliation case, including destination resolution and source-only retry under current readiness |
| `velocity-transfer-privacy-canaries` | Message/error probes and exact session-ID redaction in diagnostic logs |
| `operations.type-binding` | Per-record schema and type-binding outcomes, including valid and malformed partial cancellations |
| `operations.restore.final-state-policy` | Omitted/false startup ends stopped; explicit startup ends ready; contradictions fail |
| `operations.backup.no-resume-final-state` | Explicitly disabling resume forbids a successful ready result |

Resolve these IDs by reading the fixture files in the selected package, not by guessing filenames. Before a run, record the exact fixture paths and case counts from that snapshot. Some listed fixtures are currently proposed in separate drafts. A snapshot missing a required fixture cannot claim this profile; report it as not runnable, or name a narrower exploratory subset with its omissions.

This selection does not yet name a shared raw-byte parsing corpus. Before claiming the full initial profile, select or publish messages and expected acceptance for duplicate member names, safe-integer boundaries in ordinary/decimal/exponent notation, invalid UTF-8, lone escaped surrogates, and valid surrogate pairs. Preserve invalid wire bytes using an unambiguous encoding such as hexadecimal; parsing the test corpus itself must not normalize them. Go unit tests alone do not establish cross-language agreement on those inputs.

Backup automatic resume from its starting lifecycle state is excluded until the proposed durable context and degraded-state policy are resolved. The no-resume fixture above proves only the explicit false rule.

## Independent implementation

Use an implementation and validator runtime independent from the Go conformance evaluator. Generated bindings are acceptable when they come from the pinned schemas. The independent consumer may live outside this repository; do not add a second maintained protocol-check tool here, so repository-owned tooling remains Go. The independent implementation must consume the same package files and fixture inputs, without importing Go evaluator code or relying on Go-generated expected outputs.

Compare parsed JSON values for ordinary protocol results. Require exact digest values for RFC 8785 vectors. A schema-valid message alone does not prove behavioral interoperability. Offline message vectors do not prove that an adapter avoids side effects. Verify that claim with an implementation harness that records attempted effects.

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
