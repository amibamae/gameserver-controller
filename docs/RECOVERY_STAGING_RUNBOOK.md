# Recovery staging rehearsal: independent disposable Paper VM

**Status:** Procedure only. This repository does not ship an enabled production auto-restart daemon, and this runbook has not been executed against a real Vultr VM. Never run fault-injection steps on the current production instance or world.

## Entry requirements

Use an isolated Vultr test instance with a separate controller environment, Velocity registration, SSH key/known-hosts entry, Wasabi test storage prefix and disposable Paper world. Prefer a *different Wasabi bucket or tightly scoped storage credentials* from production. No production world restore or shared player whitelist. Budget the VM lifetime explicitly and turn off automated destruction unless final-backup safety is independently verified. Use a unique test server ID, data generation and backup-policy ID.

Before testing, capture identifiers (without secrets): test instance reference, systemd unit reference, release ID, proxy backend ID, initial invocation ID and immutable storage object version IDs if available. Snapshot the staging world so the test can be repeated without using production data.

## Safety preflight

1. Verify the controller cannot dispatch hosting instance **create**, **replace**, or **destroy** from a health-failure operation. Test requires permission to operate only on the staging server and backend.
2. Ensure `minecraft-vanilla.service` runs under its unprivileged account, has rate-limited `Restart=on-failure`, and the installed `minecraft_probe.py` is read-only.
3. Verify the proxy's staging backend can be set to `draining` and that healthy TCP probes alone cannot restore routing.
4. Confirm the controller's durable operation store is independent of the disposable VM, and that a restart is reconciled after an unexpected controller exit rather than blindly replayed.
5. Configure Wasabi credentials through the controller's credential manager, never in game config, bootstrap logs, PR comments or command-line arguments. Restrict readback to the test bucket/profile and a small maximum number of bytes.

## Scenario A: same-VM recovery

This is **not enabled in `controller serve` yet**. Only attempt live fault injection after a complete durable `server.recover` workflow and real game/execution adapter implementations exist.

1. Start the staging world; record its current systemd invocation and a known world marker.
2. Simulate a confirmed failure on the staging game only. Require two independent, fresh unhealthy samples for the exact same VM, unit, release and runtime. A deliberately stopped process or unknown log state must not trigger automatic restart.
3. Verify the controller writes an accepted operation and durable step plan before any external effect, then drains the Velocity backend.
4. For a still-running process, require a confirmed `game.save.request`, dispatch the protocol `execution.unit.restart` with `force=false`, and confirm a clean, bounded stop. For an already dead unit, use the stopped-runtime `execution.unit.start` path instead.
5. Confirm the VM's provider instance ID never changes. Require a *new* runtime ID and an invocation-local Paper `Done (...)` plus all game adapter readiness checks before Velocity re-registers the backend.
6. Repeat with a timeout during restart, controller interruption, insufficient save evidence, or a spoofed/stale `Done` marker; assert no blind retry, no rerouting and no new VM.

## Scenario B: backup-before-destroy

Destruction is a **separate explicit approval** and remains disabled by default. Testing must use only the disposable VM.

1. Drain the staging backend and cleanly quiesce/stall writes. Complete a `server.backup` operation with a confirmed game save, stopped runtime, offline preparation, full upload, independent full-object readback and game-specific validation.
2. Check the resulting immutable manifest names the staging world, exact current generation and source instance. Verify its objects are in unique per-backup keys and the reader returns matching full SHA-256, byte count and object version.
3. Construct *negative* cases: absent object, truncated body, different version, wrong backup-policy ID, wrong storage-profile ID, older generation, later game start, unverified save, and missing approval. Every negative case must reject provider deletion.
4. Revalidate all evidence immediately before provider deletion, but **do not invoke deletion** until the authorized test operator has reviewed exact resource IDs and confirmed isolation. If final write boundary cannot be proven, preserve the VM/disk for rescue.
5. Verify an isolated restore from the selected recovery point, including the known world marker, before approving any production rollout. A successful upload or completed systemd backup job is not a restore test.

## Locally available, non-destructive checks

```bash
go test -count=1 ./internal/recovery ./internal/backupintegrity ./scripts/bootstrap
go test -count=1 ./internal/workflow -run '^(TestServerDestroyRejectsIncompleteFinalRecoveryPoint|TestStoredBackupResolver.*|TestFinalRecoveryBoundary.*)$'
python3 -m unittest discover -s scripts/observability -p 'test_*.py'
(cd plugins/velocity && ./gradlew --no-daemon test --tests '*BackendRegistryRecoveryTest')
```

CI runs these without authenticating to Vultr or Wasabi and without modifying servers. It proves policy logic and simulated adapter behavior, not a real host's failure/recovery timing.

## Repeatable real-VM harness (implemented)

The non-destructive verifier at `scripts/recovery/staging_live.py` and its
hermetic tests at `scripts/recovery/test_staging_live.py` support actual
Vultr VM identity/readiness observations and isolated Wasabi file restoration.
The manually triggered `.github/workflows/recovery-staging-live.yml` runs
against the **recovery-staging** GitHub environment after explicit approval,
using its environment-only SSH identity. Normal CI never connects to Vultr.

