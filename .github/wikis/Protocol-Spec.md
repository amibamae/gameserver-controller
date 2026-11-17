# Protocol Specification

This document describes the management API contract for Game Server Controller. The protocol uses JSON Schema 2020-12 and follows JSON-RPC 2.0. It's designed to be language-agnostic; Go is just the first implementation.

---

## Overview

The controller exposes a versioned management API (currently v1) with:

- **HTTP/JSON** endpoints following OpenAPI 3.2.1
- **JSON-RPC** for CLI and direct calls
- **Versioning** via URL path prefix (`/v1/`)

All responses use JSON Schema for type safety and documentation.

---

## Lifecycle State Machine

Servers move through these states:

```
defined → provisioning → restoring → starting → running → ready
             ↓               ↓             ↓             ↓
          failed          failed         failed        (ready)
                                                                  ↓
                                                               draining → stopping
                                                                          ↓
                                                                    saving → backing up
                                                                            ↓
                                                                       verified
                                                                            ↓
                                                                        destroying
                                                                            ↓
                                                                       destroyed
```

Each state represents a checkpoint in the durable operation model. Transitions require explicit operations and may fail, triggering recovery.

---

## Operations Model

Operations are "durable": the controller persists their status before invoking adapters. A timeout doesn't prove success or failure; you reconcile the actual resource state.

### Standard Operations

| Operation | Description |
|-----------|-------------|
| `server.create` | Provision a new server instance |
| `server.start` | Start a running game process |
| `server.stop` | Stop gracefully and save |
| `server.backup` | Upload backup to storage |
| `server.destroy` | Destroy after backup verification |
| `lifecycle.check_idle` | Evaluate idle shutdown eligibility |

---

## API Endpoints

### Server Management

#### Create Server

```http
POST /v1/servers
Content-Type: application/json

{
  "server": {
    "id": "my-server",
    "definition": {...},
    "operations": [
      {
        "operation": "server.create",
        "parameters": {...}
      }
    ]
  }
}
```

#### Get Server Status

```http
GET /v1/servers/{server-id}
```

Returns current state, last operation timestamp, and backup status.

---

## Durable Operation Semantics

Operations are idempotent and recoverable:

- **Idempotent**: Repeated calls with same parameters have same effect
- **Recoverable**: Failed operations can be retried from checkpoint
- **Auditable**: Every operation has timestamp, deadline, and outcome

### Timeout Handling

A timeout during an operation doesn't indicate success or failure. The controller reconciles the actual resource state:

1. Check provider response
2. If uncertain, reconcile (e.g., verify VM exists)
3. Retry if necessary
4. Mark as failed only after verification

---

## Versioning

The API uses semantic versioning in the path:

- `/v1/servers` - Current stable version
- `/v2/...` - Future versions (backward compatible)

Clients should always use the latest v1 unless pinned to a specific version.

---

*Last updated: November 2026*