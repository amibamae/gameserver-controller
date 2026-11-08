# Integration Adapter Contract

**Status:** Draft, version 0.21.0-draft.

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
| `integration.proxy.session.inspect` | Inspect one proxy-scoped session's connection state and current backend |
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

The core determines which server may be routed. The proxy adapter performs the requested routing action and reports the result. A proxy integration MUST NOT autonomously route to a server in a transitional, degraded, or failed state. The initial backend lifecycle schemas are [inspect](../schemas/integrations/proxy-backend-inspect-input.schema.json), [register](../schemas/integrations/proxy-backend-register-input.schema.json), [drain](../schemas/integrations/proxy-backend-drain-input.schema.json), and [unregister](../schemas/integrations/proxy-backend-unregister-input.schema.json). An accepted or unknown action is not proof of routability; only an observed state bound to the exact backend and instance may be used for routing. A confirmed drain reports `draining` and `not_routable`; a confirmed unregister reports `unregistered` and `not_routable`. Confirmation requires a schema-valid response bound to the requested backend and instance, with `request_outcome` set to `confirmed` and the exact postcondition. Before retrying an unconfirmed drain or unregister, inspect the exact requested backend. This includes accepted, unknown, rejected, schema-invalid, contradictory, and mismatched-target responses.

An inspect result permits routing only when it matches the requested backend and instance, the instance belongs to the requested environment, and the result says `registered` and `routable`. A draining or unregistered backend cannot be routable; unknown state never permits routing.

The `integration.proxy.backend.players.inspect` capability reports only a count for the exact requested backend and instance. The request sets `maximum_age_seconds`. An `observed` result includes both `active_session_count` and `observed_at`; an `unknown` result includes neither. The core may use a count only when the response target matches the request, the timestamp is not in the future, and the timestamp is no older than the requested maximum age at evaluation time. Unknown, stale, future-dated, incomplete, or mismatched observations cannot prove zero sessions or confirm drain completion. Counts and diagnostics MUST NOT include player identities or session identifiers.

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

An `outcome_unknown` result means the transfer may have occurred. Before retrying, the controller MUST invoke `integration.proxy.session.inspect` for that exact proxy instance and session, then validate the response binding and freshness against `maximum_observation_age_seconds`. Only a fresh result showing the session still connected to the requested source backend permits another transfer attempt, and the retry MUST use a current readiness assertion for the same destination instance. A fresh result showing the destination backend resolves the session without a retry. Disconnected, unknown, stale, future-dated, mismatched, or other-backend observations MUST NOT authorize a retry. The adapter SHOULD use native Velocity mechanisms (for example, the `/send` command) internally where appropriate, but those mechanisms are implementation details, not protocol semantics.

The session-inspect capability is read-only, bound to one proxy instance and opaque session ID, and returns only connection state, current backend when connected, and observation time. Adapters MUST NOT include player names, account UUIDs, IP addresses, chat, or unrelated activity in this result. Session IDs SHOULD remain short-lived and MUST be redacted from ordinary logs.

The protocol uses opaque, proxy-scoped session identifiers, not Minecraft account UUIDs or player profiles. Adapters MUST avoid exposing names, UUIDs, IP addresses, chat, or unrelated player activity in operation results and logs. Session identifiers SHOULD be short-lived and MUST be redacted from ordinary diagnostic output.

The initial batch limit is 100 sessions per invocation. A controller MAY split a larger cohort into multiple operation steps while preserving one durable operation and one result per session.

## 5. Monitoring and notifications

Monitoring/notification adapters receive only explicitly permitted event fields and destinations. They MUST declare data transmitted, retention where known, and privacy implications. Optional player telemetry remains disabled by default and requires the separate consent rules in the security specification.

## 6. Conformance cases

Test registration before readiness, drain/unregister outcome uncertainty, player-count staleness, target mismatch, expired or wrong-instance readiness assertions, sessions outside the waiting cohort, disconnected sessions, partial batches, unknown transfer outcomes, reconciliation-before-retry, and privacy/redaction boundaries.
