# Integration Adapter Contract

**Status:** Draft, version 0.21.0-draft.

## 1. Responsibility

Integration adapters connect the controller to proxies, DNS providers, monitoring, notification services, and other external systems. They do not own server lifecycle state or bypass core routing/safety policy.

The integration category describes what an adapter is responsible for, not whether a deployment needs it. A server's network profile decides which integration capabilities are required (section 6). For example, a proxied Minecraft server requires proxy capabilities and may use DNS only for backend addresses, while a game without a proxy requires DNS capabilities for its join hostname.

## 2. Capability examples

| Capability | Meaning |
|---|---|
| `integration.proxy.backend.inspect` | Inspect a named backend |
| `integration.proxy.backend.register` | Register a confirmed-ready backend |
| `integration.proxy.backend.drain` | Stop new sessions/players reaching a backend |
| `integration.proxy.backend.unregister` | Remove a backend from routing |
| `integration.proxy.backend.players.inspect` | Inspect active player/session count where supported |
| `integration.proxy.cohort.inspect` | List the opaque sessions waiting in one proxy-defined waiting cohort |
| `integration.proxy.session.inspect` | Inspect one proxy-scoped session's connection state and current backend |
| `integration.velocity.players.transfer` | Transfer an explicit waiting cohort to one confirmed-ready Velocity backend |
| `integration.dns.record.inspect` | Inspect one managed DNS record's authoritative values and TTL |
| `integration.dns.record.upsert` | Set one managed DNS record to explicit values for a declared purpose |
| `integration.dns.record.delete` | Remove one managed DNS record |
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

### 3.1 Join routing and fallback

Registration is the core's routing permission. A proxy MAY send new connections to a backend on its own, for example as the default destination for players who join, only while that backend is registered and routable for the exact instance the core registered. The proxy MUST stop sending new connections to a backend as soon as it is draining, unregistered, or failing the proxy's own health check, and MUST NOT make a backend routable that the core has not registered. A proxy health check can only remove routability; it never grants it. Join-routing preferences (which registered backends new players prefer, and per-hostname preferences) are proxy configuration owned by the operator, not protocol messages.

A proxy MAY send a player to a configured fallback backend (normally an always-on lobby) when no preferred backend is routable, or when the player's backend fails. The fallback MUST be a backend the operator configured, not a backend inferred from player activity.

### 3.2 Waiting cohorts

A proxy MAY place players who are waiting for a backend into a proxy-defined waiting cohort, such as everyone sent to the fallback because the preferred backend was unavailable. The `integration.proxy.cohort.inspect` capability lists the opaque session IDs in one cohort on an exact proxy instance; its contracts are [proxy-cohort-inspect-input](../schemas/integrations/proxy-cohort-inspect-input.schema.json) and [proxy-cohort-inspect-output](../schemas/integrations/proxy-cohort-inspect-output.schema.json).

The request names the proxy instance, the cohort, `maximum_sessions`, and `maximum_observation_age_seconds`. An `observed` result includes `session_count` (the total), `player_session_ids` (at most `maximum_sessions` unique IDs), `truncated`, and `observed_at`; an `unknown` result includes none of them. The core may use a membership list only when the response names the requested proxy instance and cohort, the timestamp is not in the future and is within the requested maximum age, the list is no longer than `maximum_sessions` or `session_count`, and `truncated` is true exactly when the list is shorter than `session_count`. Membership is a selection hint: it never authorizes a core-initiated transfer by itself, and the transfer capability still verifies each session's cohort and source backend at the proxy. The proxy's own automatic moves are covered in section 3.3. Results contain opaque proxy-scoped session IDs only, never player names, account UUIDs, or IP addresses.

### 3.3 Automatic moves from a waiting cohort

A proxy MAY move the members of a backend's waiting cohort to that backend by itself, without a core-initiated transfer. The core's registration is the permission: a proxy that does this MUST follow these rules.

