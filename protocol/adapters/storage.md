# Storage Adapter Contract

**Status:** Draft, version 0.2.0-draft.

## 1. Responsibility

The storage adapter transfers and inspects persistent objects. Core owns backup policy, retention authorization, environment isolation, and the decision whether a backup is sufficient to permit destruction.

An upload acknowledgment MUST NOT be represented as a verified backup.

## 2. Capability names

| Capability | Meaning |
|---|---|
| `storage.object.put` | Upload an object |
| `storage.object.get` | Download an object |
| `storage.object.head` | Inspect object metadata |
| `storage.object.list` | List objects within an authorized prefix |
| `storage.object.copy` | Copy an object |
| `storage.object.delete` | Delete an object after explicit authorization |
| `storage.object.verify_checksum` | Verify a supported digest |
| `storage.object.versioning.inspect` | Inspect object versioning where supported |
| `storage.object.retention.inspect` | Inspect retention/lock metadata where supported |

Optional capabilities MUST be explicitly declared. The core MUST NOT assume object versioning, object lock, server-side copy, or a particular checksum algorithm.

## 3. Object identity and environment isolation

A storage object reference MUST identify adapter, storage profile/account scope, environment, bucket/container, key, and version ID where applicable. Credentials are never part of an object reference.

Production and staging MUST use separate authorization scopes. Staging may read only explicitly selected production backup objects and MUST write to staging-owned storage. A staging credential MUST NOT overwrite or delete production objects.

Object keys are opaque strings under the adapter's documented key rules. Implementations MUST prevent path confusion when mapping object keys to local files.

## 4. Backup manifest

A verified backup MUST have a manifest containing at least:
- Backup ID, logical server ID, and the exact `data_generation_id` captured.
- Environment ID and source instance reference, if present.
- Game adapter ID, game name/version, and data-format/world compatibility metadata available from the game adapter.
- Included data paths or named data sets.
- Creation timestamp and source operation ID.
- Object references, byte lengths, checksum algorithm and digest for each object.
- Consistency mode (offline, game-native snapshot, or another explicitly named mode) and evidence.
- Manifest schema/protocol version.

The manifest MUST NOT include secret values or unnecessary player telemetry.

Checksums MUST identify their algorithm. The protocol MUST NOT assume ETag is a content checksum. A backup is verified only after required objects are present, lengths match, supported digests verify, and manifest ownership/compatibility checks pass. The core separately determines whether its data generation is current; integrity verification alone does not make a backup eligible for destruction.

## 5. Operations

- Put: report object reference, byte length, and provider-returned metadata; do not infer integrity from an HTTP success status.
- Head: report existence and metadata for the exact object/version.
- Get: stream or materialize the exact object and report transfer completion; verify digest before restore.
- Verify: compare actual bytes or provider-supported trustworthy checksum with the manifest expectation.
- Delete: destructive; requires explicit core authorization, exact object reference, retention checks, and audit.
- Copy: must preserve or calculate the integrity metadata required by policy.

A multi-object backup MUST remain incomplete until every required object and the manifest have been verified.

## 6. Restore safety

Before exposing extracted data to a game runtime, the controller/game adapter MUST validate archive paths, reject traversal and unsafe symlinks, verify checksums, and check game compatibility. Restore MUST target a server/environment-owned data root. A failed or partial restore MUST be recorded and MUST NOT be treated as a valid restored world.

## 7. Conformance cases

Test partial upload, object missing after upload, length mismatch, checksum mismatch, ETag not equal to digest, staging write/delete denial, unsafe archive entries, duplicate object key/version handling, and delete without authorization.
