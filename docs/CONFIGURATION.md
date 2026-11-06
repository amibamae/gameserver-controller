# Configuration and Secret References

**Status:** Proposed Phase 1 specification. There is no configuration loader or implemented schema yet.

## What configuration must do

Validate settings before external side effects. Scope them to an environment. Declare adapter capabilities. Keep secrets out of ordinary values and make displayed configuration safe to share after redaction. Reject unknown critical fields instead of ignoring them.

## Configuration areas

1. **Controller:** runtime mode, state-store reference, audit settings, and operation deadlines.
2. **Environment:** environment ID, safety classification, network boundary, and storage boundary.
3. **Logical server:** game adapter, hosting and storage profiles, release channel, and health policy.
4. **Adapter:** non-secret endpoints, region/profile mapping, feature flags, and secret references.
5. **Policy:** permissions, approvals, retention, telemetry consent, and destructive-action gates.

A secret is not a normal configuration value. Configuration may point to a protected secret; it must never contain the secret itself.

## Illustrative configuration

This example shows the proposed shape only. Names and schema are not final.

~~~yaml
schema_version: 1

controller:
  mode: local
  state_store:
    type: sqlite
    path: ./var/controller-state.db
  audit:
    enabled: true
    destination: local
  defaults:
    operation_timeout_seconds: 900
    retry_limit: 3

adapters:
  hosting:
    id: hosting.vultr
    config:
      api_token:
        secret_ref: env://GAMECTRL_VULTR_TOKEN
  storage:
    id: storage.wasabi-s3
    config:
      endpoint: https://s3.us-west-1.wasabisys.com
      region: us-west-1
      bucket: example-production-backups
      access_key:
        secret_ref: env://GAMECTRL_WASABI_ACCESS_KEY
      secret_key:
        secret_ref: env://GAMECTRL_WASABI_SECRET_KEY

environments:
  production:
    classification: production
    allow_destroy: false
    storage_profile: production-backups
  staging:
    classification: staging
    allow_destroy: true
    production_backup_access: read_only

servers:
  vanilla:
    environment: production
    game:
      adapter: game.minecraft-paper
      version_policy: approved-release
    hosting:
      profile: vultr-seattle-approved
    storage:
      backup_profile: production-backups
    proxy:
      adapter: integration.velocity
      backend_name: vanilla
    lifecycle:
      idle_shutdown_minutes: 10
      require_verified_backup_before_destroy: true
~~~

The bucket and environment values are placeholders. The example contains references, not credential values. A production schema should represent approval gates directly; a boolean alone is not enough.

## Secret reference contract

The shared JSON representation is defined by [secret-reference.schema.json](../protocol/schemas/common/secret-reference.schema.json). An implementation may use different configuration syntax, but it must preserve the same meaning and least-privilege rules.

Possible resolvers include environment variables for local development, permission-restricted files readable by root or a service account, and a future external secrets manager.

A resolver must:

- Reject literal secret values where a reference is required.
- Never print a resolved secret.
- Resolve a secret only when needed and keep it in memory for as little time as practical.
- Keep credentials out of bootstrap scripts, Cloud-Init/user-data, release archives, source control, command-line arguments, and ordinary logs.
- Allow credential rotation without source changes.
- Separate credentials by provider, environment, and permission scope.
- Fail closed if lookup or authorization fails. Never fall back to empty or broader credentials.

## Environment boundaries

Every resource and operation belongs to one environment. Deny cross-environment access unless a narrow policy allows it.

**Production** owns live world data and production backup write permissions. Staging credentials cannot write there.

**Staging** may read only the production backup objects selected for a test. It uses read-only credentials for those objects, writes to staging-owned storage, and cannot delete or overwrite production data or sync its world back.

**Bootstrap execution** prepares an instance created from a provider-supported OS offering. Bootstrap source and user-data contain no credential values or live world data. Supply credentials through scoped references only to the steps that need them.

## Validate before acting

Before an operation, validate:

- Schema version and required fields.
- Adapter IDs and all critical fields.
- Required adapter capabilities.
- Environment and resource ownership.
- Deadline and retry bounds.
- Authorization and approval for destructive actions.
- Distinct production and staging credential scopes.
- Secret references instead of values.
- Paths against traversal and unsafe symlinks.
- Network destinations and ports against policy.

Validation and plan/preview must have no side effects.

## Changing configuration

Record the exact configuration version used by each operation. Validate changes before applying them and show a redacted diff. Require explicit approval for broader permissions, production storage changes, retention changes, or enabling destructive actions. Keep a known-good version for recovery.

## Open decisions

Maintainers still need to choose the schema format and validator, determine whether SQLite fits the first single-controller deployment, select secret resolvers and precedence, define configuration migrations, decide how to represent provider profiles, and specify operator identity and approvals for the CLI and future API.