# Integration Adapter Contract

**Status:** Draft, version 0.2.0-draft.

## 1. Responsibility

Integration adapters connect the controller to proxies, monitoring, notification services, and other external systems. They do not own server lifecycle state or bypass core routing/safety policy.

## 2. Capability examples

| Capability | Meaning |
|---|---|
| `integration.proxy.backend.inspect` | Inspect a named backend |
| `integration.proxy.backend.register` | Register a confirmed-ready backend |
| `integration.proxy.backend.drain` | Stop new sessions/players reaching a backend |
| `integration.proxy.backend.unregister` | Remove a backend from routing |
| `integration.proxy.backend.players.inspect` | Inspect active player/session count where supported |
| `integration.velocity.players.transfer` | Transfer an explicit waiting cohort to one confirmed-ready Velocity backend |
| `integration.monitoring.event.publish` | Publish an allowed operational event |
| `integration.notification.send` | Send an authorized notification |

## 3. Proxy behavior

A proxy integration MUST:
- Identify the proxy instance and configured backend name.
- Report whether a backend is registered and routable.
- Refuse registration unless the core supplies a current readiness assertion for the exact logical server and instance.
- Drain before planned shutdown when the integration supports it.
- Report player/session counts with observation timestamps when available.
- Distinguish accepted registration/removal from confirmed routing state.
- Avoid logging player identifiers unless necessary and authorized.

The core determines which server may be routed. The proxy adapter performs the requested routing action and reports the result. A proxy integration MUST NOT autonomously route to a server in a transitional, degraded, or failed state.

## 4. Velocity player transfer

The initial Velocity transfer capability is `integration.velocity.players.transfer`. Its input and output contracts are defined by [velocity-player-transfer-request.schema.json](../schemas/integrations/velocity-player-transfer-request.schema.json) and [velocity-player-transfer-result.schema.json](../schemas/integrations/velocity-player-transfer-result.schema.json). The illustrative [Velocity adapter descriptor](../examples/adapter-descriptor.velocity.json) advertises the capability with those registered schema IDs.

The controller MUST select the exact `waiting_cohort_id` and explicit list of proxy-scoped `player_session_ids`. The adapter MUST verify that each selected session still belongs to that cohort and is still connected to the declared source backend. It MUST NOT infer that every player on the source backend should be moved.

Before invoking the capability, the core MUST confirm that:
- The logical server is still in `ready` state.
- The supplied [readiness assertion](../schemas/lifecycle/readiness-assertion.schema.json) is current, unexpired, and bound to the exact server, environment, and instance reference being targeted.
- The destination backend is registered and routable for that exact instance.
- Source and destination backend IDs match the controller's current proxy registration state.
- The selected sessions remain members of the named waiting cohort.

The adapter MUST reject stale or mismatched readiness assertions and MUST NOT transfer a session to a different instance than the asserted target. The readiness assertion is evidence supplied by the controller; the adapter does not independently change lifecycle state or grant readiness.

The result MUST contain one explicit outcome for every requested proxy session. Outcomes are `transferred`, `already_at_destination`, `disconnected`, `rejected`, `failed`, or `outcome_unknown`. The result array MAY be in any order; consumers MUST match outcomes by `proxy_session_id`. The adapter and controller MUST validate that session IDs are unique and exactly match the request, `requested_count` equals the request size, and aggregate counts equal the per-session outcomes. These cross-field invariants require behavioral validation in addition to JSON Schema validation. A partial batch result is valid and MUST NOT be collapsed into one success/failure boolean.

An `outcome_unknown` result means the transfer may have occurred. The controller MUST inspect the proxy's current backend for that session before retrying; it MUST NOT blindly repeat the transfer. The adapter SHOULD use native Velocity mechanisms (for example, the `/send` command) internally where appropriate, but those mechanisms are implementation details, not protocol semantics.

The protocol uses opaque, proxy-scoped session identifiers, not Minecraft account UUIDs or player profiles. Adapters MUST avoid exposing names, UUIDs, IP addresses, chat, or unrelated player activity in operation results and logs. Session identifiers SHOULD be short-lived and MUST be redacted from ordinary diagnostic output.

The initial batch limit is 100 sessions per invocation. A controller MAY split a larger cohort into multiple operation steps while preserving one durable operation and one result per session.

## 5. Monitoring and notifications

Monitoring/notification adapters receive only explicitly permitted event fields and destinations. They MUST declare data transmitted, retention where known, and privacy implications. Optional player telemetry remains disabled by default and requires the separate consent rules in the security specification.

## 6. Conformance cases

Test registration before readiness, drain/unregister outcome uncertainty, player-count staleness, target mismatch, expired or wrong-instance readiness assertions, sessions outside the waiting cohort, disconnected sessions, partial batches, unknown transfer outcomes, reconciliation-before-retry, and privacy/redaction boundaries.
