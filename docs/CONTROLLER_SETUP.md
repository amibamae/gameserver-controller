# Controller setup and management access

**Status:** Accepted implementation direction. `controller-setup plan` validates
configuration and emits a redacted, read-only plan for an existing Debian/
systemd machine. It does not install or modify files, contact providers, or read
credential contents. `controller-setup credentials set` accepts only a
credential name referenced by the validated config, prompts twice with
`systemd-ask-password`, and stores the value with `systemd-creds` in
`/etc/credstore.encrypted`. It runs only on Debian/systemd with local
administrator privileges, requires exact name confirmation to replace an
existing value, and never prints the credential. The setup apply command,
management authentication runtime, password login, and session issuance are
not implemented. This document does not authorize live installation.

Run the preview from a checkout with:

```sh
go run ./cmd/controller-setup plan --config configs/controller.example.yaml --json
```

The example configuration contains placeholder provider references and is
intended to show the plan shape. The output reports supported-target blockers
and fixed destination paths; it does not claim that setup can be applied yet.

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

The proposed CLI flow is:

1. Inspect the machine and validate a local configuration without reading secrets
   or changing services. Report missing prerequisites and exact planned paths.
2. Collect Vultr and Wasabi credentials through hidden interactive input or a
   protected input stream. Never accept literal secret values as command-line
   options or place them in process environment variables.
3. Prepare encrypted systemd credentials and configuration containing named
   `systemd-credential://` references. Show a redacted installation plan before
   applying it. Preserve existing configuration and credentials unless an
   explicit replacement was requested.
4. Install the approved configuration and service under a dedicated account,
   with private state, restricted credentials, and local audit enabled. Initial
   setup uses local OS authorization; it cannot expose an anonymous remote
   setup or administrator-registration endpoint.
5. Optionally test provider credentials with bounded read-only calls. Report
   what was actually checked. A successful read does not prove create/delete
   permissions or authorize a billable or destructive operation.

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

The Go store and `internal/authkey` package now provide durable key issuance,
one-time secret return, verifier-only persistence, exact permission/resource/
environment scopes, controller-audience binding, expiry, and revocation. This
foundation is not yet connected to a management API, a key-management CLI, or
request authorization; no remote listener is enabled.

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
