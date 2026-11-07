# Supervised server pilot

The pilot is a local CLI for inspecting the existing server, running its backup service, and creating a controller-owned Vultr VM under a hard capacity limit. It does not install or replace the existing `minecraft-backup.service` or the script it runs. Neither is stored in this repository; the server setup remains the source of truth.

## Commands and remote effects

- `gsc-pilot status --host <ssh-alias>` makes read-only `systemctl show` calls for `minecraft-vanilla.service`, `minecraft-backup.service`, and `minecraft-backup.timer`.
- `gsc-pilot backup --host <ssh-alias>` prints a dry run. It does not connect to the server.
- The only mutating remote command is exactly `sudo -n systemctl start --wait minecraft-backup.service`.
- Before and after an executed backup, the pilot runs read-only `sudo -n systemctl show --property=ActiveState,SubState,Result,ExecMainStatus,InvocationID minecraft-backup.service`.
- The pilot sends no script path, script body, or custom unit arguments.
- `gsc-pilot history` reads the local audit log and makes no SSH connection.
- `gsc-pilot boot-status --server-id <id> --environment <environment>` reads local timing records and makes no provider API or credential call.
- `gsc-pilot provision ...` defaults to a dry run. With explicit confirmation and a systemd credential, it reserves capacity and creates one Vultr VM; it does not install a game or claim readiness.
- `gsc-pilot capacity` reports the policy limit and durable local reservations.

The SSH account needs non-interactive sudo permission for the pilot's `systemctl show` and `systemctl start --wait` commands.

For an executed backup, pass both flags with the same SSH alias:

```sh
gsc-pilot backup --host <ssh-alias> --execute --confirm-host <ssh-alias>
```

## What counts as success

Do not trust `systemctl start --wait`'s exit code by itself. After the command returns, the pilot reads systemd again. It reports service success only when the new invocation ID differs from the previous one, the unit has reached a completed state (`inactive/dead` or `active/exited`), `Result=success`, and `ExecMainStatus=0`.

The pilot records an unknown outcome and will not retry if the post-run invocation ID is missing, malformed, or unchanged; if the unit remains nonterminal, including still running; or if it cannot read the final state. Inspect the service and its logs before you try again.

The pilot does not stop Paper, restore a world, destroy a server, or install or change the backup service.

## Vultr provisioning

The [capacity policy](../internal/pilotcapacity/capacity-policy.json) sets a source-controlled hard limit of **two controller-owned VMs total**: one production VM and one shared staging/testing VM. An existing live production server may remain unmanaged and does not consume these slots. The policy is embedded in the pilot binary; changing the cap requires editing the file, incrementing its version, validating it, and rebuilding the pilot. The CLI does not accept a policy-file or state-database override.

### Production-like trial and production rollout

The two owned slots support a staged rollout. First use the shared staging/testing slot with the less expensive Vultr plan to validate the intended production setup against disposable or staging data. Keep the existing live production server running and unmanaged during this test. The policy allows one controller-owned VM in each slot at the same time, so the final production VM can be provisioned while the test VM still occupies staging/testing.

After the trial passes, take a fresh backup from the current live server of the world you intend to migrate, verify the backup and its contents, and restore it onto a newly created VM using the more expensive production plan. Keep the existing live server untouched until the new VM is ready and an operator approves the traffic cutover. Only controller-owned VMs count toward the two-slot cap; the existing unmanaged production server does not. You do not need to adopt the old server as part of this rollout.

This is the intended rollout, not a workflow the current CLI can complete. The pilot can provision a VM, but it does not install Paper, restore a world, verify backup artifacts, run readiness checks, or switch DNS or proxy traffic. The `backup --execute` success event proves only that systemd ran the service successfully. Both the first real backup on the isolated test server and the fresh migration backup require a supervised operator handoff.

To change the limit, edit `internal/pilotcapacity/capacity-policy.json`, increment `policy_version`, then run `go run ./cmd/pilot-capacity-check` and `go test ./internal/pilotcapacity ./cmd/supervised-pilot`. Rebuild `gsc-pilot` before using the updated limit; changing the source file does not change an already-built executable.

Provisioning is dry-run by default. For example:

```sh
gsc-pilot provision \
  --server-id pilot-test --environment staging \
  --region <vultr-region-id> --plan <vultr-plan-id> --os-id <vultr-os-id> \
  --ssh-key-id <vultr-ssh-key-id>
```

Only after reviewing the dry run, an operator may add `--execute --confirm-create pilot-test@staging`. The command checks the source-controlled policy, durably reserves capacity before provider dispatch, and verifies the returned instance by exact ID and managed label. A confirmed create occupies the slot. An uncertain outcome also occupies it; rerun with the printed `--operation-id` and identical parameters to reconcile. The pilot never resends an uncertain create. A definitively rejected create releases its reservation.

