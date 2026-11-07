# Protocol and RPC Errors

**Status:** Draft, version 0.21.0-draft.

## 1. Four error layers

The protocol separates:

1. **Transport errors:** a message could not be delivered or framed.
2. **JSON-RPC errors:** a request could not be parsed or dispatched under JSON-RPC 2.0.
3. **Protocol/application errors:** a valid method call was rejected or failed under its documented contract.
4. **Operation failures:** an accepted durable operation ended as `failed` or `needs_operator`. Read that from the operation record; do not infer it from an HTTP or RPC envelope.

A timeout or disconnect after dispatch may leave the outcome unknown. Reconcile it.

## 2. Error object

Here is a representative protocol error:

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

Every error includes `code`, `category`, `message`, and `retryable`. Optional fields may be absent. Absence differs from explicit `null` unless the schema says otherwise. The `details` object contains only documented, safe fields.

## 3. Categories

| Category | Meaning | Typical retry behavior |
|---|---|---|
| `validation` | Input or configuration is invalid | No; correct the request |
| `authorization` | Actor or adapter lacks permission | No; change authorization |
| `unsupported_capability` | Required capability is not advertised | No; choose another adapter or workflow |
| `not_found` | Target is absent in the stated scope | Usually no |
| `conflict` | State, lock, or idempotency conflict | Only after the conflict clears |
| `transient` | Temporary service condition | Bounded retry if safe |
| `provider_failure` | Provider reports failure | Depends on provider result |
| `integrity_failure` | Data or manifest/checksum verification failed | Do not continue destructive work automatically |
| `compatibility_failure` | Versions or formats do not match | Choose a compatible release or approved migration |
| `safety_blocked` | A safety precondition failed | Resolve it and re-authorize |
| `timeout` | Local wait or request deadline expired; external outcome may be unknown | Reconcile first if dispatch occurred |
| `cancelled` | Operation was cancelled under its rules | Use a new operation for further work |
| `internal` | Unexpected controller or adapter failure | Preserve evidence; avoid unsafe retries |

## 4. Stable codes

Codes are stable, namespaced identifiers. The protocol reserves `core.*`; adapters and domains must use their own documented namespaces and must not emit another owner's codes.

Initial codes include:

- `core.validation.invalid_request`
- `core.authorization.denied`
- `core.lifecycle.invalid_transition`
- `core.operation.conflict` (category `conflict`)
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

Once registered, a code keeps its meaning and category. Adding a code with its category is compatible; changing or removing a code requires a breaking protocol revision. Clients use the category for broad handling, tolerate unknown codes according to category, and never branch on message text.

## 5. HTTP mapping

Before operation acceptance, the management API uses HTTP status codes and RFC 9457 Problem Details with protocol fields in extensions. After acceptance, the durable operation resource carries the result or failure.

Recommended mappings:

- 400: malformed or invalid request.
- 401: unauthenticated.
- 403: authenticated, but unauthorized.
- 404: unknown resource.
- 409: state, lock, or idempotency conflict.
- 422: valid JSON with semantically invalid content.
- 429: rate limited.
- 500: unexpected controller error.
- 502, 503, or 504: external dependency or gateway failure, provided uncertain side effects are still reconciled.

An HTTP 5xx response does not prove that a dispatched provider action failed to occur.