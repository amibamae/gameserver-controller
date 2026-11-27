# Coordination CLI

`cmd/coordination` lets several agents (OpenCode and Claude sessions) on one machine claim tasks, reserve files and report progress without stepping on each other. The state lives in [`COORDINATION/`](../COORDINATION/): the task registry (`registry.json`), the agents (`.agents.json`), file reservations (`.files.json`), the dependency graph (`.deps.json`) and an append-only log (`audit.md`).

## Where the state is

Every command takes `--dir`. Without it, the CLI uses `$GSC_COORDINATION_DIR`, and then the `COORDINATION/` directory of the clone's **main worktree** (the first entry of `git worktree list`). Every linked worktree of a clone therefore shares one state directory and one lock, so agents in separate worktrees see each other's claims. If the main worktree cannot be found, the command fails and asks for `--dir`.

## Commands

Commands that change state need the acting agent: `--agent`, or `$GSC_AGENT_ID`.

```bash
go run ./cmd/coordination register --name "agent-name" --session-id ses-123 --version 1.0
# also accepted: go run ./cmd/coordination --register --name "agent-name" --sessionID ses-123
export GSC_AGENT_ID=agent-10            # the ID register printed

go run ./cmd/coordination status        # agents, tasks, reservations, revision
go run ./cmd/coordination available     # claimable tasks, with dependency warnings
go run ./cmd/coordination claim --task-id TASK-ARCH-001
go run ./cmd/coordination reserve --files 'internal/simulation/*,tests/simulation/*_test.go'
go run ./cmd/coordination update --task-id TASK-ARCH-001 --progress "simulator clock: done"
go run ./cmd/coordination block --task-id TASK-ARCH-001 --reason "waiting for TASK-LC-003"
go run ./cmd/coordination update --task-id TASK-ARCH-001 --progress "unblocked" --unblock
go run ./cmd/coordination handoff --task-id TASK-ARCH-001 --to-agent agent-8 --reason "session ending"
go run ./cmd/coordination complete --task-id TASK-ARCH-001 --pr-number 470
go run ./cmd/coordination release --task-id TASK-ARCH-001 [--force-reconcile]
go run ./cmd/coordination validate      # exits 1 if it finds errors
go run ./cmd/coordination reconcile     # releases expired and orphaned ownership
```

- **claim** fails while another agent holds a live claim on the task, and while you hold another task. Dependencies that are undefined or not completed are printed as warnings, not refused.
- **reserve** takes paths or globs relative to the repository root, and defaults `--for` to the task you hold. Overlap is checked conservatively: a pattern covers everything under the literal text before its first `*`, `?` or `[`, and a plain path also covers its subtree.
- **complete** works on a task you hold, or on one you released yourself if nobody claimed it since (the spec's release-then-complete order).
- **release --force-reconcile** lets any registered agent release another agent's claim once it has expired.
- **reconcile** runs without registering. Its audit entries name `--agent`, or `operator` if no agent is given.

Exit codes are 0 for success, 1 when a command fails or validate finds errors, and 2 for a usage error.

## Concurrency

Every change runs as optimistic concurrency over a state **revision** kept in `registry.json`, `.agents.json` and `.files.json`:

1. Read a snapshot under the lock file `COORDINATION/.lock`, and note its revision.
2. Apply the change to the snapshot.
3. Take the lock again, re-read the revision, and commit only if it is unchanged. The audit entry is appended first, then each file is written to a temporary file and renamed over the old one.

A commit that lost a race gets `stale update: coordination state is at revision N, expected M; reload state and retry`. Without `--expect-revision`, the CLI reloads and re-runs the change, so a losing claim then fails with `task … is already claimed by …`. With `--expect-revision N`, the change must apply to revision `N` exactly, and a stale commit is returned instead of retried.

The spec sketches Git commits as the locking mechanism. The CLI uses a lock file and atomic renames instead: agents on one machine share a single directory, a lock file serializes them without touching anyone's branch or index, and no commit is needed for a claim to take effect. Commit the state files whenever you want them in history.

## Expiry

Claims and reservations last 24 hours from the last heartbeat. `claim`, `reserve`, `update`, `block` and `handoff` count as heartbeats for the task and its reservations. An expired claim can be claimed by another agent, which takes it over and drops its reservations. `reconcile` also releases expired claims, drops expired reservations and reservations whose agent no longer holds the task, and marks active agents with no live claim and no heartbeat for 24 hours as idle.

## Failing closed

The CLI refuses to act on state it cannot trust:

- a missing or unparsable file;
- a field it does not know, or trailing data after the JSON;
- a version other than `1.0`;
- revisions that differ between files, which means a write stopped part-way;
- a malformed expiry time.

It never breaks a lock left behind by a crashed command. Instead it names the holder after 10 seconds and tells you to remove the file once no command is running.

## Known gaps in the seed state

`validate` reports these in the state committed with the CLI. They are left for the task owners to resolve:

- `TASK-CORE-001` and `TASK-CORE-002` are dependencies of TASK-LC-001 to TASK-LC-004, but no task defines them.
- `.deps.json` lists `TASK-PROD-001` and `TASK-PROD-002` under `requiredBy`, but they are missing from `registry.json`, although `audit.md` says they were registered.
- `TASK-LC-002` has `claimedAt` set to the placeholder `2026-11-XXT00:00:00Z`.
- `agent-9` has `currentTask` set to `monitoring-agent-coordination`, which is not a task.
