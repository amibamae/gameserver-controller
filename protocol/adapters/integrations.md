# Integration Adapter Contract

**Status:** Draft, version 0.1.0-draft.

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

## 4. Monitoring and notifications

Monitoring/notification adapters receive only explicitly permitted event fields and destinations. They MUST declare data transmitted, retention where known, and privacy implications. Optional player telemetry remains disabled by default and requires the separate consent rules in the security specification.

## 5. Conformance cases

Test registration before readiness, drain/unregister outcome uncertainty, player-count staleness, target mismatch, retry behavior, and privacy/redaction boundaries.