- **Only while routable.** It moves players to a backend only while that backend is registered and routable for the exact instance the core registered, as in section 3.1. It stops as soon as the backend is draining, unregistered, or failing the proxy's health check. A health check can stop moves but never start them.
- **Only waiting players.** It moves only connected members of that backend's waiting cohort who are still on the fallback. A player who chose the fallback themselves, for example by leaving a backend for the lobby, is not in a waiting cohort and is not moved. Players waiting for a different backend stay where they are.
- **Paced.** It moves at most `batch_size` players per `interval_seconds`, oldest waiting first. The default is 5 players every 5 seconds. A player whose move failed is not retried for `retry_after_seconds` (default 30). A player may still move sooner on their own, for example with the proxy's `/server` command.
- **Observable.** Each move changes the session's backend and removes it from the cohort, which session inspect and cohort inspect report. The proxy MUST NOT report automatic moves as results of a core-initiated transfer.

Automatic moves do not replace the transfer capability: the core still uses it for moves it chooses, such as moving players between two running backends during a migration handover. A proxy that supports automatic moves says so in its bridge or descriptor metadata, so the core does not also transfer the same cohort.

The selection rules are checked by [proxy-cohort-automatic-move.json](../conformance/test-vectors/proxy-cohort-automatic-move.json).

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

## 5. DNS records

DNS integration adapters manage individual A and AAAA records in DNS zones they are configured to own. The contracts are [inspect](../schemas/integrations/dns-record-inspect-input.schema.json), [upsert](../schemas/integrations/dns-record-upsert-input.schema.json), and [delete](../schemas/integrations/dns-record-delete-input.schema.json), each with an output schema, and the shared [record target](../schemas/integrations/dns-record-target.schema.json). The illustrative [Cloudflare DNS descriptor](../examples/adapter-descriptor.cloudflare-dns.json) matches the initial Cloudflare adapter.

A DNS adapter MUST:
- Change only records explicitly configured as managed for the environment, and refuse any other name in the zone.
- Reject requests whose zone, instance, or readiness assertion belongs to a different environment.
- Report the provider's authoritative record state. That state does not describe resolver caches: resolvers that cached an earlier answer can keep using it until its TTL expires.
- Distinguish accepted changes from confirmed record state. Confirmation requires an observation of the exact record with exactly the requested values and TTL.
- Honor `expected_values` when present: if the record's current values differ (an empty array means the record must be absent), reject with `current_values_mismatch` and change nothing.
- Avoid logging player data; DNS requests carry none.

Every upsert declares a `purpose`:

| Purpose | Points at | Additional requirement |
|---|---|---|
| `backend_address` | The exact instance named by `instance_ref` | None. Only the proxy uses the name to reach a backend, and the proxy still routes players only to registered, routable backends. |
| `direct_join` | The exact instance named by `instance_ref` | A current readiness assertion bound to the same server, environment, and instance. Players connect straight to this name, so it MUST NOT point at an instance before readiness. |
| `fallback` | An operator-configured always-on endpoint, such as a lobby | The values MUST equal the fallback values configured for that record. No instance or readiness assertion is allowed. |

An adapter MUST reject a `direct_join` upsert with an expired, not-yet-valid, or mismatched readiness assertion before contacting the provider. Direct-join records SHOULD use a 60-second TTL; see the [direct-join wake flow](../../docs/NO_PROXY_DNS_HANDOFF.md) for cutover timing. An accepted or unknown upsert does not prove the record changed: inspect the exact record before retrying or telling players to reconnect. A confirmed delete reports `absent`.

## 6. Network profile requirements

A server's network profile (`network_profile` in the [server definition](../schemas/operations/server-definition.schema.json)) declares how players reach the server and which integration capabilities that requires:

| Join mode | Required capabilities |
|---|---|
| `proxy` | `integration.proxy.backend.inspect`, `.register`, `.drain`, `.unregister`, and, when players wait in a cohort, `integration.proxy.cohort.inspect`, `integration.proxy.session.inspect`, and a transfer capability such as `integration.velocity.players.transfer` |
| `direct_dns` | `integration.dns.record.inspect`, `.upsert`, `.delete` for the join hostname (`direct_join` and `fallback` purposes) |
| `none` | None; the operator gives players the address directly |

Independently of the join mode, a profile MAY give each backend a managed `backend_address` hostname, either per backend or from one naming scheme for all backends. The proxy then reaches the backend by name, so an instance IP change updates DNS instead of the proxy configuration. Such a profile requires the DNS record capabilities as well. Each backend hostname MUST belong to exactly one backend: two backends MUST NOT share a hostname, so one backend's DNS update cannot redirect another backend's traffic.

