# Vultr hosting adapter status

**Status:** read-only. `implementation: vultr` builds the Vultr hosting adapter in [`internal/vultrhosting`](../internal/vultrhosting/README.md). It can inspect instances but cannot create, change, or destroy them.

An earlier version of this page described a complete adapter with create and start capabilities. That adapter was never merged and its files are gone; this page replaces that report.

## Implemented

| Capability | Vultr call | Notes |
| --- | --- | --- |
| `hosting.instance.inspect` | `GET /v2/instances/{id}` | Maps `status`, `power_status` and `server_status` to the protocol states (table below). Anything it cannot map is `unknown`. A 404 is reported as `unknown` with a diagnostic, never `destroyed`, because a wrong token or account also returns 404. Every other failure, including 401, 403 and 429, is an error, so the caller retries instead of recording an observation. |
| `hosting.instance.find_by_resource_key` | `GET /v2/instances`, every page | Reads the complete account inventory, which the client checks against `meta.total` and for duplicate IDs, then keeps instances whose label is exactly the `gsc-<64 hex>` resource key. Returns at most two distinct matches. |

**A find match is a reconciliation candidate, not proof of ownership.** Vultr labels can be edited, so treat one match as the create that lost its response only together with the controller's own durable record of that create. The adapter does not read Vultr tags such as `gsc-operation-…`. Instances created by the supervised pilot use `gsc-managed-…` labels and never match.

| Vultr `status` | `server_status` | `power_status` | Protocol `state` |
| --- | --- | --- | --- |
| `pending` | any | any | `creating` |
| `active` | `locked` | any | `unknown` |
| `active` | any other | `stopped` | `stopped` |
| `active` | `ok` | `running` | `active` |
| `active` | `installingbooting` | `running` | `creating` |
| anything else | anything else | anything else | `unknown` |

## Not implemented

`hosting.instance.create`, `start`, `stop`, `reboot`, `destroy`, and `wait`; `hosting.regions.list`, `plans.list`, and `os_offerings.list`; `hosting.network.inspect`, `firewall.manage`, `bootstrap.deliver`, and `bootstrap.inspect`. The mutating capabilities need the durable workflow, idempotency, and approval rules in the [hosting contract](../protocol/adapters/hosting.md). The supervised pilot (`internal/pilotvultr`) still creates its VMs through its own code path.
