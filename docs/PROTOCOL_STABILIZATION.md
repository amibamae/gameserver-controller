# Protocol 1.0 stabilization

**Status:** Release-readiness assessment, not a stable release or certification.
The current wire version remains `0.21.0-draft`. This document records gaps and
acceptance evidence; it does not add product features or relax existing gates.

## Reviewed baseline

Source commit: `737e8b6f77c82b4951857de2790d659720befee4`.
The committed [package manifest](../protocol/conformance/package-manifest.json)
reports 166 schemas and 185 fixtures, with these digests:

- Schema registry: `sha256:4016be0077ddc37e2e54311a5b5b4e6970428aaff1966f2c66caffa59f0b45a9`
- Fixture suite: `sha256:1431b40cb1e18b0ca74059303843e346a582886b7ff564a787a5f55ea9f46e43`

The [Go validation run](https://github.com/amibamae/gameserver-controller/actions/runs/46047273050)
reported success on that commit. The
[Velocity plugin run](https://github.com/amibamae/gameserver-controller/actions/runs/46047272994)
reported failure. No new local execution is claimed by this assessment.
Recheck the exact release candidate rather than relying on this historical run.
The manifest covers the indexed schemas and JSON fixtures, not the entire
normative package; retain the exact source commit or immutable release tag.

## What stable means

Protocol 1.0 freezes the reviewed observable contract and establishes the
[compatibility policy](../protocol/COMPATIBILITY.md). It does not assert that
every optional capability is implemented, certify security, or authorize
production deployment. Follow the existing Phase 2 implementation gate in
[the release process](RELEASING.md): a minimal Go controller passes the
published behavioral suite with scripted fake adapters, no live infrastructure
and no production credentials. A second-language implementation, Proxmox,
live cloud deployment and advanced availability are not prerequisites.

A capability or operation that is not supported must be explicitly rejected;
unsupported required behavior cannot be silently omitted from a claimed
conformance profile. Document which requirements apply to each profile and
which optional capabilities are not claimed.

## Evidence checklist

| Gate | Baseline finding | Evidence needed before 1.0 |
|---|---|---|
| Normative contract review | Phase 1 draft review complete; management recovery and expiry wording needed clarification | Review every required contract for contradictions, undefined defaults, failure outcomes and implicit Go-only assumptions. Resolve or explicitly scope out open decisions. |
| Package validation | Committed manifest; Go validation passed on baseline | Exact candidate passes protocol-check, schemas, local references, examples, OpenAPI and all applicable fixtures; regenerate and verify manifest. |
| Behavioral suite | 185 fixture files and registered reference evaluators | Map every mandatory requirement to executable evidence or a justified non-applicability; distinguish reference evaluator results from tests of actual implementation. |
| Minimal controller | Workflow definitions and fake tests exist; normal runtime registers only server.define | Prove a minimal composed controller can accept and execute the required lifecycle with scripted adapters; verify all seven standard operations, and record any approved scope decision explicitly. Do not substitute schema validation or individual workflow tests for this evidence. |
| Recovery and liveness | Durable engine exists; runner drops submissions at queue saturation until next restart | Prove accepted work is rediscovered without silent loss, and waiting, uncertain, interrupted and conflicting work is reconciled under recorded contracts. |
| Management client interoperability | Initial authenticated routes; cancellation/events return not implemented | Verify authorization, exact errors, idempotent replay, expiry and ambiguous acceptance recovery; document optional/unsupported routes and capability discovery. |
| Independent implementability | Rust consumer described in the interoperability plan; limited profile, not full controller | Ensure a human can implement the contract from the pinned package without consulting Go or AI; review examples, public fixtures, error semantics and transport bindings. Independent implementation is welcome but not mandatory. |
| Compatibility and release | No stable release authorized by this assessment | Verify exact-version negotiation and recovery, review breaking changes and migrate or safely suspend old operations, prepare immutable package, review release notes and obtain owner approval. |

## Concrete follow-up work

1. Extend the published management submit-decision fixture with expired-key
   cases for both authorized routes. The existing operation-level expired-key
   fixture covers tombstone behavior; the new Go evaluator regression test
   checks the HTTP decision mapping. Also test the actual handler and
   unauthorized/unsupported precedence. Regenerate the manifest with the
   repository command and update independent consumers where applicable.
2. Verify lost acceptance-response handling through the management API:
   known reference polling, replay with identical actor/key/request when
   reference is unknown, changed request conflict, tombstone rejection and
   denial after authorization changes. Assert no duplicate side effects.
3. Build the minimal composition using the existing scripted adapters and
   workflow definitions, not production fake adapters. Exercise durable
   acceptance, dispatch and results through the controller boundary, including
   crash recovery, queue saturation, retries and uncertain effects.
4. Record the evidence at the exact candidate commit. Review the mandatory
   conformance groups, including readiness, storage integrity, destructive
   approvals, isolation and privacy. Keep missing or failing requirements
   visible; do not treat passing a subset as a full pass.
5. After independent review and the existing gate passes, prepare a single
   coherent 1.0 version update: VERSION, version-scoped schema IDs and
   references, fixtures, examples, API metadata, compatibility prose and
   implementation constants. Check the entire package for stale draft-version
   references, preserving intentional historical examples. Regenerate the
   manifest and run the full checks on the exact candidate. Publish an
   immutable tag/package only with owner approval.

Use the existing coordination registry and separate worktrees for follow-up
changes. Keep the basic standalone implementation simple; optional
infrastructure is not a release requirement. Do not edit production resources
or loosen the safety invariants to make the tests pass.