The structured profile is described by the [network-profile schema](../schemas/integrations/network-profile.schema.json); `network_profile` in a server definition names one. A profile that waits players in a cohort is treated as always doing so in `proxy` mode, so the cohort, session, and transfer capabilities are required there.

The controller MUST resolve a server's network profile before planning operations for it, and MUST reject planning when a required capability is not advertised by an adapter registered for that environment. It MUST NOT skip a required routing or DNS step because the adapter is missing, and MUST NOT emulate a missing capability. A profile that requires no integration capabilities MUST NOT cause the controller to call one.

## 7. Monitoring and notifications

Monitoring/notification adapters receive only explicitly permitted event fields and destinations. They MUST declare data transmitted, retention where known, and privacy implications. Optional player telemetry remains disabled by default and requires the separate consent rules in the security specification.

### 7.1 Proxy availability

The controller probes each proxy integration it runs. A probe checks only availability and identity (for Velocity, the bridge's `GET /v1/info`); it MUST NOT change proxy state. A probe result is `up`, `unreachable`, or `identity_mismatch` (the proxy answers, but with a different proxy instance or an unsupported API version).

- **Outage:** an outage is declared after `failure_threshold` consecutive failed probes; a failed check at startup counts as the first. Declaring it emits one event for its reason. If the reason changes during the outage, the controller emits one event for the new reason.
- **Recovery:** the first `up` after a declared outage emits `integration.proxy.recovered`. A failure streak shorter than the threshold ends silently, so one dropped probe does not page anyone.
- **Probe spacing:** while up, probes run every `check_interval`. After the n-th consecutive failure the next probe waits `initial_backoff · 2^(n-1)`, capped at `max_backoff`. Defaults are 1 minute, 2 failures, 30 seconds, and 5 minutes.
- **Proxy calls:** any failed probe makes the controller verify the proxy identity again before its next proxy call. Calls made while the identity is unverified fail without acting.
- **Event identity:** `event_id` is `evt-` followed by the first 16 bytes, in lowercase hex, of SHA-256 over the adapter ID, event type, `outage_started_at`, and `occurred_at`, joined by NUL bytes. Timestamps are UTC RFC 3339.

Events use [notification-event.schema.json](../schemas/integrations/notification-event.schema.json). The controller also writes every event as one line to its own log.

### 7.2 Notification delivery

A notifier adapter advertises `integration.notification.send` with [notification-send-input](../schemas/integrations/notification-send-input.schema.json) and [notification-send-output](../schemas/integrations/notification-send-output.schema.json). Each notifier is bound to one environment and receives only events for that environment, optionally filtered by event type.

A notification changes no managed resource. The capability is therefore declared with `mutating: false`, and it runs outside durable operations. Delivery is at least once:

- `delivered` and `rejected` are final. `rejected` means a retry would fail the same way, for example a refused request, bad credentials, or a destination that breaks the transport rules.
- `outcome_unknown` is retried with the same event after 30 seconds, doubling, for at most 5 attempts. A response that is schema-invalid or names another event counts as `outcome_unknown`. Receivers can drop duplicates by `event_id`.
- Delivery state is kept in memory only. A controller restart drops undelivered events, and a restart during an outage reports that outage again once the threshold is reached after startup.

**Data sent:** exactly the notification-event fields. These are the event type and severity, the environment, adapter, and proxy instance IDs, the outage start, the time the event occurred, the failure count, and a fixed-template summary. Events MUST NOT contain player names, account IDs, session IDs, IP addresses, chat, raw error text, URLs, or credentials, and the schema allows no other fields. Each destination keeps what it receives under its own retention policy, which the controller does not control. Webhook URLs, SMTP passwords, and ntfy tokens are secrets that come from systemd credentials.

## 8. Conformance cases

Test registration before readiness, drain/unregister outcome uncertainty, player-count staleness, target mismatch, expired or wrong-instance readiness assertions, sessions outside the waiting cohort, disconnected sessions, partial batches, unknown transfer outcomes, reconciliation-before-retry, waiting-cohort freshness and binding, direct-join DNS changes before readiness, unconfirmed or mismatched DNS values, and privacy/redaction boundaries.
