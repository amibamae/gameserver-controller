# Wasabi storage adapter

`internal/wasabistorage` is the first Go implementation of the Wasabi storage
adapter. It currently advertises only `storage.object.put`,
`storage.object.get`, and `storage.object.head`. The management runtime does not
construct or register this adapter yet, and the configuration loader does not
parse its provider-specific fields. Treat the package as an implementation
stage, not as a deployable Wasabi integration.

The adapter uses AWS SDK for Go v2 with a regional Wasabi S3 endpoint. It only
accepts HTTPS hosts under `wasabisys.com`, uses path-style bucket addressing,
and does not load the AWS environment, shared credential files, or instance
metadata credential chains. Upload and download bodies stay outside JSON-RPC.
Before sending an upload, the adapter spools it into a private temporary file
and verifies the declared length and SHA-256. This avoids writing malformed
data to Wasabi, at the cost of temporary disk space up to `MaxObjectBytes`.
The upload is multipart-capable and uses one part at a time. Downloads stream
directly to the controller-provided bounded destination and are only reported
complete after size and SHA-256 checks pass. Provider version IDs are returned
when available. ETags are reported as metadata and are never treated as
checksums.

## Profile construction

One adapter instance represents one adapter ID, storage profile, and
environment. Use separate adapter instances and Wasabi sub-user keys for
production and staging. A runtime can build the profile with fields in this
shape:

```go
wasabistorage.Config{
    AdapterID:          "storage.wasabi-staging",
    StorageProfileID:   "wasabi-staging",
    EnvironmentID:      "staging",
    Container:          "example-staging-backups",
    KeyPrefix:          "server/",
    Region:             "us-west-1",
    EndpointURL:        "https://s3.us-west-1.wasabisys.com",
    AccessKeyIDRef:     "systemd-credential://wasabi-staging-access-key-id",
    SecretAccessKeyRef: "systemd-credential://wasabi-staging-secret-access-key",
    MaxObjectBytes:     1073741824,
    RequireVersionIDs:  true,
}
```

Resolve credentials through `internal/credential` at invocation time. The
access key ID and secret access key are separate encrypted systemd credentials;
neither value belongs in YAML, command arguments, process environment, operation
records, or logs. Keep production and staging references distinct. The adapter
only resolves the two references pinned in its profile and requires both to be
present in the invocation context.

`Container` and slash-terminated `KeyPrefix` fence every object operation to
one configured bucket and key namespace. The invocation environment must also
match the profile. Cross-environment reads, including staging restore from a
production backup, need a separate explicit authorization path and are not
implemented.

`MaxObjectBytes` is a required profile bound. It cannot exceed the S3
10,000-part limit with a maximum 128 MiB part. The adapter limits each stream
to that bound and uses one upload part at a time. Set `RequireVersionIDs` when
the storage policy requires an immutable
provider version; upload, head, and get then fail closed if Wasabi returns no
real version ID. Enable bucket versioning before using this setting.

Give each backup a unique object key. The current adapter does not implement a
conditional write, delete, copy, list, versioning inspection, or retention
inspection capability. It does not implement controller policy itself. In
particular, a write to a reused key can replace the current object when bucket
versioning is disabled. The core must allocate exact backup keys and perform
readback verification before marking a backup complete.

## Validation status

The package tests use an in-memory S3 API fake and the embedded protocol
schemas. They do not contact Wasabi, validate a live key policy, or test a
real Wasabi account. Before wiring the adapter into a runtime, finish
configuration and secret-reference integration, add lifecycle and
cross-environment tests, and run an isolated staging upload/readback using a
dedicated bucket and least-privilege key. Do not use production credentials for
that test.

Wasabi documents regional [service URLs](https://docs.wasabi.com/docs/service-urls-for-wasabis-storage-regions)
and S3 SDK compatibility. The adapter follows the AWS SDK v2 custom endpoint
pattern and pins a release compatible with this repository's Go 1.23 baseline.
