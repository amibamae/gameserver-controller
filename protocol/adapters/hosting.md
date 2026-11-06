# Hosting Adapter Contract

**Status:** Draft, version 0.3.0-draft.

## 1. Responsibility

The hosting adapter translates provider-independent infrastructure requests into provider-specific API operations. It owns provider API details, provider-native identifiers, asynchronous operation polling, and normalized provider error reporting.

It does not install the game, decide backup safety, route players, or own lifecycle policy.

## 2. Capability names

An adapter MAY declare any subset of these capabilities:

| Capability | Meaning |
|---|---|
| `hosting.regions.list` | Discover regions |
| `hosting.plans.list` | Discover instance plans |
| `hosting.os_offerings.list` | Discover provider OS offerings and versions |
| `hosting.instance.create` | Create an instance from a validated specification |
| `hosting.instance.inspect` | Inspect one exact instance |
| `hosting.instance.start` | Start a stopped instance |
| `hosting.instance.stop` | Stop an instance |
| `hosting.instance.reboot` | Reboot an instance |
| `hosting.instance.destroy` | Destroy an instance |
| `hosting.instance.wait` | Wait/poll until a provider operation reaches a defined outcome |
| `hosting.network.inspect` | Inspect network attachments and addresses |
| `hosting.firewall.manage` | Manage firewall policy, when explicitly supported |
| `hosting.bootstrap.deliver` | Deliver or invoke a protected bootstrap reference |

Capabilities MUST declare constraints, permission scopes, idempotency behavior, and whether the operation can be reconciled after a lost response.

The adapter-level `hosting.instance.create` contract uses [instance-create-input.schema.json](../schemas/hosting/instance-create-input.schema.json) and [instance-create-output.schema.json](../schemas/hosting/instance-create-output.schema.json). The broader [provision request](../schemas/hosting/provision-request.schema.json) remains the controller's orchestration input and is not passed wholesale to the hosting adapter.

## 3. Create specification

A create request SHOULD include:
- Logical server and environment IDs.
- Region and plan identifiers scoped to this adapter.
- Provider OS offering identifier, plus human-readable distribution/version/architecture.
- Hostname and non-secret labels.
- SSH public-key references.
- Network and firewall policy references.
- Bootstrap source reference, immutable revision, integrity digest, and non-secret inputs.
- Minimum resource requirements and requested expiry/retention policy, if applicable.

Provider-native plan, region, and OS IDs are opaque strings. The protocol does not assume that providers share catalogs or naming.

A create request MUST NOT include raw API tokens, passwords, private keys, storage secrets, or game player data. Cloud-init/user-data and provider metadata MUST NOT be used to smuggle secret values.

## 4. Operations and outcomes

- Create: return a provider operation reference and instance reference when available; do not claim active until the instance postcondition is observed.
- Inspect: return the exact target identity, normalized state, region/plan/OS facts when available, network observations, and observation timestamp.
- Start/stop/reboot: distinguish accepted provider request from confirmed resulting instance state.
- Destroy: require a core-authorized operation context, exact instance reference, and destruction plan. The adapter MUST reject missing/mismatched target scope. The core re-checks backup and authorization immediately before dispatch.
- Wait: bounded by request deadline; timeout means unknown unless the provider's state establishes otherwise.

The adapter MUST support lookup/inspection sufficient to reconcile create/destroy results, or declare that it cannot and cause the core to suspend uncertain operations for operator action.

## 5. Instance references and security

An instance reference MUST contain adapter ID, environment ID, resource kind, and opaque provider ID. A provider ID from one environment MUST NOT be reused as a target in another environment.

An adapter MUST use least-privilege provider credentials and must never return credentials in diagnostics. Network/firewall changes are privileged operations and require separate capabilities and authorization.

## 6. Bootstrap

The preferred initial workflow is a provider-supported standard OS offering followed by a reviewed, versioned bootstrap script. The adapter may deliver a protected script reference or report a host connection target for a separate bootstrap runner.

The adapter MUST report how the bootstrap was initiated, its immutable revision/digest, execution reference, exit status, and validation evidence. It MUST NOT claim preparation success based only on successful upload or remote command submission.

## 7. Conformance cases

Test create success, create timeout/ambiguous outcome, duplicate idempotency key, inspect scope mismatch, provider transient failure, destroy target mismatch, destroy with missing core authorization, and destroy outcome reconciliation.
