# Controller setup and management access

**Status:** The Go CLI can preview and apply the initial local installation on
an existing Debian/systemd machine. Apply installs a controller binary, a
validated configuration, a hardened systemd unit, and private state
directories. It does not enable, start, or stop the service, contact providers,
or overwrite different existing files. `controller-setup plan` remains
read-only and redacted. `controller-setup credentials set` accepts only a
credential name referenced by the validated config, prompts twice with
`systemd-ask-password`, and stores the value with `systemd-creds` in
`/etc/credstore.encrypted`. It runs only on Debian/systemd with local
administrator privileges, requires exact name confirmation to replace an
existing value, and never prints the credential. `controller serve` can run a
disabled-by-default HTTPS listener for the authenticated operation-inspection
route. It requires a fixed audience and a TLS certificate/private-key pair; the
current API exposes no lifecycle mutation routes. Provider credential
installation is a separate command. Provider credential checks, audit
integration, remaining management API routes, password login, and session
issuance are not implemented. The example configuration keeps the listener
disabled. These instructions document behavior; they do not authorize applying
it to a live host.

Run the preview from a checkout with:

```sh
go run ./cmd/controller-setup plan --config configs/controller.example.yaml --json
```

The example configuration contains placeholder provider references and is
intended to show the plan shape. The output reports supported-target blockers
and fixed destination paths. Applying that example would install placeholders,
not working provider credentials.

To apply a reviewed configuration on the target machine, build both Go
executables for that Linux machine, inspect the plan, then run the explicit
confirmation command as root:

```sh
go build -o /tmp/controller ./cmd/controller
go build -o /tmp/controller-setup ./cmd/controller-setup
/tmp/controller-setup plan --config /path/to/controller.yaml --json
sudo /tmp/controller-setup apply \
  --config /path/to/controller.yaml \
  --binary /tmp/controller \
  --confirm INSTALL-GAMESERVER-CONTROLLER
```

Apply validates that the binary is a Go build of this project for Linux and
the current machine architecture. It requires Debian/systemd and local
administrator privileges. It creates the dedicated service account and private
directories, installs files without replacing differing files, then reloads
systemd. It does not enable, start, or stop the service. If installation fails,
new files are rolled back when possible; the service account or directories
created earlier in the attempt may remain. Review the host before retrying.

After installing a real configuration, store each configured provider
credential with `controller-setup credentials set` as described below. That
command uses hidden input and encrypted systemd credentials. If the management
API is enabled in the configuration, valid TLS files must already be present
at the configured paths and readable by the dedicated service account. A
private key can be installed as `root:gameserver-controller` mode `0640`, with
its parent directories traversable by the service group. The setup command does
not create TLS certificates or enable the listener.

The `controller serve` command runs only when `controller.management_api.enabled`
is explicitly set to `true`. Configure an IP address, controller audience, and
absolute certificate/key paths first. The TLS private key must be a regular
file that is not group-writable and has no permissions for other users. The
listener requires TLS 1.2 or newer, applies bounded HTTP timeouts, and authenticates
every request before routing it. Use a trusted certificate for clients outside
the local test environment.

```sh
go run ./cmd/controller serve --config /etc/gameserver-controller/controller.yaml
```

This runtime currently supports `GET /v1/operations/{operation_id}` only. It is
not ready to operate game-server lifecycles or replace the supervised pilot.

To store one of the credential names already referenced by your validated
configuration, use:

```sh
go build -o /tmp/controller-setup ./cmd/controller-setup
sudo /tmp/controller-setup credentials set --config /etc/gameserver-controller/controller.yaml --name <configured-credential-name>
```