### One-time preparation on a separate, disposable test VM

**Do not run these setup commands on the production Minecraft VM.** First
confirm its Vultr instance UUID in the account portal, isolate its SSH key,
player routing, and Wasabi staging credentials, and place only a throwaway
world there. Use the actual disposable UUID in the example:

```sh
sudo install -d -m 0700 -o gsc-stage-tester -g gsc-stage-tester /srv/gsc-recovery-restore
sudo tee /etc/gsc-recovery-staging.json >/dev/null <<'JSON'
{
  "environment": "staging",
  "purpose": "minecraft-recovery-isolated-test",
  "server_id": "recovery-test-paper",
  "instance_id": "11111111-1111-4111-8111-111111111111",
  "unit": "minecraft-vanilla.service",
  "restore_root": "/srv/gsc-recovery-restore",
  "remote_prefix": "stagingwasabi:world-staging/recovery-staging/recovery-test-paper/"
}
JSON
sudo chown root:root /etc/gsc-recovery-staging.json
sudo chmod 0644 /etc/gsc-recovery-staging.json
```

These UUID and storage paths are **examples only**. The marker must match
actual Vultr instance metadata, including the current signed-token claims
obtained locally from its link-local metadata endpoint. The verifier reads
the claims *inside* the VM and never exports the JWT. This guards against
accidental targeting mistakes but is not a substitute for provider-side
authorization or checking the instance in the Vultr console.

Install the read-only Paper probe at
`/usr/local/libexec/gsc/minecraft_probe.py` using the repository bootstrap.
The `gsc-stage-tester` SSH account needs permission to read the systemd
journal for its own test Paper unit, and a separately scoped rclone
`stagingwasabi:` remote configured with access to **only test backup objects**.
It needs write access only to `/srv/gsc-recovery-restore`. Never give this
account permission to destroy Vultr instances, modify production worlds,
or access production Wasabi keys.

Create a unique `recovery-test-marker.txt` in the root of the throwaway
Paper data directory *before* the test backup; record its SHA-256 digest.
Back up to a unique, isolated path below
`stagingwasabi:world-staging/recovery-staging/recovery-test-paper/<backup-id>`.
The test expects at least one restored Minecraft `level.dat` and an
exact SHA-256 match for the marker. It reports **verified restore files**,
not that the restored Paper process has successfully booted.

### Run from a trusted workstation

Create `staging-target.json` locally (do **not** commit actual hostnames,
credentials or a private SSH configuration to the repo):

```json
{
  "environment": "staging",
  "server_id": "recovery-test-paper",
  "instance_id": "11111111-1111-4111-8111-111111111111",
  "ssh_alias": "gsc-test-seattle",
  "unit": "minecraft-vanilla.service"
}
```

After verifying `ssh gsc-test-seattle` has a pinned host key and a
non-root staging-only identity:

```bash
python3 scripts/recovery/staging_live.py --target staging-target.json observe --out before.json
# Trigger failure/recovery ONLY through the future controller durable
# server.recover operation, once implemented. Do NOT use an SSH restart here.
python3 scripts/recovery/staging_live.py --target staging-target.json verify-recovery --before before.json
python3 scripts/recovery/staging_live.py --target staging-target.json verify-restore \
  --source stagingwasabi:world-staging/recovery-staging/recovery-test-paper/backup-001 \
  --marker-sha256 <sha256-of-test-marker>
```

The `verify-recovery` comparison rejects unchanged Paper invocation IDs,
unknown/unhealthy states, stale timestamps and changed VM identity. It does
**not** produce a restart, automatically trigger a fault, or claim that
the controller made the change. Verify that separately from the controller's
durable accepted operation history and the Velocity drain/register timeline.
The scratch restore uses `rclone copy --checksum` and never writes to the
live Paper directory. Confirm that the restored files can then boot in a
**second isolated test runtime** before accepting production restoration
as proven.

### Optional GitHub Actions execution

Set up the `recovery-staging` repository environment with a required
human reviewer and these **environment-only** values:
`GSC_RECOVERY_STAGING_HOST` and `GSC_RECOVERY_STAGING_USER` (user must
be `gsc-stage-tester`), and secrets
`GSC_RECOVERY_STAGING_SSH_KEY` and
`GSC_RECOVERY_STAGING_KNOWN_HOSTS`. Keep all production credential
references out of it. From Actions, manually run **Real Vultr staging
verification (manual only)** with `STAGING_ONLY` acknowledgement and
the exact disposable UUID. Choose `observe` for a read-only check;
`verify-restore` creates only a scratch copy with the explicitly
provided staging backup source and marker digest. The workflow does not
create, restart, stop, or delete any VM.

## Exit criteria

Record the commit SHA, CI run IDs, sanitized operation and exact-instance IDs, recovery evidence timestamps, backup manifest IDs, object integrity checks and restored test world marker. Approve rollout only after all CI checks pass and a separate staging owner verifies real adapter behavior, permissions, costs, recovery timing, world consistency and privacy. If a check is unknown or fails, retain the staging VM and report **not ready** rather than force merging or deleting the test source.
