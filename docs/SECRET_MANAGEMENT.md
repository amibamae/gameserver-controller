# Provider credential handling

**Status:** Required design for provider integrations. The repository contains
only example credential references, never real keys. A bounded systemd
credential accessor exists in `internal/credential`. The capacity-backed
`pilotvultr` service and `gsc-pilot provision` command use the accessor for
Vultr creates. No command currently accesses Wasabi.

## Initial Debian deployment

For the first controller deployment on Debian with systemd, keep credential
values in encrypted systemd credential files and load only the named values
needed by the controller service. Use `systemd-creds encrypt` and the service
unit's `LoadCredentialEncrypted=` setting. Do not place a value in a unit file,
YAML configuration, shell command, environment variable, bootstrap script, or
user-data. Run the controller under a dedicated unprivileged service account.
The production command should read a named file through
`$CREDENTIALS_DIRECTORY` only when the provider call needs it; the path is not
hard-coded. Systemd documents that
service credentials are restricted to the service user, are not propagated
through the process environment, and can be encrypted for storage.

Configuration uses references such as
`systemd-credential://vultr-pilot-api-token`. The reference is only a handle;
it does not contain or prove authorization to use the value. The accessor
opens only a valid name inside the configured credential directory, rejects
symlinks and non-regular files, bounds reads to 1 MiB, and zeroes the borrowed
buffer after the callback. It does not guarantee removal of runtime/compiler
copies or replace service isolation. The pilot CLI reads the reference only
when invoked with `CREDENTIALS_DIRECTORY` provided by systemd. The repository
does not yet provide a systemd unit or automate loading the encrypted
credential, so operators must review and set up that host-side service
boundary before using the billable command. A future managed deployment may use a dedicated secret manager
with workload identity and per-secret access policies instead of systemd
credentials.

Provider adapters are trusted executable code. In-process adapters are not a
security sandbox. Review and pin adapter implementations, keep their
permissions narrow, and do not load untrusted community adapters into a
controller process that can access provider keys.

## Vultr

Use a dedicated service identity or isolated account for controller API calls,
not an operator's personal or account-owner key. Grant only the actions and
resources the controller needs. Restrict API use to the controller's known
egress IP where the account supports an allowlist. Vultr documents separate
service-user/API identities and API source-IP controls; API key scope and
permissions vary by account type, so verify the effective permissions before
enabling create or destroy.

The pilot uses this credential only for controller-owned instances.
An existing live production VM may remain unmanaged. The pilot capacity rule
must be checked before create/adopt API calls; the current policy is
source-controlled in [the embedded capacity file](../internal/pilotcapacity/capacity-policy.json).

## Wasabi

Do not use the Wasabi root key for server operations. Create separate API-only
sub-users and key pairs for each role. Attach policies restricted to the
required bucket and object prefix:

- Production backup writer: write and verify production backup objects.
- Staging production reader: list and read only the selected production
  backup objects.
- Staging writer: write staging-owned objects only.

Staging keys must not write or delete production backups. Use an explicit deny
bucket-policy guardrail when broader identity policies could otherwise grant
access. Require HTTPS for storage requests. Wasabi documents sub-user keys,
IAM and bucket policies, key rotation, and explicit-deny behavior.

For the existing `minecraft-backup.service`, keep SSH as the authenticated
operator path for setup and rotation, but do not put a key in a remote command,
environment variable, bootstrap script, or plaintext service configuration.
Store the role-specific key as an encrypted systemd credential on the host and
expose it only to the backup service with `LoadCredentialEncrypted=`. The
service should read the named credential from `$CREDENTIALS_DIRECTORY` under
its dedicated service account. The repository does not install or configure
this host-side credential; this is the target handling model for the current
manual backup service.

This is a transitional exception for the existing service that uploads from
its host. Keep permanent Wasabi keys out of new game-server VMs. In the
controller-owned design, only the storage adapter should hold the permanent
service credential. If a future worker has to access Wasabi directly, use a
short-lived, role-scoped credential from `AssumeRole` with an inline policy
narrowing the bucket/prefix; protect the sub-user key used to request that
session as a controller credential. Wasabi's `GetSessionToken` preserves the
caller's original permissions, so it is not a least-privilege replacement for
`AssumeRole`.

## Handling, rotation, and incident response

- Keep resolved values out of operation records, configuration output, audit
  events, errors, logs, command arguments, and child-process environments.
- Resolve a key only for the adapter call that needs it. Keep permanent
  storage keys out of new game-server VMs, bootstrap code, lobby software, and
  customer apps; the existing `minecraft-backup.service` is the transitional
  host-side exception.
- Name each reference by provider, environment, and purpose; do not reuse a
  storage key between production and staging. The config validator rejects a
  shared storage credential reference across those environments.
- Rotate by creating a replacement key, storing it as a new encrypted
  credential, switching the service reference, verifying the provider call,
  and revoking the old key. On suspected exposure, revoke first, then rotate
  and investigate access logs.
- Keep encrypted credential files and the host's systemd credential-encryption
  material in protected backups. A host-bound encrypted credential may need to
  be re-encrypted on a replacement host.

This design follows the least-privilege, access-control, rotation, and audit
guidance in the [OWASP Secrets Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html)
and the [systemd Credentials documentation](https://systemd.io/CREDENTIALS/).
Provider guidance: [Vultr service-user API access](https://docs.vultr.com/support/platform/iam/what-is-a-user-in-vultr-iam),
[Vultr API access controls](https://docs.vultr.com/platform/other/api/manage-api-access-control),
[Wasabi user security practices](https://docs.wasabi.com/docs/what-are-wasabis-recommended-general-user-security-best-practices),
[Wasabi bucket access policies](https://docs.wasabi.com/docs/how-do-i-restrict-bucket-access-with-resource-based-policies),
and [Wasabi STS temporary credentials](https://docs.wasabi.com/docs/aws-sts-with-wasabi).