The provider key is read only through the configured systemd credential reference, defaulting to `systemd-credential://vultr-pilot-api-token`. Execute the command from a systemd-managed process that receives this named credential through `LoadCredentialEncrypted`; do not pass the key in arguments, environment variables, config, or logs. See [secret management](SECRET_MANAGEMENT.md). The repository does not yet include an operator unit or automate credential provisioning, so do not run the billable command until the host-side systemd credential setup has been reviewed.

`gsc-pilot capacity` displays durable reservation state from the current account's `.local/state/gsc-pilot/pilot.db`. The bbolt database is local state and must be backed up and protected like other controller state. Do not delete it to clear an occupied slot; reconcile the provider outcome first. The pilot CLI does not yet adopt or destroy a VM, install Paper, restore a world, configure DNS, or check game readiness. The generic durable `server.destroy` workflow is implemented and fake-tested, but it is not wired into this CLI or a production hosting adapter.

For controller-owned VMs, the local store can record the provider creation timestamp and the time Paper's required readiness checks pass. If Vultr does not return a usable creation timestamp, the pilot leaves that time unknown; it does not substitute the later provider observation time. `gsc-pilot boot-status` uses completed intervals for the same server and environment to show an advisory estimate after at least three samples; it uses the 75th percentile with a 25% safety factor. Progress stays below 100% until readiness is confirmed. With fewer samples, an unmanaged VM, or an unknown creation timestamp, progress is indeterminate. A completed initial boot is historical evidence, not a live Paper health check. Readiness recorded for a non-active VM remains historical and does not show current progress as ready. The first ready timestamp is committed atomically with the successful `server.start` transition and bound to the exact hosting adapter and VM. Older reservations without adapter identity remain usable for capacity enforcement but do not gain automatic timing samples. These measurements stay local. Sharing aggregate averages with a hosted lobby/DNS service remains a separate future opt-in and is never required for local timing.

The current pilot CLI does not yet run the controller's `server.start` workflow. A controller process must use the same local store for its Paper readiness observation to appear in `boot-status`; the command does not infer readiness from Vultr's provider state or from systemd's VM status.

### Updating from an earlier pilot build

Stop all pilot processes before moving state. Earlier builds defaulted to `var/pilot.db` under the working directory and allowed `--state-db` to select another location. Before using `provision` or `capacity` with this build, carry forward the exact database used by the earlier build. For the old default, copy it to the account-state path below and move the original aside; replace `/path/to/account-home` with the home directory of the account that runs the pilot:

```sh
install -d -m 0700 /path/to/account-home/.local/state/gsc-pilot
install -m 0600 var/pilot.db /path/to/account-home/.local/state/gsc-pilot/pilot.db
mv var/pilot.db var/pilot.db.pre-upgrade
```

For an earlier custom `--state-db` path, use that exact source file instead of `var/pilot.db`. If the new account-state database already exists and is different, do not overwrite or combine either database; reconcile the recorded provider instances before choosing the authoritative state. The pilot refuses to use its state path when it finds the old default database in the current working directory. After migration, run `gsc-pilot capacity` and confirm every unresolved or managed VM still occupies its slot. Keep the pre-upgrade copy until the records have been checked.

## Operator handoff

The repository's automated checks and this checklist do not claim that a live backup has been run or that a Vultr VM has been created. This is a supervised handoff, not a live-resource validation report. Provisioning tests use fake providers.

Before you execute:

1. Confirm the SSH alias reaches the intended server and review its host key.
2. Check that the account has non-interactive sudo for the read-only `systemctl show` calls and the exact backup start command.
3. Run `gsc-pilot status --host <ssh-alias>` and review the game, backup, and timer unit states.
4. Run `gsc-pilot backup --host <ssh-alias>` without `--execute`. Review the service and target in the dry-run output.
5. Only then run the command with both flags and the same alias:

   ```sh
   gsc-pilot backup --host <ssh-alias> --execute --confirm-host <ssh-alias>
   ```

6. Review the result and run `gsc-pilot history` as the same local user. If the result is `outcome_unknown`, do not retry; inspect service state and logs through the server's existing process first.
7. After systemd reports success, verify that the expected backup artifacts exist. Then use the existing backup process to check storage and integrity. The `service_succeeded` event confirms the systemd invocation only.

## What the success event proves

The `service_succeeded` audit event records a successful systemd invocation. It does not inspect the backup script, files, object storage, checksums, or world contents. Use the server's existing backup process to verify that a usable backup exists.

The local audit log is `~/.local/state/gsc-pilot/operations.jsonl` with owner-only file permissions. Pilot tests use fake SSH and fake Vultr APIs; they do not run the backup service, create a VM, or connect to a live server.
