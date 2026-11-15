# Operator Quick Reference: Start, Stop, Save, and Drain

This page is the fast reference for the everyday operations a server operator
runs against the controller: starting a server, stopping it, saving game data,
and draining a backend from the proxy. It describes **what the operator does,
how it flows through the system, and where each piece lives**. It is a
reference for the operator; the authoritative rules are the linked protocol
contracts, and this page must never contradict them.

Read this alongside:

- [Standard operation catalog](../protocol/operation-catalog.md) — the exact
  operation types, parameters, and postconditions.
- [Durable operation model](../protocol/operations.md) — how an accepted
  request becomes a durable, retryable operation.
- [Management API overview](../protocol/management-api/overview.md) — the
  routes an operator (or an operator-facing client) calls.
- [Proxy bridge API](PROXY_BRIDGE_API.md) — the Velocity-side routes,
  including `drain`.

## 1. Where the operations live (read this first)

Start, stop, save, and drain do **not** all live in one place. They route
through different parts of the system. Confusing these is the most common
operator mistake.

| Operator action | Standard operation | Handled by | Velocity plugin route? |
|---|---|---|---|
| Start the server | `server.start` | Execution adapter | No |
| Stop the server | `server.stop` | Execution adapter + game adapter (save) + proxy integration (drain) | No |
| Save game data | *(part of `server.stop`)* or `server.backup` | Game adapter | No |
| Drain the backend | *(part of `server.stop`)* or manual | Proxy integration / proxy bridge | **Yes** |

Key points:

- **There is no standalone `save` standard operation.** A save/flush is
  performed as a step inside `server.stop` (the game-native save request) and
  again as the consistency capture inside `server.backup`. An operator never
  calls a bare `save` route.
- **`server.stop` is the one operation that does several things at once.**
  When its proxy integration is configured, it drains new-player routing,
  requests the game-native save/flush, gracefully stops the execution unit,
  and confirms termination. That is why a stop both "saves" and "drains" as
  side effects.
- **The Velocity plugin only exposes proxy-bridge routes.** It implements
  registration, drain, session inspection, and session transfer for the proxy.
  It does **not** implement start, stop, or save. If you are looking for a
  `POST /v1/.../start` style route on the plugin, it does not exist and never
  will — start/stop/save are controller operations routed to the execution and
  game adapters.

## 2. How the operator invokes an operation

Every one of these operations is requested as a **durable operation** through
the management API, not as a direct adapter call:

```
POST /v1/servers/{server_id}/operations
```

with the operation type and its type-specific parameters in the body, plus a
required `Idempotency-Key` header. The controller validates the parameters,
checks authorization and environment scope, acquires the per-server lock, and
persists the durable operation **before** any external side effect. The
operator then reads the outcome with:

```
GET /v1/operations/{operation_id}
```

Polling the operation resource is authoritative; the optional
`GET /v1/operations/{operation_id}/events` stream is informational.

What the operator actually needs to know for each operation is the **type**
and the **parameters**. Everything else (persistence, lock, retry,
reconciliation, audit) is handled by the controller.

> Note on current wiring: the start/stop workflows are implemented and
> fake-adapter-tested, and the management runtime currently serves operation
> inspection. The remaining management routes (including the submit route for
> every operation) and the live adapter wiring are tracked in the
> [roadmap](ROADMAP.md). The operation *names* and *semantics* below are the
> protocol contract and will not change.

## 3. Start — `server.start`

**What it does:** starts the current recorded execution unit on the current
recorded instance and validates that the game is ready.

- **Handled by:** the execution adapter (the unit-management step is
  `execution.unit.start`), then the game adapter's readiness checks.
- **Prerequisite:** the logical server exists and has a recorded instance and
  execution unit (i.e. it was defined and provisioned, or restored). Starting
  an `unprovisioned` server fails.
- **Postcondition (must hold before the operation is `succeeded`):** the
  current runtime is ready and the required checks passed; server state is
  `ready`.
- **Idempotent start:** a repeated start request may return success without
  starting a duplicate process when inspection confirms the runtime is already
  ready.

Operator flow: submit `server.start` for the server, then poll the operation
until it reaches a terminal status.

## 4. Stop — `server.stop`

**What it does:** drains new-player routing (when the configured proxy
integration supports it), requests the game-native save/flush, gracefully
stops the execution unit, and confirms termination.

- **Handled by:** proxy integration (drain) → game adapter (save/flush,
  `game.save.request`) → execution adapter (graceful stop,
  `execution.unit.graceful_stop`).
