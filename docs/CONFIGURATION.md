# Configuration and Secret Reference Model

**Status:** Proposed Phase 1 specification. No loader or schema implementation is implied.

## Goals

Configuration must be validated before external side effects, versioned, environment-scoped, free of embedded credentials, explicit about adapter capabilities, and safe to display after redaction. Unknown critical fields must not be silently ignored.

## Configuration categories

1. Controller: runtime mode, state-store reference, audit settings, operation timeouts.
2. Environment: environment ID, safety classification, network/storage boundaries.
3. Logical server: game adapter, hosting/storage profiles, release channel, health policy.
4. Adapter: non-secret endpoints, region/profile mapping, feature flags, secret references.
5. Policy: permissions, approvals, retention, telemetry consent, destructive-action gates.

Secrets are not ordinary configuration values. Config may contain a protected secret reference, never the secret value.

## Illustrative configuration

This YAML is conceptual only. Names and schema are provisional.

```yaml
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
```

Bucket and environment values above are placeholders. The example contains secret references, not credentials. A real schema should model production approval gates explicitly rather than relying only on a boolean.

## Secret reference contract

Possible resolvers:
- Environment variables for local development.
- Root-readable or service-account-readable files with restrictive permissions.
- External secret manager in a future deployment.

Requirements:
- Reject literal secret values where a reference is required.
- Never print resolved secrets.
- Resolve secrets at point of use and keep their lifetime short.
- Never embed credentials in bootstrap scripts, cloud-init/user-data, release archives, source control, command-line arguments, or ordinary logs.
- Support credential rotation without source changes.
- Separate credentials by provider, environment, and permission scope.
- A resolver failure blocks the dependent operation; never fall back to empty/default/broader credentials.

## Environment isolation

Every resource and operation belongs to exactly one environment. Cross-environment access is denied unless a narrow policy explicitly permits it.

**Production:** owns live world data and production backup write permissions. Staging credentials cannot write to it.

**Staging:** reads only the production backup objects explicitly needed for a test, using read-only credentials; writes to staging-owned storage; cannot delete or overwrite production objects; cannot sync its world back to production.

**Bootstrap execution:** prepares an instance created from a provider-supported standard OS offering. Bootstrap source and user-data contain no credential values or live world data. Credentials are supplied separately through scoped secret references only to the operation steps that require them.

## Validation rules

Before any operation:
- Validate schema version and required fields.
- Reject unknown adapter IDs and unknown critical fields.
- Check required adapter capabilities.
- Verify environment/resource ownership.
- Bound timeouts and retry limits.
- Check authorization and approval path for destructive actions.
- Verify production/staging credential scopes are distinct.
- Ensure secret fields contain references.
- Validate paths against traversal and unsafe symlinks.
- Validate network destinations and ports against policy.

Configuration validation and plan/preview must be side-effect free.

## Configuration changes

- Record exact configuration version used by each operation.
- Validate proposed changes before applying them.
- Show redacted diffs.
- Require explicit approval for permission broadening, production storage changes, retention changes, or enabling destructive actions.
- Keep a known-good configuration version for recovery.

## Open decisions

- Final schema format and validation library.
- Whether SQLite is sufficient for the initial single-controller deployment.
- Secret resolver implementation and precedence.
- Configuration migration policy.
- Provider profiles embedded or separately referenced.
- Operator identity and approval representation in CLI/future API.
