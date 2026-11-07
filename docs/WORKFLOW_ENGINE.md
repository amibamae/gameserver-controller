# Durable workflow engine

`internal/workflow` advances an accepted operation through its persisted steps
and the versioned in-process adapter registry. It is a reusable Phase 2
component; the CLI does not expose it yet. The metadata-only `server.define`
workflow is implemented, while the other standard lifecycle definitions are
still to be wired.

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

This package does not create operation plans, provide the standard
`server.provision`, `server.start`, `server.stop`, `server.backup`,
`server.restore`, or `server.destroy` workflow definitions, or expose a
controller API/worker. Cancellation handling, process or remote
adapter transports, runtime configuration, credential-accessor integration,
and provider adapters remain separate work. A bounded systemd credential
accessor exists in `internal/credential`, but the workflow engine does not call
it. Tests use only scripted fake adapters and a local temporary bbolt database;
they do not contact a provider or server.

The store now creates a durable logical-server record in the same bbolt
transaction that accepts `server.define`. That record starts as
`unprovisioned`, keeps the selected profile references and original validated
definition, and is keyed by environment and server ID. Definition requests
cannot contain adapter steps, secret references, or an external instance
reference. This is a persistence primitive; standard lifecycle transitions
and typed operation results are not yet wired to update the server record.

Operation acceptance also requires an injected trusted planner. A request
cannot supply adapter steps, and authorization runs against the planner's
result before the operation is persisted. The metadata-only definition planner
is covered; profile-resolving plans for provision, start, stop, backup, restore,
and destroy have not been implemented.

`SchemaRequestValidator` checks the standard operation request against the
compiled protocol schemas, binds `server.define` and `server.provision` targets
to their parameters, and validates separate secret-reference metadata. The
acceptance package exposes this validator, but there is not yet a production
controller/API composition that wires it into a running service.
