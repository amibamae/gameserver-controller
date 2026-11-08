# Adapter RPC Methods

**Status:** Draft, version 0.21.0-draft. These methods define the adapter RPC surface. The process transport has a separate [specification](transport.md).

## 1. Method model

The initial binding uses a small set of stable JSON-RPC methods. Adapter-specific work runs through capabilities named in the relevant contract. Do not invent undocumented methods for standard capabilities.

| Method | Purpose | Side effects |
|---|---|---|
| `adapter.initialize` | Negotiate a protocol version and establish a session | Session only |
| `adapter.describe` | Return the adapter descriptor and capabilities | None |
| `adapter.health` | Report process/readiness diagnostics | None |
| `adapter.invoke` | Call one declared capability | Depends on capability |
| `adapter.reconcile` | Inspect an earlier call with an uncertain outcome | Read-only by default |

## 2. Request context

An `adapter.invoke` request carries:

- `protocol_version`: the negotiated version.
- `capability`: the exact name the adapter declared.
- `request_context`: request and correlation IDs; operation and step IDs for mutating work; deadline; environment and, where applicable, logical server ID; and an idempotency key when supported. A controller may add one-use `stream_transfers` immediately before dispatch when a capability needs bounded byte streams. These bearer handles are ephemeral invocation context; they MUST NOT be persisted in operation records or copied into logs, audit records, errors, or management API responses.
- `input`: the capability-specific object, validated against its declared input schema.

The controller must not trust actor identity or authorization claims from an adapter. It gives the adapter only the scoped operation context and permissions required for the work.

## 3. Initialize

Example request:

```json
{
  "jsonrpc": "2.0",
  "id": "req-001",
  "method": "adapter.initialize",
  "params": {
    "protocol_versions": ["0.21.0-draft"],
    "stream_transport": {
      "profile": "local-unix-stream-v1",
      "endpoint": "/run/user/1000/gameserver-controller/adapter-stream.sock"
    },
    "controller": {
      "implementation_id": "example.controller",
      "implementation_version": "0.1.0"
    }
  }
}
```

Example result:

```json
{
  "jsonrpc": "2.0",
  "id": "req-001",
  "result": {
    "selected_protocol_version": "0.21.0-draft",
    "session_id": "session-example",
    "selected_stream_transport": "local-unix-stream-v1"
  }
}
```

The adapter selects only a mutually supported version. If none is compatible, initialization returns a structured protocol-version error. It may omit `selected_stream_transport` when it does not support the optional profile. A selected profile must be one the controller offered.

## 4. Describe and health

`adapter.describe` returns the descriptor defined in [adapter contracts](../adapters/README.md). The controller validates it before enabling the adapter.

`adapter.health` is read-only. It returns health, observation time, and safe diagnostics. It must not expose credentials, raw environment variables, or unredacted provider responses.

## 5. Invoke

Example request:

```json
{
  "jsonrpc": "2.0",
  "id": "req-002",
  "method": "adapter.invoke",
  "params": {
    "protocol_version": "0.21.0-draft",
    "capability": "hosting.instance.inspect",
    "request_context": {
      "request_id": "req-002",
      "correlation_id": "corr-001",
      "environment_id": "test",
      "server_id": "minecraft-test",
      "deadline": "2026-11-05T23:00:00Z"
    },
    "input": {
      "instance_ref": {
        "adapter_id": "hosting.example",
        "environment_id": "test",
        "resource_kind": "instance",
        "external_id": "opaque-provider-id"
      }
    }
  }
}
```

Mutating capabilities also include `operation_id` and `step_id`. When the capability contract defines provider idempotency, the controller may include a provider-scoped token. That is separate from the management API's client `Idempotency-Key`; the client key does not prove a provider call is idempotent. The adapter contract defines the provider token's scope and meaning.

A result distinguishes:

- `completed`: the capability's postcondition is confirmed.
- `accepted`: the external system accepted work, but it remains pending; return an external operation reference and a way to reconcile.
- `outcome_unknown`: the effect may have happened, so reconcile before retrying.

Example accepted result:

```json
{
  "jsonrpc": "2.0",
  "id": "req-003",
  "result": {
    "outcome": "accepted",
    "external_operation_ref": {
      "adapter_id": "hosting.example",
      "environment_id": "test",
      "resource_kind": "operation",
      "external_id": "provider-operation-id"
    },
    "output": {},
    "observed_at": "2026-11-05T22:45:00Z"
  }
}
```

The descriptor or protocol spec links the exact capability output schema. A successful JSON-RPC response alone does not prove the external operation finished.

For an `outcome_unknown` response, the controller validates the capability output, then normalizes its `reconciliation_ref` or `external_operation_ref` into the durable operation step's `external_operation_ref`. If the JSON-RPC result also supplies its own `external_operation_ref`, it MUST exactly match any reference in the capability output; a conflict is untrusted and requires operator review. The controller persists the immutable step input and normalized reference, but MUST NOT persist the capability output as if the effect were complete. Reconciliation uses the operation/step IDs and the SHA-256 fingerprint of RFC 8785 JCS bytes for the durable capability input; transient request context, stream handles, outputs, and resolved secrets are excluded.

## 6. Reconcile

`adapter.reconcile` receives the original operation and step IDs, capability name, the fingerprint of the durable capability input, and any prior external references. It should inspect state without mutating it. The controller computes the fingerprint from RFC 8785 JCS bytes and excludes transient request context, stream handles, outputs, and resolved secret values.

It returns a documented result such as `completed`, `not_applied`, `still_pending`, or `indeterminate`. It must not repeat the original side effect just to discover what happened. If the adapter cannot determine the outcome, the controller suspends the operation for operator review.

## 7. JSON-RPC errors

JSON-RPC envelope errors cover parse and dispatch failures. Application errors use the common data object in [errors.md](errors.md). Clients branch on stable protocol error codes, not message text.

Required JSON-RPC codes:

- `-32700`: Parse error.
- `-32600`: Invalid Request.
- `-32601`: Method not found.
- `-32602`: Invalid params.
- `-32603`: Internal error.

Application errors may use the server-error range and must include a protocol error code in `error.data`.

## 8. Notifications and batches

Do not use notifications for mutating adapter calls; the caller needs an outcome or an uncertainty marker. Batch calls are outside the initial binding. Reject unsupported JSON-RPC features, or clearly decline to advertise them, rather than processing them inconsistently.
