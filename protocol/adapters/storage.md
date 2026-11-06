# Storage Adapter Contract

**Status:** Draft, version 0.10.0-draft.

## 1. Responsibility

The storage adapter transfers and inspects persistent objects. The core owns backup policy, retention authorization, environment isolation, and the decision whether a backup is sufficient to permit destruction.

An upload acknowledgment MUST NOT be represented as a verified backup. The core verifies every required object against the immutable backup manifest before marking the backup verified.

## 2. Capability names and schemas

An adapter MAY implement any subset of these capabilities. Every advertised capability MUST use its registered input and output schema.

| Capability | Input schema | Output schema | Meaning |
|---|---|---|---|
| `storage.object.put` | [object-put-input](../schemas/storage/object-put-input.schema.json) | [object-put-output](../schemas/storage/object-put-output.schema.json) | Upload a bounded stream to an exact object target |
| `storage.object.get` | [object-get-input](../schemas/storage/object-get-input.schema.json) | [object-get-output](../schemas/storage/object-get-output.schema.json) | Download one exact object version to a bounded stream |
| `storage.object.head` | [object-head-input](../schemas/storage/object-head-input.schema.json) | [object-head-output](../schemas/storage/object-head-output.schema.json) | Inspect existence and metadata for an exact object/version |
| `storage.object.list` | [object-list-input](../schemas/storage/object-list-input.schema.json) | [object-list-output](../schemas/storage/object-list-output.schema.json) | List objects within one authorized container and prefix |
| `storage.object.copy` | [object-copy-input](../schemas/storage/object-copy-input.schema.json) | [object-copy-output](../schemas/storage/object-copy-output.schema.json) | Copy an exact object to an authorized target |
| `storage.object.delete` | [object-delete-input](../schemas/storage/object-delete-input.schema.json) | [object-delete-output](../schemas/storage/object-delete-output.schema.json) | Delete one exact object/version after core authorization |
| `storage.object.verify_checksum` | [object-verify-checksum-input](../schemas/storage/object-verify-checksum-input.schema.json) | [object-verify-checksum-output](../schemas/storage/object-verify-checksum-output.schema.json) | Compare size and digest with the manifest |
| `storage.object.versioning.inspect` | [object-versioning-inspect-input](../schemas/storage/object-versioning-inspect-input.schema.json) | [object-versioning-inspect-output](../schemas/storage/object-versioning-inspect-output.schema.json) | Inspect versioning for one container |
| `storage.object.retention.inspect` | [object-retention-inspect-input](../schemas/storage/object-retention-inspect-input.schema.json) | [object-retention-inspect-output](../schemas/storage/object-retention-inspect-output.schema.json) | Inspect retention and legal-hold state |

Optional capabilities MUST be explicitly declared. The core MUST NOT assume object versioning, object lock, server-side copy, or a particular checksum algorithm. An adapter MUST NOT advertise a capability unless it can satisfy its complete schema and behavior contract.

## 3. Object identity and environment isolation

A [storage container reference](../schemas/storage/storage-container-reference.schema.json) identifies an adapter, non-secret storage profile/account scope, environment, and bucket/container. A [storage object reference](../schemas/storage/storage-object-reference.schema.json) adds an opaque key and, where the provider exposes it, the exact version ID. A write target deliberately has no provider version ID.

Credentials are never part of a storage reference. A manifest object's environment MUST match the manifest environment. The core MUST check adapter, profile, environment, and object ownership before reads, copies, writes, or deletion.

Production and staging MUST use separate authorization scopes. Staging may read only explicitly selected production backup objects and MUST write to staging-owned storage. A staging credential MUST NOT overwrite or delete production objects.

Object keys are opaque strings under the adapter's documented key rules. Implementations MUST prevent path confusion when mapping object keys to local files. Continuation tokens MUST remain bound to the authorized container, prefix, and caller context.

## 4. Bounded data transfers

Large object bytes MUST NOT be embedded in JSON-RPC messages. A [storage transfer reference](../schemas/storage/storage-transfer-reference.schema.json) identifies a controller-issued, one-use, operation-scoped, bounded stream with an expiry and a direction. The request context binds the transfer to the exact operation, step, and environment. The adapter MUST reject expired, reused, oversized, or direction-mismatched handles and MUST NOT log the handle.

For `storage.object.put`, the stream direction is `controller_to_adapter`; for `storage.object.get`, it is `adapter_to_controller`. The maximum byte limit MUST be at least the expected object size. Stream completion is separate from object integrity verification.

The in-process adapter contract MAY map the transfer reference to a direct bounded stream. The optional `jsonrpc-stdio-v1` process profile does not define a byte-stream channel. A process adapter using that profile MUST NOT advertise `storage.object.put` or `storage.object.get` until a separately versioned transport binding maps transfer references to a bounded stream.

## 5. Backup manifest

A verified backup MUST have a manifest conforming to [backup-manifest.schema.json](../schemas/storage/backup-manifest.schema.json), containing at least:
- Backup ID, logical server ID, and the exact `data_generation_id` captured.
- Environment ID and source instance reference, if present.
- Game adapter ID, game name/version, and data-format/world compatibility metadata available from the game adapter.
- Included data paths or named data sets.
- Creation timestamp and source operation ID.
- Exact storage object references, byte lengths, checksum algorithm, and digest for each object.
- Consistency mode (offline, game-native snapshot, or another explicitly named mode) and evidence.
- Manifest schema/protocol version.

The manifest MUST NOT include secret values or unnecessary player telemetry. The core MUST reject a manifest whose object references cross the manifest's environment or authorized storage profile.

## 6. Operation semantics

- **Put:** report the exact target, received byte count, and provider metadata. A completed upload is not a verified backup.
- **Head:** inspect the exact object/version. A missing object is an explicit result, not a transport error.
- **Get:** transfer the exact object version and report bytes sent plus the digest computed from those bytes.
- **List:** return a bounded page of opaque object references and a continuation token, if more results exist.
- **Copy:** preserve or calculate the integrity metadata required by the request and policy.
- **Delete:** destructive; requires explicit core authorization, exact object/version, retention checks, and audit. The adapter MUST NOT broaden an unversioned or conditional request to delete unrelated versions.
- **Verify:** compare actual bytes or a provider-supported trustworthy checksum with the manifest expectation. ETag MUST NOT be assumed to be a content checksum.
- **Versioning/retention inspection:** report provider support and observed state; unknown or unsupported MUST remain explicit.

A multi-object backup MUST remain incomplete until every required object and the manifest have been verified. A failed or partial upload MUST NOT be treated as verified or eligible to authorize destruction.

## 7. Restore safety

Before exposing extracted data to a game runtime, the controller/game adapter MUST validate archive paths, reject traversal and unsafe symlinks, verify checksums, and check game compatibility. Restore MUST target a server/environment-owned data root. A failed or partial restore MUST be recorded and MUST NOT be treated as a valid restored world.

## 8. Conformance cases

Test partial upload, object missing after upload, length mismatch, checksum mismatch, ETag not equal to digest, staging write/delete denial, unsafe archive entries, duplicate object key/version handling, reused/expired/oversized transfer handles, and delete without authorization.
