# ADR 0005: Initial operation persistence

**Status:** Accepted for the first Go controller process.

## Context

The protocol requires accepted operations and their recovery state to be durably committed before the controller dispatches any external side effect. The initial controller should run as one local process without requiring a separately operated database service.

## Decision

Use [bbolt](https://github.com/etcd-io/bbolt) as the first embedded operation store. Keep the database on a local filesystem in a user-private state directory. Use bbolt's default synchronous transactions, one transaction per accepted-operation or operation-update write, a versioned metadata bucket, and an exclusive database-file lock so only one controller process owns the store at a time.

The store exposes atomic acceptance, read, compare-and-swap update, recovery listing, and resource-lock inspection. Acceptance checks an actor-scoped idempotency key, writes the operation, and reserves its logical-server resource lock in one transaction. The index stores versioned HMAC verifiers and a keyed digest of the request binding rather than the client key or request metadata. New operation acceptance fails closed if any key version already present in the database is missing from the configured keyring. All seven standard `server.*` operations share an exclusive lock keyed by environment and server ID. Startup verifies that operation, idempotency, and both directions of the resource-lock indexes agree; inconsistent recovery state fails closed. The lock is restored with the database on restart and released in the same transaction that records a safe terminal outcome.

Accepted request fields, schema revision, step order, and recorded adapter capability identity are immutable. Operation and step status changes must follow the protocol transition tables. Terminal operations cannot be changed, and unresolved external effects block terminal failure/cancellation.

The store accepts only validated request snapshots containing secret references; resolved secret values must be removed before the snapshot reaches this package. The database file is created with mode `0600`. Operators should also rely on the host's disk protection and keep database copies out of public or shared storage.

## Consequences and limits

- The first release supports one controller process per database file. Multiple controllers or remote workers require a different transactional and locking deployment.
- The initial implementation retains operation records indefinitely. Automated 90-day retention, tombstone conversion, backup export, and migration tooling are follow-up work.
- The keyring is protected installation configuration and must be backed up with the database. Opening a nonempty earlier schema without an explicit migration currently fails closed.
- A live database file must be backed up using a consistent bbolt snapshot or while the controller is stopped; copying the file during writes is not a supported backup procedure.
- bbolt is an embedded key/value database, not a protocol dependency. The operation-store interface keeps controller services independent of the selected backend.

## Reconsider when

Revisit this choice before adding multiple controller replicas, network-attached storage, automated operation retention, or a migration/backup workflow that needs a database snapshot interface.