Replacing a value also requires `--replace --confirm-replace <same-name>`. The
command passes the prompt response to `systemd-creds encrypt` on standard input;
secret values are never command-line arguments. See the upstream
[`systemd-ask-password`](https://freedesktop.org/software/systemd/man/latest/systemd-ask-password.html)
and [`systemd-creds`](https://freedesktop.org/software/systemd/man/latest/systemd-creds.html)
manuals for the host tools it uses.

## Setup on an existing machine

The initial target is an operator-owned Debian machine running systemd. Setup
configures that machine; it does not create a controller VM or count the
controller host as a disposable game-server slot.

Implement setup in Go as application services with a CLI as the first client.
Those services must remain usable by a future management client. Do not build a
web panel as part of this work. A hosted panel remains optional, and the
controller must work without one.

The implemented local setup flow currently covers:

1. Inspect the machine and validate a local configuration without reading
   secrets or changing services. Report missing prerequisites and exact paths.
2. Install the controller binary, configuration, hardened service unit, and
   private directories after an explicit local confirmation. Existing differing
   files are preserved and cause failure.
3. Enter configured provider credentials separately through hidden prompts and
   store them as encrypted systemd credentials.

Provider credential checks, local audit integration, and a complete controller
runtime remain future work. Setup uses local OS authorization; it does not
expose an anonymous remote setup or administrator-registration endpoint.

Keep Wasabi access-key IDs and secret-access keys out of source, generated
bootstrap scripts, user data, release artifacts, operation records, and logs.
Provider endpoint, region, bucket, prefix, and deployment identity belong in
validated local configuration. Production and staging storage use separate
credentials with the access boundaries in [secret management](SECRET_MANAGEMENT.md).

Secret updates are separate privileged operations. They must audit the actor,
purpose, credential identifier, target environment, and result without recording
the value. Credentials are write-only through management clients. Failed setup
or rotation must preserve the last working configuration and report whether any
local changes committed. Repeating setup must not create provider resources or
silently replace working secrets.

For a future panel, authenticated credential submission may call the same
application service over TLS, subject to an explicit credential-management
permission. Provider secrets remain on the controller and are never returned
to a client. A remote setup route cannot bootstrap its own authorization.

## Local controller API key administration

The first key-management client is a local, root-only `controller-setup keys`
command backed by an application service. It operates only on an existing
private bbolt database and does not start or enable a remote listener. The
service receives the requester identity from its trusted boundary, checks
authorization before issuing or revoking, and records that requester as the
creator or revoker.

Issue a key with an explicit actor, controller audience, expiry, and one or more
exact scopes:

```sh
sudo controller-setup keys issue \
  --database /var/lib/gameserver-controller/controller.db \
  --actor backup-automation \
  --audience controller-instance-1 \
  --expires-in 720h \
  --scope storage.backup.write:staging:test-server
```

The lifetime is required, ranges from one minute to 90 days, and uses Go
duration syntax. The command prints the bearer token once as JSON on standard
output. Protect that output as a secret. The database keeps only the verifier.
The command has no key-list operation, does not accept token values as
arguments, and rejects wildcard scopes.

Revoke a key by repeating its exact identifier:

```sh
KEY_ID="replace-with-key-id"
sudo controller-setup keys revoke \
  --database /var/lib/gameserver-controller/controller.db \
  --key-id "$KEY_ID" \
  --confirm-key-id "$KEY_ID"
```

This local command is not a replacement for authenticated, scoped key
administration through a future management API. A future panel must use a
separate authenticated application boundary and cannot assert a requester ID
from browser input.

## HTTP operation inspection

The Go management API package currently implements only
`GET /v1/operations/{operation_id}`. It authenticates the bearer token against
one configured audience before reading the operation store, then requires the
exact `server.inspect` scope for that operation's environment and server. A
missing operation and an inaccessible operation both return the same 404
response. Storage failures return a generic 500 response without database
diagnostics. The handler does not start a listener; TLS configuration, runtime
composition, and the remaining API routes are still required before remote
management is available.

## Controller API keys

Controller API credentials identify clients of the controller. They are separate
from Vultr and Wasabi credentials and cannot directly grant provider access.
The controller applies the protocol's [authentication](../protocol/management-api/authentication.md)
and [authorization](../protocol/management-api/authorization.md) rules.

Automation keys need a stable key identifier, cryptographically random secret,
trusted actor, controller audience, explicit allowed actions, resource and
environment scopes, expiry, and individually revocable state. Show a new
automation key once; persist a verifier rather than its plaintext secret.
Never include raw key material in listing, inspection, errors, or audit.

Remote requests require TLS and bearer credentials in the Authorization header.
Missing, malformed, expired, revoked, or wrong-audience credentials establish no
actor and cannot create an operation or dispatch an adapter. A valid key still
needs current authorization for the exact action, server, and environment.
Request bodies and headers cannot supply trusted actor roles or permissions.
Default to denial and make any wildcard expansion explicit.

Issuance, revocation, rotation, expiry enforcement, rate limits, and audit must
exist before a remote listener can be enabled. Permission changes take effect
in subsequent authorization decisions, including checks immediately before
sensitive steps. Expiring or revoking a client session does not itself cancel
an already accepted durable operation; the protocol's continuation and current
permission checks still apply.

## Future panel login

The future panel requires password authentication. Successful login creates a
short-lived authenticated session and, when needed, a delegated controller access
token bounded by that user's permissions. Login does not automatically create a
persistent automation key or grant administrative access.

Store passwords with a maintained password-hashing implementation using
Argon2id and unique salts, following the current [OWASP password-storage
guidance](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html).
The implementation must include generic login failures, bounded attempt rates,
protected password reset, and session invalidation after reset.

For a hosted panel, use server-side sessions with Secure, HttpOnly, SameSite
cookies, explicit CSRF protection for mutations, session rotation after login,
finite idle and absolute lifetimes, and server-side logout/revocation. Keep
controller credentials in the panel backend rather than browser persistent
storage. These controls follow [OWASP session-management
guidance](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html).

Delegation must preserve the authenticated human actor in the controller's
trusted identity mapping. A panel cannot turn browser-supplied actor metadata
into authority or use one unrestricted service identity to bypass per-user
permissions. A standalone client that talks directly to the controller uses
its own short-lived, audience-bound bearer token over TLS.

Persistent automation keys are created separately through an authorized action,
shown once, scoped, expiring, and individually revocable. Login sessions and
automation keys have separate lifecycle controls.

The Go store and `internal/authkey` package provide durable key issuance,
one-time secret return, verifier-only persistence, exact permission/resource/
environment scopes, controller-audience binding, expiry, and revocation. The
`internal/managementauth` package adds HTTP bearer authentication and exact
route-scope checks. These components are not yet connected to a management API
or an authorized key-management application service/CLI. TLS termination, rate
limiting, and audit integration are also outstanding; no listener is enabled.

## Implementation gates

Before declaring setup and remote management ready, demonstrate:

- Go setup tests for redacted plans, hidden/protected credential input, private
  files, interrupted writes, safe replacement, repeat execution, and preservation
  of an existing installation. No test may need production credentials.
- Synthetic provider tests proving setup makes no create, delete, start, stop,
  or other provider mutation, including on retries and failures.
- Authentication tests covering missing, invalid, expired, revoked, and
  wrong-audience credentials, actor spoofing, key rotation, and secret redaction.
- Authorization tests covering read versus mutation, other users' resources,
  cross-environment access, changed permissions, and exact destructive approvals.
- When a panel is eventually implemented, login/session/CSRF and delegation tests
  proving a browser session cannot obtain broader controller permissions or
  read stored provider credentials.

Do not treat this design, a passing configuration check, provider presence, or a
successful password check as evidence that the complete runtime exists.
