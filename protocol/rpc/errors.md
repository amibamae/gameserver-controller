# Protocol and RPC Errors

**Status:** Draft, version 0.21.0-draft.

## 1. Error layers

The protocol distinguishes:
1. **Transport errors:** the message could not be delivered or framed.
2. **JSON-RPC errors:** the request could not be parsed or dispatched according to JSON-RPC 2.0.
3. **Protocol/application errors:** a valid method call was rejected or failed under a documented contract.
4. **Operation failures:** a previously accepted durable operation ended in `failed` or `needs_operator`; these are read from the operation record, not inferred from an HTTP/RPC envelope.

A timeout or disconnect after dispatch may leave the operation outcome unknown. It MUST be reconciled.

## 2. Protocol error object

The canonical error is:

```json
{
  "code": "core.validation.invalid_request",
  "category": "validation",
  "message": "The request is invalid.",
  "retryable": false,
  "details": {},
  "correlation_id": "corr-001",
  "operation_id": null,
  "recovery": {
    "action": "correct_request",
    "operator_required": false
  }
}
```

Required fields are `code`, `category`, `message`, and `retryable`. Optional fields may be absent; an absent field is not the same as explicit null unless the schema says so. `details` MUST contain only documented, safe fields.

## 3. Categories

| Category | Meaning | Typical retry behavior |
|---|---|---|
| `validation` | Invalid input or configuration | No; correct request |
| `authorization` | Actor or adapter lacks permission | No; authorization must change |
| `unsupported_capability` | Required capability not advertised | No; choose another adapter/workflow |
| `not_found` | Target absent in stated scope | Usually no |
| `conflict` | State/lock/idempotency conflict | Only after conflict resolves |
| `transient` | Temporary service condition | Bounded retry if side-effect safe |
| `provider_failure` | Provider reports failure | Depends on provider result |
| `integrity_failure` | Data/manifest/checksum invalid | No automatic destructive continuation |
| `compatibility_failure` | Release/world/runtime incompatible | No; select compatible release or approved migration |
| `safety_blocked` | Safety precondition failed | No; resolve and re-authorize |
| `timeout` | Local wait or request deadline expired | Reconcile before retry if dispatched |
| `cancelled` | Operation cancelled under its rules | New operation for further work |
| `internal` | Unexpected implementation fault | Depends; preserve evidence and avoid unsafe retry |

## 4. Stable codes

Codes MUST be stable, machine-readable, and namespaced. Core codes are reserved to the protocol and MUST use the `core.*` namespace. Adapter and domain-specific codes MUST use a documented, stable namespace; adapter implementations MUST NOT claim or emit another owner's namespace.

Initial codes include:
- `core.validation.invalid_request`
- `core.authorization.denied`
- `core.lifecycle.invalid_transition`
- `core.operation.idempotency_conflict`
- `core.operation.idempotency_key_expired` (category `conflict`)
- `core.operation.outcome_unknown`
- `core.compatibility.unsupported_version` (category `compatibility_failure`)
- `core.compatibility.unknown_state` (category `compatibility_failure`)
- `core.safety.backup_not_verified`
- `core.safety.backup_generation_mismatch`
- `hosting.instance.not_found`
- `storage.object.checksum_mismatch`
- `execution.runtime.start_failed`
- `game.backup.incompatible`
- `integration.proxy.backend_not_ready`

A registered code's meaning and category MUST remain stable. Adding a code with its category is a compatible registry addition; removing or changing the meaning of a code requires a breaking protocol revision. Consumers MUST use the category for broad handling, MUST tolerate unknown codes according to their category, and MUST NOT branch on message text.

## 5. HTTP mapping

The management API uses HTTP status codes for request/transport outcome and RFC 9457 Problem Details with the protocol error fields in extensions for errors that occur before an operation is accepted. Once an operation is accepted, its result/failure is represented by the durable operation resource.

Recommended mappings:
- 400 malformed/invalid request.
- 401 unauthenticated.
- 403 authenticated but unauthorized.
- 404 unknown resource.
- 409 state, lock, or idempotency conflict.
- 422 valid JSON but semantically invalid request.
- 429 rate limited.
- 500 unexpected controller error.
- 502/503/504 external dependency or gateway failure, provided uncertain side effects are still reconciled.

A HTTP 5xx response MUST NOT be assumed to mean a dispatched provider action did not occur.
