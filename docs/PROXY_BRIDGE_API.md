# Proxy bridge API, version 1

**Status:** Draft. Implemented by the Velocity plugin in
[`plugins/velocity`](../plugins/velocity) and the Go client in
`internal/velocityproxy`. Joining and routing rules follow
[the integration contract](../protocol/adapters/integrations.md#31-join-routing-and-fallback).

The controller's proxy integration adapter does not talk to Velocity's
internals. It talks to a small HTTP API served by a plugin running inside the
proxy. This document defines that API so it can be reused: another proxy
(BungeeCord, Gate, a custom proxy) can implement the same API and work with
the same Go adapter, and other tools can use the plugin without the
controller.

The bridge API is separate from the controller's adapter protocol in
[`protocol/`](../protocol). The adapter enforces the protocol rules
(readiness assertions, exact-instance binding, per-session outcomes). The
bridge only performs the proxy actions and reports what it observes.

## Transport and authentication

- HTTP/1.1 with JSON bodies (`Content-Type: application/json`, UTF-8).
- Every request carries `Authorization: Bearer <token>`. The token is read
  from a file on the proxy host and from a systemd credential on the
  controller host. It is never passed on a command line or logged.
- Version 1 listens on a loopback address only (the controller and proxy run
  on the same host). Clients refuse plain HTTP to a non-loopback host. A
  later version can add TLS for remote proxies.
- Responses are at most 1 MiB. Clients use short timeouts and treat a
  timeout on a mutating call as an unknown outcome.

## Identifiers

| Value | Format |
|---|---|
| `backend_id` | Proxy server name: `^[a-z0-9][a-z0-9._-]{0,63}$` |
| `session_id` | Opaque, random, per connection. Not a Minecraft account UUID. |
| `cohort_id` | `^[a-z0-9][a-z0-9._-]{0,127}$` |
| `address` | `host:port`, where host is an IP literal or DNS hostname |

Session IDs must not be derived from player identity. The bridge keeps a
disconnected session for at least 10 minutes so the controller can see
`disconnected` instead of losing track of it. Bridges must not return player
names, account UUIDs, IP addresses, or chat in any response, and must not log
session IDs at ordinary log levels.

## Managed backends

The bridge only changes backends it manages: ones registered through this
API, plus any listed as managed in its configuration. Requests that would
change a backend configured statically by the operator (for example the
lobby) fail with `409 backend_not_managed`.

A managed backend's `routable` field is true only when it is registered, not
draining, and the proxy's own health check reached it recently. The bridge
never makes a backend routable that the controller has not registered.

## Endpoints

### `GET /v1/info`

```json
{
  "api_version": "1",
  "proxy_platform": "velocity",
  "proxy_version": "3.4.0",
  "proxy_instance_id": "velocity-edge-001",
  "features": ["backends", "sessions", "transfer", "cohorts", "join_routing", "auto_move"]
}
```

Clients check `api_version` and `proxy_instance_id` before other calls. `auto_move` is present when the plugin moves waiting cohorts by itself (see [Automatic moves](#automatic-moves)); a controller that sees it does not also transfer those cohorts.

### `GET /v1/backends/{backend_id}`

Returns the backend state. An unknown backend is reported as
`unregistered`, not as an error.

```json
{
  "backend_id": "paper-main",
  "registration": "registered",
  "instance_ref": {"adapter_id": "hosting.vultr", "environment_id": "production", "resource_kind": "instance", "external_id": "..."},
  "address": "203.0.113.20:25565",
  "routable": true,
  "active_sessions": 3,
  "observed_at": "2026-11-07T10:00:00Z"
}
```

`registration` is `registered`, `draining`, or `unregistered`.
`instance_ref` and `address` are `null` when unregistered.

### `PUT /v1/backends/{backend_id}`

Registers the backend, or replaces its instance and address. The bridge
health-checks the address before replying, so a successful reply usually
already shows `routable`.

```json
{"instance_ref": {"adapter_id": "hosting.vultr", "...": "..."}, "address": "203.0.113.20:25565"}
```

Replies `200` with the backend state.

### `POST /v1/backends/{backend_id}/drain`

Stops new players being routed to the backend. Connected players stay.
Replies `200` with the backend state (`draining`, not routable). A backend that is not registered replies `404 backend_not_found`.

### `DELETE /v1/backends/{backend_id}`

Removes the backend from the proxy. Players still connected are moved to the
fallback server by the proxy's normal handling. Replies `200` with the state
(`unregistered`).

### `GET /v1/sessions/{session_id}`

```json
{
  "session_id": "3f1c9e2a7b6d4e10a8c5f2e9d7b1a6c4",
  "state": "connected",
  "backend_id": "lobby",
  "waiting_cohort_id": "waiting-paper-main",
  "observed_at": "2026-11-07T10:00:00Z"
}
```

`state` is `connected` or `disconnected`. `backend_id` and
`waiting_cohort_id` are omitted when they do not apply. An unknown or expired
session ID replies `404 session_not_found`.

### `POST /v1/sessions/{session_id}/transfer`

```json
{"destination_backend_id": "paper-main"}
```

Moves one connected session and waits for the proxy's result:

```json
{"outcome": "transferred"}
```

`outcome` is `transferred`, `not_attempted` (the bridge refused before
contacting the backend, for example because the destination is not routable
or the session is not connected), or `failed` (the proxy tried and the
player stayed where they were). A transport error or timeout is an unknown
outcome; inspect the session before retrying.

### `GET /v1/cohorts/{cohort_id}`

Lists the sessions currently waiting in a cohort.

```json
{"cohort_id": "waiting-paper-main", "session_ids": ["..."], "observed_at": "2026-11-07T10:00:00Z"}
```

An empty or unknown cohort replies with an empty list.

## Join routing and waiting cohorts

Join routing is plugin configuration, not part of the controller protocol,
so each proxy owner decides it. The Velocity plugin supports:

- **Join backends:** an ordered list of managed backends. A new player is
  sent to the first one that is routable.
- **Forced hosts:** a per-hostname list that replaces the join list for
  players who connect through that hostname.
- **Fallback:** where players go when no join backend is routable (normally
  the lobby). Those players are added to the waiting cohort
  `waiting-<first join backend>`, so the controller can move that cohort
  once the backend is ready.
- **Fallback on failure:** a player kicked from a managed backend because it
  went down is sent to the fallback and added to the waiting cohort instead
  of being disconnected.

A player leaves a waiting cohort when they are transferred, move server, or
disconnect.

## Automatic moves

When `auto_move` is enabled, the plugin moves each backend's waiting cohort to that backend by itself, following [integrations section 3.3](../protocol/adapters/integrations.md#33-automatic-moves-from-a-waiting-cohort):

- Only while the controller has the backend registered and it passes the health check. Moves stop as soon as it is draining, unregistered, or unhealthy.
- Only connected cohort members who are still on the fallback. Players who chose the fallback themselves are not in a cohort and are not moved.
- `auto-move-batch` players (default 5) per backend every `auto-move-interval-seconds` (default 5), longest waiting first. A failed move is retried after 30 seconds.

Each move changes the session's `backend_id` and removes it from its cohort, as `GET /v1/sessions/{id}` and `GET /v1/cohorts/{id}` show.

## Errors

Errors reply with a 4xx or 5xx status and:

```json
{"error": {"code": "backend_not_managed", "message": "Backend is configured statically."}}
```

| Status | Meaning for mutating calls |
|---|---|
| `400`, `401`, `403`, `404`, `409`, `422` | Refused before any change. Safe to report as not attempted. |
| `5xx`, timeout, connection error | Outcome unknown. Inspect before retrying. |

Error messages never include session IDs or player data.

## Versioning

New optional fields and endpoints can be added within version 1; clients
ignore fields they do not know. Removing or changing a field's meaning
requires `/v2`. Features a bridge supports are listed in `GET /v1/info`.
