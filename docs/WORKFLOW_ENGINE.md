# Durable workflow engine

`internal/workflow` advances an accepted operation through its persisted steps
and the versioned in-process adapter registry. It is a reusable Phase 2
component; the CLI does not expose it yet. The metadata-only `server.define`
and fake-adapter `server.start` workflows are implemented. Other standard
lifecycle definitions are still to be wired.

## `server.start`

At acceptance, a trusted `ServerStartProfileResolver` selects the game release,
execution unit, readiness set, and exact check versions for the stored logical
server. The workflow verifies that the selected check set is declared by the
game adapter and persists a four-step plan: inspect the exact provider
instance, start the exact execution unit, inspect that unit, and evaluate the
versioned game readiness checks. Adapter IDs, capability versions, resource
references, and readiness requirements are immutable before dispatch.

The workflow starts only a stopped logical server whose managed provider
instance and optional `expected_instance_ref` still match. It dispatches the
runtime start only after the provider reports `active`. If a start result is
ambiguous, the workflow invokes the read-only unit inspection capability pinned
in its plan. It retries only after inspection confirms `absent` or `stopped`;
`starting` remains pending and unclear states require operator review. A
provider or runtime inspection that definitively blocks the next step fails the
operation without issuing that step.

The lifecycle is projected through `starting` and `validating`. The operation
becomes `succeeded` and the logical server becomes `ready` atomically only
after the runtime is confirmed running, every exact versioned readiness check
passes, and the controller can create a fresh readiness assertion. A failed
readiness postcondition produces a failed operation and a `degraded` server.
The workflow is exercised exclusively with scripted fake adapters; it does
not connect to Vultr, a game server, or an execution host.

## Dispatch and recovery

Before dispatch, the engine checks that the operation's exact protocol version,
adapter, capability version, input schema, and output schema are available. A
step is durably marked `running` and its attempt number is advanced before the
adapter is called. A completed response is schema-validated and its output is
stored with the step. An accepted response moves the operation to `waiting`;
the external operation reference is retained. A timeout, malformed response,
or other possibly-effectful failure moves the step to `outcome_unknown` and the
operation to `reconciling`.

An operation-specific `Definition` supplies preconditions, reconciliation,
and typed success and partial-failure result construction. The engine never
infers that an ambiguous action is safe to repeat. A definition may return
`safe_to_retry` only after it has established that repeating that exact step is
safe. The engine rechecks preconditions and enforces a bounded attempt count
before retrying.

`Recover` marks a step that remained `running` at process shutdown as
`outcome_unknown`; it does not contact adapters. A caller can then schedule the
operation for reconciliation. `Resume` requires an injected authorizer for the
exact persisted operation and resumes through reconciliation, never by
directly replaying an uncertain action.

The current bbolt deployment supports one controller process per database
file. The engine has an in-process dispatcher guard and the store uses
revision-checked updates; there is no distributed worker lease. Multiple
controllers or remote workers need a different transactional locking design.

## Limits

This package does not provide the standard
`server.provision`, `server.stop`, `server.backup`,
`server.restore`, or `server.destroy` workflow definitions, or expose a
controller API/worker. Cancellation handling, process or remote
adapter transports, runtime configuration, credential-accessor integration,
and provider adapters remain separate work. A bounded systemd credential
accessor exists in `internal/credential`, but the workflow engine does not call
it. Tests use only scripted fake adapters and a local temporary bbolt database;
they do not contact a provider or server.

The workflow package can build a short-lived readiness assertion from a
persisted, successful `game.readiness.check` step and a trusted, versioned
readiness check set. It binds the assertion to the operation, logical server,
managed instance, release, and exact check versions, and uses the earliest
expiry among the individual checks. `server.start` validates and returns this
assertion before its atomic transition to `ready`; other workflows do not yet
consume it for restore or provision postconditions.

The store now creates a durable logical-server record in the same bbolt
transaction that accepts `server.define`. That record starts as
`unprovisioned`, keeps the selected profile references and original validated
definition, and is keyed by environment and server ID. Definition requests
cannot contain adapter steps, secret references, or an external instance
reference. The store also supports an atomic operation/server-state commit.
Each committed lifecycle transition records the authenticated actor, operation,
previous and next state, cause, request fingerprint, timestamp, and completed
step IDs that provide evidence. The engine routes definitions that implement
the pure lifecycle projector through this transaction. This is an integration
seam; standard lifecycle definitions and their typed results are not yet wired
to update the server record.

Operation acceptance also requires an injected trusted planner. A request
cannot supply adapter steps, and authorization runs against the planner's
result before the operation is persisted. The metadata-only definition planner
is covered; profile-resolving plans for provision, start, stop, backup, restore,
and destroy have not been implemented. The store rejects every accepted
`server.*` lifecycle operation except `server.define` unless its target already
has a valid durable logical-server record; a rejected request cannot leave an
operation or lock behind.

`SchemaRequestValidator` checks the standard operation request against the
compiled protocol schemas, binds `server.define` and `server.provision` targets
to their parameters, and validates separate secret-reference metadata. The
acceptance package exposes this validator, but there is not yet a production
controller/API composition that wires it into a running service.
