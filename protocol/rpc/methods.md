# Adapter RPC Methods

**Status:** Draft, version 0.14.0-draft. These methods define the logical adapter RPC surface. The process transport is specified separately in [transport.md](transport.md).

## 1. Method model

The initial binding uses a small stable set of JSON-RPC methods. Adapter-specific work is invoked through capability names defined by the relevant adapter contract. Implementations MUST NOT invent an undocumented method for a standard capability.

| Method | Purpose | Side effects |
|---|---|---|
| `adapter.initialize` | Negotiate protocol version and establish adapter session | Session only |
| `adapter.describe` | Return adapter descriptor and capabilities | None |
| `adapter.health` | Return adapter process/readiness diagnostics | None |
| `adapter.invoke` | Invoke one declared capability | Depends on capability |
| `adapter.reconcile` | Inspect/reconcile an earlier call with uncertain outcome | Read-only by default |

## 2. Request context

An `adapter.invoke` request contains:
- `protocol_version`: negotiated protocol version.
- `capability`: exact capability name declared by the adapter.
- `request_context`: request ID, correlation ID, operation ID and step ID for mutating work, deadline, environment ID, logical server ID where applicable, and idempotency key when supported.
- `input`: capability-specific object validated against the capability's declared input schema.

The controller MUST NOT trust adapter-supplied actor identity or authorization claims. The adapter receives only the scoped operation context and permissions granted by the controller.

## 3. Initialize

Illustrative request:

```json
{
  "jsonrpc": "2.0",
  "id": "req-001",
  "method": "adapter.initialize",
  "params": {
    "protocol_versions": ["0.14.0-draft"],
    "controller": {
      "implementation_id": "example.controller",
      "implementation_version": "0.1.0"
    }
  }
}
```

Illustrative result:

```json
{
  "jsonrpc": "2.0",
  "id": "req-001",
  "result": {
    "selected_protocol_version": "0.14.0-draft",
    "session_id": "session-example"
  }
}
```

The adapter MUST select only a mutually supported version. If no version is compatible, initialization fails with a structured protocol-version error.

## 4. Describe and health

`adapter.describe` returns the descriptor defined in [adapter contracts](../adapters/README.md). The controller MUST validate the descriptor before enabling the adapter.

`adapter.health` is read-only and returns health status, observation time, and safe diagnostics. It MUST NOT expose credentials, raw environment variables, or unredacted provider responses.

## 5. Invoke

Illustrative request:

```json
{
  "jsonrpc": "2.0",
  "id": "req-002",
  "method": "adapter.invoke",
  "params": {
    "protocol_version": "0.14.0-draft",
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

A mutating capability MUST include `operation_id` and `step_id` in request context. It SHOULD include an idempotency key when supported.

The result MUST distinguish:
- `completed`: capability postcondition is confirmed.
- `accepted`: external system accepted work, but completion is pending; an external operation reference and reconciliation method are supplied.
- `outcome_unknown`: effect may have occurred and must be reconciled before retry.

Illustrative accepted result:

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

The exact `output` schema is capability-specific and MUST be linked from the descriptor or protocol specification. A successful JSON-RPC response does not itself mean the capability's external work completed.

## 6. Reconcile

`adapter.reconcile` receives the original operation/step identity, capability name, prior external operation/resource references, and safe original input reference or fingerprint. It MUST use read-only inspection where possible.

A reconcile call returns a documented outcome such as `completed`, `not_applied`, `still_pending`, or `indeterminate`. It MUST NOT repeat the original side effect merely to discover its result. If it cannot determine the outcome, the controller suspends the operation for operator intervention.

## 7. JSON-RPC errors

JSON-RPC envelope errors handle parse/dispatch failures. Application errors use the standard error data object defined in [errors.md](errors.md). Clients MUST branch on stable protocol error codes, not human-readable messages.

Required JSON-RPC standard error codes:
- `-32700`: Parse error.
- `-32600`: Invalid Request.
- `-32601`: Method not found.
- `-32602`: Invalid params.
- `-32603`: Internal error.

Application errors MAY use the JSON-RPC server error range and MUST include a protocol error code in `error.data`.

## 8. Notifications and batch calls

Notifications MUST NOT be used for mutating adapter calls because the caller needs an outcome or uncertainty marker. JSON-RPC batch requests are not part of the initial adapter binding. An implementation MUST reject or explicitly not advertise unsupported JSON-RPC features rather than process them inconsistently.
