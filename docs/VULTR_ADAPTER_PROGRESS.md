# Vultr hosting adapter status

**Status:** read-only. `implementation: vultr` builds the Vultr hosting adapter in [`internal/vultrhosting`](../internal/vultrhosting/README.md). It can inspect instances but cannot create, change, or destroy them.

An earlier version of this page described a complete adapter with create and start capabilities. That adapter was never merged and its files are gone; this page replaces that report.

## Implemented

| Capability | Vultr call | Notes |
| --- | --- | --- |
| `hosting.instance.inspect` | `GET /v2/instances/{id}` | Maps `status` and `power_status` to the protocol states. Anything it cannot map is `unknown`. If Vultr refuses the lookup (any 4xx, including 404) the state is `unknown` with a diagnostic, because a wrong token or account also returns 404. Transport and response failures are errors, so the caller can retry. |
| `hosting.instance.find_by_resource_key` | `GET /v2/instances?label=…`, every page | Matches instances whose label is exactly the `gsc-<64 hex>` resource key. Returns at most two distinct matches and reports `inventory_complete` only after reading every page. Instances created by the supervised pilot use `gsc-managed-…` labels and are not matched. |

| Vultr `status` | Vultr `power_status` | Protocol `state` |
| --- | --- | --- |
| `pending` | any | `creating` |
| `active` | `running` | `active` |
| `active` | `stopped` | `stopped` |
| anything else | anything else | `unknown` |

## Not implemented

`hosting.instance.create`, `start`, `stop`, `reboot`, `destroy`, and `wait`; `hosting.regions.list`, `plans.list`, and `os_offerings.list`; `hosting.network.inspect`, `firewall.manage`, `bootstrap.deliver`, and `bootstrap.inspect`. The mutating capabilities need the durable workflow, idempotency, and approval rules in the [hosting contract](../protocol/adapters/hosting.md). The supervised pilot (`internal/pilotvultr`) still creates its VMs through its own code path.