- **Default parameters:** `drain=true` and `force=false` when omitted. Force
  stop is **not** the default; when supported it needs a separate capability,
  permission, an explicit `force=true`, and a valid `approval_id` (the schema
  rejects `force=true` without an approval reference).
- **Prerequisite:** the server has a running or recorded execution unit.
- **Postcondition (must hold before `succeeded`):** the runtime is confirmed
  stopped and the save/flush result is recorded; server state is `stopped`.
- **It does not destroy anything.** A successful stop leaves the server
  `stopped`; it does not destroy the VM or delete data.

Operator flow: submit `server.stop` (defaults are safe for routine shutdowns),
then poll the operation. If you intentionally want a hard stop, that requires
the force path and approval — treat it as an incident action, not a routine
one.

**This is the operation that "saves" and "drains" for you.** Most operators do
not need to trigger save or drain separately; a normal `server.stop` already
drains the backend and saves the world before stopping.

## 5. Save — there is no standalone save

**Save is not a top-level standard operation.** It happens as a step inside
other operations:

- **Inside `server.stop`:** the game-native save/flush
  (`game.save.request`) runs before the execution unit is stopped. Use a
  normal stop when the goal is "save and shut down cleanly."
- **Inside `server.backup`:** a backup captures a *consistent* snapshot using
  the game adapter's declared consistency mode, which for a live game means
  quiescing/preparing the data so the capture is safe. Use `server.backup`
  when the goal is "persist a restorable copy without stopping the server."

So: if you tell yourself "I want to save," decide *why*:

- **Save and stop the server** → run `server.stop` (it saves, then stops).
- **Save as a restorable backup without stopping** → run `server.backup`.
- **Just flush the game's in-memory data and keep playing** → there is no
  standalone operation for that today; the controller's save step is always
  paired with a stop or a backup capture. Do not assume a bare save route
  exists.

## 6. Drain — `server.stop`'s drain step, or the proxy bridge route

**Drain** stops new players being routed to a backend while leaving connected
players in place. It is the only one of these actions with a direct
operator-facing route, and it lives on the **proxy bridge**, not on the
controller's operation catalog.

Two ways an operator drains:

1. **Implicitly, through `server.stop`.** When the configured proxy
   integration supports draining, `server.stop` drains new-player routing as
   its first step. This is the normal path for a planned shutdown.
2. **Directly, against the proxy bridge**, when you want to drain a backend
   without stopping the server (e.g. maintenance that should stop new players
   but keep the world running):

   ```
   POST /v1/backends/{backend_id}/drain
   ```

   This replies `200` with the backend state (`draining`, not routable). A
   backend that is not registered replies `404 backend_not_found`.

After a drain, confirm the backend state and that no new sessions are arriving:

```
GET /v1/backends/{backend_id}
```

`registration` will read `draining`. Connected sessions keep working; new ones
go to the fallback. To stop the server after a manual drain, run `server.stop`
(as configured it will attempt the drain again — a backend already draining
simply stays draining).

The full set of proxy-bridge routes (`GET/PUT/DELETE /v1/backends/...`,
`POST .../drain`, session inspect/transfer, `GET /v1/info`) is in the
[proxy bridge API](PROXY_BRIDGE_API.md).

## 7. Operator cheat sheet

| I want to... | Do this | Type / route | Ends with |
|---|---|---|---|
| Start the server | Submit the start operation | `server.start` | state `ready` |
| Stop and save the server (normal) | Submit the stop operation (defaults) | `server.stop` | state `stopped`, save recorded |
| Hard-stop with approval | Submit stop with `force=true` + `approval_id` | `server.stop` | state `stopped` |
| Persist a restorable copy without stopping | Submit the backup operation | `server.backup` | verified backup, per resume policy |
| Stop new players, keep the world running | Hit the proxy bridge drain route | `POST /v1/backends/{backend_id}/drain` | backend `draining` |
| Check any operation's outcome | Poll the operation | `GET /v1/operations/{operation_id}` | terminal status |

## 8. Cross-references

- Operation types, parameters, and postconditions:
  [operation catalog](../protocol/operation-catalog.md).
- Execution capabilities (`execution.unit.start`, `execution.unit.graceful_stop`):
  [execution adapter contract](../protocol/adapters/execution.md).
- Game capabilities (`game.save.request`, readiness, backup prepare/capture):
  [game adapter contract](../protocol/adapters/games.md).
- Proxy bridge routes, including `drain`:
  [proxy bridge API](PROXY_BRIDGE_API.md).
- Durable operation acceptance, retry, cancellation, reconciliation:
  [operations protocol](../protocol/operations.md).
- Management API routes and idempotency:
  [management API overview](../protocol/management-api/overview.md).
